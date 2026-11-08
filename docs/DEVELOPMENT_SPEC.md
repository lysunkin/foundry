# Foundry Registry — Development Specification

| | |
|---|---|
| **Status** | Current as of the November 2026 review/hardening pass |
| **Audience** | Engineers maintaining or extending Foundry |
| **Scope** | Registry server (`cmd/registry`), CLI (`cmd/regctl`), all `internal/` packages |

This document defines what Foundry must do, the invariants the code relies on,
and the engineering rules for changing it. §9 records the defects found in the
review and how each was resolved. §10 is the prioritized roadmap.

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

- Multi-tenant or internet-facing deployment. There is no authn/authz (see §10, R1).
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
| Tag | `[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}` | 400 `MANIFEST_INVALID` |
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

- No authentication. The admin API and `/ui/gc` can delete data. The default
  bind is `:8080` (all interfaces) to keep Docker Desktop working; the README
  tells users to bind to loopback when appropriate.
- Path traversal is prevented by digest/ID validation inside storage (F2).
- Stored XSS: manifest bytes are served with their stored media type, so only
  manifest media types are accepted (D8) and `nosniff` is set.
- CSRF: `POST /api/gc` and `POST /ui/gc` are "simple" cross-origin requests,
  so a malicious page could trigger GC. DELETE endpoints need a CORS preflight
  and are not exposed. Impact is low (GC only removes unreferenced data). See R1.
- Resource limits: manifest body 4 MiB; `ReadHeaderTimeout` 10s,
  `MaxHeaderBytes` 1 MiB. Blob size is unbounded (see R6).

### 6.4 Web UI

- `ui.Load` builds **one template set per page** (page + `base.html` +
  shared partials). A single shared set made every page render the
  last-parsed page's `content` block, so the dashboard showed the tag page.
- Rendering goes into a buffer and is written only on success.
- Deletes use `hx-swap="delete"` on the row (or a redirect for manifests),
  never swap API JSON into the page.
- htmx is loaded from unpkg (see R4).

### 6.5 Configuration

| Flag / env | Default | Purpose |
|---|---|---|
| `-addr` / `FOUNDRY_ADDR` | `:8080` | Listen address |
| `-data` / `FOUNDRY_DATA` | `./data` | State directory |
| `-gc-grace` | `1h` | GC grace period |
| `-upload-ttl` | `24h` | Upload session expiry |
| `FOUNDRY_URL` / `regctl -registry` | `http://localhost:8080` | CLI target |

`registry.Config` zero values **disable** grace/TTL (handy in tests).
Production callers must set them explicitly, as `cmd/registry` does.

The server shuts down gracefully on SIGINT/SIGTERM (30s drain).

---

## 7. Engineering standards

| Area | Rule |
|---|---|
| Formatting | `make fmt` (gofmt + goimports, local prefix `github.com/foundry/registry`). CI runs `make fmt-check` |
| Linting | `make lint` with `.golangci.yml` (standard + bodyclose, errorlint, gosec, misspell, nilerr, unconvert, usestdlibvars). Zero issues on main. `//nolint` only with a linter name **and** a reason |
| Errors | Wrap with `%w`, compare with `errors.Is/As`, no `==` on errors. Never ignore errors silently; log best-effort cleanup failures |
| Validation | Validate at the HTTP boundary; storage re-validates anything that becomes a path |
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
| Handlers | `internal/registry/handlers/*_test.go` | Status codes and headers via `httptest.NewRecorder` |
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

---

## 10. Roadmap

Ordered by priority. Each item lists its acceptance criteria.

**R1 — Authentication (high).** Optional static bearer token or htpasswd
basic auth for `/v2` writes and all of `/api` and `/ui` mutations, following the
Docker token-auth challenge (`WWW-Authenticate`). Also add CSRF protection for
UI POSTs (SameSite cookie + token, or require a custom header that htmx sends).
*Accept:* `docker login` works; unauthenticated push gets 401 `UNAUTHORIZED`;
pull stays anonymous unless configured otherwise.

**R2 — Context propagation (high).** Thread `context.Context` from the request
through services (`QueryContext`, `ExecContext`, `BeginTx`) so cancelled
requests stop DB work and shutdown can drain cleanly. Then re-enable the
`noctx` linter. *Accept:* `noctx` passes; a cancelled request aborts an
in-flight GC.

**R3 — Structured logging and observability (medium).** Move to `log/slog`
with request IDs (the `RequestID` middleware is already present); add
`/healthz` and optional Prometheus metrics (push/pull counts, bytes, GC stats).

**R4 — Vendor htmx (medium).** Embed `htmx.min.js` under `web/static/js/` and
drop the unpkg dependency, so the UI works offline (matching the
single-binary goal). *Accept:* the UI works with no network access.

**R5 — Interfaces for stores (medium).** Have handlers and services depend on
`storage.BlobStore` and a metadata interface rather than concrete types, to
allow fakes in unit tests and alternative backends.

**R6 — Resource limits (medium).** Configurable maximum blob size and total
`data/` quota; reject with 413 `SIZE_INVALID`. Streaming uploads should abort
early.

**R7 — Real-client conformance (medium).** CI job that runs the
[OCI distribution conformance suite](https://github.com/opencontainers/distribution-spec/tree/main/conformance)
and a `crane`/`docker` push-pull round trip against a built binary.

**R8 — OCI 1.1 referrers API (low).** `GET /v2/<name>/referrers/<digest>` plus
`subject` handling (`OCI-Subject` header) for signatures and SBOMs.

**R9 — GC policy options (low).** `-gc-delete-untagged` (remove untagged
manifests not referenced by an index), and optional scheduled GC
(`-gc-interval`).

**R10 — Per-repository blob links (low).** Today any repository can read any
blob by digest (blobs are global). Add a `repository_blobs` link table
populated on upload/mount. Required before R1 becomes meaningful for
per-repo access control.

**R11 — Manifest content negotiation (low).** Honour `Accept` for clients that
cannot handle OCI indexes (return 404 `MANIFEST_UNKNOWN` instead of an index
they can't parse).

**R12 — Online backup (low).** `regctl backup` using SQLite `VACUUM INTO` plus
a blob rsync, so backups don't require stopping the server.
