# Contributing to maket

Thanks for helping. maket is small, so a short issue or draft PR is a good way to start before a larger change.

## Prerequisites

- **Go 1.27** or newer, as set by the `go` directive in [`apps/api/go.mod`](apps/api/go.mod).
- **Bun**. The repo does not pin a version (no `packageManager` field), and CI installs the latest Bun, so use a recent release.
- **Podman** (optional). Only needed to test the container and deploy setup in [`docs/deploy.md`](docs/deploy.md).
- **[`air`](https://github.com/air-verse/air)** (optional). `bun run dev` uses it for Go hot reload when it is installed, and otherwise runs `go run`.

## Development setup

```sh
bun install
cp .env.example .env
bun run dev
```

`.env` holds the `MAKET_*` settings. For local sign-in, create a GitHub OAuth app with the callback URL `http://app.localhost:5173/api/auth/github/callback`, then put its client ID and secret in `.env` and list your GitHub username in `MAKET_ALLOWED_GITHUB_USERS`.

Once `bun run dev` is running:

- The dashboard is at **http://app.localhost:5173**. Vite proxies `/api` to the Go server on `:8080` and keeps the `Host` header.
- Previews are served by Go at **http://`<slug>`.preview.localhost:8080**.
- Modern browsers resolve `*.localhost` to your machine, so you do not need to edit `/etc/hosts`.

### Dashboard without a server

In development, add `?mock` to the dashboard URL to use an in-memory fake API. It is only compiled into dev builds.

- `?mock`: signed in as a fake User
- `?mock=401`: signed out (Login view)
- `?mock=403`: signed in as a User not on the Allowlist
- `?mock=off`: turn the mock off

## Running checks

Run the focused checks while you work, then the full suite before you open a PR:

```sh
cd apps/api && go test ./...          # Go tests only
gofmt -l apps/api                     # lists files that need formatting; no output means clean
bun run test                          # Go tests plus the dashboard type check
```

`bun run test` is the check that matters for review. Do not run it alongside another build or test run on a small machine.

## Project layout

- `apps/api`: the Go module. `cmd/server` is the server and `cmd/maket` is the CLI.
- `apps/web`: the Svelte 5 dashboard. It is built into `apps/api/internal/webui/dist` and embedded in the server.
- `docs/`: user docs ([`cli.md`](docs/cli.md), [`api.md`](docs/api.md), [`deploy.md`](docs/deploy.md)) and the design brief.

## Vocabulary and decisions

Use the terms in [`GLOSSARY.md`](GLOSSARY.md) in code, issues and docs. Words such as Prototype, Version, Upload, Preview and Slug have specific meanings, and the glossary lists the words to avoid. Read the decisions in [`docs/adr/`](docs/adr/) before changing an area they cover. If your change contradicts an ADR, say so in the PR and explain why.

## Commits and branches

- Use [Conventional Commits](https://www.conventionalcommits.org/): `<type>[scope][!]: <description>`, for example `fix(storage): fail at startup when the S3 bucket is not reachable`. Types include `feat`, `fix`, `docs`, `refactor`, `test`, `perf`, `build`, `ci` and `chore`. Mark breaking changes with `!` or a `BREAKING CHANGE:` footer.
- Name branches `<type>/<short-slug>`, for example `feat/free-registrations`.

## Pull requests

- Keep one topic per PR. Split unrelated changes.
- Add or update tests for behaviour changes.
- Update the docs (`README.md`, `docs/`) when behaviour, flags, endpoints or settings change.
- Make sure `bun run test` passes and say so in the PR.
- Use a Conventional Commit title. The PR template has a checklist.
- Link the issue with `Closes #<number>` when there is one.

## Issues and triage

Open issues with the bug or feature form. Blank issues are disabled. Maintainers triage issues with these labels:

| Label | Meaning |
|---|---|
| `needs-triage` | A maintainer still has to evaluate the issue. New issues start here. |
| `needs-info` | Waiting for the reporter to add details, such as version or logs. |
| `ready-for-agent` | Fully specified and ready for an automated agent to implement. |
| `ready-for-human` | Needs a person to implement it. |
| `wontfix` | Will not be actioned. |

## Releases

Releases are cut from a version tag. Tag the commit on `main` as `vX.Y.Z` and push the tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The `release` workflow runs the Go tests, cross-compiles the CLI for Linux, macOS and Windows, writes `checksums.txt`, and publishes a GitHub Release with the binaries attached.

## Security

Do not report security problems in a public issue. Follow [`SECURITY.md`](SECURITY.md) and use GitHub's private vulnerability reporting.

## License

By contributing, you agree that your contributions are released under the [MIT License](LICENSE).
