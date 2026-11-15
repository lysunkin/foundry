# Changelog

All notable user-visible changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

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

### Changed

- CSRF protection is always on: state-changing requests from another site
  (detected via `Sec-Fetch-Site`/`Origin`) are rejected with 403, and
  `POST /ui/gc` requires the `HX-Request: true` header that htmx sends.
  Non-browser clients are unaffected.
- With authentication on, `GET /v2/` returns 401 to unauthenticated requests
  so that clients discover the registry requires login.
