# maket — Build Brief

You are building **maket**, a self-hosted tool for uploading and hosting static frontend prototypes (HTML, CSS, JS, and assets). It is used by **humans** (via a web dashboard) and **AI agents** (via a CLI or plain HTTP with an API key).

Read this whole document before writing code. Work milestone by milestone (see the end). **Do not change the stack decisions below without asking first.** If something is ambiguous, ask instead of guessing.

---

## 1. What maket does

- A user logs in with **GitHub** on the dashboard.
- The user uploads a prototype: a single `.html` file, or a `.zip` of a folder / build output (`index.html` + CSS + JS + images + fonts).
- maket stores the files and serves them at a unique preview URL: `https://<slug>.cloudshare.my.id`.
- The user can create **API keys** in the dashboard. Agents and scripts use the key with the `maket` CLI or `curl` to upload, list, update, and delete prototypes.
- Hosting is **static only**: JavaScript runs in the visitor's browser; there is no server-side code execution for prototypes.

---

## 2. Stack (decided)

| Layer | Choice | Notes |
|---|---|---|
| Hosting | Single Ubuntu 26.04 VPS | |
| App domain | `app.kotakserver.my.id` | Dashboard, `/api/*`, login, API keys |
| Preview domain | `*.cloudshare.my.id` | Uploaded prototypes only. Nothing else ever runs on this domain |
| DNS / CDN | Cloudflare (free plan), proxy on for both domains | Cache Rule "cache everything" on `*.cloudshare.my.id` only |
| TLS | Cloudflare Origin Certificate loaded in Caddy | One cert for `app.kotakserver.my.id` + `*.cloudshare.my.id`; SSL mode Full (strict) |
| Containers | Podman (rootless) + Quadlet units | Caddy + API in one pod, managed by systemd |
| Reverse proxy | Caddy | TLS + `reverse_proxy` to the Go app only |
| Monorepo | Bun workspaces | Bun is package manager and task runner. Go keeps its own `go.mod` |
| Backend | Go (latest stable), standard library `net/http` router (Go 1.22+ patterns) | Serves API, embedded dashboard, and previews, routed by `Host` header |
| Frontend | Vite + Svelte 5 + TypeScript, SPA | Built to static files and embedded in the Go binary via `embed.FS` |
| Database | SQLite, WAL mode, `modernc.org/sqlite` (no CGO) | Plain SQL migrations embedded in the binary |
| Storage | `Storage` interface; `LocalStore` now, `S3Store` later | S3Store must work with any S3-compatible service (Cloudflare R2, MinIO) via AWS SDK for Go v2 |
| Login | GitHub OAuth (`golang.org/x/oauth2`) | Design so Google can be added later |
| Browser auth | Session cookie | |
| Programmatic auth | API keys | |
| CLI | Go binary `maket`, same Go module | |

---

## 3. Repository layout

```
maket/
├── apps/
│   ├── api/                      # Go module
│   │   ├── go.mod
│   │   ├── cmd/
│   │   │   ├── server/           # main server
│   │   │   └── maket/            # CLI
│   │   ├── internal/
│   │   │   ├── config/           # env-based config
│   │   │   ├── db/               # SQLite open, migrations, queries
│   │   │   ├── auth/             # GitHub OAuth, sessions, API keys, middleware
│   │   │   ├── storage/          # Storage interface, LocalStore, (later) S3Store
│   │   │   ├── prototype/        # upload, unzip, validation, service layer
│   │   │   ├── preview/          # preview-domain file serving
│   │   │   ├── httpapi/          # JSON API handlers + router
│   │   │   └── webui/            # embed.FS for the built dashboard
│   │   └── migrations/
│   └── web/                      # Vite + Svelte dashboard
├── deploy/
│   ├── Caddyfile
│   ├── Containerfile
│   ├── quadlet/                  # maket.pod, maket-api.container, maket-caddy.container, volume
│   └── backup.sh
├── docs/
│   ├── api.md
│   └── cli.md
├── package.json                  # bun workspaces + scripts
└── README.md
```

Note: Go's `embed` cannot reference files outside the package directory. The web build must output into (or be copied into) `apps/api/internal/webui/dist` before `go build`. Keep `dist` gitignored but keep a placeholder so `go build` works without a web build.

