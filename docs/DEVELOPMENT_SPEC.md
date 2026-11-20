# Foundry Registry — Development Specification

| | |
|---|---|
| **Status** | Current as of the November 2026 review/hardening pass |
| **Audience** | Engineers maintaining or extending Foundry |
| **Scope** | Registry server (`cmd/registry`), CLI (`cmd/regctl`), all `internal/` packages |

This document defines what Foundry must do, the invariants the code relies on,
and the engineering rules for changing it. §9 records the defects found in the
review and how each was resolved. §10 is the prioritized roadmap. §11 lists
post-MVP stretch goals.

---

## 1. Goals and non-goals

**Goals**

1. A single-binary, zero-dependency (no external DB, no Node) OCI registry for
   local development and small trusted networks.
2. Interoperate with standard clients (Docker, containerd, Podman, crane, oras)
   via the OCI Distribution Spec v1.1 *pull*, *push* and *content management*
   categories.
3. Make state trivially inspectable and backed up: everything lives in `data/`.
4. Provide an admin API, CLI and server-rendered web UI for browsing and cleanup.

**Non-goals (for now)**

- Multi-tenant or internet-facing deployment. Authentication is optional
  single-role Basic auth (§6.3); there is no per-repository authorization
  (R10) and no TLS.
- Horizontal scaling: exactly one process owns a `data/` directory.
- Pull-through caching / mirroring.

---

## 2. Architecture

```
cmd/registry ──► internal/registry (server wiring, http.Server)
                    │
                    ├─► handlers  (HTTP: /v2, /api, /ui; routing.go)
                    │      │
                    │      ├─► services (metadata, manifest rules, GC)
                    │      │      └─► database/sql  ──► SQLite (internal/db, migrations)
                    │      └─► storage  (blob store, upload store) ──► filesystem
                    │
                    └─► ui (per-page html/template sets) + web (static assets)

internal/oci   ── shared spec constants and validators (no dependencies)
internal/model ── plain data structs
cmd/regctl     ── CLI; talks only to /api over HTTP
```

**Layering rules**

- `oci` and `model` import nothing from this module.
- `storage` must not import `services` or `handlers`.
- `services` may import `storage` (GC only) and `oci`.
- `handlers` are the only layer that knows about HTTP status codes and OCI
  error codes. Services return sentinel errors (§5.4).
- Consumers depend on small interfaces they define themselves (R5), listing
  only the methods they call: `handlers/stores.go` (`RegistryMetadata`,
  `APIMetadata`, `WebMetadata`, `BlobStore`, `UploadStore`, `Collector`) and,
  for GC, unexported interfaces in `services/gc.go`. Only `registry.New`
  (the composition root) handles the concrete types. GC runs through
  `services.Collector` (`NewCollector(repo, blobs, uploads).Collect`).
- Validation of untrusted input happens at the HTTP boundary (`handlers`) **and**
  at the storage choke point (`storage.blobPath`, `UploadStore.safePath`). This
  is defence in depth: storage must stay safe even when called incorrectly.

---

## 3. Data model

### 3.1 SQLite schema (after migration 002)

| Table | Key | Notes |
|---|---|---|
| `repositories` | `id` PK, `name` UNIQUE | Created implicitly on first push |
| `blobs` | `digest` PK | Global (not per-repo). `created_at` drives the GC grace period for rows with no file |
| `manifests` | **(`repository_id`, `digest`)** PK | Raw content stored inline. FK → repositories ON DELETE CASCADE |
| `tags` | (`repository_id`, `tag`) PK | FK (`repository_id`, `manifest_digest`) → manifests ON DELETE CASCADE |
| `uploads` | `id` PK | FK → repositories ON DELETE CASCADE |
| `schema_migrations` | `version` PK | Managed by `internal/db` |

Connection settings: WAL journal, `foreign_keys=ON`, `busy_timeout=5000`,
`synchronous=NORMAL`, `MaxOpenConns=1` (single writer).

### 3.2 Invariants

| ID | Invariant | Enforced by |
|---|---|---|
| D1 | A manifest belongs to exactly one repository; the same digest may exist in many repositories independently | Composite PK |
| D2 | A tag always points at a manifest in the **same** repository | Composite FK |
| D3 | Deleting a repository removes its manifests, tags and upload sessions | ON DELETE CASCADE |
| D4 | Deleting a manifest removes all tags pointing at it | ON DELETE CASCADE |
| D5 | Every non-foreign blob referenced by a stored (schema 2) manifest had a `blobs` row when the manifest was stored | `PutManifest` check, inside a transaction |
| D6 | Every child manifest of a stored index exists in the same repository | `PutManifest` check |
| D7 | Manifest + tag writes are atomic | Single transaction in `PutManifest` |
| D8 | Stored manifest `media_type` is one of `oci.IsManifestMediaType` | `PutManifest` (security: §6.3) |

### 3.3 Filesystem layout

```
data/blobs/sha256/<hex[0:2]>/<hex>     immutable blob content
data/blobs/sha256/upload-*.tmp         in-flight monolithic writes (GC'd after upload TTL)
data/uploads/<id>.upload               chunked upload sessions
```

| ID | Invariant |
|---|---|
| F1 | A file at `blobs/sha256/xx/<hex>` always hashes to `sha256:<hex>`. Writes go to a temp file, are verified, then atomically renamed |
| F2 | No path is ever built from a digest or upload ID that failed validation (`oci.ValidDigest`, `storage.ValidUploadID`) |
| F3 | A failed digest verification never leaves a committed blob behind |

