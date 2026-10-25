#!/usr/bin/env bash
#
# 03-install.sh: installs and starts maket (docs/deploy.md sections 5, 6 and 8).
#
# Run as the deploy user, after 01-server.sh (as root) and 02-secrets.sh:
#   bash deploy/setup/03-install.sh
#   bash deploy/setup/03-install.sh --release v0.2.2 --dry-run
#
# Steps. Each one prints its exact commands and asks [y/N] before it changes
# anything. A step that is already in place is skipped.
#   1. Checkout      clone or fetch ~/maket, check out the release tag (detached)
#   2. Image         podman build -f deploy/Containerfile -t localhost/maket:latest
#   3. Units         install maket.kube and maket-pod.yaml, daemon-reload
#   4. Start         systemctl --user start maket
#   5. Verify        health check through Caddy on loopback (not part of the prompts)
#   6. restic        restic init, if the R2 repository is not initialised yet
#   7. Timer         install and enable maket-backup.timer (nightly)
#   8. First backup  run deploy/backup.sh once (optional)
#
# Options:
#   --release TAG  release to deploy (default: the newest vX.Y.Z tag on origin;
#                  MAKET_RELEASE works too)
#   --dry-run      print every command and change nothing. Works as any user.
#   --yes          answer yes to every prompt.
#   -h, --help     show this help.

set -euo pipefail

REPO_URL="https://github.com/itsgitz/maket.git"
CHECKOUT="$HOME/maket"
QUADLET_DIR="$HOME/.config/containers/systemd"
USER_UNIT_DIR="$HOME/.config/systemd/user"
CONF_DIR="$HOME/.config/maket"
CERT_DIR="$CONF_DIR/certs"
BACKUP_ENV="$CONF_DIR/backup.env"
APP_HOST="app.kotakserver.my.id"
MIN_PODMAN="4.4.0"

DRY_RUN=0
ASSUME_YES=0
PLAN=0
RELEASE="${MAKET_RELEASE:-}"
DONE_STEPS=()
SKIPPED_STEPS=()

# ──────────────────────────────────────────────────────────────────────────
# Output helpers and step runner (same as 01-server.sh)
# ──────────────────────────────────────────────────────────────────────────

if [[ -t 1 ]] && command -v tput >/dev/null 2>&1 && [[ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]]; then
  BOLD=$(tput bold 2>/dev/null || true); DIM=$(tput dim 2>/dev/null || true); RESET=$(tput sgr0 2>/dev/null || true)
  BLUE=$(tput setaf 4 2>/dev/null || true); GREEN=$(tput setaf 2 2>/dev/null || true); YELLOW=$(tput setaf 3 2>/dev/null || true); RED=$(tput setaf 1 2>/dev/null || true)
else
  BOLD=""; DIM=""; RESET=""; BLUE=""; GREEN=""; YELLOW=""; RED=""
fi

say()  { printf '  %s\n' "$1"; }
note() { printf '  %s%s%s\n' "$DIM" "$1" "$RESET"; }
warn() { printf '  %s⚠ %s%s\n' "$YELLOW" "$1" "$RESET"; }
die()  { printf '  %s✗ %s%s\n' "$RED" "$1" "$RESET" >&2; exit 1; }
ok()   { printf '  %s✓ %s%s\n' "$GREEN" "$1" "$RESET"; }

# problem MSG: a failed precondition. Stops the script on a real run and
# only warns during a dry run, so the plan can still be reviewed.
problem() {
  if (( DRY_RUN )); then warn "$1"; else die "$1"; fi
}

# confirm "question": y/N gate. --yes answers yes.
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

# run_in DIR CMD...: like run, from another directory.
run_in() {
  local dir="$1"; shift
  printf '    %s+ (cd %s && %s)%s\n' "$DIM" "$dir" "$*" "$RESET"
  if (( PLAN )); then return 0; fi
  (cd "$dir" && "$@")
}

# do_step TITLE CHECK ACTION. CHECK returns 0 when the step is already done.
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
  ok "done"
}

# version_ge A B: true when version A >= B (dotted numbers).
version_ge() {
  [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]]
}

# restic_run ARGS...: restic with backup.env loaded, in a subshell.
restic_run() {
  # shellcheck source=/dev/null
  ( set -a; . "$BACKUP_ENV"; set +a; restic "$@" )
}

# ──────────────────────────────────────────────────────────────────────────
# Preflight: read-only checks, shown before any step
# ──────────────────────────────────────────────────────────────────────────

resolve_release() {
  if [[ -n "$RELEASE" ]]; then return 0; fi
  RELEASE="$(git ls-remote --tags --refs "$REPO_URL" 2>/dev/null |
    sed -n 's#.*refs/tags/##p' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n1 || true)"
  if [[ -z "$RELEASE" ]]; then
    problem "could not find a release tag on $REPO_URL. Pass --release vX.Y.Z."
  fi
}

