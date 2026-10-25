#!/usr/bin/env bash
#
# 01-server.sh: prepares the production server (docs/deploy.md sections 1 and 2).
#
# Run as root on the Ubuntu 26.04 LTS server:
#   sudo bash deploy/setup/01-server.sh
#
# Steps. Each one prints its exact commands and asks [y/N] before it changes
# anything. A step that is already in place is skipped, so re-running is safe.
#   1. Packages        apt install podman, passt, uidmap, sqlite3, restic, ufw, curl, git, openssl
#   2. deploy user     useradd --create-home --shell /bin/bash deploy
#   3. subuid/subgid   rootless Podman needs id ranges for the deploy user
#   4. SSH access      copy root's authorized_keys to deploy (only when deploy has none)
#   5. linger          loginctl enable-linger deploy (services run after logout)
#   6. sysctl          net.ipv4.ip_unprivileged_port_start=443
#   7. swap            2 GB /swapfile (only when the machine has no swap at all)
#   8. firewall        ufw: allow OpenSSH, then Cloudflare 443 ranges, then enable
#   9. refresh script  /usr/local/sbin/maket-cf-ufw-refresh (optional)
#
# Options:
#   --dry-run  print every command and change nothing. Works without root.
#   --yes      answer yes to every prompt.
#   -h, --help show this help.
#
# Requires: apt (Ubuntu), curl. Nothing here touches the checkout or secrets.

set -euo pipefail

DRY_RUN=0
ASSUME_YES=0
PLAN=0
DONE_STEPS=()
SKIPPED_STEPS=()

# ──────────────────────────────────────────────────────────────────────────
# Output helpers (same look as the /wizard library)
# ──────────────────────────────────────────────────────────────────────────

if [[ -t 1 ]] && command -v tput >/dev/null 2>&1 && [[ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]]; then
  BOLD=$(tput bold); DIM=$(tput dim); RESET=$(tput sgr0)
  BLUE=$(tput setaf 4); GREEN=$(tput setaf 2); YELLOW=$(tput setaf 3); RED=$(tput setaf 1)
else
  BOLD=""; DIM=""; RESET=""; BLUE=""; GREEN=""; YELLOW=""; RED=""
fi

say()  { printf '  %s\n' "$1"; }
note() { printf '  %s%s%s\n' "$DIM" "$1" "$RESET"; }
warn() { printf '  %s⚠ %s%s\n' "$YELLOW" "$1" "$RESET"; }
die()  { printf '  %s✗ %s%s\n' "$RED" "$1" "$RESET" >&2; exit 1; }

# confirm "question": y/N gate. --yes answers yes. Returns success on yes.
confirm() {
  local reply=""
  if (( ASSUME_YES )); then
    printf '  %s? %s [y/N] y (--yes)%s\n' "$YELLOW" "$1" "$RESET"
    return 0
  fi
  printf '  %s? %s [y/N] %s' "$YELLOW" "$1" "$RESET"
  read -r reply || true
  [[ "$reply" =~ ^[Yy] ]]
}

# run CMD...: prints the command. It runs only outside the plan pass.
run() {
  printf '    %s+ %s%s\n' "$DIM" "$*" "$RESET"
  if (( PLAN )); then return 0; fi
  "$@"
}

# write_file PATH MODE: the file content comes on stdin. The plan pass prints
# it. The real pass writes it.
write_file() {
  local path="$1" mode="$2" content
  content="$(cat)"
  printf '    %s+ write %s (mode %s):%s\n' "$DIM" "$path" "$mode" "$RESET"
  printf '%s\n' "$content" | sed 's/^/        | /'
  if (( PLAN )); then return 0; fi
  printf '%s\n' "$content" > "$path"
  chmod "$mode" "$path"
}

# do_step TITLE CHECK ACTION. CHECK returns 0 when the step is already done.
# ACTION runs twice: first as a plan (PLAN=1, prints commands), then for real
# after the human says yes.
do_step() {
  local title="$1" check="$2" action="$3"
  printf '\n%s%s▸ %s%s\n' "$BOLD" "$BLUE" "$title" "$RESET"
  if "$check"; then
    note "already in place, skipping"
    SKIPPED_STEPS+=("$title (already in place)")
    return 0
  fi
  PLAN=1
  "$action" || die "could not plan: $title"
  PLAN=0
  if (( DRY_RUN )); then
    note "dry-run: nothing was changed"
    SKIPPED_STEPS+=("$title (dry-run)")
    return 0
  fi
  if ! confirm "Run these commands for: $title?"; then
    note "skipped by you"
    SKIPPED_STEPS+=("$title (declined)")
    return 0
  fi
  "$action" || die "step failed: $title"
  DONE_STEPS+=("$title")
  printf '  %s✓ done%s\n' "$GREEN" "$RESET"
}

# ──────────────────────────────────────────────────────────────────────────
# Steps
# ──────────────────────────────────────────────────────────────────────────

PACKAGES=(podman passt uidmap sqlite3 restic ufw curl git openssl)