### 3.4 Migrations

- Files live in `internal/db/migrations/NNN_description.sql`, embedded and
  applied in lexical order, each in its own transaction, on startup.
- **Never edit an applied migration.** Add a new file.
- Table rebuilds follow the pattern in `002_repo_scoped_manifests.sql`: create
  `*_new`, copy, drop the old table (children first), then rename. `ALTER TABLE
  … RENAME` rewrites FK references automatically.
- Every migration that transforms data needs a test that builds the previous
  schema and upgrades it (see `internal/db/db_test.go`).

---

## 4. HTTP routing

Repository names may contain `/`. chi path parameters match one segment only,
so routes that contain a name are registered as wildcards (`/v2/*`,
`/api/repos/*`, `/api/tags/*`, `/api/manifests/*`, `/ui/repo/*`) and dispatched
by `handlers/routing.go`:

- Each `route` either has a fixed `suffix` (e.g. `/tags/list`) or a `marker`
  (e.g. `/manifests/`), split on the **last** occurrence. The part after the
  marker must not contain `/`.
- Routes are tried in order. `/blobs/uploads/` must precede `/blobs/`.
- The matched values are written into chi's `URLParams` (`name`, plus
  `digest`/`reference`/`uuid`/`tag`), so handlers keep using `chi.URLParam`.
- A path that matches a route but not its method returns 405 with `Allow`.

**Known ambiguity:** in `/api/repos/*` and `/ui/repo/*`, a repository whose
last path component is literally `tags` (e.g. `x/tags`) is shadowed by the
`/tags` suffix route. `/v2` is not affected. Accepted; revisit if it matters.

---

## 5. API behaviour contract

### 5.1 Input validation (`internal/oci`)

| Input | Rule | Failure |
|---|---|---|
| Repository name | Distribution spec regexp, ≤ 255 chars | 400 `NAME_INVALID` |
| Tag | `[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}` | PUT: 400 `MANIFEST_INVALID`; GET/HEAD/DELETE: 404 `MANIFEST_UNKNOWN` (it cannot exist) |
| Digest | `sha256:[a-f0-9]{64}` only | 400 `DIGEST_INVALID` |
| Reference | Contains `:` → digest rules, else tag rules | as above |
| Upload ID | `[A-Za-z0-9_-]{1,64}` (server issues UUIDs) | 404 `BLOB_UPLOAD_UNKNOWN` |
| Manifest body | ≤ 4 MiB, valid JSON, schemaVersion 1 or 2, valid descriptor digests | 413 `SIZE_INVALID` / 400 `MANIFEST_INVALID` |
| Manifest media type | Must satisfy `oci.IsManifestMediaType` | 400 `MANIFEST_INVALID` |

Media type resolution on push: the document's `mediaType` field, else the
request `Content-Type` (parameters stripped; generic JSON/octet-stream ignored),
else inferred from shape (`manifests` present → OCI index, otherwise OCI manifest).

### 5.2 Registry endpoints: required behaviour

| Operation | Success | Notable failures / rules |
|---|---|---|
| `GET /v2/` | 200 `{}` | — |
| `GET /v2/_catalog?n&last` | 200, `Link: <…>; rel="next"` when truncated | 400 bad `n` |
| `HEAD/GET blob` | 200 or 206 via `http.ServeContent`; `ETag`, immutable `Cache-Control` | 404 `BLOB_UNKNOWN`. Existence is defined by the **file**, not the DB row |
| `DELETE blob` | 202, file and row removed | 404 if neither exists |
| `POST uploads/?mount=&from=` | 201 if the blob exists (blobs are global) | Otherwise falls through to a new session (202) |
| `POST uploads/?digest=` | 201 | 400 `DIGEST_INVALID`. Nothing stored on mismatch (F3) |
| `POST uploads/` | 202, `Location`, `Docker-Upload-UUID` | — |
| `PATCH upload` | 202, `Range: 0-<end>` | 416 if `Content-Range` is malformed or doesn't start at the current offset. Serialised per session |
| `PUT upload?digest=` | 201 | 400 on digest/size mismatch. The session is always destroyed after PUT |
| `DELETE upload` | 204 | 404 if unknown |
| Upload ops on another repo's session | — | 404 (sessions are scoped to their repo) |
| Any blob upload over `-max-blob-size` (offset + body) or `-storage-quota` | — | 413 `SIZE_INVALID` with `Connection: close`; rejected before reading when `Content-Length` is too large, otherwise as soon as the limit is crossed. Monolithic: nothing stored. Chunked: the session is discarded (R6) |
| `PUT manifest` | 201, `Location` by digest | 400 `DIGEST_INVALID` if a digest reference ≠ content hash; 400 `MANIFEST_BLOB_UNKNOWN` (D5/D6) |
| `GET/HEAD manifest` | 200 with the stored media type | 404 `MANIFEST_UNKNOWN` |
| `DELETE manifest/<digest>` | 202, tags cascade | 404 |
| `DELETE manifest/<tag>` | 202, only the tag is removed (OCI 1.1) | 404 |
| `GET tags/list?n&last` | 200, sorted, `Link` when truncated | 404 `NAME_UNKNOWN` |

Every `/v2` response sets `Docker-Distribution-Api-Version: registry/2.0` and
`X-Content-Type-Options: nosniff`.

