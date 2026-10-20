#!/usr/bin/env bash
# Nightly backup of maket's data volume to a restic repository (Cloudflare R2).
#
# What it does:
#   1. Makes a consistent copy of maket.db with sqlite3 .backup (safe with WAL).
#   2. Backs up that copy and the storage/ directory with restic.
#   3. Forgets old snapshots and prunes: 7 daily, 4 weekly, 6 monthly.
#
# The volume's files are owned by the container's subordinate uid, so the host
# user cannot read or write them directly. Access goes through `podman unshare`.
#
# Configuration (environment variables, or the env file below):
#   MAKET_BACKUP_ENV  env file (default ~/.config/maket/backup.env, must be mode 600)
#                     It sets RESTIC_REPOSITORY, RESTIC_PASSWORD_FILE,
#                     AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY.
#                     For Cloudflare R2: RESTIC_REPOSITORY=s3:https://<account>.r2.cloudflarestorage.com/<bucket>
#   MAKET_VOLUME      Podman volume to back up (default maket-data)
#   MAKET_BACKUP_STAGE  scratch directory for the SQLite copy
#                     (default ~/.cache/maket-backup). Stays the same between
#                     runs, so restic groups every snapshot under the same paths.
#
# Requires podman, restic and sqlite3 on the host. Restore: see docs/deploy.md.

set -euo pipefail

ENV_FILE="${MAKET_BACKUP_ENV:-$HOME/.config/maket/backup.env}"
VOLUME="${MAKET_VOLUME:-maket-data}"
STAGE="${MAKET_BACKUP_STAGE:-$HOME/.cache/maket-backup}"

die() {
	echo "maket-backup: $*" >&2
	exit 1
}

for cmd in podman restic sqlite3; do
	command -v "$cmd" >/dev/null 2>&1 || die "$cmd is not installed"
done

[[ -f "$ENV_FILE" ]] || die "missing env file $ENV_FILE"
mode="$(stat -c %a "$ENV_FILE")"
[[ "$mode" == "600" || "$mode" == "400" ]] || die "$ENV_FILE must be mode 600 (it is $mode)"

set -a
# shellcheck source=/dev/null
. "$ENV_FILE"
set +a

: "${RESTIC_REPOSITORY:?RESTIC_REPOSITORY is not set in $ENV_FILE}"
: "${RESTIC_PASSWORD_FILE:?RESTIC_PASSWORD_FILE is not set in $ENV_FILE}"
: "${AWS_ACCESS_KEY_ID:?AWS_ACCESS_KEY_ID is not set in $ENV_FILE}"
: "${AWS_SECRET_ACCESS_KEY:?AWS_SECRET_ACCESS_KEY is not set in $ENV_FILE}"
export RESTIC_REPOSITORY RESTIC_PASSWORD_FILE AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY

mountpoint="$(podman volume inspect --format '{{.Mountpoint}}' "$VOLUME")" ||
	die "podman volume $VOLUME not found"
db="$mountpoint/maket.db"
storage="$mountpoint/storage"

rm -rf "$STAGE"
mkdir -p "$STAGE"
chmod 700 "$STAGE"
trap 'rm -rf "$STAGE"' EXIT

# A consistent snapshot of the live database, taken from inside the user namespace.
podman unshare sqlite3 "$db" ".backup '$STAGE/maket.db'"

paths=("$STAGE/maket.db")
if [[ -d "$storage" ]]; then
	paths+=("$storage")
fi

podman unshare restic backup --tag maket "${paths[@]}"
restic forget --prune --keep-daily 7 --keep-weekly 4 --keep-monthly 6
echo "maket-backup: done"
