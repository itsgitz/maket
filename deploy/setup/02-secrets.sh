#!/usr/bin/env bash
#
# 02-secrets.sh: interactive wizard for the steps only a human can do in the
# Cloudflare and GitHub dashboards (docs/deploy.md sections 3, 4 and 8).
#
# Run as the deploy user, not root:
#   bash deploy/setup/02-secrets.sh
#
# It writes, all in ~/.config/maket (directory mode 700, files mode 600):
#   maket-env.yaml     the api ConfigMap (same format as deploy/quadlet/maket-env.example.yaml)
#   backup.env         restic settings for Cloudflare R2
#   restic-password    the restic repository password
#   certs/origin.pem   Cloudflare Origin Certificate
#   certs/origin.key   its private key
#
# Secrets are read with hidden input and are never printed or logged. Re-run it
# any time: saved values are offered as defaults (secrets are shown masked).
# Nothing here changes the system; it does not need sudo.

set -euo pipefail

CONF_DIR="$HOME/.config/maket"
CERT_DIR="$CONF_DIR/certs"
CFG_FILE="$CONF_DIR/maket-env.yaml"
BACKUP_ENV="$CONF_DIR/backup.env"
RESTIC_PW="$CONF_DIR/restic-password"

case "${1:-}" in
  "") ;;
  -h|--help)
    sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
    exit 0 ;;
  *) printf 'unknown option: %s (this wizard is interactive; see --help)\n' "$1" >&2; exit 1 ;;
esac

if (( EUID == 0 )); then
  printf 'Refusing to run as root. Log in as the deploy user and run this again.\n' >&2
  exit 1
fi

# ──────────────────────────────────────────────────────────────────────────
# Wizard library: copied from the /wizard template. Changes: values are kept
# in memory (CURRENT) and saved by save_cfg / save_backup, because this wizard
# writes three files in two formats. ask/ask_secret read CURRENT.
# ──────────────────────────────────────────────────────────────────────────

if [[ -t 1 ]] && command -v tput >/dev/null 2>&1 && [[ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]]; then
  BOLD=$(tput bold 2>/dev/null || true); DIM=$(tput dim 2>/dev/null || true); RESET=$(tput sgr0 2>/dev/null || true)
  BLUE=$(tput setaf 4 2>/dev/null || true); GREEN=$(tput setaf 2 2>/dev/null || true); YELLOW=$(tput setaf 3 2>/dev/null || true); RED=$(tput setaf 1 2>/dev/null || true)
else
  BOLD=""; DIM=""; RESET=""; BLUE=""; GREEN=""; YELLOW=""; RED=""
fi

# Author sets this at the top of the stages section.
TOTAL_STAGES=0

_STAGE_INDEX=0
declare -A CURRENT=()
WRITTEN_ENV=()    # files written this run
SKIPPED=()        # things left for the human to do

# Undo a hidden-input step if the script is interrupted.
trap 'stty echo 2>/dev/null || true' EXIT

# _clear wipes the terminal so only the current step is on screen. No-op when
# output isn't a terminal, so piped logs stay readable.
_clear() {
  [[ -t 1 ]] || return 0
  if command -v tput >/dev/null 2>&1; then tput clear 2>/dev/null || printf '\033[2J\033[3J\033[H'; else printf '\033[2J\033[3J\033[H'; fi
}

# banner "Title" shows the opening frame: what this wizard does.
banner() {
  _clear
  printf '\n%s%s  %s%s\n' "$BOLD" "$BLUE" "$1" "$RESET"
  printf '%s  %s stages%s\n\n' "$DIM" "$TOTAL_STAGES" "$RESET"
  printf '%s  You drive the browser; this wizard tells you exactly what to do and\n' "$DIM"
  printf '  captures the values you copy back. Stop any time with Ctrl-C and re-run\n'
  printf '  later, since it remembers values already saved.%s\n' "$RESET"
  pause "Ready to start?"
}

# stage "Name" clears the screen, then announces a stage and shows progress.
stage() {
  _clear
  _STAGE_INDEX=$((_STAGE_INDEX + 1))
  printf '\n%s%s▸ Stage %s/%s · %s%s\n' \
    "$BOLD" "$BLUE" "$_STAGE_INDEX" "$TOTAL_STAGES" "$1" "$RESET"
}

