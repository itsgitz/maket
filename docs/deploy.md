# Deploying maket

> **These are instructions for the future production server.** They describe the setup the team will follow. Parts of them were checked locally with rootless Podman. They have not been run on the production VPS yet.

Production is one Ubuntu VPS. It runs rootless Podman as a non-root `deploy` user, with the maket server and Caddy in one pod managed by a systemd user unit. Cloudflare sits in front for DNS, TLS to the origin, and caching of previews. Nightly restic backups go to Cloudflare R2.

Throughout this guide, `deploy` is the deploy user and the checkout is `~/maket`. Adjust the paths if you change either. The pod YAML's hostPath values must be absolute, so they need the real home directory.

| What | Where |
|---|---|
| Checkout | `~/maket` |
| Quadlet units | `~/.config/containers/systemd/maket.kube`, `maket-pod.yaml` |
| Environment (ConfigMap, mode 600) | `~/.config/maket/maket-env.yaml` |
| Origin certificate (mode 600) | `~/.config/maket/certs/origin.pem`, `origin.key` |
| Backup settings (mode 600) | `~/.config/maket/backup.env` |
| restic repository password (mode 600) | `~/.config/maket/restic-password` |
| Data volume | Podman volume `maket-data` (`maket.db`, `storage/`) |

Read [ADR 0001](adr/0001-quadlet-kube-unit-instead-of-pod.md) for why the unit is a `.kube` unit and not a `.pod` unit.

## Guided setup

[`deploy/setup/`](../deploy/setup/README.md) has scripts that do most of the steps below. Run them in this order:

1. As root: `sudo bash deploy/setup/01-server.sh` (sections 1 and 2). It has an optional step that turns off SSH password login. Test a new login as `root` and as `deploy` in a second terminal before you close the first one.
2. As `deploy`: `bash deploy/setup/02-secrets.sh` (the dashboard steps in sections 3, 4 and 8, and the secret files).
3. As `deploy`: `bash deploy/setup/03-install.sh` (sections 5, 6 and 8).
4. Later, as `deploy`: `bash deploy/setup/04-upgrade.sh` (section 7).

Scripts 01, 03 and 04 print each command before it runs and ask before they change anything. Script 02 asks for each value and never prints a secret. `--dry-run` shows the plan without changing anything. This document stays the reference: if a script and this page disagree, this page is right, and the script needs a fix.

## 1. Server preparation

Run these as root or with `sudo` on a fresh Ubuntu server.

```sh
sudo apt update
sudo apt install -y podman sqlite3 restic ufw curl git
sudo useradd --create-home --shell /bin/bash deploy
# Ubuntu adds subordinate UID/GID ranges for new users. Check both:
grep deploy /etc/subuid /etc/subgid
```

Keep the deploy user's login shell open for the next steps. Run the rest as `deploy`.

Let user services keep running after logout, and start at boot:

```sh
sudo loginctl enable-linger deploy
```

Rootless Podman publishes port 443 as a non-root user, so the kernel must allow unprivileged binding from port 443:

```sh
echo 'net.ipv4.ip_unprivileged_port_start=443' | sudo tee /etc/sysctl.d/99-maket-unprivileged-443.conf
sudo sysctl --system
```

## 2. Firewall

Only SSH and HTTPS from Cloudflare should reach the server. Port 80 stays closed.

```sh
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow OpenSSH
# Add one rule per range in https://www.cloudflare.com/ips-v4 and
# https://www.cloudflare.com/ips-v6. The refresh script below does this for you.
# Example, one IPv4 range:
sudo ufw allow proto tcp from 173.245.48.0/20 to any port 443
sudo ufw enable
sudo ufw status verbose
```

Cloudflare publishes its ranges and sometimes changes them. Refresh the 443 rules with a small script, run as root:

```bash
#!/usr/bin/env bash
# Allow 443/tcp from Cloudflare's published ranges. Safe to re-run: ufw ignores duplicate rules.
set -euo pipefail
for family in ips-v4 ips-v6; do
  curl -fsS "https://www.cloudflare.com/$family" | while read -r cidr; do
    [ -n "$cidr" ] && ufw allow proto tcp from "$cidr" to any port 443 comment 'cloudflare'
  done
done
```

Ranges that Cloudflare removes stay until you delete them. Find them with `sudo ufw status numbered` and remove them with `sudo ufw delete <n>`.

Rootless Podman's port forwarder runs as the deploy user, but the packets still reach the host's INPUT chain, so these rules apply.

## 3. Cloudflare

Do these in the Cloudflare dashboard. Both zones are on the Free plan, and every record is proxied (orange cloud).

