# maket CLI

`maket` uploads static frontend Prototypes and manages them from a terminal or an AI agent. It is a single static binary with no runtime dependencies. Everything it does is also possible with plain HTTP; see [`api.md`](api.md).

## Install

1. Download the binary for your OS and architecture from the releases page:

   | OS | amd64 | arm64 |
   |---|---|---|
   | Linux | `maket-linux-amd64` | `maket-linux-arm64` |
   | macOS | `maket-darwin-amd64` | `maket-darwin-arm64` |
   | Windows | `maket-windows-amd64.exe` | `maket-windows-arm64.exe` |

2. Make it executable and put it on your `PATH` under the name `maket`:

   ```sh
   chmod +x maket-linux-amd64
   mv maket-linux-amd64 ~/.local/bin/maket   # or /usr/local/bin/maket
   ```

   On Windows, rename the file to `maket.exe` and place it in a folder listed in `PATH`.

3. Check it:

   ```sh
   maket version
   ```

To build from source instead, run `bun run build:cli` in the repository; the binaries land in `bin/`.

## Sign in

The CLI authenticates with an API Key. Create one in the dashboard under **API keys** (the full key is shown once). An API Key can manage your Prototypes but cannot create, list or revoke API Keys.

```sh
maket login
```

On a terminal this prompts `API Key: ` with hidden input. When stdin is not a terminal it reads one line from stdin, so a key can be piped in:

```sh
echo "$MAKET_KEY" | maket login
```

`login` checks the key against `GET /api/me`, saves the API URL and the key to the config file, and prints `Signed in as <github-login> (<api-url>)` on stderr. It never prints the key.

Use `--url` to sign in to another maket server:

```sh
maket login --url http://app.localhost:5173
```

To forget the key:

```sh
maket logout
```

`logout` removes the API Key from the config file (the API URL is kept), prints `Signed out` on stderr, and exits 0 even when you were not signed in.

### Environment variables

| Variable | Meaning |
|---|---|
| `MAKET_API_KEY` | API Key to use, e.g. `mk_...` |
| `MAKET_API_URL` | Base URL of the maket server. Default `https://app.kotakserver.my.id` |

Each value is resolved on its own, highest priority first:

1. `--url` (only for `maket login`)
2. The environment variable
3. The config file
4. The default (API URL only)

So `MAKET_API_KEY` alone is enough in CI or in an agent sandbox; no `maket login` is needed.

The API URL must start with `http://` or `https://`; a trailing `/` is ignored. If it is plain `http://` and the host is not `localhost`, `*.localhost`, `127.0.0.1` or `::1`, the CLI prints a warning on stderr that the API Key is sent unencrypted, and continues.

### Config file

| OS | Location |
|---|---|
| Linux, macOS, others | `$XDG_CONFIG_HOME/maket/config.json`, else `~/.config/maket/config.json` |
| Windows | `%AppData%\maket\config.json` |

```json
{
  "api_url": "https://app.kotakserver.my.id",
  "api_key": "mk_..."
}
```

The directory is created with mode 0700 and the file with mode 0600. The file is replaced atomically on every write.

## Commands

Flags may come before or after positional arguments (`maket push ./dist --spa` and `maket push --spa ./dist` are the same). Both `-flag` and `--flag` work. `-h` or `--help` on any command prints its usage on stdout and exits 0. `maket help` prints the top-level usage; `maket` with no arguments prints it on stderr and exits 2.

### `maket whoami`

```
maket whoami [--json]
```

Prints the GitHub login of the User the API Key belongs to.

```sh
$ maket whoami
octocat
```

### `maket push`

```
maket push <path> [--name NAME] [--spa[=false]] [--slug SLUG] [--json]
```

Uploads a file or a folder. Without `--slug` it creates a new Prototype; with `--slug` it uploads a new Version of that existing Prototype, which replaces the previous one at the same Preview URL.

