# Production setup scripts

Guided scripts for the production server. They do the steps in [`docs/deploy.md`](../../docs/deploy.md), which stays the reference. Target: Ubuntu 26.04 LTS, Podman 4.4 or newer. The dev VPS runs Podman 4.9. Nothing here has run end to end on a real server yet.

## Order

| # | Script | Run as | What it does |
|---|---|---|---|
| 1 | `01-server.sh` | root | Packages, `deploy` user, subuid/subgid, SSH keys for `deploy`, linger, port 443 sysctl, 2 GB swap, ufw (OpenSSH, then Cloudflare 443 ranges), optional Cloudflare range refresh script |
| - | (clone) | deploy | `git clone https://github.com/itsgitz/maket.git ~/maket && cd ~/maket && git checkout v0.2.1`. `02` lives in this checkout. |
| 2 | `02-secrets.sh` | deploy | Interactive wizard for the Cloudflare, GitHub and R2 dashboards. Writes `~/.config/maket/maket-env.yaml`, `backup.env`, `restic-password`, `certs/origin.pem` and `origin.key` (mode 600). |
| 3 | `03-install.sh` | deploy | Builds the image, installs the Quadlet units, starts maket, checks health through Caddy, `restic init`, nightly backup timer, optional first backup. |
| 4 | `04-upgrade.sh` | deploy | Moves to a newer release tag: checkout, units, build, restart, health check. Rollback is manual. |

Release: `03` and `04` deploy the newest `vX.Y.Z` tag on origin. Pin one with `--release v0.2.1` or `MAKET_RELEASE=v0.2.1`.

## Changes each script makes

- **01** changes system state: apt packages, a user, `/etc/subuid` and `/etc/subgid` if missing, `/etc/sysctl.d/99-maket-unprivileged-443.conf`, `/swapfile` and `/etc/fstab` (only if no swap is active), ufw rules and `ufw enable`, `/usr/local/sbin/maket-cf-ufw-refresh` (optional).
- **02** writes only under `~/.config/maket`. It does not use sudo.
- **03** writes `~/maket`, `~/.config/containers/systemd/`, `~/.config/systemd/user/maket-backup.*`, a Podman image and the R2 repository. It starts the service and enables the timer.
- **04** updates `~/maket`, possibly the Quadlet units, the image, and restarts the service.

## Dry run and yes

```sh
sudo bash deploy/setup/01-server.sh --dry-run   # prints commands, changes nothing, works without root
bash deploy/setup/03-install.sh --dry-run       # run as any user to review the plan
bash deploy/setup/04-upgrade.sh --dry-run --release v0.2.1
```

`--yes` answers yes to every prompt. Use it only after a dry run you read through.

Each step is idempotent. Re-running a script skips the steps that are already in place.

## Not covered

- Podman 5.7 on Ubuntu 26.04 has not been tested with these scripts. Check the notes printed by `03` (pasta networking, client addresses in Caddy logs).
- The Caddyfile has no access log. Add one before relying on client addresses in logs.
- The restore test (docs/deploy.md section 8) overwrites the live data. It is manual on purpose.
