# Changelog

All notable user-visible changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Structured logging with `log/slog` (roadmap R3): `-log-format` /
  `FOUNDRY_LOG_FORMAT` (`text` default, or `json`) and `-log-level` /
  `FOUNDRY_LOG_LEVEL` (`debug`, `info` default, `warn`, `error`).
- `X-Request-Id` on every response; a well-formed client value is kept,
  otherwise a UUID is generated. Log records of a request carry its
  `request_id`.
- `GET /healthz` (liveness) and `GET /readyz` (database readiness, 503 when
  not ready). Neither requires authentication.
- Optional Prometheus metrics at `GET /metrics` (`-metrics` /
  `FOUNDRY_METRICS`, default off): HTTP requests, latency, blob bytes, GC
  runs. Adds the `github.com/prometheus/client_golang` dependency.

- Optional HTTP Basic authentication (roadmap R1). Enable it with
  `-auth-htpasswd` / `FOUNDRY_AUTH_HTPASSWD` pointing at a bcrypt htpasswd
  file (`htpasswd -B`). `docker login` works; unauthenticated pushes, deletes
  and GC get 401 `UNAUTHORIZED` with a `WWW-Authenticate: Basic` challenge.
- `-auth-anonymous-pull` / `FOUNDRY_AUTH_ANONYMOUS_PULL` (default `true`):
  with authentication on, allow unauthenticated pulls and read-only browsing
  of the API and web UI.
- `regctl` credentials: `FOUNDRY_USER` / `FOUNDRY_PASSWORD`, or `-user` with
  `-password-stdin`.
- Startup warning when authentication is enabled on a non-loopback address
  (Basic credentials are cleartext without TLS).
- `regctl -timeout` (default `30s`, `0` for none). `gc` has no timeout unless
  `-timeout` is given. Ctrl-C cancels the in-flight request.

### Changed

- Log output format changed: one structured `request` record per request
  replaces chi's access log line, and all server messages are structured
  (text by default). Update any log parsing accordingly.
- The web UI no longer loads htmx from unpkg: htmx 1.9.12 is embedded in the
  binary and served from `/static/js/htmx.min.js`, so the UI works without
  network access (roadmap R4).
- CSRF protection is always on: state-changing requests from another site
  (detected via `Sec-Fetch-Site`/`Origin`) are rejected with 403, and
  `POST /ui/gc` requires the `HX-Request: true` header that htmx sends.
  Non-browser clients are unaffected.
- With authentication on, `GET /v2/` returns 401 to unauthenticated requests
  so that clients discover the registry requires login.
- Requests are cancelled when the client disconnects or times out (roadmap
  R2). A cancelled GC stops at the next blob; deletions already made stay and
  the next run continues. Errors caused by cancellation return 503
  "request cancelled" instead of 500 and are not logged as server errors.
- Graceful shutdown now cancels requests still running after the 30s drain
  period and waits up to 5s for them to finish, instead of exiting with them
  still running. A second SIGINT/SIGTERM exits immediately.