### 5.3 Admin API

Unchanged paths (see README). JSON errors are `{"error": "<msg>"}`. GC results
use snake_case fields: `scanned_blobs`, `deleted_blobs`, `freed_bytes`,
`deleted_uploads`, `dry_run`, `errors` (always an array). `?dry_run=true` is
supported.

### 5.4 Error handling rules

- Services return sentinels: `ErrNotFound`, `ErrManifestInvalid`,
  `ErrManifestBlobUnknown`, `ErrDigestMismatch`. Storage returns
  `ErrBlobNotFound`, `ErrInvalidDigest`, `ErrDigestMismatch`, `ErrSizeMismatch`,
  `ErrInvalidUploadID`. Wrap with `%w`; compare with `errors.Is`.
- **Not-found and failure are different.** A database error must never be
  reported as 404 (the original code did this throughout).
- 5xx responses carry a generic message (`UNKNOWN` / "internal server error").
  Details are logged server-side only, never sent to the client.
- Log request paths with `%q` (log-injection safety).

---

## 6. Cross-cutting concerns

### 6.1 Concurrency

| Resource | Mechanism |
|---|---|
| SQLite writes | `MaxOpenConns=1` plus `busy_timeout` |
| Blob commit | Atomic rename. Identical content makes concurrent writers of one digest harmless (no locks) |
| Upload session | `UploadStore.Lock(id)` around PATCH/PUT |
| GC vs. manifest writes | `services.gcLock` (RWMutex): GC holds the write lock, `PutManifest` a read lock |

### 6.2 Garbage collection

```
lock gcLock (exclusive)
mark   := ⋃ digests referenced by every manifest in every repository
for d in (blobs on disk ∪ blob rows in DB):
    skip if d ∈ mark
    skip if file mtime (or row created_at if no file) is younger than GracePeriod
    delete file, then row        (dry-run: count only)
expire upload sessions older than UploadTTL (row + temp file)
remove orphaned blob temp files older than UploadTTL
```

- Untagged manifests are **retained**, and their blobs stay alive.
- The grace period covers the push window (layers are uploaded before the
  manifest that references them). **Residual race:** a client that HEADs an
  *old* unreferenced blob, skips uploading it, and pushes a manifest after GC
  deleted it will get `MANIFEST_BLOB_UNKNOWN` and must re-push. This is safe
  (no corrupt image is ever stored) and matches distribution/distribution.

### 6.3 Security posture

- Authentication (R1) is optional and off unless `-auth-htpasswd` is set.
  `handlers.RequireAuth` wraps the whole router:
  - HTTP Basic against a bcrypt-only htpasswd file (`internal/auth`), loaded
    at startup. Unknown users are compared against a dummy hash of the
    highest cost in the file, so timing does not reveal usernames.
  - Reads (`GET`/`HEAD`) are anonymous when `-auth-anonymous-pull` (default
    `true`); everything else needs valid credentials. `GET /v2/` always
    challenges unauthenticated requests so clients discover auth and
    `docker login` verifies the password. `/static/` is public.
  - Credentials that are present but wrong are rejected (401) even on
    requests allowed anonymously, and logged with username and remote address.
  - 401s carry `WWW-Authenticate: Basic realm="foundry"` and use the error
    format of the API (`/v2`: OCI `UNAUTHORIZED`; `/api`: JSON; else text).
  - A Docker token server (Bearer challenge) was considered and rejected: a
    spike showed Docker 29 (containerd image store) handles a plain Basic
    challenge with anonymous pull and authenticated push. Clients using the
    classic image store are unverified.
  - Single role; no per-repository authorization until R10.
- The default bind is `:8080` (all interfaces) to keep Docker Desktop working;
  the server warns when auth is enabled on a non-loopback address, because
  Basic credentials travel in cleartext without TLS.
- Path traversal is prevented by digest/ID validation inside storage (F2).
- Stored XSS: manifest bytes are served with their stored media type, so only
  manifest media types are accepted (D8) and `nosniff` is set.
- CSRF (always on, with or without auth): `http.CrossOriginProtection`
  rejects unsafe-method requests whose `Sec-Fetch-Site`/`Origin` mark them as
  cross-site (403). Non-browser clients send neither header and pass.
  `POST /ui/*` additionally requires `HX-Request: true` (`RequireHTMX`), which
  a cross-site form cannot set without a CORS preflight.
- Resource limits: manifest body 4 MiB; `ReadHeaderTimeout` 10s,
  `MaxHeaderBytes` 1 MiB; optional `-max-blob-size` and `-storage-quota`
  (both off by default, see §6.8).

### 6.4 Web UI

- `ui.Load` builds **one template set per page** (page + `base.html` +
  shared partials). A single shared set made every page render the
  last-parsed page's `content` block, so the dashboard showed the tag page.
- Rendering goes into a buffer and is written only on success.
- Deletes use `hx-swap="delete"` on the row (or a redirect for manifests),
  never swap API JSON into the page.
- htmx 1.9.12 is vendored at `web/static/js/htmx.min.js` (0BSD license
  alongside) and embedded in the binary, so the UI needs no network access
  (R4). The file is byte-identical to `dist/htmx.min.js` in the npm package
  `htmx.org@1.9.12` (sha256
  `449317ade7881e949510db614991e195c3a099c4c791c24dacec55f9f4a2a452`).
  Templates must not load assets from other origins
  (`TestTemplatesHaveNoExternalAssets`).