1. **Add both zones.** Add `kotakserver.my.id` and `cloudshare.my.id`, and point their nameservers at Cloudflare.
2. **DNS records.** On `kotakserver.my.id`, add an `A` record named `app` with the VPS IPv4 address, proxied. On `cloudshare.my.id`, add an `A` record named `*` with the same address, proxied.
3. **SSL/TLS mode.** On both zones, set **SSL/TLS > Overview > Full (strict)**.
4. **Origin Certificate.** On `cloudshare.my.id` (or `kotakserver.my.id`), go to **SSL/TLS > Origin Server > Create Certificate**. Use RSA or ECC and list these hostnames: `app.kotakserver.my.id`, `cloudshare.my.id`, `*.cloudshare.my.id`. Choose the longest validity. Cloudflare shows the private key only once. On the server:

   ```sh
   mkdir -p ~/.config/maket/certs && chmod 700 ~/.config/maket ~/.config/maket/certs
   # Paste the certificate into origin.pem and the private key into origin.key
   nano ~/.config/maket/certs/origin.pem
   nano ~/.config/maket/certs/origin.key
   chmod 600 ~/.config/maket/certs/origin.pem ~/.config/maket/certs/origin.key
   ```

   These files never go into the repository. `.gitignore` also excludes `certs/`.
5. **Always Use HTTPS.** On both zones, go to **SSL/TLS > Edge Certificates** and turn on **Always Use HTTPS**. This replaces the port 80 listener.
6. **Cache Rule for previews only.** On `cloudshare.my.id`, go to **Caching > Cache Rules > Create rule**. Set the expression to `http.host wildcard "*.cloudshare.my.id"`. Set **Cache eligibility** to "Eligible for cache" (cache everything), and **Edge TTL** to "Use cache-control header if present". Do not add a rule for `kotakserver.my.id`. The dashboard and API must never be cached.
7. **Optional: cache purge.** If you want previews purged after re-upload or delete, create an API token with **Zone > Cache Purge > Purge**, scoped to `cloudshare.my.id`. Put the token in `MAKET_CF_API_TOKEN`, and the zone ID from the zone's Overview page in `MAKET_CF_ZONE_ID`. Set both or neither.

## 4. GitHub OAuth app

On GitHub, go to **Settings > Developer settings > OAuth Apps > New OAuth App**:

- Homepage URL: `https://app.kotakserver.my.id`
- Authorization callback URL: `https://app.kotakserver.my.id/api/auth/github/callback`

Copy the client ID and generate a client secret. You will put both in the ConfigMap in the next section.

## 5. Install

```sh
# Checkout
git clone https://github.com/itsgitz/maket.git ~/maket
cd ~/maket

# Image, built on the server (there is no registry yet)
podman build -f deploy/Containerfile -t localhost/maket:latest .

# Environment: create the real file from the template, and keep it private
mkdir -p ~/.config/maket && chmod 700 ~/.config/maket
cp deploy/quadlet/maket-env.example.yaml ~/.config/maket/maket-env.yaml
chmod 600 ~/.config/maket/maket-env.yaml
nano ~/.config/maket/maket-env.yaml   # replace every replace-me value

# Quadlet units. The pod YAML's hostPath values must be absolute.
mkdir -p ~/.config/containers/systemd
cp deploy/quadlet/maket.kube deploy/quadlet/maket-pod.yaml ~/.config/containers/systemd/
# Replace /home/deploy with your home directory in the pod YAML
sed -i "s#/home/deploy#$HOME#g" ~/.config/containers/systemd/maket-pod.yaml

# Load the units and start the pod
systemctl --user daemon-reload
systemctl --user start maket
systemctl --user status maket --no-pager
```

Caddy reads `/etc/caddy/Caddyfile` from `~/maket/deploy/Caddyfile` through a read-only mount. The certificate files come from `~/.config/maket/certs`. The first start creates the `maket-data` volume and runs the database migrations.

To start the service at boot, the `[Install]` section of `maket.kube` (`WantedBy=default.target`) takes effect after `systemctl --user daemon-reload` once linger is enabled. Check `systemctl --user is-enabled maket`.

## 6. Verify

From the server:

```sh
curl -fsS https://app.kotakserver.my.id/api/healthz    # {"status":"ok"}
curl -fsSI https://app.kotakserver.my.id/ | head -1    # HTTP/2 200
```

From your own machine, with the CLI installed (see [`cli.md`](cli.md)):

```sh
maket login                    # paste an API key created under "API keys" in the dashboard
maket push ./index.html        # prints the Prototype's URL
maket ls
```

Open the URL in a browser. Preview pages are served only on `*.cloudshare.my.id`. `https://app.kotakserver.my.id` must not serve any preview.

## 7. Upgrade

```sh
cd ~/maket
git pull --ff-only
podman build -f deploy/Containerfile -t localhost/maket:latest .
# If deploy/quadlet/maket-pod.yaml changed, copy it again and re-apply the hostPath edit above
systemctl --user restart maket
```

The data volume survives upgrades, and migrations run when the new container starts. Check `journalctl --user -u maket` after the restart.

## 8. Backups

`deploy/backup.sh` copies `maket.db` with `sqlite3 .backup`, which is safe while the server is writing in WAL mode. It then backs up that copy and `storage/` with restic. It keeps 7 daily, 4 weekly and 6 monthly snapshots.