Root `package.json` scripts (at minimum):

- `dev` — run Vite dev server and the Go server together (Vite proxies `/api` to Go; use `air` for Go hot reload if available)
- `build:web` — build Svelte into `apps/api/internal/webui/dist`
- `build:api` — build server binary
- `build:cli` — build CLI for linux/darwin/windows (amd64 + arm64)
- `build` — `build:web` then `build:api`
- `test` — Go tests + web type check

---

## 4. Host-based routing

One Go server handles everything and routes by the request `Host` header:

- `app.kotakserver.my.id`
  - `/api/*` → JSON API
  - everything else → embedded Svelte SPA (fall back to `index.html` for client-side routes)
- `<slug>.cloudshare.my.id` → preview serving for that slug
- anything else → 404

Hosts must come from config (see section 11) so local dev can use e.g. `app.localhost` and `*.preview.localhost`.

---

## 5. Authentication

### GitHub OAuth (browser)

- `GET /api/auth/github/login` → redirect to GitHub with a random `state` (stored in a short-lived cookie).
- `GET /api/auth/github/callback` → verify `state`, exchange code, fetch GitHub user, upsert into `users` by `github_id`, create a session, redirect to dashboard.
- **Access allowlist:** only GitHub usernames in `MAKET_ALLOWED_GITHUB_USERS` (comma-separated) can log in. Everyone else gets a clear "not allowed" page. This is a private tool; open signup would invite abuse of the preview domain.

### Sessions

- Random 32-byte token; store only its SHA-256 hash in `sessions`.
- Cookie: name `__Host-maket_session`, `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`, no `Domain` attribute (host-only on the app domain). Never set any cookie on the preview domain.
- Expiry: 30 days, sliding.
- CSRF: for cookie-authenticated `POST`/`PUT`/`PATCH`/`DELETE`, require the `Origin` header to exactly equal the app origin.
- `POST /api/auth/logout` deletes the session.

### API keys (programmatic)

- Format: `mk_` + 32 random bytes encoded base62.
- Shown **once** at creation. Store SHA-256 hash, a display prefix (first 8 chars after `mk_`), name, `created_at`, `last_used_at`, `revoked_at`.
- Sent as `Authorization: Bearer mk_...`.
- **API keys can manage prototypes but cannot create, list, or revoke API keys.** Key management requires a browser session.

### Middleware

One middleware resolves either a session cookie or a bearer key into a `user_id` and an `auth_method` (`session` | `api_key`). Handlers share the same service layer regardless of method.

---

## 6. Database schema (initial)

```sql
users (
  id INTEGER PRIMARY KEY,
  github_id INTEGER UNIQUE NOT NULL,
  github_login TEXT NOT NULL,
  name TEXT,
  avatar_url TEXT,
  created_at TIMESTAMP NOT NULL
)

sessions (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash BLOB UNIQUE NOT NULL,
  expires_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP NOT NULL
)

api_keys (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  key_hash BLOB UNIQUE NOT NULL,
  last_used_at TIMESTAMP,
  revoked_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL
)

prototypes (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  slug TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL,
  spa INTEGER NOT NULL DEFAULT 0,
  current_version INTEGER NOT NULL,
  size_bytes INTEGER NOT NULL,
  file_count INTEGER NOT NULL,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL
)
```

Enable `PRAGMA journal_mode=WAL`, `foreign_keys=ON`, and a sensible `busy_timeout`.

---

## 7. Uploads

### Accepted input

- A single `.html` / `.htm` file → stored as `index.html`.
- A `.zip` → extracted. If the zip contains a single top-level folder, strip it so `index.html` sits at the root.
- The upload must result in an `index.html` at the root, otherwise reject with a clear error.

### Allowlisted extensions (reject everything else)

- Web: `.html .htm .css .js .mjs .json .map .txt .xml .webmanifest`
- Images: `.png .jpg .jpeg .gif .webp .avif .svg .ico`
- Fonts: `.woff .woff2 .ttf .otf`
- Media: `.mp4 .webm .mp3`

### Safety checks (must have tests)

- Reject path traversal (`..`), absolute paths, backslash paths, symlinks, and entries outside the root.
- Enforce limits from config: max upload size, max total extracted size (zip-bomb protection), max file count, per-user total storage quota.
- Extract into a temp directory first; only move into place when everything validates.