# say "..." prints a plain instruction line.
say()  { printf '  %s\n' "$1"; }
# step "..." is a numbered-feeling action the human takes in the browser.
step() { printf '  %s•%s %s\n' "$BLUE" "$RESET" "$1"; }
note() { printf '  %s%s%s\n' "$DIM" "$1" "$RESET"; }
warn() { printf '  %s⚠ %s%s\n' "$YELLOW" "$1" "$RESET"; }
die()  { printf '  %s✗ %s%s\n' "$RED" "$1" "$RESET" >&2; exit 1; }

# open_url URL opens it in the human's browser, cross-platform incl. WSL.
open_url() {
  local url="$1"
  printf '  %s↗ opening%s %s\n' "$GREEN" "$RESET" "$url"
  { if   command -v wslview     >/dev/null 2>&1; then wslview "$url"
    elif command -v explorer.exe >/dev/null 2>&1; then explorer.exe "$url"
    elif command -v xdg-open    >/dev/null 2>&1; then xdg-open "$url"
    elif command -v open        >/dev/null 2>&1; then open "$url"
    else false; fi
  } >/dev/null 2>&1 || warn "couldn't open a browser, so visit it manually: $url"
}

# pause "msg" waits for the human to confirm they've done the manual part.
pause() {
  printf '  %s%s%s ' "$DIM" "${1:-Press Enter to continue}" "$RESET"
  read -r _ || true
}

# confirm "question" is a y/N gate; returns success on yes.
confirm() {
  local reply=""
  printf '  %s? %s [y/N] ' "$YELLOW" "$1"
  read -r reply || true
  [[ "$reply" =~ ^[Yy] ]]
}

# _existing KEY: the saved value of KEY (from the files above), if any.
_existing() {
  [[ -n "${CURRENT[$1]-}" ]] || return 1
  printf '%s' "${CURRENT[$1]}"
}

# cv KEY prints the current value, or nothing.
cv() { printf '%s' "${CURRENT[$1]-}"; }

# put KEY VALUE stores a value in memory. save_cfg / save_backup write it out.
put() { CURRENT["$1"]="$2"; }