1. **Create the R2 bucket and an API token.** In Cloudflare R2, create a private bucket (for example `maket-backups`). Create an R2 API token with **Object Read & Write** on that bucket only. Note the access key ID and secret access key. The endpoint is `https://<account-id>.r2.cloudflarestorage.com`.
2. **Create the restic password file.** Keep a copy of this password in a password manager. Without it the backups cannot be read.

   ```sh
   umask 077
   openssl rand -base64 32 > ~/.config/maket/restic-password
   chmod 600 ~/.config/maket/restic-password
   ```

3. **Create the backup settings file.** Mode 600, outside the repository:

   ```sh
   cat > ~/.config/maket/backup.env <<'EOF'
   RESTIC_REPOSITORY=s3:https://<account-id>.r2.cloudflarestorage.com/maket-backups
   RESTIC_PASSWORD_FILE=/home/deploy/.config/maket/restic-password
   AWS_ACCESS_KEY_ID=<r2-access-key-id>
   AWS_SECRET_ACCESS_KEY=<r2-secret-access-key>
   EOF
   chmod 600 ~/.config/maket/backup.env
   ```

4. **Initialize the repository and run one backup by hand:**

   ```sh
   set -a; . ~/.config/maket/backup.env; set +a
   restic init
   ~/maket/deploy/backup.sh
   restic snapshots
   ```

5. **Install and enable the nightly timer:**

   ```sh
   mkdir -p ~/.config/systemd/user
   cp ~/maket/deploy/systemd/maket-backup.service ~/maket/deploy/systemd/maket-backup.timer ~/.config/systemd/user/
   systemctl --user daemon-reload
   systemctl --user enable --now maket-backup.timer
   systemctl --user list-timers maket-backup.timer
   ```

   The timer runs at 03:30 with up to 15 minutes of random delay. `Persistent=true` runs a missed backup at the next start.

### Using Cloudflare R2 for storage

By default Prototype files live in `storage/` under the data volume. To keep them in R2 instead, set `MAKET_STORAGE_DRIVER=s3` and the `MAKET_S3_*` variables in the ConfigMap (see `deploy/quadlet/maket-env.example.yaml`).

1. **Create a private bucket** for Prototype files (for example `maket-prototypes`).
2. **Create an R2 API token** with **Object Read & Write** on that bucket only. Note the access key ID and secret access key.
3. **Set the variables:**

   ```yaml
   MAKET_STORAGE_DRIVER: "s3"
   MAKET_S3_ENDPOINT: "https://<account-id>.r2.cloudflarestorage.com"
   MAKET_S3_BUCKET: "maket-prototypes"
   MAKET_S3_REGION: "auto"        # R2 uses "auto"; the default is also "auto"
   MAKET_S3_ACCESS_KEY_ID: "<r2-access-key-id>"
   MAKET_S3_SECRET_ACCESS_KEY: "<r2-secret-access-key>"
   ```

Things to know:

- With the `s3` driver, `backup.sh` still backs up `maket.db`, but it does not copy Prototype files, because they are not in the volume. Rely on R2's durability, or add a periodic `rclone copy` of the bucket to another place if you need a second copy.
- `backup.sh` skips the `storage/` directory when it does not exist, so the same script works with either driver.
- The R2 token for this bucket is separate from the backup token in the backup section above. Each token should only reach its own bucket.

### Restore

Restore onto the same server. Stop the server first so nothing writes during the restore.

```sh
systemctl --user stop maket
set -a; . ~/.config/maket/backup.env; set +a

# Restore the latest snapshot into a scratch directory. restic keeps the original absolute paths.
rm -rf /tmp/maket-restore && restic restore latest --tag maket --target /tmp/maket-restore
find /tmp/maket-restore -name maket.db -o -name storage -type d

# Copy into the volume. Use podman unshare: the volume's files belong to the container's subordinate UID.
VOL="$(podman volume inspect --format '{{.Mountpoint}}' maket-data)"
DB="$(find /tmp/maket-restore -name maket.db | head -n1)"
ST="$(find /tmp/maket-restore -name storage -type d | head -n1)"
podman unshare sh -c "
  rm -f '$VOL/maket.db-wal' '$VOL/maket.db-shm' &&
  cp '$DB' '$VOL/maket.db' &&
  rm -rf '$VOL/storage' && cp -r '$ST' '$VOL/storage' &&
  chown 65532:65532 '$VOL/maket.db' &&
  chown -R 65532:65532 '$VOL/storage'
"

systemctl --user start maket
curl -fsS https://app.kotakserver.my.id/api/healthz
rm -rf /tmp/maket-restore
```

To restore an older point in time, replace `latest` with a snapshot ID from `restic snapshots`.

## 9. Logs and troubleshooting

```sh
journalctl --user -u maket -f          # systemd and kube play output
podman logs -f maket-api               # the maket server (JSON logs)
podman logs -f maket-caddy             # Caddy
journalctl --user -u maket-backup      # nightly backups
```

If the service fails to start, check `~/maket/deploy/Caddyfile` and `~/.config/maket/certs/` (both must exist and be readable), and check the variables in `~/.config/maket/maket-env.yaml`. The server exits with a list of every missing or invalid setting.