### Slugs and versions

- Slug: 8 random lowercase alphanumeric characters (valid DNS label).
- Each upload of an existing prototype creates a new version. Storage key layout: `prototypes/<slug>/v<version>/<path>`.
- After a successful new version, update `current_version`, then delete older versions (keep only current for v1).

---

## 8. Preview serving (`<slug>.cloudshare.my.id`)

- Look up slug → current version → serve file from storage.
- `/` → `index.html`; `/dir/` → `dir/index.html`.
- Unknown path: if `spa` is true, serve root `index.html`; otherwise a plain 404.
- Correct `Content-Type` by extension.
- Headers on every preview response:
  - `X-Content-Type-Options: nosniff`
  - `X-Robots-Tag: noindex, nofollow`
  - `Referrer-Policy: strict-origin-when-cross-origin`
  - `Cache-Control: public, max-age=300` (short, so re-uploads show up quickly even with Cloudflare caching)
- Never set cookies. Never serve anything except prototype files on this domain.
- Optional (behind config): purge the slug's Cloudflare cache via the Cloudflare API after a re-upload or delete.

---

## 9. JSON API

All under `https://app.kotakserver.my.id/api`. JSON errors use `{ "error": { "code": "...", "message": "..." } }`.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/auth/github/login` | none | Start OAuth |
| GET | `/auth/github/callback` | none | OAuth callback |
| POST | `/auth/logout` | session | Log out |
| GET | `/me` | session or key | Current user |
| GET | `/keys` | session | List API keys (no secrets) |
| POST | `/keys` | session | Create key `{name}` → returns the full key once |
| DELETE | `/keys/{id}` | session | Revoke key |
| GET | `/prototypes` | session or key | List own prototypes |
| POST | `/prototypes` | session or key | Multipart: `file`, optional `name`, optional `spa` → `{slug, url, ...}` |
| GET | `/prototypes/{slug}` | session or key | Details |
| PUT | `/prototypes/{slug}` | session or key | Upload a new version (same multipart fields) |
| DELETE | `/prototypes/{slug}` | session or key | Delete prototype and its files |
| GET | `/healthz` | none | Health check (on app host) |

Users can only see and modify their own prototypes.

---

## 10. Dashboard (Svelte)

Pages:

- **Login** — "Sign in with GitHub" button.
- **Prototypes** — list (name, URL, size, updated), copy-link button, open link, delete, re-upload.
- **Upload** — drag-and-drop or file picker for `.html` / `.zip`, optional name, SPA toggle, upload progress, shows resulting URL.
- **API keys** — list, create (show key once with copy button and a warning), revoke.
- **Agent/CLI help** — short page showing `maket` CLI install + usage and a `curl` example with a placeholder key.

Keep the UI simple and clean. Use `fetch` against `/api` with `credentials: "same-origin"`.

---

## 11. Configuration (environment variables)

| Variable | Example | Purpose |
|---|---|---|
| `MAKET_LISTEN_ADDR` | `:8080` | |
| `MAKET_APP_HOST` | `app.kotakserver.my.id` | |
| `MAKET_PREVIEW_DOMAIN` | `cloudshare.my.id` | Previews at `<slug>.<this>` |
| `MAKET_PUBLIC_SCHEME` | `https` | For building URLs |
| `MAKET_DATA_DIR` | `/data` | SQLite file + local storage |
| `MAKET_GITHUB_CLIENT_ID` / `MAKET_GITHUB_CLIENT_SECRET` | | OAuth app |
| `MAKET_ALLOWED_GITHUB_USERS` | `anggit,teammate1` | Login allowlist |
| `MAKET_STORAGE_DRIVER` | `local` or `s3` | |
| `MAKET_S3_ENDPOINT`, `MAKET_S3_BUCKET`, `MAKET_S3_REGION`, `MAKET_S3_ACCESS_KEY_ID`, `MAKET_S3_SECRET_ACCESS_KEY` | | Used when driver is `s3` |
| `MAKET_MAX_UPLOAD_MB` | `20` | |
| `MAKET_MAX_EXTRACTED_MB` | `100` | |
| `MAKET_MAX_FILES` | `500` | |
| `MAKET_USER_QUOTA_MB` | `1024` | |
| `MAKET_CF_API_TOKEN`, `MAKET_CF_ZONE_ID` | | Optional cache purge |

Fail fast at startup with clear messages if required variables are missing. Provide `.env.example`. Never commit secrets.

---

## 12. CLI (`maket`)

Same Go module, `apps/api/cmd/maket`. Single static binary.

- Config: `MAKET_API_KEY` and `MAKET_API_URL` (default `https://app.kotakserver.my.id`) env vars, falling back to `~/.config/maket/config.json`.
- Commands:
  - `maket login` — prompt for API key, verify via `/api/me`, save to config.
  - `maket whoami`
  - `maket push <file-or-folder> [--name NAME] [--spa] [--slug SLUG] [--json]` — folders are zipped in memory; `--slug` uploads a new version of an existing prototype. Prints the preview URL (only the URL on stdout by default, so agents can capture it).
  - `maket ls [--json]`
  - `maket rm <slug> [--yes]`