### 6.5 Configuration

| Flag / env | Default | Purpose |
|---|---|---|
| `-addr` / `FOUNDRY_ADDR` | `:8080` | Listen address |
| `-data` / `FOUNDRY_DATA` | `./data` | State directory |
| `-gc-grace` | `1h` | GC grace period |
| `-upload-ttl` | `24h` | Upload session expiry |
| `-auth-htpasswd` / `FOUNDRY_AUTH_HTPASSWD` | empty (auth off) | bcrypt htpasswd file |
| `-auth-anonymous-pull` / `FOUNDRY_AUTH_ANONYMOUS_PULL` | `true` | Anonymous reads when auth is on. Invalid env values fail startup |
| `-log-format` / `FOUNDRY_LOG_FORMAT` | `text` | `text` or `json`; anything else fails startup |
| `-log-level` / `FOUNDRY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`; anything else fails startup |
| `-metrics` / `FOUNDRY_METRICS` | `false` | Serve `/metrics`. Invalid env values fail startup |
| `-max-blob-size` / `FOUNDRY_MAX_BLOB_SIZE` | `0` (no limit) | Bytes, or with `KB`/`MB`/`GB`/`TB` (×1000) or `KiB`/`MiB`/`GiB`/`TiB` (×1024). Invalid values fail startup |
| `-storage-quota` / `FOUNDRY_STORAGE_QUOTA` | `0` (no limit) | Same format |
| `FOUNDRY_URL` / `regctl -registry` | `http://localhost:8080` | CLI target |
| `FOUNDRY_USER`, `FOUNDRY_PASSWORD` / `regctl -user`, `-password-stdin` | empty | CLI credentials (Basic auth) |
| `regctl -timeout` | `30s` | Per-request timeout, `0` for none. `gc` has none unless set explicitly |

`registry.Config` zero values **disable** grace/TTL (handy in tests).
Production callers must set them explicitly, as `cmd/registry` does. With
`Users` set, the zero value of `AnonymousPull` (false) requires auth for
everything; `cmd/registry` defaults the flag to true.

### 6.6 Cancellation and shutdown (R2)

- `context.Context` flows from the request (`r.Context()`) through services
  (`QueryContext`, `ExecContext`, `BeginTx`) and the storage operations that
  stream or walk directories (`Put`, `PutVerified`, `Append` via
  `copyContext`; `AllDigests`, `RemoveStaleTemp`). Single-file storage
  operations (`Stat`, `Get`, `Delete`, `PutAt`) take no context: they cannot
  be interrupted mid-syscall.
- A request is cancelled when its client disconnects or times out, or when
  shutdown gives up waiting (below).
- Errors raised while the request context is done are **not** server faults:
  `serverError`, `apiServerError` and the web `fail` answer 503 "request
  cancelled" in the API's format and log at info level.
- Cancelled writes are atomic: `PutManifest` rolls back (D7); a cancelled
  `Put` commits nothing and removes its temp file (F3). A cancelled `Append`
  leaves the session's file in an unknown state relative to its DB offset.
- Upload-session cleanup (`discardUpload`) uses `context.WithoutCancel`, so a
  finished session is discarded even if its request was cancelled.
- **GC** checks the context before the mark phase, before each blob and
  before each expired upload, and returns an error wrapping `ctx.Err()`.
  Deletions already made stay; every step is idempotent, so the next run
  continues. A blob's file is deleted before its row, so cancellation can at
  worst leave a row without a file, which the next GC removes.
- **Not cancellable:** waiting for `gcLock`. A push blocked behind a long GC
  waits until GC finishes or is cancelled.
- **Shutdown** (SIGINT/SIGTERM): stop accepting connections and wait up to
  30s for in-flight requests; then cancel the request base context
  (`HTTPServer(base)` sets `BaseContext`) and wait up to 5s more. A second
  signal terminates immediately. `http.Server.Shutdown` alone does not
  cancel running handlers, hence the two phases.
- **Blocked body reads:** cancelling a request context server-side does not
  interrupt a handler blocked reading a stalled request body (only a client
  disconnect does). `handlers.AbortReadsOnCancel`, the outermost middleware,
  sets an immediate read deadline on cancellation so such uploads unwind.
- **Startup:** `db.Open(ctx, …)` uses the signal context; an interrupted
  migration rolls back its transaction.

### 6.7 Observability (R3)

- **Logging:** `log/slog` only. `cmd/registry` installs the logger built by
  `internal/logging` with `slog.SetDefault`; packages log with
  `slog.*Context(ctx, …)` so the handler can add `request_id`. Use stable
  keys (`err`, `method`, `path`, `user`, `remote_addr`, `digest`,
  `upload_id`). Levels: server faults `error`; rejected credentials and
  degraded readiness `warn`; lifecycle, cancellations, GC summary,
  migrations `info`; per-item detail (each GC deletion) `debug`. Never log
  query strings, bodies, `Authorization`, or passwords.
- **Middleware order** (`registry.New`, outermost first):
  `AbortReadsOnCancel` (needs the server's own writer) → `RequestID` →
  `AccessLog(observer)` → `Recover` → CSRF → `RequireAuth` → routes.
  `Recover` sits inside `AccessLog` so panics are logged and counted as 500.
  `AccessLog`'s response wrapper implements `Unwrap` (for
  `http.ResponseController`) and `ReadFrom` (keeps sendfile for blobs).