preflight() {
  local podman_version tool f mode ports
  if (( EUID == 0 )); then
    problem "run this as the deploy user, not root"
  fi
  if [[ "$(id -un)" != deploy ]]; then
    problem "you are $(id -un). Run this as the deploy user: paths assume ~/maket and ~/.config/maket"
  fi
  if (( DRY_RUN )); then
    note "dry-run: podman, systemd and network state are read but not changed"
  fi

  for tool in git curl restic sqlite3 openssl podman sha256sum; do
    command -v "$tool" >/dev/null 2>&1 || problem "$tool is not installed (run 01-server.sh)"
  done

  podman_version="$(podman --version 2>/dev/null | awk '{print $3}' || true)"
  if [[ -z "$podman_version" ]]; then
    problem "podman did not report a version"
  elif version_ge "$podman_version" "$MIN_PODMAN"; then
    ok "podman $podman_version (>= $MIN_PODMAN required for .kube units)"
  else
    problem "podman $podman_version is too old. Quadlet .kube units need $MIN_PODMAN or newer."
  fi
  if version_ge "$podman_version" 5.0.0; then
    warn "Podman 5 uses pasta for rootless networking. Client addresses in Caddy can differ from Podman 4.9. See the log check after start."
  fi

  if [[ "$(cat /proc/sys/net/ipv4/ip_unprivileged_port_start 2>/dev/null || echo unknown)" == 443 ]]; then
    ok "unprivileged port start is 443"
  else
    problem "net.ipv4.ip_unprivileged_port_start is not 443 (run 01-server.sh, step 6)"
  fi

  if ! grep -q '^deploy:' /etc/subuid 2>/dev/null || ! grep -q '^deploy:' /etc/subgid 2>/dev/null; then
    problem "no subuid/subgid range for deploy (run 01-server.sh, step 3)"
  fi

  if (( ! DRY_RUN )); then
    if [[ "$(loginctl show-user "$(id -un)" --property=Linger --value 2>/dev/null)" != yes ]]; then
      problem "linger is off for deploy (run 01-server.sh, step 5)"
    fi
    if ! systemctl --user show-environment >/dev/null 2>&1; then
      problem "systemctl --user does not work here. Log in over SSH as deploy (not with sudo su)."
    fi
  fi

  for f in "$CONF_DIR/maket-env.yaml" "$BACKUP_ENV" "$CONF_DIR/restic-password" \
           "$CERT_DIR/origin.pem" "$CERT_DIR/origin.key"; do
    if [[ ! -e "$f" ]]; then
      problem "missing $f (run 02-secrets.sh)"
      continue
    fi
    mode="$(stat -c %a "$f")"
    if [[ "$mode" != 600 && "$mode" != 400 ]]; then
      problem "$f is mode $mode, expected 600"
    fi
  done
  for f in "$CONF_DIR" "$CERT_DIR"; do
    [[ -d "$f" ]] && { mode="$(stat -c %a "$f")"; [[ "$mode" == 700 ]] || problem "$f is mode $mode, expected 700"; }
  done
  ok "secret files are present with mode 600 (checked: maket-env.yaml, backup.env, restic-password, origin.pem, origin.key)"

  ports="$(ss -tlnp 2>/dev/null | awk '/:443 / {print $4}' | tr '\n' ' ' || true)"
  if [[ -n "$ports" ]]; then note "something already listens on 443: $ports"; fi
}

# verify_config compares the saved ConfigMap with the template in the checkout.
verify_config() {
  local tmpl="$CHECKOUT/deploy/quadlet/maket-env.example.yaml" real="$CONF_DIR/maket-env.yaml"
  if [[ ! -f "$tmpl" || ! -f "$real" ]]; then
    note "config key check skipped (template or saved ConfigMap not present yet)"
    return 0
  fi
  if ! diff <(grep -E '^  [A-Z0-9_]+:' "$tmpl" | cut -d: -f1) \
            <(grep -E '^  [A-Z0-9_]+:' "$real" | cut -d: -f1) >/dev/null; then
    problem "maket-env.yaml keys differ from deploy/quadlet/maket-env.example.yaml. Re-run 02-secrets.sh after checking the template."
  else
    ok "maket-env.yaml has the same keys as the template"
  fi
  if grep -qE '^  [A-Z0-9_]+: "replace-me"' "$real"; then
    problem "maket-env.yaml still has replace-me values. Re-run 02-secrets.sh."
  fi
  local tok zone
  tok="$(grep -E '^  MAKET_CF_API_TOKEN:' "$real" | sed 's/.*: "\(.*\)"/\1/')"
  zone="$(grep -E '^  MAKET_CF_ZONE_ID:' "$real" | sed 's/.*: "\(.*\)"/\1/')"
  if [[ -n "$tok" && -z "$zone" ]] || [[ -z "$tok" && -n "$zone" ]]; then
    problem "MAKET_CF_API_TOKEN and MAKET_CF_ZONE_ID must both be set or both empty"
  fi
}

