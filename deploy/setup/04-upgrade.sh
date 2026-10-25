#!/usr/bin/env bash
#
# 04-upgrade.sh: moves a running install to a newer release (docs/deploy.md section 7).
#
# Run as the deploy user, after 03-install.sh has run once:
#   bash deploy/setup/04-upgrade.sh
#   bash deploy/setup/04-upgrade.sh --release v0.2.2 --dry-run
#
# Steps. Each one prints its exact commands and asks [y/N] before it changes
# anything. The data volume is not touched; migrations run when the new container starts.
#   1. Checkout   fetch tags, check out the release tag (detached); refuses local changes
#   2. Units      refresh maket.kube and maket-pod.yaml if they changed, daemon-reload
#   3. Image      podman build (the old container keeps running during the build)
#   4. Restart    systemctl --user restart maket
#   5. Verify     health check through Caddy on loopback
#
# Rollback is manual, on purpose: check out the previous tag, build, restart.
# The previous tag is printed at the start.
#
# Options:
#   --release TAG  release to deploy (default: the newest vX.Y.Z tag on origin)
#   --dry-run      print every command and change nothing. Works as any user.
#   --yes          answer yes to every prompt.
#   -h, --help     show this help.

set -euo pipefail

REPO_URL="https://github.com/itsgitz/maket.git"
CHECKOUT="$HOME/maket"
QUADLET_DIR="$HOME/.config/containers/systemd"
CONF_DIR="$HOME/.config/maket"
APP_HOST="app.kotakserver.my.id"

DRY_RUN=0
ASSUME_YES=0
PLAN=0
RELEASE="${MAKET_RELEASE:-}"
PREVIOUS=""
DONE_STEPS=()
SKIPPED_STEPS=()

# ──────────────────────────────────────────────────────────────────────────
# Output helpers and step runner (same as 03-install.sh)
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

problem() {
  if (( DRY_RUN )); then warn "$1"; else die "$1"; fi
}

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

run() {
  printf '    %s+ %s%s\n' "$DIM" "$*" "$RESET"
  if (( PLAN )); then return 0; fi
  "$@"
}

run_in() {
  local dir="$1"; shift
  printf '    %s+ (cd %s && %s)%s\n' "$DIM" "$dir" "$*" "$RESET"
  if (( PLAN )); then return 0; fi
  (cd "$dir" && "$@")
}

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

version_ge() {
  [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]]
}

# ──────────────────────────────────────────────────────────────────────────
# Preflight
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
  local tool podman_version
  if (( DRY_RUN == 0 )) && [[ "$(id -un)" != deploy ]]; then
    die "run this as the deploy user (you are $(id -un))"
  fi
  if (( EUID == 0 )); then problem "do not run this as root"; fi
  for tool in git curl podman sha256sum; do
    command -v "$tool" >/dev/null 2>&1 || problem "$tool is not installed"
  done
  podman_version="$(podman --version 2>/dev/null | awk '{print $3}' || true)"
  if [[ -z "$podman_version" ]] || ! version_ge "$podman_version" 4.4.0; then
    problem "podman 4.4 or newer is needed (found: ${podman_version:-none})"
  fi
  [[ -d "$CHECKOUT/.git" ]] || problem "no checkout at $CHECKOUT. Run 03-install.sh first."
  [[ -f "$CONF_DIR/maket-env.yaml" ]] || problem "no $CONF_DIR/maket-env.yaml. Run 03-install.sh first."
  if [[ -d "$CHECKOUT/.git" ]]; then
    PREVIOUS="$(git -C "$CHECKOUT" describe --tags --exact-match 2>/dev/null || git -C "$CHECKOUT" rev-parse --short HEAD)"
  else
    PREVIOUS="(no checkout)"
  fi
  note "current checkout: $PREVIOUS"
  if (( DRY_RUN )); then
    note "dry-run: nothing is changed"
  fi
}

# pod_yaml_expected: the pod YAML as 03-install.sh installs it.
pod_yaml_expected() {
  sed "s|/home/deploy|$(printf '%s' "$HOME" | sed 's/[&|\\]/\\&/g')|g" "$CHECKOUT/deploy/quadlet/maket-pod.yaml"
}

# ──────────────────────────────────────────────────────────────────────────
# Steps
# ──────────────────────────────────────────────────────────────────────────

check_checkout() {
  [[ "$(git -C "$CHECKOUT" describe --tags --exact-match 2>/dev/null)" == "$RELEASE" ]]
}
action_checkout() {
  if (( ! PLAN )) && [[ -n "$(git -C "$CHECKOUT" status --porcelain)" ]]; then
    die "$CHECKOUT has local changes. Commit or discard them first; this script will not touch them."
  fi
  run git -C "$CHECKOUT" fetch --tags origin
  run git -C "$CHECKOUT" checkout --detach "$RELEASE"
}