- **Request IDs:** a client `X-Request-Id` matching `[A-Za-z0-9._-]{1,64}` is
  kept, otherwise replaced by a UUID; always echoed.
- **Health:** `/healthz` checks nothing (liveness must not depend on the
  DB); `/readyz` pings the DB with a 2s timeout. Both bypass `RequireAuth`.
- **Metrics** (`internal/metrics`, off by default): own
  `prometheus.Registry`; HTTP metrics come from `AccessLog`'s
  `RequestObserver`, GC metrics from a `Collector` decorator. Labels must
  stay bounded: route class (`metrics.Route`), known methods or `other`,
  status code, GC result. Never repository names, tags, digests or users.
  Manifest push/pull counts are HTTP request series, not separate metrics.

### 6.8 Resource limits (R6)

- **Usage accounting:** `storage.Usage`, shared by both stores
  (`storage.WithUsage`), counts the files under `data/blobs/` (including
  `*.tmp`) and `data/uploads/`; the DB is not counted. `Init` walks them once
  at startup. Afterwards every write **reserves** bytes before writing
  (`quotaWriter`; re-sent upload bytes that overwrite counted data are free)
  and every successful file removal releases the size read just before it.
  Invariant: the count equals the bytes on disk. Any new code that creates or
  removes files in storage must go through `quotaWriter` / `removeFile`.
- **Quota:** a reservation that would pass the quota fails with
  `storage.ErrQuotaExceeded`, so concurrent writers can never exceed it
  together. Starting above the quota is allowed (warned); writes are rejected
  until GC frees space. A warning is logged once when usage crosses 90% and
  re-armed below it.
- **Known drift:** two writers committing the same digest at the same instant
  both rename, so one file's bytes stay counted. The count can only be too
  high; a restart recomputes it.
- **Max blob size** is enforced in `handlers` (`limitBody`): up front from
  `Content-Length`, then by a reader that fails on the first byte past the
  limit (offset included for chunked uploads). Mounts copy nothing and are
  not limited.
- **On rejection:** 413 `SIZE_INVALID` with `Connection: close` (the access
  log's response wrapper hides `http.MaxBytesReader`'s own connection-close
  hook, hence the explicit header). Chunked sessions are discarded: after an
  aborted write the file's state past the offset is unknown.
- **Metrics:** `foundry_storage_used_bytes`, `foundry_storage_quota_bytes`.

---

## 7. Engineering standards

| Area | Rule |
|---|---|
| Formatting | `make fmt` (gofmt + goimports, local prefix `github.com/foundry/registry`). CI runs `make fmt-check` |
| Linting | `make lint` with `.golangci.yml` (standard + bodyclose, contextcheck, errorlint, gosec, misspell, nilerr, noctx, unconvert, usestdlibvars). Zero issues on main. `//nolint` only with a linter name **and** a reason |
| Errors | Wrap with `%w`, compare with `errors.Is/As`, no `==` on errors. Never ignore errors silently; log best-effort cleanup failures |
| Validation | Validate at the HTTP boundary; storage re-validates anything that becomes a path |
| Context | `ctx` is the first parameter of anything that queries the DB or streams data; pass the caller's context, never `context.Background()` below `main`. Use `context.WithoutCancel` only for cleanup that must finish |
| SQL | Parameterised queries only. Multi-statement writes in a transaction. Prefer schema constraints (FK/CASCADE) over application-side cleanup |
| HTTP | Handlers map service errors to status codes in one place per handler; set headers before `WriteHeader` |
| Tests | Every bug fix ships a regression test that fails without the fix. New endpoints need integration coverage, including repository names with `/` |
| Gate | `make check` (fmt-check, vet, lint, race tests) must pass before merge |

---

## 8. Test strategy

| Layer | Location | Focus |
|---|---|---|
| Storage | `internal/registry/storage/*_test.go` | Atomicity, dedup, digest verification, path safety |
| Services | `internal/registry/services/*_test.go` | SQL behaviour, manifest parsing, GC (grace, dry-run, DB/disk sync) |
| DB | `internal/db/db_test.go` | Migration upgrade from previous schema |
| Logging, metrics | `internal/logging`, `internal/metrics` | Request ID attribution, formats/levels; route classification, label bounds, GC decorator |
| Handlers | `internal/registry/handlers/*_test.go` | Status codes and headers via `httptest.NewRecorder`, against real stores, or against hand-written fakes (`fakes_test.go`) to force backend failures. Log assertions use `captureLogs` (not parallel: it swaps the default logger) |
| Integration | `tests/integration/*_test.go` | Full server over real HTTP. `regression_test.go` pins every fix from §9 |
| Manual | `make test-push` | Real Docker client (see README note on Docker Desktop) |

**Gap:** no automated test drives a real Docker/containerd client. See R7.

---

## 9. Review findings and resolutions

Severity: **C** = data loss, security or core feature broken; **H** = spec
violation or wrong results; **M** = robustness or maintainability.