check_packages() {
  local p
  for p in "${PACKAGES[@]}"; do
    dpkg -s "$p" >/dev/null 2>&1 || return 1
  done
}
action_packages() {
  run apt-get update
  run env DEBIAN_FRONTEND=noninteractive apt-get install -y "${PACKAGES[@]}"
}

check_user() { id -u deploy >/dev/null 2>&1; }
action_user() { run useradd --create-home --shell /bin/bash deploy; }

check_subids() {
  grep -q '^deploy:' /etc/subuid 2>/dev/null && grep -q '^deploy:' /etc/subgid 2>/dev/null
}
action_subids() {
  note "Ubuntu normally adds these when the user is created. This adds them if they are missing."
  run usermod --add-subuids 100000-165535 --add-subgids 100000-165535 deploy
}

check_ssh_keys() { [[ -s /home/deploy/.ssh/authorized_keys ]]; }
action_ssh_keys() {
  if [[ ! -s /root/.ssh/authorized_keys ]]; then
    warn "/root/.ssh/authorized_keys is empty or missing. Add an SSH key for deploy by hand before you log in as deploy."
    return 0
  fi
  note "Copies root's keys so you can log in as deploy. Skip this if you prefer another key."
  run install -d -m 700 -o deploy -g deploy /home/deploy/.ssh
  run install -m 600 -o deploy -g deploy /root/.ssh/authorized_keys /home/deploy/.ssh/authorized_keys
}

check_linger() {
  [[ "$(loginctl show-user deploy --property=Linger --value 2>/dev/null)" == yes ]]
}
action_linger() { run loginctl enable-linger deploy; }

SYSCTL_FILE=/etc/sysctl.d/99-maket-unprivileged-443.conf
check_sysctl() {
  [[ "$(cat /proc/sys/net/ipv4/ip_unprivileged_port_start 2>/dev/null)" == 443 ]] &&
    grep -qx 'net.ipv4.ip_unprivileged_port_start=443' "$SYSCTL_FILE" 2>/dev/null
}
action_sysctl() {
  write_file "$SYSCTL_FILE" 0644 <<'EOF'
net.ipv4.ip_unprivileged_port_start=443
EOF
  run sysctl --system
}

check_swap() { [[ -n "$(swapon --noheadings --show=NAME 2>/dev/null)" ]]; }
action_swap() {
  if [[ -e /swapfile ]]; then
    if (( ! PLAN )); then die "/swapfile exists but no swap is active. Inspect it, then re-run."; fi
    warn "/swapfile already exists, so the real run will stop here."
    return 0
  fi
  run fallocate -l 2G /swapfile
  run chmod 600 /swapfile
  run mkswap /swapfile
  run swapon /swapfile
  if ! grep -qE '^/swapfile[[:space:]]' /etc/fstab 2>/dev/null; then
    say "Adds this line to /etc/fstab so the swap comes back after a reboot:"
    run bash -c 'echo "/swapfile none swap sw 0 0" >> /etc/fstab'
  fi
}

check_ufw() {
  ufw status 2>/dev/null | grep -q '^Status: active' &&
    ufw status 2>/dev/null | grep -qi cloudflare
}

# fetch_cf_ranges URL REGEX prints the matching CIDRs. It fails when the
# download fails or returns no ranges, so the firewall is never half-changed.
fetch_cf_ranges() {
  local body ranges
  body="$(curl -fsS --max-time 20 "$1")" || { warn "could not fetch $1"; return 1; }
  ranges="$(printf '%s\n' "$body" | tr -d '\r' | grep -E "$2" || true)"
  if [[ -z "$ranges" ]]; then
    warn "no ranges returned by $1"
    return 1
  fi
  printf '%s\n' "$ranges"
}

action_ufw() {
  local v4 v6 cidr
  v4="$(fetch_cf_ranges https://www.cloudflare.com/ips-v4 '^[0-9.]+/[0-9]+$')" ||
    die "firewall step aborted before any change: IPv4 ranges unavailable"
  v6="$(fetch_cf_ranges https://www.cloudflare.com/ips-v6 '^[0-9a-fA-F:]+/[0-9]+$')" ||
    die "firewall step aborted before any change: IPv6 ranges unavailable"

  # OpenSSH comes first, so SSH stays reachable when the default deny starts.
  run ufw allow OpenSSH
  run ufw default deny incoming
  run ufw default allow outgoing
  while read -r cidr; do
    run ufw allow proto tcp from "$cidr" to any port 443 comment cloudflare
  done < <(printf '%s\n%s\n' "$v4" "$v6")
  run ufw --force enable
  run ufw status verbose
}

