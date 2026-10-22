# Performance notes

Status: notes for the next phase. Nothing here has been measured yet.

## Why the standard library

The server uses `net/http` with `http.ServeMux` patterns (Go 1.22+), as the build brief specifies. No web framework.

- Go 1.22's ServeMux matches methods and path wildcards (`GET /api/prototypes/{slug}`), which covered most of what routers like Gin or chi added. maket has 13 explicit API routes, a few middleware (auth, Origin check, security headers) and routing by `Host` (app vs. Preview).
- Gin runs on `net/http`, so it serves requests no faster; its router is only quicker in microbenchmarks.
- Fiber runs on `fasthttp`, which is not `net/http`-compatible: standard middleware, `httptest`, `golang.org/x/oauth2` integration and `http.ServeContent` (ranged reads for Previews from S3) would need adapters or rewrites. Its advantage shows mostly in synthetic hello-world benchmarks.
- Few dependencies: 6 direct modules (`aws-sdk-go-v2`, `aws-sdk-go-v2/credentials`, `aws-sdk-go-v2/service/s3`, `golang.org/x/oauth2`, `golang.org/x/term`, `modernc.org/sqlite`). Smaller image (about 18 MB at M7), smaller supply-chain surface, fewer Dependabot updates.
- If the API grows to dozens of routes with many middleware chains, the first candidate is chi, because it is `net/http`-compatible.

## Why no Viper or Cobra in the CLI

- Config is two values (API URL, API Key) from environment variables or one JSON file; see `docs/cli.md`.
- Viper brings many indirect modules (YAML/TOML parsers, fsnotify, mapstructure, …) and case-insensitive keys, for no gain here.
- Six commands are handled with the standard `flag` package, with exact control over exit codes and `--json` output.

## Where time is expected to go

Routing cost is expected to be small next to the costs below. It has not been measured. Expected costs, larger by orders of magnitude:

| Path | Expected cost |
|---|---|
| Preview request (local storage) | disk read, Content-Type, headers |
| Preview request (S3/R2) | network round-trip to R2 per uncached file; Cloudflare caches Previews in front |
| Upload | request body size, zip extraction, writes to storage, SQLite transaction |
| Dashboard / API | SQLite queries |

## Benchmark plan (next phase)

Run on the production server, not the dev VPS (it is a personal machine).

1. Preview throughput and latency (p50/p95/p99) for a small HTML file and a ~1 MB asset, local and S3 storage, bypassing Cloudflare (direct to Caddy) and through Cloudflare.
2. Upload: time and peak memory for uploads near the size limits (`MAKET_MAX_UPLOAD_MB`, `MAKET_MAX_EXTRACTED_MB`, `MAKET_MAX_FILES`).
3. Concurrent uploads and reads: SQLite write contention.
4. Resource use at idle and under load: RSS, CPU, open files.
5. Go profiling (`pprof`) during runs to find hot spots before changing anything.

Candidate tools: `oha` or `k6` for load, `go test -bench` for hot functions. Record hardware, versions and commands with every result.