| # | Sev | Finding | Resolution |
|---|---|---|---|
| 1 | C | Repository names containing `/` (`library/nginx`) returned 404 on **every** route: chi `{name:.+}` cannot span segments | Slash-aware wildcard dispatcher (`routing.go`), §4 |
| 2 | C | `manifests.digest` was a global PK: pushing the same image to a second repo moved it out of the first (404 by digest, failed deletes, dangling tags) | Migration 002: composite PK/FK plus cascades |
| 3 | C | GC marked every blob that had a DB row, and every pushed blob has one, so GC never collected anything; it also never removed DB rows | Mark only manifest references; delete file and row; grace period; dry-run |
| 4 | C | Web UI: all pages shared one template set, so every page rendered the tag page's content | Per-page template sets |
| 5 | C | Stored XSS: manifest `mediaType` was echoed as `Content-Type` (e.g. `text/html`) on the UI's origin | Media type allow-list plus `nosniff` |
| 6 | H | Digests were not validated: short digests panicked (500); crafted digests reached filesystem paths | `oci.ValidDigest` at the boundary and inside `blobPath` |
| 7 | H | Manifest PUT accepted non-JSON, unknown blob references, and a digest reference that didn't match the content | `ParseManifest` + `PutManifest` checks (D5–D8) |
| 8 | H | Manifest and tag writes were separate statements (partial writes possible) | Single transaction |
| 9 | H | Monolithic push with a wrong digest still stored the blob | `PutVerified` (verify, then commit) |
| 10 | H | Repository names were never validated | `NAME_INVALID` |
| 11 | H | `DELETE` of an unknown upload returned 204; uploads weren't scoped to their repo | 404; `GetRepoUpload` |
| 12 | H | DB errors reported as 404; internal error text leaked to clients | Sentinel errors, generic 5xx bodies |
| 13 | H | HTMX: delete buttons swapped raw JSON into the page; the tag-delete button had conflicting `hx-delete`/`hx-get`; row ids with `/` broke selectors | `hx-swap="delete"`, `closest tr`, GC HTML fragment |
| 14 | H | `make clean` deleted `./data` (all registry content) | `clean` keeps data; `clean-data` asks first |
| 15 | H | GC racing a manifest push could delete newly referenced blobs | `gcLock` RWMutex |
| 16 | M | No `http.Server` timeouts, no graceful shutdown, `RealIP` trusted spoofable headers | `HTTPServer()` with timeouts, signal handling, `RealIP` removed |
| 17 | M | Per-digest `sync.Map` mutexes leaked memory forever (and weren't needed) | Removed; rely on atomic rename |
| 18 | M | Dashboard did N+1 queries and ignored errors | `ListRepositorySummaries` single query |
| 19 | M | Unparsed `Content-Range`, unsynchronised concurrent PATCH | Strict parsing, per-session lock |
| 20 | M | Missing spec features: `_catalog`, tag pagination, blob mount, Range GETs, tag delete via manifests | Implemented |
| 21 | M | `regctl`: GC output tied to Go field names, no digest refs, trailing-slash URLs | snake_case JSON, `repo@digest`, `gc -dry-run`, URL normalisation |
| 22 | M | Runtime DB files committed to git | `data/` ignored and untracked |
| 23 | M | Dead code (`var _ = hex…`, unused fields), `err ==` comparisons | Removed; `errors.Is` |
| 24 | H | GET/HEAD/DELETE of a malformed tag (e.g. `.INVALID_MANIFEST_NAME`) returned 400 instead of 404; failed 3 OCI conformance specs (found by R7) | 404 `MANIFEST_UNKNOWN` for reads and deletes; PUT stays 400 |

---

## 10. Roadmap

Ordered by priority. Each item lists its acceptance criteria.

**R1 — Authentication (high). Done** (November 2026; see §6.3 for the
implemented design, which uses Basic auth without a token server).
Original item: optional static bearer token or htpasswd
basic auth for `/v2` writes and all of `/api` and `/ui` mutations, following the
Docker token-auth challenge (`WWW-Authenticate`). Also add CSRF protection for
UI POSTs (SameSite cookie + token, or require a custom header that htmx sends).
*Accept:* `docker login` works; unauthenticated push gets 401 `UNAUTHORIZED`;
pull stays anonymous unless configured otherwise.

**R2 — Context propagation (high). Done** (November 2026; see §6.6).
Original item: thread `context.Context` from the request
through services (`QueryContext`, `ExecContext`, `BeginTx`) so cancelled
requests stop DB work and shutdown can drain cleanly. Then re-enable the
`noctx` linter. *Accept:* `noctx` passes; a cancelled request aborts an
in-flight GC.

**R3 — Structured logging and observability (medium). Done** (November
2026; see §6.7). Original item: move to `log/slog`
with request IDs (the `RequestID` middleware is already present); add
`/healthz` and optional Prometheus metrics (push/pull counts, bytes, GC stats).

**R4 — Vendor htmx (medium). Done** (November 2026; see §6.4).
Original item: embed `htmx.min.js` under `web/static/js/` and
drop the unpkg dependency, so the UI works offline (matching the
single-binary goal). *Accept:* the UI works with no network access.

**R5 — Interfaces for stores (medium). Done** (November 2026; see §2).
Original item: have handlers and services depend on
`storage.BlobStore` and a metadata interface rather than concrete types, to
allow fakes in unit tests and alternative backends.

**R6 — Resource limits (medium). Done** (November 2026; see §6.8).
Original item: configurable maximum blob size and total
`data/` quota; reject with 413 `SIZE_INVALID`. Streaming uploads should abort
early.

**R7 — Real-client conformance (medium).** CI job that runs the
[OCI distribution conformance suite](https://github.com/opencontainers/distribution-spec/tree/main/conformance)
and a `crane`/`docker` push-pull round trip against a built binary.

**R8 — OCI 1.1 referrers API (low).** `GET /v2/<name>/referrers/<digest>` plus
`subject` handling (`OCI-Subject` header) for signatures and SBOMs. Prerequisite for S4.

**R9 — GC policy options (low).** `-gc-delete-untagged` (remove untagged
manifests not referenced by an index), and optional scheduled GC
(`-gc-interval`). Must not delete referrers (signatures, SBOMs)
whose `subject` is still alive (see S4).

**R10 — Per-repository blob links (low).** Today any repository can read any
blob by digest (blobs are global). Add a `repository_blobs` link table
populated on upload/mount. Required before R1 becomes meaningful for
per-repo access control.

**R11 — Manifest content negotiation (low).** Honour `Accept` for clients that
cannot handle OCI indexes (return 404 `MANIFEST_UNKNOWN` instead of an index
they can't parse).

**R12 — Online backup (low).** `regctl backup` using SQLite `VACUUM INTO` plus
a blob rsync, so backups don't require stopping the server.

---

## 11. Stretch goals (post-MVP)

These are product features, not hardening. Work on them starts only when the
MVP is stable, defined as:

- no open **C** or **H** defects;
- R1 (auth), R2 (context propagation) and R7 (real-client conformance) are
  done, with the OCI conformance suite passing in CI;
- `make check` is the CI gate.

S1 and S2 are independent and can start in either order. S3 depends on S2's
size data only for display. S4 depends on R8.

### S1 — Search

**Goal:** find repositories, tags and digests from the web UI, the admin API
and `regctl`.

| Query | Matches | Example |
|---|---|---|
| Repository | substring/prefix of `repositories.name`, including path components | `back` → `team/backend` |
| Tag | tag name, across all repositories or within one | `v1.2` → `backend:v1.2.0`, `api:v1.2.3` |
| Digest | prefix of a manifest or blob digest, with or without `sha256:`; at least 8 hex chars | `089c73da` → manifest in `nginx` (tag `latest`) |

**Design**
- A digest is detected by shape (hex, ≥ 8 chars, optional `sha256:`). It is
  answered by an indexed prefix range query on `manifests.digest` and
  `blobs.digest` (`digest >= ? AND digest < ?`). A blob hit is resolved to the
  manifests/tags that reference it. This needs a `manifest_blobs` reference
  table (populated in `PutManifest`; also speeds up GC marking).
- Names and tags use an SQLite **FTS5** table (confirmed available in
  `modernc.org/sqlite`), kept in sync by triggers on `repositories` and `tags`.
  Use the `trigram` tokenizer so substrings like `kend` match `backend`.
- Results are capped (default 50) and grouped by type.

**Interfaces**
- `GET /api/search?q=<query>[&type=repo|tag|digest][&repo=<name>]` →
  `{"repositories":[…], "tags":[{"repository","tag","digest"}], "digests":[{"digest","kind":"manifest|blob","repositories":[…]}]}`
- UI: search box in the nav bar with htmx live results (`hx-trigger="keyup changed delay:300ms"`).
- CLI: `regctl search <query> [-type tag]`.

**Acceptance:** each example in the table above returns the expected hit; a
query that matches nothing returns empty arrays, not 404; p95 < 50 ms with
10k repositories and 100k tags.

### S2 — Image size analysis

**Goal:** show how big an image is and where the bytes are, e.g.:

```
backend:latest           2.1 GB
  largest layer:       800 MB   sha256:3f1a…  (40% of image)
  layers: 14   config: 9 KB   shared with other images: 1.2 GB
```

**Definitions** (state them in the UI; users conflate these)

| Metric | Source |
|---|---|
| **Image size** | sum of config + layer descriptor `size` values = compressed bytes a client downloads |
| **Largest layer** | max layer descriptor `size` |
| **Shared bytes** | layers also referenced by another manifest in the registry |
| **Unique bytes** | image size − shared bytes = what deleting this image could free in GC |

Sizes are **compressed** (transfer) sizes. Uncompressed size would require
decompressing every layer. It is out of scope for v1 but could later be
computed lazily and cached.

**Design**
- Computed from the stored manifest JSON plus the `manifest_blobs` table from
  S1. Descriptor sizes are cross-checked against `blobs.size`; a mismatch is
  flagged, not silently trusted.
- For an index / manifest list: one row per platform, plus the deduplicated
  total across platforms.
- Repository-level view: total and unique bytes per repository, sortable on
  the dashboard ("what is using my disk?").

**Interfaces**
- `GET /api/repos/{name}/manifests/{ref}/size` → `{"total","config","largest_layer":{"digest","size"},"layers":[{"digest","size","media_type","shared"}],"unique","shared","platforms":[…]}`
- UI: size column on the repo/tag tables; per-layer bar chart on the tag page.
- CLI: `regctl size <repo>:<tag>` (output as above, human-readable units).

**Acceptance:** for a pushed `nginx` image, the total equals the sum of
`docker manifest inspect` layer sizes plus config; for an image that shares a
base with another, unique + shared = total, and unique matches GC's freed
bytes after deleting that image alone.

### S3 — Vulnerability scanning (Trivy)

**Goal:** scan pushed images for known CVEs and show results per tag.

**Integration choice:** run the **external `trivy` binary** (optional,
`-trivy-path`; the feature is disabled if absent). Do not link Trivy as a Go
library: it would multiply binary size and dependencies, which conflicts with
the single-lean-binary goal. Trivy scans the image *through this registry*
(`trivy image --insecure localhost:<port>/<repo>@<digest> --format json`),
so no image export is needed. Trivy server mode (`trivy server`) is a later
option for faster repeated scans.

**Design**
- Scans are **asynchronous**. `PutManifest` enqueues a job in a `scan_jobs`
  table and a bounded worker pool (default 1) processes it; pushes never wait
  for a scan.
- Results are keyed by **manifest digest** (content-addressed), so the same
  image in several repositories is scanned once. Table `scan_results(digest,
  scanned_at, scanner_version, db_version, critical, high, medium, low, report
  BLOB)`; the full JSON report is stored compressed.
- Rescan when the Trivy vulnerability DB changes (scheduled, e.g. daily), and
  on demand.
- Index / multi-platform images: scan each platform manifest; the tag shows
  the worst result.
- **Operational constraints:** Trivy downloads its vulnerability DB from the
  internet. Support `-trivy-cache-dir`, and offline mode (`--skip-db-update`
  with a pre-seeded DB). Once R1 lands, the scanner needs a service
  credential to pull from the registry.
- Scan results are advisory. Blocking pulls of vulnerable images (policy
  enforcement) is a separate, later decision.

**Interfaces**
- `POST /api/scan/{name}/{ref}` (enqueue) · `GET /api/scan/{name}/{ref}` →
  summary + `status` (`pending|running|done|failed|unsupported`) · `GET …?full=true` → full report.
- UI: severity badges (C/H/M/L counts) on tag rows; findings table on the tag
  page (CVE, package, installed → fixed version, severity).
- CLI: `regctl scan <repo>:<tag> [-wait] [-severity HIGH,CRITICAL]` (exit
  non-zero if findings at or above the threshold, for CI use).

**Acceptance:** pushing a known-vulnerable image (e.g. an old `alpine`
release) produces a `done` result with non-zero counts without delaying the
push; the registry runs normally with Trivy absent (`unsupported` status);
a scan failure is recorded and retried with backoff, never lost silently.

### S4 — OCI artifacts: Helm charts, SBOMs, signatures

**Goal:** treat non-image OCI artifacts as first-class: store, browse,
discover and garbage-collect them correctly.

**Current state (verified):** the registry already *accepts* these. A Helm
chart (OCI manifest with `application/vnd.cncf.helm.config.v1+json` config)
and a cosign-style signature (empty config `application/vnd.oci.empty.v1+json`,
`artifactType`, `subject`, tag `sha256-<hex>.sig`) both push successfully.
What is missing:

| Gap | Effect today |
|---|---|
| Referrers API (`GET /v2/<name>/referrers/<digest>`) returns 404 | Clients fall back to the tag scheme. `oras discover` / `cosign tree` can't list attached artifacts |
| No `OCI-Subject` header on manifest PUT | Clients don't know the registry understands `subject` |
| UI treats everything as an image | Signature tags (`sha256-….sig`) clutter tag lists; charts show meaningless "layers" |
| GC (and R9 delete-untagged) ignore `subject` | Deleting an image leaves its signatures/SBOMs orphaned. Untagged referrers would be deleted by R9 |

**Design**
- **Referrers (R8, prerequisite):** store `subject_digest` and
  `artifact_type` columns on `manifests` (filled by `PutManifest`). Serve
  `GET /v2/<name>/referrers/<digest>[?artifactType=]` as an OCI index of
  descriptors (with `artifactType` and `annotations`). Return `OCI-Subject:
  <digest>` on PUT when `subject` is present.
- **Artifact kind detection**, stored per manifest: `artifactType`, else
  config media type → `image | helm-chart | sbom | signature | attestation | other`.
  Known types: Helm (`application/vnd.cncf.helm.config.v1+json`), SBOMs
  (`application/spdx+json`, `application/vnd.cyclonedx+json`), signatures
  (cosign `application/vnd.dev.cosign.artifact.sig.v1+json`, Notary v2
  `application/vnd.cncf.notary.signature`), in-toto attestations.
- **Lifecycle:** referrers live and die with their subject. Deleting a
  manifest deletes its referrers (recursively), and GC/R9 never treat a
  referrer with a live subject as untagged garbage.
- **Media-type safety (D8):** the manifest media-type allow-list stays as is
  (artifacts use the OCI manifest type). Never serve artifact *blob* content
  with a client-chosen `Content-Type` (it stays `application/octet-stream` +
  `nosniff`).

**Interfaces**
- UI: kind badge per tag; Helm chart page (name, version, appVersion from the
  config blob); an "Attached" panel on the tag page listing signatures, SBOMs
  and attestations via referrers; signature tags hidden from tag lists by
  default (toggle to show).
- Admin API: `kind` and `artifact_type` in tag and manifest responses;
  `GET /api/repos/{name}/manifests/{ref}/referrers`.
- SBOMs can feed S3 later (Trivy can scan an SBOM instead of the image).

**Acceptance** (each with the real client in CI, see R7):
- `helm push chart.tgz oci://localhost:<port>/charts` and `helm pull` round-trip;
- `cosign sign` then `cosign verify` (key pair), and `cosign tree` lists the signature;
- `oras attach --artifact-type application/spdx+json` then `oras discover` lists the SBOM;
- deleting the signed image removes its signature and SBOM manifests, and the
  following GC frees their blobs.
