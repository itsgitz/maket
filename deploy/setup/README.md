# Production setup scripts

Guided scripts for the production server. They do the steps in [`docs/deploy.md`](../../docs/deploy.md), which stays the reference. Target: Ubuntu 26.04 LTS, Podman 4.4 or newer. The dev VPS runs Podman 4.9. Nothing here has run end to end on a real server yet.

## Order

| # | Script | Run as | What it does |
|---|---|---|---|
| 1 | `01-server.sh` | root | Packages, `deploy` user, subuid/subgid, SSH keys for `deploy`, optional SSH password login off (root key only, asks first), linger, port 443 sysctl, 2 GB swap, ufw (OpenSSH, then Cloudflare 443 ranges), optional Cloudflare range refresh script |
| - | (clone) | deploy | `git clone https://github.com/itsgitz/maket.git ~/maket && cd ~/maket && git checkout "$(git tag --sort=-v:refname \| head -1)"`. Checks out the newest tag. `02` lives in this checkout. |
| 2 | `02-secrets.sh` | deploy | Interactive wizard for the Cloudflare, GitHub and R2 dashboards. Writes `~/.config/maket/maket-env.yaml`, `certs/app.pem` and `app.key`, `certs/preview.pem` and `preview.key` (mode 600). It also writes `backup.env` and `restic-password` if you set up R2 backups (optional). |
| 3 | `03-install.sh` | deploy | Builds the image, installs the Quadlet units, starts maket, checks health through Caddy. If `backup.env` exists: `restic init`, nightly backup timer, optional first backup. Otherwise those steps are skipped. |
| 4 | `04-upgrade.sh` | deploy | Moves to a newer release tag: checkout, units, build, restart, health check. Rollback is manual. |

Release: `03` and `04` deploy the newest `vX.Y.Z` tag on origin. Pin one with `--release v0.2.2` or `MAKET_RELEASE=v0.2.2`.

## Backups are optional

R2 needs a payment method on the Cloudflare account. If you do not have one yet, install without backups:

1. `02-secrets.sh` asks "Set up R2 backups now?". Answer no. It writes no `backup.env` and skips the restic password.
2. `03-install.sh` runs without a restic repository, nightly timer or first backup. It warns that there are no backups.
3. When R2 is ready, re-run `02-secrets.sh` and answer yes, then re-run `03-install.sh`. Those steps are added and the install is otherwise unchanged. Re-running is safe.

**The risk:** until backups are set up, nothing is backed up. A disk failure, a bad migration or an accidental delete of the data volume cannot be restored. Set them up before you put real data on the server.

If `backup.env` already exists and you answer no in `02-secrets.sh`, the existing file is kept as it is.

## Changes each script makes

- **01** changes system state: apt packages, a user, `/etc/subuid` and `/etc/subgid` if missing, `/etc/ssh/sshd_config.d/00-maket-hardening.conf` (optional: turns off password login, root key only; test a new login in a second terminal before you close the first), `/etc/sysctl.d/99-maket-unprivileged-443.conf`, `/swapfile` and `/etc/fstab` (only if no swap is active), ufw rules and `ufw enable`, `/usr/local/sbin/maket-cf-ufw-refresh` (optional).
- **02** writes only under `~/.config/maket`. It does not use sudo.
- **03** writes `~/maket`, `~/.config/containers/systemd/`, a Podman image, and (only if `backup.env` exists) `~/.config/systemd/user/maket-backup.*` and the R2 repository. It starts the service and enables the timer when backups are set up.
- **04** updates `~/maket`, possibly the Quadlet units, the image, and restarts the service.

## Dry run and yes

```sh
sudo bash deploy/setup/01-server.sh --dry-run   # prints commands, changes nothing, works without root
bash deploy/setup/03-install.sh --dry-run       # run as any user to review the plan
bash deploy/setup/04-upgrade.sh --dry-run --release v0.2.2
```

`--yes` answers yes to every prompt, except the root key question in step 5 of `01-server.sh`: that step is skipped under `--yes`, so run it without `--yes`. Use `--yes` only after a dry run you read through.

Each step is idempotent. Re-running a script skips the steps that are already in place.

## Not covered

- Podman 5.7 on Ubuntu 26.04 has not been tested with these scripts. Check the notes printed by `03` (pasta networking, client addresses in Caddy logs).
- The Caddyfile has no access log. Add one before relying on client addresses in logs.
- The restore test (docs/deploy.md section 8) overwrites the live data. It is manual on purpose.