| Flag | Meaning |
|---|---|
| `--name NAME` | Display name, 1 to 100 characters. New Prototypes default to the file or folder name without its extension. |
| `--spa`, `--spa=false` | Turn SPA mode on or off. New Prototypes default to off. |
| `--slug SLUG` | Upload a new Version of the Prototype with this Slug. |
| `--json` | Print the Prototype as JSON instead of the URL. |

With `--slug`, `--name` and `--spa` are only sent when given; when omitted, the Prototype keeps its current name and SPA mode.

`<path>` is either:

- a file ending in `.html`, `.htm` or `.zip` (any case), sent as is. Any other file type is a usage error (exit 2) before any request is made. A single page is served as `index.html`.
- a folder, zipped in memory. See [Folder zipping](#folder-zipping).

By default stdout gets only the Preview URL and a newline:

```sh
$ maket push ./dist --spa
https://k3x9q2ab.cloudshare.my.id
```

When stderr is a terminal, a short progress line such as `Uploading dist.zip (1.2 MB)...` may appear on stderr. Nothing else is written to stdout.

Examples:

```sh
maket push index.html
maket push ./dist --name "Pricing page v2" --spa
maket push ./dist --slug k3x9q2ab              # new Version, same URL, same name and SPA mode
maket push site.zip --slug k3x9q2ab --spa=false
```

### `maket ls`

```
maket ls [--json]
```

Lists your Prototypes, most recently updated first, as a table on stdout, followed by your Quota usage on stderr:

```
$ maket ls
SLUG      NAME             VERSION  SIZE    UPDATED           URL
k3x9q2ab  Pricing page v2  3        1.2 MB  2026-10-19 15:04  https://k3x9q2ab.cloudshare.my.id
p7m2c8zt  index            1        820 B   2026-10-18 09:12  https://p7m2c8zt.cloudshare.my.id
1.2 MB of 1 GB used
```

Sizes are 1024-based. `UPDATED` is in local time. With no Prototypes, `ls` prints `No prototypes yet` on stderr and nothing on stdout.

### `maket rm`

```
maket rm <slug> [--yes] [--json]
```

Deletes a Prototype and all its files. Its Preview URL stops working.

Without `--yes`, when both stdin and stderr are terminals, `rm` asks `Delete <slug>? [y/N] ` on stderr. Anything other than `y` or `yes` aborts with exit 1. When not on a terminal, `--yes` is required; without it `rm` fails with a usage error (exit 2).

On success it prints `Deleted <slug>` on stderr and nothing on stdout.

```sh
maket rm k3x9q2ab --yes
```

### `maket version`

```
maket version
maket --version
```

Prints the CLI version on stdout.

## Output

### stdout and stderr

- stdout carries only the result: the Preview URL for `push`, the login for `whoami`, the table for `ls`, or JSON with `--json`.
- stderr carries everything meant for people: prompts, progress, confirmations (`Deleted ...`, `Signed in as ...`), Quota usage, warnings and errors.

### Errors

An error is one line on stderr:

```
maket: the API Key was rejected (revoked or wrong): run `maket login`
```

When an Upload is rejected because of specific files, the offending paths follow, one per line, indented two spaces, then a count of any not listed:

```
maket: These files have types that are not allowed; remove them and upload again. Allowed: .html .htm .css ...
  backup.tar.gz
  notes.docx
  ...and 3 more
```

### `--json` output

`--json` prints exactly one JSON document on stdout, on success and on failure. The output is stable: fields may be added, but are never renamed or removed. Timestamps are RFC 3339 in UTC and may include fractional seconds.

`whoami --json`:

```json
{
  "id": 1,
  "github_login": "octocat",
  "name": "The Octocat",
  "avatar_url": "https://avatars.githubusercontent.com/u/583231?v=4",
  "auth_method": "api_key"
}
```

`push --json` (the Prototype):

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
| `slug` | The Slug; pass it to `--slug` and `rm` |
| `name` | Display name |
| `url` | The Preview URL |
| `spa` | Whether SPA mode is on |
| `version` | Current Version number, starting at 1 |
| `size_bytes` | Extracted size of the current Version; counts toward the Quota |
| `file_count` | Number of files in the current Version |
| `created_at`, `updated_at` | When the Prototype was created and last uploaded |

`ls --json`:

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

`prototypes` is `[]`, never `null`, when you have none.

`rm --json`:

```json
{"slug": "k3x9q2ab", "deleted": true}
```

Failure, with any command and `--json`: the same error line still goes to stderr, and stdout gets an error object. Errors from the server are passed through unchanged (see the error codes in [`api.md`](api.md#errors)):

```json
{
  "error": {
    "code": "disallowed_files",
    "message": "These files have types that are not allowed; remove them and upload again. Allowed: .html .htm .css .js .mjs .json .map .txt .xml .webmanifest .png .jpg .jpeg .gif .webp .avif .svg .ico .woff .woff2 .ttf .otf .mp4 .webm .mp3.",
    "details": {
      "paths": ["backup.tar.gz", "notes.docx"],
      "more": 0
    }
  }
}
```

`details` is present only when specific files caused the failure. Errors raised by the CLI itself use these codes:

| Code | Meaning |
|---|---|
| `usage` | Bad flags or arguments, a push path that does not exist, unsupported file type, empty folder, empty `--slug` or API Key, an invalid API URL, or `rm` without `--yes` off a terminal |
| `not_signed_in` | No API Key configured |
| `network` | The server could not be reached |
| `invalid_response` | The server answered with something that is not the expected JSON; the message includes the HTTP status |
| `aborted` | `rm` confirmation was declined |
| `local_error` | A local file could not be read or written: the config file, the path being pushed, or stdin |

```json
{
  "error": {
    "code": "not_signed_in",
    "message": "not signed in: run `maket login` or set MAKET_API_KEY"
  }
}
```

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Any other failure: server error, network error, rejected Upload, Quota exceeded, aborted `rm` |
| 2 | Usage error: bad flags or arguments, unsupported file type, `rm` without `--yes` off a terminal |
| 3 | Not signed in, or the API Key was rejected (no key configured, HTTP 401, or HTTP 403 `not_allowed` because the key's owner is no longer on the Allowlist) |
| 4 | Not found: no Prototype with that Slug (HTTP 404) |

These codes are stable.

## Folder zipping

When `<path>` is a folder, `push` builds a zip in memory:

- Every regular file under the folder is included, with paths relative to the folder and `/` separators.
- Any file or folder whose name starts with `.` is skipped (`.git`, `.env`, `.DS_Store`, `.well-known`, ...).
- Symlinks and other non-regular files are skipped; their targets are not followed.
- An empty folder (after skipping) is an error before any request is made.
- The zip is named after the folder (`dist.zip` for `./dist`; `.` uses the current folder's name), so a new Prototype's default name is the folder name.

The folder must contain `index.html` at its top level, or inside a single top-level subfolder, which the server strips. Every file must have an allowed extension; see [Limits](api.md#limits). The server also drops `__MACOSX/` entries, `.DS_Store` and `Thumbs.db` files.

## For AI agents

Recommended invocation:

```sh
URL=$(maket push ./dist --spa) || exit $?
echo "Preview: $URL"
```

- Set `MAKET_API_KEY` (and `MAKET_API_URL` for a non-default server) in the environment instead of running `maket login`.
- `push` prints only the Preview URL on stdout, so `$(...)` captures exactly the URL.
- Check the exit code: 2 means fix the command, 3 means the API Key is missing or rejected, 4 means the Slug does not exist, 1 is anything else.
- Use `--json` when you need the Slug or other fields, and to get errors as JSON with a stable `error.code`. `details.paths` lists the files to fix after a rejected Upload.
- To update a Preview in place, keep the Slug from the first push and use `maket push ./dist --slug <slug>`; the URL does not change.
- Use `maket rm <slug> --yes`; without `--yes` a non-interactive `rm` fails.

```sh
OUT=$(maket push ./dist --spa --json)
SLUG=$(printf '%s' "$OUT" | jq -r .slug)
URL=$(printf '%s' "$OUT" | jq -r .url)
```