# mask VALUE shows only the last four characters of a secret.
mask() {
  local v="$1"
  if (( ${#v} <= 4 )); then printf '****'; else printf '****%s' "${v: -4}"; fi
}

# ask KEY "Prompt" reads a value into $KEY. Enter keeps the saved value.
# Visible input (non-secret), edited with Readline. Fails at EOF with no input.
ask() {
  local key="$1" prompt="$2" current input s=$'\001' e=$'\002' p rc=0
  current=$(_existing "$key" || true)
  # Readline needs the colour codes wrapped in \001..\002 to place the cursor.
  p="  $s$BOLD$e$prompt$s$RESET$e "
  [[ -n "$current" ]] && p+="$s$DIM${e}[Enter keeps current]$s$RESET$e "
  read -e -r -p "$p" input || rc=1
  if (( rc )) && [[ -z "$input" ]]; then return 1; fi
  [[ -z "$input" && -n "$current" ]] && input="$current"
  printf -v "$key" '%s' "$input"
}

# ask_secret KEY "Prompt" is like ask, but input is hidden. A saved value is
# shown masked.
ask_secret() {
  local key="$1" prompt="$2" current input rc=0
  current=$(_existing "$key" || true)
  if [[ -n "$current" ]]; then
    printf '  %s%s%s %s[Enter keeps current: %s]%s ' "$BOLD" "$prompt" "$RESET" "$DIM" "$(mask "$current")" "$RESET"
  else
    printf '  %s%s%s ' "$BOLD" "$prompt" "$RESET"
  fi
  read -rs input || rc=1
  printf '\n'
  if (( rc )) && [[ -z "$input" ]]; then return 1; fi
  [[ -z "$input" && -n "$current" ]] && input="$current"
  printf -v "$key" '%s' "$input"
}

# need KEY "Prompt" REGEX "Hint" [secret] asks until the value matches REGEX,
# then stores it. Values that could break the file format are refused by the regexes.
need() {
  local key="$1" prompt="$2" re="$3" hint="$4" secret="${5:-0}"
  while :; do
    if (( secret )); then ask_secret "$key" "$prompt" || die "input closed"
    else ask "$key" "$prompt" || die "input closed"; fi
    if [[ -n "${!key}" && "${!key}" =~ $re ]]; then
      put "$key" "${!key}"
      return 0
    fi
    warn "not accepted: $hint"
  done
}

# finish clears, then shows a closing summary of everything configured.
finish() {
  _clear
  printf '\n%s%s  ✓ Secrets step complete%s\n' "$BOLD" "$GREEN" "$RESET"
  (( ${#WRITTEN_ENV[@]} )) && note "wrote: ${WRITTEN_ENV[*]}"
  if (( ${#SKIPPED[@]} )); then
    printf '\n'; warn "still to do by hand:"
    for s in "${SKIPPED[@]}"; do note "  - $s"; done
  fi
  printf '\n'
}

# ──────────────────────────────────────────────────────────────────────────
# Files: load saved values, write them back
# ──────────────────────────────────────────────────────────────────────────

load_existing() {
  local line
  if [[ -f "$CFG_FILE" ]]; then
    while IFS= read -r line; do
      if [[ "$line" =~ ^[[:space:]]{2}([A-Z0-9_]+):\ \"(.*)\"$ ]]; then
        CURRENT["${BASH_REMATCH[1]}"]="${BASH_REMATCH[2]}"
      fi
    done < "$CFG_FILE"
  fi
  if [[ -f "$BACKUP_ENV" ]]; then
    while IFS= read -r line; do
      if [[ "$line" =~ ^([A-Z_]+)=\'([^\']*)\'$ ]]; then
        CURRENT["${BASH_REMATCH[1]}"]="${BASH_REMATCH[2]}"
      fi
    done < "$BACKUP_ENV"
  fi
  # RESTIC_REPOSITORY holds the account ID and the bucket. Split them again.
  if [[ "$(cv RESTIC_REPOSITORY)" =~ ^s3:https://([a-f0-9]{32})\.r2\.cloudflarestorage\.com/(.+)$ ]]; then
    put CF_ACCOUNT_ID "${BASH_REMATCH[1]}"
    put R2_BUCKET "${BASH_REMATCH[2]}"
  fi
  : "${CURRENT[R2_BUCKET]:=maket-backups}"
  : "${CURRENT[MAKET_CF_API_TOKEN]:=}"
  : "${CURRENT[MAKET_CF_ZONE_ID]:=}"
}

# save_cfg writes maket-env.yaml. Same format and keys as
# deploy/quadlet/maket-env.example.yaml.
save_cfg() {
  local tmp
  tmp="$(mktemp "$CONF_DIR/.maket-env.XXXXXX")"
  cat > "$tmp" <<EOF
# The api container's environment, used as a Kubernetes ConfigMap.
# Written by deploy/setup/02-secrets.sh. Same keys as deploy/quadlet/maket-env.example.yaml.
# Mode 600. Never commit it.
apiVersion: v1
kind: ConfigMap
metadata:
  name: maket-env
data:
  # Must match the Caddyfile and the Cloudflare DNS records.
  MAKET_APP_HOST: "app.kotakserver.my.id"
  MAKET_PREVIEW_DOMAIN: "cloudshare.my.id"
  MAKET_PUBLIC_SCHEME: "https"

  # Inside the pod, Caddy reaches the API over loopback.
  MAKET_LISTEN_ADDR: "127.0.0.1:8080"
  MAKET_DATA_DIR: "/data"
  # "local" keeps files in /data/storage. "s3" stores them in R2 (see docs/deploy.md).
  MAKET_STORAGE_DRIVER: "local"
  # With "s3", set these and change the driver above. Region "auto" is what R2 uses.
  # MAKET_S3_ENDPOINT: "https://<account-id>.r2.cloudflarestorage.com"
  # MAKET_S3_BUCKET: "maket-prototypes"
  # MAKET_S3_REGION: "auto"
  # MAKET_S3_ACCESS_KEY_ID: "replace-me"
  # MAKET_S3_SECRET_ACCESS_KEY: "replace-me"

  # GitHub OAuth app. Callback URL:
  # https://app.kotakserver.my.id/api/auth/github/callback
  MAKET_GITHUB_CLIENT_ID: "$(cv MAKET_GITHUB_CLIENT_ID)"
  MAKET_GITHUB_CLIENT_SECRET: "$(cv MAKET_GITHUB_CLIENT_SECRET)"
  # Comma-separated GitHub usernames allowed to sign in.
  MAKET_ALLOWED_GITHUB_USERS: "$(cv MAKET_ALLOWED_GITHUB_USERS)"

  # Upload limits (defaults from the brief).
  MAKET_MAX_UPLOAD_MB: "20"
  MAKET_MAX_EXTRACTED_MB: "100"
  MAKET_MAX_FILES: "500"
  MAKET_USER_QUOTA_MB: "1024"

  # Optional Cloudflare cache purge. Set both or neither.
  # The token needs Zone > Cache Purge > Purge permission for cloudshare.my.id.
  MAKET_CF_API_TOKEN: "$(cv MAKET_CF_API_TOKEN)"
  MAKET_CF_ZONE_ID: "$(cv MAKET_CF_ZONE_ID)"
EOF
  chmod 600 "$tmp"
  mv -f "$tmp" "$CFG_FILE"
  WRITTEN_ENV+=("$CFG_FILE")
  printf '  %s✓ wrote%s %s\n' "$GREEN" "$RESET" "$CFG_FILE"
}

# save_backup writes backup.env for deploy/backup.sh.
save_backup() {
  local tmp
  tmp="$(mktemp "$CONF_DIR/.backup-env.XXXXXX")"
  cat > "$tmp" <<EOF
# Settings for deploy/backup.sh (restic to Cloudflare R2). Mode 600. Never commit.
RESTIC_REPOSITORY='s3:https://$(cv CF_ACCOUNT_ID).r2.cloudflarestorage.com/$(cv R2_BUCKET)'
RESTIC_PASSWORD_FILE='$RESTIC_PW'
AWS_ACCESS_KEY_ID='$(cv AWS_ACCESS_KEY_ID)'
AWS_SECRET_ACCESS_KEY='$(cv AWS_SECRET_ACCESS_KEY)'
EOF
  chmod 600 "$tmp"
  mv -f "$tmp" "$BACKUP_ENV"
  WRITTEN_ENV+=("$BACKUP_ENV")
  printf '  %s✓ wrote%s %s\n' "$GREEN" "$RESET" "$BACKUP_ENV"
}

# validate_cert CERT KEY checks that the pair is usable for the origin.
validate_cert() {
  local cert="$1" key="$2" san name
  openssl x509 -in "$cert" -noout >/dev/null 2>&1 || { warn "the certificate is not a PEM certificate"; return 1; }
  openssl pkey -in "$key" -noout </dev/null >/dev/null 2>&1 || { warn "the private key is not a PEM key"; return 1; }
  if [[ "$(openssl x509 -in "$cert" -noout -pubkey | sha256sum)" != "$(openssl pkey -in "$key" -pubout </dev/null | sha256sum)" ]]; then
    warn "the certificate and the private key do not match"
    return 1
  fi
  if ! openssl x509 -in "$cert" -noout -checkend 0 >/dev/null 2>&1; then
    warn "the certificate has expired"
    return 1
  fi
  san="$(openssl x509 -in "$cert" -noout -ext subjectAltName 2>/dev/null || true)"
  for name in app.kotakserver.my.id cloudshare.my.id '*.cloudshare.my.id'; do
    if ! grep -qF "DNS:$name" <<<"$san"; then
      warn "the certificate does not list $name"
      return 1
    fi
  done
  say "certificate OK, expires $(openssl x509 -in "$cert" -noout -enddate | cut -d= -f2)"
}

# paste_block DEST LABEL SECRET: reads lines until a line that says END.
# Hidden input when SECRET=1.
paste_block() {
  local dest="$1" label="$2" secret="$3" line body=""
  say "Paste the $label. Then type END on its own line and press Enter."
  if (( secret )); then stty -echo 2>/dev/null || true; fi
  while IFS= read -r line; do
    [[ "$line" == END ]] && break
    body+="$line"$'\n'
  done
  if (( secret )); then stty echo 2>/dev/null || true; printf '\n'; fi
  umask 077
  printf '%s' "$body" > "$dest"
  chmod 600 "$dest"
}

# ──────────────────────────────────────────────────────────────────────────
# STAGES
# ──────────────────────────────────────────────────────────────────────────

TOTAL_STAGES=11

mkdir -p "$CONF_DIR" "$CERT_DIR"
chmod 700 "$CONF_DIR" "$CERT_DIR"
load_existing
: "${CURRENT[MAKET_GITHUB_CLIENT_ID]:=}"

IPV4_RE='^([0-9]{1,3}\.){3}[0-9]{1,3}$'
ACCOUNT_RE='^[a-f0-9]{32}$'

banner "maket server setup: secrets and dashboards"

# ── 1 ─────────────────────────────────────────────────────────────────────
stage "Welcome and checks"
say "This walks you through the dashboard steps from docs/deploy.md (sections 3, 4, 8)."
say "It writes the secret files under $CONF_DIR. Nothing is printed or logged."
for tool in openssl sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
if [[ "$(id -un)" != deploy ]]; then
  warn "You are $(id -un), not deploy. The paths in the output assume the deploy user's home."
  confirm "Continue anyway?" || die "stopped"
fi
note "Files: $CFG_FILE, $BACKUP_ENV, $RESTIC_PW, $CERT_DIR/origin.{pem,key}"
pause "Press Enter to start the first dashboard step"

# ── 2 ─────────────────────────────────────────────────────────────────────
stage "Cloudflare: zones and DNS records"
say "Type the server's public IPv4 address. It is used only to tell you what to type here."
need VPS_IPV4 "Server public IPv4:" "$IPV4_RE" "enter an IPv4 address such as 203.0.113.10"
open_url "https://dash.cloudflare.com/"
step "Add both zones: kotakserver.my.id and cloudshare.my.id. Free plan."
step "Point each zone's nameservers at Cloudflare at your registrar, then wait for both to show Active."
pause "Press Enter when both zones are Active"
step "On kotakserver.my.id, DNS > Records > Add record: Type A, Name app, IPv4 $VPS_IPV4, Proxy ON (orange cloud)."
step "On cloudshare.my.id, DNS > Records > Add record: Type A, Name *, IPv4 $VPS_IPV4, Proxy ON (orange cloud)."
pause "Press Enter when both records are saved"

# ── 3 ─────────────────────────────────────────────────────────────────────
stage "Cloudflare: SSL/TLS mode and HTTPS"
open_url "https://dash.cloudflare.com/"
step "On BOTH zones: SSL/TLS > Overview > set the mode to Full (strict)."
step "On BOTH zones: SSL/TLS > Edge Certificates > turn on Always Use HTTPS."
step "Do not turn on any other redirect or port 80 rule."
pause "Press Enter when both zones are set"

# ── 4 ─────────────────────────────────────────────────────────────────────
stage "Cloudflare: Origin Certificate"
say "The origin certificate lets Cloudflare reach this server over HTTPS in Full (strict) mode."
open_url "https://dash.cloudflare.com/"
step "Open cloudshare.my.id, then SSL/TLS > Origin Server > Create Certificate."
step "Key type: RSA or ECC. Hostnames: app.kotakserver.my.id, cloudshare.my.id, *.cloudshare.my.id."
step "Choose the longest validity. Cloudflare shows the private key only once. Keep the page open."
pause "Press Enter when the certificate and private key are on screen"

replace_cert=1
if [[ -s "$CERT_DIR/origin.pem" && -s "$CERT_DIR/origin.key" ]] &&
  validate_cert "$CERT_DIR/origin.pem" "$CERT_DIR/origin.key" 2>/dev/null; then
  say "A valid origin certificate is already saved."
  if ! confirm "Replace it with a new one?"; then replace_cert=0; fi
fi
if (( replace_cert )); then
  paste_block "$CERT_DIR/origin.pem.new" "certificate (starts with -----BEGIN CERTIFICATE-----)" 0
  paste_block "$CERT_DIR/origin.key.new" "private key (starts with -----BEGIN PRIVATE KEY----- or RSA/EC)" 1
  if validate_cert "$CERT_DIR/origin.pem.new" "$CERT_DIR/origin.key.new"; then
    mv -f "$CERT_DIR/origin.pem.new" "$CERT_DIR/origin.pem"
    mv -f "$CERT_DIR/origin.key.new" "$CERT_DIR/origin.key"
    chmod 600 "$CERT_DIR/origin.pem" "$CERT_DIR/origin.key"
    WRITTEN_ENV+=("$CERT_DIR/origin.pem" "$CERT_DIR/origin.key")
    printf '  %s✓ saved%s origin.pem and origin.key (mode 600)\n' "$GREEN" "$RESET"
  else
    rm -f "$CERT_DIR/origin.pem.new" "$CERT_DIR/origin.key.new"
    die "nothing was replaced. Run this stage again with the right files."
  fi
fi

# ── 5 ─────────────────────────────────────────────────────────────────────
stage "Cloudflare: cache rule for previews"
say "Only preview hosts (*.cloudshare.my.id) are cached. The dashboard and API must never be cached."
open_url "https://dash.cloudflare.com/"
step "Open cloudshare.my.id, then Caching > Cache Rules > Create rule."
step "Expression: http.host wildcard \"*.cloudshare.my.id\""
step "Cache eligibility: Eligible for cache. Edge TTL: Use cache-control header if present."
step "Do not create a rule for kotakserver.my.id."
note "Cloudflare moves menu items now and then. Keep the expression and the two settings."
pause "Press Enter when the rule is saved"

# ── 6 ─────────────────────────────────────────────────────────────────────
stage "Cloudflare: cache purge (optional)"
say "Purging the cache after a preview is re-uploaded or deleted needs an API token."
if confirm "Set up cache purge?"; then
  open_url "https://dash.cloudflare.com/profile/api-tokens"
  step "Create Token > Create Custom Token. Permission: Zone > Cache Purge > Purge."
  step "Zone resources: Include > Specific zone > cloudshare.my.id. Create, then copy the token."
  open_url "https://dash.cloudflare.com/"
  step "Zone ID: open cloudshare.my.id, the Overview page, right column under API."
  need MAKET_CF_ZONE_ID "Paste the zone ID:" "$ACCOUNT_RE" "a 32-character hex zone ID"
  need MAKET_CF_API_TOKEN "Paste the API token (hidden):" '^[A-Za-z0-9_-]{20,}$' "the token from the dashboard" 1
else
  put MAKET_CF_API_TOKEN ""
  put MAKET_CF_ZONE_ID ""
  note "Cache purge stays off. Any saved purge values were cleared."
fi
save_cfg

# ── 7 ─────────────────────────────────────────────────────────────────────
stage "GitHub OAuth app"
say "maket signs users in with GitHub. Create the OAuth app for the production host."
open_url "https://github.com/settings/developers"
step "OAuth Apps > New OAuth App."
step "Homepage URL: https://app.kotakserver.my.id"
step "Authorization callback URL: https://app.kotakserver.my.id/api/auth/github/callback"
step "Register the application. Copy the Client ID."
step "Generate a new client secret and copy it now. GitHub shows it only once."
need MAKET_GITHUB_CLIENT_ID "Paste the Client ID:" '^[A-Za-z0-9._-]{8,}$' "a GitHub Client ID"
need MAKET_GITHUB_CLIENT_SECRET "Paste the client secret (hidden):" '^[A-Za-z0-9._-]{20,}$' "the client secret from GitHub" 1
save_cfg

# ── 8 ─────────────────────────────────────────────────────────────────────
stage "Allowed GitHub users"
say "Only these GitHub accounts can sign in. Separate usernames with commas."
need MAKET_ALLOWED_GITHUB_USERS "GitHub usernames:" \
  '^ *[A-Za-z0-9-]+ *(, *[A-Za-z0-9-]+ *)*$' "comma-separated GitHub usernames, letters digits and hyphens"
put MAKET_ALLOWED_GITHUB_USERS "${CURRENT[MAKET_ALLOWED_GITHUB_USERS]//[[:space:]]/}"
save_cfg

# ── 9 ─────────────────────────────────────────────────────────────────────
stage "Cloudflare R2: backup bucket and API token"
say "restic backups go to a private R2 bucket. Use a token that only reaches this bucket."
open_url "https://dash.cloudflare.com/"
step "Open R2 object storage. Copy the Account ID from the R2 overview page."
need CF_ACCOUNT_ID "Account ID:" "$ACCOUNT_RE" "a 32-character hex account ID"
step "Create a bucket (default name maket-backups). Keep it private."
need R2_BUCKET "Bucket name:" '^[a-z0-9][a-z0-9.-]{2,62}$' "a lowercase bucket name"
step "Manage API tokens > Create API token. Permission: Object Read & Write. Apply to this bucket only."
step "Copy the Access Key ID and the Secret Access Key. The secret is shown only once."
need AWS_ACCESS_KEY_ID "Access Key ID:" '^[A-Za-z0-9]{16,}$' "the R2 access key ID"
need AWS_SECRET_ACCESS_KEY "Secret Access Key (hidden):" '^[A-Za-z0-9/+=_-]{16,}$' "the R2 secret access key" 1
save_backup

# ── 10 ────────────────────────────────────────────────────────────────────
stage "restic repository password"
say "This password encrypts every backup. Lose it and the backups cannot be read."
if [[ -s "$RESTIC_PW" ]]; then
  say "A password file already exists. Keeping it keeps existing backups readable."
  if confirm "Replace it? (Existing backups in the bucket will then be unreadable)"; then
    rm -f "$RESTIC_PW"
  fi
fi
if [[ ! -s "$RESTIC_PW" ]]; then
  if confirm "Generate a random password with openssl now?"; then
    umask 077
    openssl rand -base64 32 > "$RESTIC_PW.new"
    mv -f "$RESTIC_PW.new" "$RESTIC_PW"
    chmod 600 "$RESTIC_PW"
    warn "Save the password in a password manager now. It is in $RESTIC_PW (run cat yourself; it is not shown here)."
  else
    ask_secret RESTIC_PASSWORD_INPUT "Paste the restic password (hidden):" || die "input closed"
    [[ -n "$RESTIC_PASSWORD_INPUT" ]] || die "an empty password is not allowed"
    umask 077
    printf '%s\n' "$RESTIC_PASSWORD_INPUT" > "$RESTIC_PW"
    chmod 600 "$RESTIC_PW"
    unset RESTIC_PASSWORD_INPUT
  fi
  WRITTEN_ENV+=("$RESTIC_PW")
  printf '  %s✓ saved%s %s (mode 600)\n' "$GREEN" "$RESET" "$RESTIC_PW"
fi

# ── 11 ────────────────────────────────────────────────────────────────────
stage "Check the files"
missing=()
for key in MAKET_GITHUB_CLIENT_ID MAKET_GITHUB_CLIENT_SECRET MAKET_ALLOWED_GITHUB_USERS; do
  [[ -n "$(cv "$key")" ]] || missing+=("$key")
done
cf_token="$(cv MAKET_CF_API_TOKEN)"
cf_zone="$(cv MAKET_CF_ZONE_ID)"
if [[ -n "$cf_token" && -z "$cf_zone" ]] || [[ -z "$cf_token" && -n "$cf_zone" ]]; then
  missing+=("MAKET_CF_API_TOKEN and MAKET_CF_ZONE_ID (set both or neither)")
fi
[[ -s "$BACKUP_ENV" ]] || missing+=("backup.env")
[[ -s "$RESTIC_PW" ]] || missing+=("restic-password")
[[ -s "$CERT_DIR/origin.pem" && -s "$CERT_DIR/origin.key" ]] || missing+=("origin certificate and key")
if (( ${#missing[@]} )); then
  warn "still missing: ${missing[*]}"
  SKIPPED+=("re-run this wizard for: ${missing[*]}")
else
  note "all required values are present"
fi
for f in "$CFG_FILE" "$BACKUP_ENV" "$RESTIC_PW" "$CERT_DIR/origin.pem" "$CERT_DIR/origin.key"; do
  [[ -e "$f" ]] && note "$(stat -c '%a' "$f") $f"
done
save_cfg
finish
say "Next: bash deploy/setup/03-install.sh (run as deploy, from ~/maket)."