- Non-zero exit codes on failure, errors on stderr.
- `--json` output must be stable and documented in `docs/cli.md`.
- Reuse request/response types from the server code where practical.

Also document an equivalent `curl` upload in `docs/api.md` for agents that cannot install the CLI.

---

## 13. Deployment

- `deploy/Containerfile`: multi-stage — Bun builds the web app, Go builds a static server binary, final image is minimal (distroless static or scratch with CA certs). Runs as non-root.
- `deploy/quadlet/`: `maket.pod`, `maket-api.container`, `maket-caddy.container`, and a named volume for `/data`. Caddy mounts the origin cert read-only.
- Rootless Podman on ports 80/443: document `net.ipv4.ip_unprivileged_port_start=80` (or the chosen alternative) in the README.
- `deploy/Caddyfile`:

  ```caddy
  app.kotakserver.my.id, *.cloudshare.my.id {
      tls /certs/origin.pem /certs/origin.key
      reverse_proxy 127.0.0.1:8080
  }
  ```

- `deploy/backup.sh`: consistent SQLite backup (`sqlite3 .backup` or `VACUUM INTO`) plus the storage directory, suitable for a nightly systemd timer with `restic` or `rsync`.
- README section with manual Cloudflare steps: add both zones, proxied DNS records (`app` on kotakserver, wildcard `*` on cloudshare), generate the origin certificate, set SSL to Full (strict), create the Cache Rule for `*.cloudshare.my.id`.

---

## 14. Milestones

Complete and verify each before moving on. Commit per milestone.

1. **Skeleton** — monorepo, Bun workspaces, Go module, config loading, Host-based router, `/healthz`, embedded placeholder SPA, `dev` and `build` scripts working.
2. **Database + storage** — SQLite with migrations, `Storage` interface + `LocalStore` with tests.
3. **Auth** — GitHub OAuth with allowlist, sessions, Origin check, API keys, middleware, `/me`, key endpoints. Tests for middleware and key hashing.
4. **Uploads + previews** — upload, zip validation and extraction, versions, preview serving with headers and SPA mode. Tests for path traversal, zip bombs, disallowed extensions, missing `index.html`.
5. **Dashboard** — all pages in section 10.
6. **CLI** — all commands in section 12, cross-compiled builds.
7. **Deployment** — Containerfile, Quadlet units, Caddyfile, backup script, README deploy guide.
8. **S3Store** — S3-compatible storage driver, verified against MinIO locally.

---

## 15. Out of scope for v1

Google login, version history in the UI, API key scopes, custom slugs, teams/sharing permissions, MCP server, public docs/landing site, server-side code in prototypes.

---

## 16. Definition of done (v1)

- A logged-in allowlisted user can upload `.html` and `.zip` from the dashboard and open the preview at `https://<slug>.cloudshare.my.id`.
- `maket push ./dist` with an API key prints a working preview URL; `--json` works.
- The same works with the documented `curl` command.
- Unsafe zips are rejected with clear errors.
- No cookie is ever set on the preview domain, and preview pages cannot make authenticated requests to the app API.
- `bun run build` produces a single server binary; the container runs under rootless Podman behind Caddy.
- `go test ./...` passes.