# Config keys in the template that the saved ConfigMap lacks. Adding them needs
# 02-secrets.sh (or an edit by hand); this only reports.
check_config_keys() {
  local tmpl="$CHECKOUT/deploy/quadlet/maket-env.example.yaml" real="$CONF_DIR/maket-env.yaml" missing
  [[ -f "$tmpl" && -f "$real" ]] || return 0
  missing="$(comm -23 <(grep -E '^  [A-Z0-9_]+:' "$tmpl" | cut -d: -f1 | sort) \
                      <(grep -E '^  [A-Z0-9_]+:' "$real" | cut -d: -f1 | sort) | tr '\n' ' ')"
  if [[ -n "$missing" ]]; then
    warn "the new template has keys missing from $real: $missing"
    warn "Add them (see 02-secrets.sh and the template) before the restart, or the server may refuse to start."
    if (( ! DRY_RUN )) && ! confirm "Continue without them?"; then die "stopped before any change"; fi
  else
    ok "maket-env.yaml has every key in the new template"
  fi
}

check_units() {
  cmp -s "$CHECKOUT/deploy/quadlet/maket.kube" "$QUADLET_DIR/maket.kube" &&
    pod_yaml_expected | cmp -s - "$QUADLET_DIR/maket-pod.yaml"
}
action_units() {
  run cp "$CHECKOUT/deploy/quadlet/maket.kube" "$QUADLET_DIR/maket.kube"
  printf '    %s+ pod YAML: copy maket-pod.yaml, replace /home/deploy with %s%s\n' "$DIM" "$HOME" "$RESET"
  if (( ! PLAN )); then
    pod_yaml_expected > "$QUADLET_DIR/maket-pod.yaml"
  fi
  run systemctl --user daemon-reload
}

check_image() { false; }
action_image() {
  run_in "$CHECKOUT" podman build -f deploy/Containerfile \
    --build-arg "MAKET_VERSION=$RELEASE" -t localhost/maket:latest .
}

check_restarted() { false; }
action_restart() {
  run systemctl --user restart maket
  if (( PLAN )); then return 0; fi
  local _
  for _ in $(seq 1 60); do
    if systemctl --user is-active --quiet maket; then return 0; fi
    sleep 2
  done
  journalctl --user -u maket -n 40 --no-pager || true
  die "maket is not active after 120 seconds. Roll back (see the end of this script's output)."
}

verify_local() {
  local body
  printf '    %s+ curl -fsS -k --resolve %s:443:127.0.0.1 https://%s/api/healthz%s\n' "$DIM" "$APP_HOST" "$APP_HOST" "$RESET"
  if (( DRY_RUN )); then note "dry-run: check not run"; return 0; fi
  body="$(curl -fsS -k --max-time 10 --resolve "$APP_HOST:443:127.0.0.1" "https://$APP_HOST/api/healthz")" ||
    die "health check failed after the restart. Read: journalctl --user -u maket -n 80 --no-pager"
  if [[ "$body" == *'"status":"ok"'* ]]; then ok "healthz: $body"; else die "unexpected healthz response: $body"; fi
}

# ──────────────────────────────────────────────────────────────────────────
# Main
# ──────────────────────────────────────────────────────────────────────────

usage() {
  sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
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

main() {
  parse_args "$@"
  printf '\n%s%s  maket upgrade (deploy user)%s\n' "$BOLD" "$BLUE" "$RESET"
  preflight
  resolve_release
  say "Release to deploy: $RELEASE (from $PREVIOUS)"
  if [[ "$RELEASE" == "$PREVIOUS" ]]; then
    note "already on $RELEASE. Steps that are already in place are skipped."
  fi

  do_step "1. Checkout $RELEASE" check_checkout action_checkout
  check_config_keys
  do_step "2. Refresh Quadlet units (only if they changed)" check_units action_units
  do_step "3. Build image localhost/maket:latest" check_image action_image
  do_step "4. Restart maket (data volume is kept; migrations run on start)" check_restarted action_restart
  verify_local

  printf '\n%s%s  Summary%s\n' "$BOLD" "$BLUE" "$RESET"
  say "Release: $RELEASE (was $PREVIOUS)"
  if (( ${#DONE_STEPS[@]} )); then
    say "Done now:"; for s in "${DONE_STEPS[@]}"; do note "  - $s"; done
  fi
  if (( ${#SKIPPED_STEPS[@]} )); then
    say "Skipped:"; for s in "${SKIPPED_STEPS[@]}"; do note "  - $s"; done
  fi
  say "Check the logs: journalctl --user -u maket -n 100 --no-pager"
  note "Rollback, if the new release fails: cd ~/maket && git checkout --detach $PREVIOUS,"
  note "then run this script again with --release $PREVIOUS. The data volume is unchanged by the code,"
  note "but migrations may already have run: restore from backup if the schema is incompatible."
}

main "$@"