# ──────────────────────────────────────────────────────────────────────────
# Steps
# ──────────────────────────────────────────────────────────────────────────

check_checkout() {
  [[ -d "$CHECKOUT/.git" ]] &&
    [[ "$(git -C "$CHECKOUT" describe --tags --exact-match 2>/dev/null)" == "$RELEASE" ]]
}
action_checkout() {
  if [[ ! -d "$CHECKOUT/.git" ]]; then
    run git clone "$REPO_URL" "$CHECKOUT"
  else
    if (( ! PLAN )) && [[ -n "$(git -C "$CHECKOUT" status --porcelain)" ]]; then
      die "$CHECKOUT has local changes. Commit or discard them first; this script will not touch them."
    fi
    run git -C "$CHECKOUT" fetch --tags origin
  fi
  run git -C "$CHECKOUT" checkout --detach "$RELEASE"
}

# The image is always rebuilt. Unchanged layers are cached, so this is quick.
check_image() { false; }
action_image() {
  run_in "$CHECKOUT" podman build -f deploy/Containerfile \
    --build-arg "MAKET_VERSION=$RELEASE" -t localhost/maket:latest .
}

# Installed units must match the checkout, with the real home directory in the pod YAML.
pod_yaml_expected() {
  sed "s|/home/deploy|$(printf '%s' "$HOME" | sed 's/[&|\\]/\\&/g')|g" "$CHECKOUT/deploy/quadlet/maket-pod.yaml"
}
check_units() {
  cmp -s "$CHECKOUT/deploy/quadlet/maket.kube" "$QUADLET_DIR/maket.kube" &&
    pod_yaml_expected | cmp -s - "$QUADLET_DIR/maket-pod.yaml"
}
action_units() {
  run mkdir -p "$QUADLET_DIR"
  run cp "$CHECKOUT/deploy/quadlet/maket.kube" "$QUADLET_DIR/maket.kube"
  printf '    %s+ pod YAML: copy maket-pod.yaml, replace /home/deploy with %s%s\n' "$DIM" "$HOME" "$RESET"
  if (( ! PLAN )); then
    pod_yaml_expected > "$QUADLET_DIR/maket-pod.yaml"
    grep -q "$HOME/maket/deploy/Caddyfile" "$QUADLET_DIR/maket-pod.yaml" ||
      die "the pod YAML does not point at $HOME/maket. Check the copy."
  fi
  run systemctl --user daemon-reload
}

check_running() { systemctl --user is-active --quiet maket; }
action_start() {
  run systemctl --user start maket
  if (( PLAN )); then return 0; fi
  local _
  for _ in $(seq 1 60); do
    if systemctl --user is-active --quiet maket; then
      systemctl --user status maket --no-pager || true
      return 0
    fi
    sleep 2
  done
  journalctl --user -u maket -n 40 --no-pager || true
  die "maket did not become active in 120 seconds. Check the log above and docs/deploy.md section 9."
}

# Health check through Caddy on loopback. The origin certificate is a Cloudflare
# Origin CA certificate, which no public CA chain verifies, so curl uses -k here.
# The real public check (with a verified certificate) is docs/deploy.md section 6,
# once DNS and Cloudflare are live.
verify_local() {
  local body
  printf '    %s+ curl -fsS -k --resolve %s:443:127.0.0.1 https://%s/api/healthz%s\n' "$DIM" "$APP_HOST" "$APP_HOST" "$RESET"
  printf '    %s+ curl -sSI -k --resolve %s:443:127.0.0.1 https://%s/ | head -n1%s\n' "$DIM" "$APP_HOST" "$APP_HOST" "$RESET"
  printf '    %s+ curl -s -o /dev/null -w "%%{http_code}" -k --resolve unknown.example.net:443:127.0.0.1 https://unknown.example.net/%s\n' "$DIM" "$RESET"
  if (( DRY_RUN )); then note "dry-run: checks not run"; return 0; fi
  body="$(curl -fsS -k --max-time 10 --resolve "$APP_HOST:443:127.0.0.1" "https://$APP_HOST/api/healthz")" ||
    die "health check failed. Read: journalctl --user -u maket -n 80 --no-pager"
  if [[ "$body" == *'"status":"ok"'* ]]; then ok "healthz: $body"; else die "unexpected healthz response: $body"; fi
  ok "app host: $(curl -sSI -k --max-time 10 --resolve "$APP_HOST:443:127.0.0.1" "https://$APP_HOST/" | head -n1 | tr -d '\r')"
  ok "unknown host gets: $(curl -s -o /dev/null -w '%{http_code}' -k --max-time 10 --resolve unknown.example.net:443:127.0.0.1 https://unknown.example.net/)"
  note "Caddy does not write access logs by default. To see client addresses, see the next steps."
}