CF_SCRIPT=/usr/local/sbin/maket-cf-ufw-refresh
check_cf_script() { [[ -x "$CF_SCRIPT" ]]; }
action_cf_script() {
  note "Optional. Re-run it later (as root) to add the Cloudflare ranges that were added since."
  write_file "$CF_SCRIPT" 0755 <<'EOF'
#!/usr/bin/env bash
# Allow 443/tcp from Cloudflare's published ranges. Run as root. Safe to re-run:
# ufw skips rules that already exist. Ranges Cloudflare removed stay until you
# delete them: list them with `ufw status numbered`, then `ufw delete <n>`.
set -euo pipefail

fetch() {
  local body
  body="$(curl -fsS --max-time 20 "$1")" || { echo "fetch failed: $1" >&2; exit 1; }
  printf '%s\n' "$body" | tr -d '\r' | grep -E "$2" || true
}

v4="$(fetch https://www.cloudflare.com/ips-v4 '^[0-9.]+/[0-9]+$')"
v6="$(fetch https://www.cloudflare.com/ips-v6 '^[0-9a-fA-F:]+/[0-9]+$')"
if [[ -z "$v4" || -z "$v6" ]]; then
  echo "no ranges returned; no rules changed" >&2
  exit 1
fi

while read -r cidr; do
  ufw allow proto tcp from "$cidr" to any port 443 comment cloudflare
done < <(printf '%s\n%s\n' "$v4" "$v6")
EOF
}

# ──────────────────────────────────────────────────────────────────────────
# Main
# ──────────────────────────────────────────────────────────────────────────

usage() {
  sed -n '2,27p' "$0" | sed 's/^# \{0,1\}//'
}

parse_args() {
  while (( $# )); do
    case "$1" in
      --dry-run|-n) DRY_RUN=1 ;;
      --yes|-y) ASSUME_YES=1 ;;
      -h|--help) usage; exit 0 ;;
      *) die "unknown option: $1 (see --help)" ;;
    esac
    shift
  done
}

preflight() {
  local os_id os_ver os_name arch ports
  # shellcheck source=/dev/null
  . /etc/os-release
  os_id="${ID:-}"
  os_ver="${VERSION_ID:-}"
  os_name="${PRETTY_NAME:-unknown}"
  arch="$(uname -m)"
  say "System: $os_name, $arch"
  if [[ "$os_id" != ubuntu || "$os_ver" != 26.04 ]]; then
    warn "This script is written for Ubuntu 26.04 LTS. Read each step before you confirm it."
    if (( ! DRY_RUN )) && ! confirm "Continue on $os_name?"; then die "stopped"; fi
  fi
  if [[ "$arch" != x86_64 ]]; then
    warn "Built for x86_64. Found $arch."
  fi
  ports="$(ss -tlnp 2>/dev/null | awk '/sshd/ {print $4}' | tr '\n' ' ')"
  say "sshd listens on: ${ports:-(not detected)}"
  if [[ -n "$ports" ]] && ! grep -q ':22' <<<"$ports"; then
    warn "The OpenSSH ufw profile allows port 22 only. If sshd uses another port, stop now and allow that port first."
  fi
}

summary() {
  printf '\n%s%s  Summary%s\n' "$BOLD" "$BLUE" "$RESET"
  if (( ${#DONE_STEPS[@]} )); then
    say "Done now:"; for s in "${DONE_STEPS[@]}"; do note "  - $s"; done
  fi
  if (( ${#SKIPPED_STEPS[@]} )); then
    say "Skipped:"; for s in "${SKIPPED_STEPS[@]}"; do note "  - $s"; done
  fi
  printf '\n%s%s  Next step%s\n' "$BOLD" "$GREEN" "$RESET"
  if (( DRY_RUN )); then
    say "This was a dry run. Run again without --dry-run to apply the changes."
  else
    say "1. Log in as deploy over SSH (ssh deploy@<this-server>)."
    say "2. Clone the checkout, which holds the setup scripts:"
    # shellcheck disable=SC2016  # single quotes keep $(...) literal in the printed command
    note '   git clone https://github.com/itsgitz/maket.git ~/maket && cd ~/maket && git checkout "$(git tag --sort=-v:refname | head -1)"'
    say "3. Run: bash deploy/setup/02-secrets.sh"
  fi
}

main() {
  parse_args "$@"
  if (( ! DRY_RUN )) && (( EUID != 0 )); then
    die "run as root: sudo bash deploy/setup/01-server.sh (or use --dry-run to preview)"
  fi
  if (( DRY_RUN )) && (( EUID != 0 )); then
    note "dry-run as a non-root user: checks that need root are reported as not done"
  fi

  printf '\n%s%s  maket server setup, part 1 (system)%s\n' "$BOLD" "$BLUE" "$RESET"
  note "Prints each command before it runs. Ctrl-C any time; re-running skips finished steps."
  preflight

  do_step "1. Packages" check_packages action_packages
  do_step "2. deploy user" check_user action_user
  do_step "3. subuid/subgid ranges for deploy" check_subids action_subids
  do_step "4. SSH access for deploy" check_ssh_keys action_ssh_keys
  do_step "5. Linger (services keep running after logout)" check_linger action_linger
  do_step "6. Unprivileged port 443" check_sysctl action_sysctl
  do_step "7. Swap (2 GB)" check_swap action_swap
  do_step "8. Firewall (ufw, Cloudflare 443 only)" check_ufw action_ufw
  do_step "9. Cloudflare range refresh script (optional)" check_cf_script action_cf_script

  summary
}

main "$@"
