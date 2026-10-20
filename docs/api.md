# maket HTTP API

The JSON API behind the dashboard and the `maket` CLI. Agents that cannot install the CLI ([`cli.md`](cli.md)) can do everything here with `curl`.

## Base URL

```
https://app.kotakserver.my.id
```

All endpoints are under `/api`. Previews are served on a separate domain; see [Previews](#previews).

## Authentication

Send an API Key as a bearer token:

```
Authorization: Bearer mk_...
```

Create API Keys in the dashboard under **API keys**. The full key is shown once. An API Key can do everything on this page: read the current User and list, create, replace and delete that User's Prototypes. It cannot create, list or revoke API Keys.

Rules:

- If an `Authorization` header is present, it is the only credential considered. Anything other than exactly one well-formed `Bearer mk_...` value is `401 unauthorized`.
- A revoked or unknown key is `401 unauthorized`.
- If the User is no longer on the Allowlist, every request is `403 not_allowed`.
- Users see and change only their own Prototypes. Another User's Slug is `404 not_found`.

The dashboard uses a session cookie instead, set by GitHub sign-in. Session-authenticated `POST`, `PUT`, `PATCH` and `DELETE` requests must carry an `Origin` header equal to the app origin (`https://app.kotakserver.my.id`), otherwise they get `403 bad_origin`. Requests with an API Key are not subject to this check.

The key-management endpoints (`GET /api/keys`, `POST /api/keys`, `DELETE /api/keys/{id}`) and `POST /api/auth/logout` are for the dashboard only. With an API Key they return `403 session_required`.

## Errors

Every error from `/api` is JSON with this shape:

```json
{
  "error": {
    "code": "missing_index",
    "message": "The upload has no index.html at its root; add one (a single top-level folder is stripped automatically)."
  }
}
```

`code` is stable and machine-readable; `message` is for people and may change. When specific files caused a rejected Upload, `details` lists them, sorted, up to 50; `more` counts the rest:

```json
{
  "error": {
    "code": "unsafe_paths",
    "message": "These entries have unsafe paths (absolute, \"..\", \".\" or empty segments, backslashes, control characters, symlinks, duplicates, or too long); fix them and upload again.",
    "details": {
      "paths": ["../etc/passwd", "assets/link.js"],
      "more": 0
    }
  }
}
```

| Status | Code | When |
|---|---|---|
| 400 | `invalid_body` | Not `multipart/form-data`; malformed multipart; more than one `file` part; upload interrupted; `name` field over 1 KiB; `spa` not a recognised boolean |
| 400 | `invalid_file` | No `file` part; the file is not `.html`, `.htm` or `.zip`; not a valid zip; a corrupt, encrypted or unsupported zip entry (with `details`) |
| 400 | `invalid_name` | `name` longer than 100 characters or not valid UTF-8 |
| 400 | `missing_index` | No `index.html` at the root after stripping a single top-level folder |
| 400 | `disallowed_files` | Files with extensions not on the allowlist (with `details`) |
| 400 | `unsafe_paths` | Unsafe zip entry paths, symlinks, duplicates, or paths starting with `.tmp` (with `details`) |
| 400 | `too_many_files` | More files than the file-count limit |
| 401 | `unauthorized` | Missing, malformed, unknown or revoked credentials |
| 403 | `not_allowed` | The User is not on the Allowlist |
| 403 | `session_required` | API Key used on a dashboard-only endpoint |
| 403 | `bad_origin` | Session request with a missing or wrong `Origin` header |
| 404 | `not_found` | No such Prototype for this User, a malformed Slug, or no such API endpoint |
| 413 | `too_large` | The uploaded file is over the upload limit, or the extracted files are over the extracted-size limit |
| 413 | `quota_exceeded` | The Upload would take the User over their Quota |
| 500 | `internal` | Server error; safe to retry |

All API responses carry `Cache-Control: no-store`.

## Endpoints

| Method | Path | Success | Purpose |
|---|---|---|---|
| GET | `/api/me` | 200 | The current User |
| GET | `/api/prototypes` | 200 | List Prototypes and Quota usage |
| POST | `/api/prototypes` | 201 | Upload a new Prototype |
| GET | `/api/prototypes/{slug}` | 200 | One Prototype |
| PUT | `/api/prototypes/{slug}` | 200 | Upload a new Version |
| DELETE | `/api/prototypes/{slug}` | 204 | Delete a Prototype and its files |
| GET | `/api/healthz` | 200 | Health check, no auth: `{"status":"ok"}` |

Timestamps are RFC 3339 in UTC and may include fractional seconds.

### GET /api/me

```json
{
  "id": 1,
  "github_login": "octocat",
  "name": "The Octocat",
  "avatar_url": "https://avatars.githubusercontent.com/u/583231?v=4",
  "auth_method": "api_key"
}
```

`auth_method` is `api_key` or `session`. Errors: 401, 403.

### The Prototype object

Returned by `POST`, `PUT` and `GET /api/prototypes/{slug}`, and as each entry of the list.

```json
{
  "slug": "k3x9q2ab",
  "name": "dist",
  "url": "https://k3x9q2ab.cloudshare.my.id",
  "spa": true,
  "version": 1,
  "size_bytes": 1258291,
  "file_count": 14,
  "created_at": "2026-10-19T08:04:11.52731Z",
  "updated_at": "2026-10-19T08:04:11.52731Z"
}
```

| Field | Meaning |
|---|---|
| `slug` | 8 lowercase letters or digits; identifies the Prototype |
| `name` | Display name, 1 to 100 characters |
| `url` | The Preview URL |
| `spa` | Whether SPA mode is on |
| `version` | Current Version number, starting at 1 and increasing by 1 per Upload |
| `size_bytes` | Total extracted size of the current Version; this is what counts toward the Quota |
| `file_count` | Number of files in the current Version |
| `created_at` | When the Prototype was created |
| `updated_at` | When its current Version was uploaded |

### GET /api/prototypes

The User's Prototypes, most recently updated first, and Quota usage. `prototypes` is `[]` when there are none.

```json
{
  "prototypes": [
    {
      "slug": "k3x9q2ab",
      "name": "Pricing page v2",
      "url": "https://k3x9q2ab.cloudshare.my.id",
      "spa": true,
      "version": 3,
      "size_bytes": 1258291,
      "file_count": 14,
      "created_at": "2026-10-17T10:22:03.118204Z",
      "updated_at": "2026-10-19T08:04:11.52731Z"
    }
  ],
  "usage": {
    "used_bytes": 1258291,
    "quota_bytes": 1073741824
  }
}
```

Errors: 401, 403.

### POST /api/prototypes

Creates a Prototype from an Upload. The body is `multipart/form-data`:

| Field | Required | Meaning |
|---|---|---|
| `file` | yes | One `.html`, `.htm` or `.zip` file (extension checked case-insensitively on the part's filename) |
| `name` | no | Display name, 1 to 100 characters after trimming whitespace. Default: the file name without its extension, or `Untitled`. An empty or whitespace-only value counts as omitted. |
| `spa` | no | `true`, `1`, `on` or `false`, `0`, `off` (case-insensitive). Default: off. An empty value counts as omitted. |

A single `.html` or `.htm` file is stored as `index.html`. A zip is validated and extracted as described in [Uploads](#uploads).

Response: `201 Created` with the Prototype object.

Errors: 400 (`invalid_body`, `invalid_file`, `invalid_name`, `missing_index`, `disallowed_files`, `unsafe_paths`, `too_many_files`), 401, 403, 413 (`too_large`, `quota_exceeded`).

### GET /api/prototypes/{slug}

Response: `200 OK` with the Prototype object. Errors: 401, 403, 404.

### PUT /api/prototypes/{slug}

Uploads a new Version of an existing Prototype. Same multipart fields as `POST`. When `name` or `spa` is omitted, the Prototype keeps its current value.

The new Version replaces the old one at the same Preview URL; only the current Version is kept. `version` increases by 1 and `updated_at` changes; `slug`, `url` and `created_at` stay the same.

Response: `200 OK` with the updated Prototype object.

Errors: as for `POST`, plus 404 when the Slug is not one of the User's Prototypes.

### DELETE /api/prototypes/{slug}

Deletes the Prototype and all its files. Its Preview URL starts returning 404.

Response: `204 No Content`, empty body. Errors: 401, 403, 404.

## Uploads

A zip is checked in full before anything is stored:

1. Junk is dropped silently: anything under a `__MACOSX/` folder, and files named `.DS_Store` or `Thumbs.db` (any case). Directory entries are ignored.
2. Every remaining entry must have a safe path: relative, `/`-separated, no `.` or `..` or empty segments, no backslashes, no control characters, no drive letters, valid UTF-8, at most 900 bytes overall and 255 bytes per segment. Symlinks and duplicate entries are rejected. Failing entries: `unsafe_paths`.
3. Every file must have an allowed extension (below). Failing files: `disallowed_files`. A file without an extension is not allowed.
4. The file count must be within the limit: `too_many_files`.
5. If every file sits inside the same top-level folder, that folder is stripped. Paths may not then start with `.tmp`: `unsafe_paths`.
6. `index.html` must exist at the root: `missing_index`.
7. The extracted size must be within the limit, counting the real decompressed bytes: `too_large`. Only stored and deflated entries without encryption are supported: `invalid_file`.
8. The Quota is checked: `quota_exceeded`.

### Allowed file extensions

| Kind | Extensions |
|---|---|
| Web | `.html .htm .css .js .mjs .json .map .txt .xml .webmanifest` |
| Images | `.png .jpg .jpeg .gif .webp .avif .svg .ico` |
| Fonts | `.woff .woff2 .ttf .otf` |
| Media | `.mp4 .webm .mp3` |

Extensions are matched case-insensitively.

## Limits

Defaults; the server operator can change them. MB here means 1024 × 1024 bytes.

| Limit | Default | Server setting | Error |
|---|---|---|---|
| Uploaded file size | 20 MB | `MAKET_MAX_UPLOAD_MB` | `413 too_large` |
| Total extracted size of one Upload | 100 MB | `MAKET_MAX_EXTRACTED_MB` | `413 too_large` |
| Files in one Upload | 500 | `MAKET_MAX_FILES` | `400 too_many_files` |
| Quota per User | 1024 MB | `MAKET_USER_QUOTA_MB` | `413 quota_exceeded` |
| Prototype name | 100 characters | | `400 invalid_name` |

The Quota covers the total `size_bytes` of the User's current Versions. A re-Upload counts only the difference: the new Version's size replaces the old one's. `GET /api/prototypes` reports the current usage and Quota.

## Previews

Each Prototype's current Version is served at its Preview URL:

```
https://<slug>.cloudshare.my.id
```

Previews are public: anyone with the URL can view them. They accept only `GET` and `HEAD` (other methods: 405). A Prototype whose owner is no longer on the Allowlist is not served (404).

How a request path is resolved:

1. `/` and any path ending in `/` serve that folder's `index.html`.
2. A path that names a file serves that file, with a `Content-Type` from its extension.
3. A folder requested without its trailing `/` (`/docs` where `docs/index.html` exists) is redirected with `301` to `/docs/`, keeping the query string.
4. In SPA mode, a missing path whose last segment has no `.` (`/settings/profile`, not `/app.js`) serves the root `index.html` with status 200, so client-side routing works. Missing files such as `/logo.png` are still 404.
5. Anything else is `404`. If the Version has a `404.html` at its root, its content is the response body; otherwise a plain-text `404 page not found`.

Paths with `.` or `..` segments, empty segments, backslashes or control characters are 404.

Caching:

- Files are served with `Cache-Control: public, max-age=0, must-revalidate, s-maxage=300`: browsers revalidate on every visit (a cheap `304` when unchanged, using `Last-Modified`), and a shared cache such as Cloudflare may keep a file for up to 5 minutes.
- Error responses, including a custom `404.html`, use `Cache-Control: no-store`.
- When the server has Cloudflare purging configured, a re-Upload or delete purges the Preview's cached files, so the new Version shows up at once. Without it, a shared cache may serve the previous Version for up to 5 minutes.

Every Preview response also carries `X-Content-Type-Options: nosniff`, `X-Robots-Tag: noindex, nofollow` and `Referrer-Policy: strict-origin-when-cross-origin`. Previews never set cookies.

## curl examples

```sh
export MAKET_API_KEY=mk_...
API=https://app.kotakserver.my.id
```

Upload a single page:

```sh
curl -fsS -H "Authorization: Bearer $MAKET_API_KEY" \
  -F file=@index.html \
  "$API/api/prototypes"
```

Upload a build folder as a zip, with SPA mode and a name, and keep only the Preview URL:

```sh
(cd dist && zip -qr ../dist.zip . -x '.*' '*/.*')
URL=$(curl -fsS -H "Authorization: Bearer $MAKET_API_KEY" \
  -F file=@dist.zip \
  -F spa=true \
  -F name="Pricing page v2" \
  "$API/api/prototypes" | jq -r .url)
echo "$URL"
```

Upload a new Version of an existing Prototype (same URL; name and SPA mode kept unless sent):

```sh
curl -fsS -X PUT -H "Authorization: Bearer $MAKET_API_KEY" \
  -F file=@dist.zip \
  "$API/api/prototypes/k3x9q2ab" | jq -r .url
```

List Prototypes:

```sh
curl -fsS -H "Authorization: Bearer $MAKET_API_KEY" "$API/api/prototypes" \
  | jq -r '.prototypes[] | "\(.slug)  \(.url)"'
```

Get one Prototype:

```sh
curl -fsS -H "Authorization: Bearer $MAKET_API_KEY" "$API/api/prototypes/k3x9q2ab"
```

Delete a Prototype:

```sh
curl -fsS -X DELETE -H "Authorization: Bearer $MAKET_API_KEY" "$API/api/prototypes/k3x9q2ab"
```

`-f` makes curl exit non-zero on an HTTP error but hides the error body. To read the JSON error instead, drop `-f` and check the status:

```sh
curl -sS -o resp.json -w '%{http_code}' -H "Authorization: Bearer $MAKET_API_KEY" \
  -F file=@dist.zip "$API/api/prototypes"
jq -r '.error.code // .url' resp.json
```

curl sends `-F file=@path` with the file's own name, which the server uses to tell `.html` from `.zip` and as the default name. Override it with `-F 'file=@build.zip;filename=site.zip'`.