check_repo_init() { [[ -f "$BACKUP_ENV" ]] && restic_run cat config >/dev/null 2>&1; }
action_restic_init() {
  printf '    %s+ (set -a; . %s; restic init)%s\n' "$DIM" "$BACKUP_ENV" "$RESET"
  if (( PLAN )); then return 0; fi
  restic_run init
}

check_timer() {
  cmp -s "$CHECKOUT/deploy/systemd/maket-backup.service" "$USER_UNIT_DIR/maket-backup.service" &&
    cmp -s "$CHECKOUT/deploy/systemd/maket-backup.timer" "$USER_UNIT_DIR/maket-backup.timer" &&
    systemctl --user is-enabled --quiet maket-backup.timer
}
action_timer() {
  run mkdir -p "$USER_UNIT_DIR"
  run cp "$CHECKOUT/deploy/systemd/maket-backup.service" "$CHECKOUT/deploy/systemd/maket-backup.timer" "$USER_UNIT_DIR/"
  run systemctl --user daemon-reload
  run systemctl --user enable --now maket-backup.timer
  run systemctl --user list-timers maket-backup.timer --no-pager
}

check_never() { false; }
action_first_backup() {
  run "$CHECKOUT/deploy/backup.sh"
  if (( PLAN )); then return 0; fi
  restic_run snapshots
}

# ──────────────────────────────────────────────────────────────────────────
# Main
# ──────────────────────────────────────────────────────────────────────────

usage() {
  sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
}

parse_args() {
  while (( $# )); do
    case "$1" in
      --dry-run|-n) DRY_RUN=1 ;;
      --yes|-y) ASSUME_YES=1 ;;
      --release) shift; [[ $# -gt 0 ]] || die "--release needs a tag"; RELEASE="$1" ;;
      -h|--help) usage; exit 0 ;;
      *) die "unknown option: $1 (see --help)" ;;
    esac
    shift
  done
}

summary() {
  printf '\n%s%s  Summary%s\n' "$BOLD" "$BLUE" "$RESET"
  say "Release: $RELEASE"
  if (( ${#DONE_STEPS[@]} )); then
    say "Done now:"; for s in "${DONE_STEPS[@]}"; do note "  - $s"; done
  fi
  if (( ${#SKIPPED_STEPS[@]} )); then
    say "Skipped:"; for s in "${SKIPPED_STEPS[@]}"; do note "  - $s"; done
  fi
  printf '\n%s%s  Next steps (by hand)%s\n' "$BOLD" "$GREEN" "$RESET"
  say "1. Sign in: open https://$APP_HOST and sign in with GitHub (an allowed user)."
  say "2. Create an API key under API keys in the dashboard, then from your machine:"
  note "   maket login && maket push ./index.html && maket ls   (see docs/cli.md)"
  say "3. Check client addresses in Caddy (see the note below)."
  say "4. Restore test: docs/deploy.md section 8, Restore. It overwrites the live data volume,"
  say "   so run it on a fresh install or right after a verified backup, never casually."
  say "5. Public check once DNS and Cloudflare are live: curl -fsS https://$APP_HOST/api/healthz"
  note "   Client addresses: podman logs maket-caddy shows Caddy output. The Caddyfile has no access"
  note "   log, so enable one first if you need remote_ip values. Podman 5 pasta can show a gateway"
  note "   address instead of Cloudflare's. Visitor IPs arrive in the CF-Connecting-IP header."
}

main() {
  parse_args "$@"
  if (( DRY_RUN == 0 )) && (( EUID == 0 )); then
    die "run as the deploy user, not root (use --dry-run to preview as any user)"
  fi

  printf '\n%s%s  maket install (deploy user)%s\n' "$BOLD" "$BLUE" "$RESET"
  note "Prints each command before it runs. Re-running skips finished steps."
  preflight
  resolve_release
  say "Release to deploy: $RELEASE"

  do_step "1. Checkout $RELEASE in $CHECKOUT" check_checkout action_checkout
  verify_config
  do_step "2. Build image localhost/maket:latest" check_image action_image
  do_step "3. Install Quadlet units and reload systemd" check_units action_units
  do_step "4. Start maket" check_running action_start
  verify_local
  do_step "5. restic repository (R2) init" check_repo_init action_restic_init
  do_step "6. Nightly backup timer" check_timer action_timer
  do_step "7. First backup now (optional)" check_never action_first_backup

  summary
}

main "$@"
