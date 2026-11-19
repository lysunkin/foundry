# Foundry Registry

A local Docker/OCI container registry in a single Go binary.

## Features

- OCI Distribution Spec v2: push, pull, delete, tag listing (with pagination), catalog
- Chunked, monolithic and cross-repository-mount blob uploads
- Repository names with slashes (`library/nginx`, `org/team/app`)
- Input validation: repository names, tags, digests, manifest media types and referenced blobs
- Blob downloads support HTTP `Range` requests
- SQLite metadata (WAL mode, foreign keys, migrations auto-applied on startup)
- Content-addressable filesystem blob store (Git-style shard layout)
- Mark-and-sweep garbage collector with grace period, dry-run and stale-upload cleanup
- Server-rendered web UI with HTMX (no React, no Node.js)
- Admin JSON API
- `regctl` companion CLI
- Optional HTTP Basic authentication (htpasswd, bcrypt) with anonymous pull
- CSRF protection for the web UI and admin API
- Graceful shutdown on SIGINT/SIGTERM
- Structured logs (text or JSON) with request IDs, `/healthz` and `/readyz`
  probes, optional Prometheus metrics

> **Security:** authentication is **off by default**, and there is no TLS.
> Without `-auth-htpasswd`, anyone who can reach the port can push, pull and
> **delete**. Enable [authentication](#authentication), and bind to loopback
> (`-addr 127.0.0.1:8080`) or put a TLS-terminating proxy in front when the
> network is not trusted.

## Quick Start

```bash
make run                     # build, then serve ./data on :8080
# or
make build
./foundry-registry -data ./data -addr :8080
```

Open http://localhost:8080 in your browser. `make run` accepts the same
settings as Make variables, e.g. `make run ADDR=127.0.0.1:5000 DATA_DIR=/tmp/reg`.

### Server flags

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `-addr` | `FOUNDRY_ADDR` | `:8080` | Listen address |
| `-data` | `FOUNDRY_DATA` | `./data` | Data directory |
| `-gc-grace` | | `1h` | GC never deletes unreferenced blobs younger than this (protects in-flight pushes) |
| `-upload-ttl` | | `24h` | GC expires upload sessions older than this |
| `-auth-htpasswd` | `FOUNDRY_AUTH_HTPASSWD` | (empty: auth off) | bcrypt htpasswd file; enables authentication |
| `-auth-anonymous-pull` | `FOUNDRY_AUTH_ANONYMOUS_PULL` | `true` | With auth on, allow unauthenticated pulls and browsing |
| `-log-format` | `FOUNDRY_LOG_FORMAT` | `text` | `text` or `json` |
| `-log-level` | `FOUNDRY_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `-metrics` | `FOUNDRY_METRICS` | `false` | Serve Prometheus metrics at `/metrics` |
| `-max-blob-size` | `FOUNDRY_MAX_BLOB_SIZE` | `0` (no limit) | Reject larger blobs, e.g. `5GiB` |
| `-storage-quota` | `FOUNDRY_STORAGE_QUOTA` | `0` (no limit) | Maximum bytes of blobs and in-flight uploads, e.g. `100GB` |

Invalid values for any of these fail startup.

### Authentication

Create users with bcrypt hashes (other htpasswd formats are rejected at
startup), then point the server at the file:

```bash
htpasswd -cB htpasswd alice          # -c creates the file; omit it to add users
./foundry-registry -auth-htpasswd ./htpasswd
docker login localhost:8080
```

With authentication on:

| Request | Anonymous (default) | `-auth-anonymous-pull=false` |
|---|---|---|
| Pull, `_catalog`, tag list (`GET`/`HEAD /v2/...`) | allowed | 401 |
| Push, delete (`/v2` writes) | 401 | 401 |
| Admin API and web UI reads | allowed | 401 |
| Admin API and web UI writes (delete, GC) | 401 | 401 |
| `GET /v2/` (client probe) | 401 | 401 |
| `/static/`, `/healthz`, `/readyz` | allowed | allowed |
| `/metrics` (with `-metrics`) | allowed | 401 |

- 401 responses carry the standard `WWW-Authenticate: Basic realm="foundry"`
  challenge (verified with Docker 29 using the containerd image store). In
  the UI, the browser shows its login prompt when you delete or run GC.
- Every authenticated user can do everything (no roles, no per-repository
  permissions yet).
- Wrong credentials are rejected even where anonymous access is allowed, so
  `docker login` with a bad password fails. Rejections are logged with the
  username and client address.
- Credentials travel in cleartext over plain HTTP. The server logs a warning
  when auth is enabled on a non-loopback address.
- The htpasswd file is read at startup; restart to apply changes.
- Docker clients that use the classic (non-containerd) image store were not
  verified. If anonymous pull fails for them while auth is on, `docker login`
  first.

**CSRF:** independent of authentication, state-changing browser requests from
another site (detected via `Sec-Fetch-Site`/`Origin`) are rejected with 403,
and `POST /ui/*` requires the `HX-Request` header that htmx sends. Clients
that are not browsers (docker, regctl, curl) are unaffected.

## Docker Setup

Docker treats `localhost` registries as insecure (plain HTTP) by default. For
any other host name, add it to your Docker daemon config
(`/etc/docker/daemon.json` or Docker Desktop → Settings → Docker Engine):

```json
{
  "insecure-registries": ["myhost:8080"]
}
```

Then push:

```bash
docker tag nginx localhost:8080/nginx:latest
docker push localhost:8080/nginx:latest
docker pull localhost:8080/nginx:latest
```

`make test-push` does the same round trip as `localhost:8080/nginx:test`
against a running registry. It needs a local `nginx` image (`docker pull nginx`).

### Docker in a VM (colima, Docker Desktop)

`docker push` is performed by the Docker daemon, not the CLI. If the daemon
runs inside a VM, `localhost` means the VM, so a registry running on your Mac
fails with:

```
dial tcp 127.0.0.1:8080: connect: connection refused
```

**colima:** run `make colima-forward` once (and again after every
`colima restart`). It adds an iptables NAT rule in the VM, sending
`localhost:8080` to the host, so `localhost:8080/...` image names work
unchanged. `make colima-forward-down` removes it; `make colima-forward-status`
shows its state. See [scripts/colima-forward.sh](scripts/colima-forward.sh).
(A plain port forwarder such as socat in the VM does **not** work: Lima
auto-forwards it back to the Mac's `127.0.0.1:8080` and creates a loop.)

**Docker Desktop:** use `host.docker.internal:8080` as the registry host and
add it to `insecure-registries`.

## CLI

```bash
regctl repos                         # List all repositories
regctl tags nginx                    # List tags
regctl inspect nginx:latest          # Show a manifest (also: nginx@sha256:...)
regctl delete nginx:latest           # Delete a tag
regctl delete nginx@sha256:...       # Delete a manifest and every tag pointing at it
regctl gc                            # Run garbage collection
regctl gc -dry-run                   # Report what GC would delete
```

Set `FOUNDRY_URL` or use `-registry <url>` to point at a non-default address.
Global flags must come **before** the command: `regctl -registry http://host:8080 repos`.

If the registry requires authentication, set `FOUNDRY_USER` and
`FOUNDRY_PASSWORD`, or pass `-user` and pipe the password with
`-password-stdin`:

```bash
FOUNDRY_USER=alice FOUNDRY_PASSWORD=... regctl gc
echo "$PASSWORD" | regctl -user alice -password-stdin gc
```

Requests time out after 30s (`-timeout <duration>`, `0` for none), except
`gc`, which waits for the collection unless `-timeout` is given. Ctrl-C
cancels the request; for `gc` the server then stops collecting.

## API

### Docker Registry v2

`{name}` may contain slashes. Every `/v2` response carries
`Docker-Distribution-Api-Version: registry/2.0`. Errors use the OCI format
`{"errors":[{"code":"…","message":"…"}]}`.

| Method | Path | Description |
|--------|------|-------------|
| GET    | /v2/ | Ping / version check |
| GET    | /v2/_catalog?n=&last= | List repositories (paginated, `Link` header) |
| HEAD/GET | /v2/{name}/blobs/{digest} | Blob existence / download (`Range` supported) |
| DELETE | /v2/{name}/blobs/{digest} | Delete blob |
| POST   | /v2/{name}/blobs/uploads/ | Start upload; `?digest=` for monolithic, `?mount=&from=` to mount |
| GET    | /v2/{name}/blobs/uploads/{uuid} | Upload status |
| PATCH  | /v2/{name}/blobs/uploads/{uuid} | Append chunk (`Content-Range` checked) |
| PUT    | /v2/{name}/blobs/uploads/{uuid}?digest= | Complete upload (digest verified) |
| DELETE | /v2/{name}/blobs/uploads/{uuid} | Cancel upload |
| HEAD/GET | /v2/{name}/manifests/{ref} | Manifest by tag or digest |
| PUT    | /v2/{name}/manifests/{ref} | Push manifest (referenced blobs must exist) |
| DELETE | /v2/{name}/manifests/{ref} | Delete manifest by digest, or tag by name |
| GET    | /v2/{name}/tags/list?n=&last= | List tags (paginated, `Link` header) |

Manifests up to 4 MiB with these media types are accepted: OCI image
manifest/index, Docker manifest v2/list, Docker schema 1.

### Admin API

| Method | Path | Description |
|--------|------|-------------|
| GET    | /api/repos | List repositories |
| GET    | /api/repos/{name}/tags | List tags |
| GET    | /api/repos/{name}/manifests/{ref} | Get manifest (metadata + JSON content) |
| DELETE | /api/repos/{name} | Delete repository (manifests, tags, uploads) |
| DELETE | /api/tags/{name}/{tag} | Delete tag |
| DELETE | /api/manifests/{name}/{digest} | Delete manifest and its tags |
| POST   | /api/gc[?dry_run=true] | Run garbage collection |

`POST /api/gc` returns:

```json
{"scanned_blobs": 12, "deleted_blobs": 3, "freed_bytes": 7340032,
 "deleted_uploads": 0, "dry_run": false, "errors": []}
```

### Garbage collection

GC deletes a blob (file + DB row) when **no manifest in any repository
references it** and it is older than `-gc-grace`. Notes:

- Deleting a tag does **not** make its manifest's blobs collectable: untagged
  manifests are kept. Delete the manifest (`regctl delete repo@sha256:…`) or
  the repository to release them.
- Upload sessions older than `-upload-ttl` are removed along with their temp files.
- Only one GC runs at a time.
- GC stops when its request is cancelled (client disconnect, client timeout,
  or server shutdown). Deletions already made stay, and the next run picks up
  where it stopped. The cancelled request gets 503.
- On SIGINT/SIGTERM the server waits up to 30s for in-flight requests (such
  as a long GC), then cancels them. A second signal exits immediately.

### Limits

Both limits are off by default. Sizes are bytes or a number with a unit:
`KB`, `MB`, `GB`, `TB` (powers of 1000) or `KiB`, `MiB`, `GiB`, `TiB` (powers
of 1024), e.g. `-max-blob-size 5GiB -storage-quota 200GB`.

- **`-max-blob-size`** limits a single blob. For chunked uploads, everything
  uploaded so far counts. Cross-repository mounts copy nothing and are always
  allowed. Manifests have their own fixed 4 MiB limit.
- **`-storage-quota`** limits the bytes in `data/blobs/` and `data/uploads/`
  (the SQLite database is not counted). Abandoned uploads count until GC
  expires them (`-upload-ttl`). The server measures usage at startup (logged)
  and logs a warning when usage passes 90% of the quota.
- An upload over either limit is rejected with `413` `SIZE_INVALID` as soon
  as the limit is crossed. Nothing is kept: a chunked upload session is
  discarded and must be restarted.
- If usage is already over the quota at startup (for example after lowering
  it), the server starts but rejects pushes until you delete images and run
  GC.

### Observability

**Logs** go to stderr, one record per line, in `text` (default) or `json`
format (`-log-format`). Every request gets one `request` record with
`method`, `path`, `status`, `bytes`, `duration_ms`, `remote_addr`,
`request_id` and, when authenticated, `user`. Query strings, bodies and
credentials are never logged. Probe and scrape requests (`/healthz`,
`/readyz`, `/metrics`) log at `debug`.

**Request IDs:** every response carries `X-Request-Id`. A client-supplied
`X-Request-Id` of 1 to 64 characters from `[A-Za-z0-9._-]` is kept;
anything else is replaced with a UUID. All log records for a request carry
its `request_id`.

**Health checks** (never require authentication):

| Endpoint | Meaning | Response |
|---|---|---|
| `GET /healthz` | The process serves HTTP (liveness). Checks no dependencies | `200 ok` |
| `GET /readyz` | The database answers within 2s (readiness) | `200 ok` or `503 not ready` |

**Metrics** (`-metrics`): `GET /metrics` in the Prometheus format. It is
anonymous when anonymous pull is on, otherwise it needs Basic auth.

| Metric | Labels |
|---|---|
| `foundry_http_requests_total` | `route`, `method`, `code` |
| `foundry_http_request_duration_seconds` (histogram) | `route`, `method` |
| `foundry_blob_bytes_received_total` / `foundry_blob_bytes_sent_total` | |
| `foundry_gc_runs_total` | `result` (`success`, `error`, `cancelled`) |
| `foundry_gc_deleted_blobs_total`, `foundry_gc_freed_bytes_total` (dry runs excluded) | |
| `foundry_gc_duration_seconds` (histogram), `foundry_gc_last_success_timestamp_seconds` | |
| `foundry_storage_used_bytes`, `foundry_storage_quota_bytes` (`0`: no quota) | |

`route` is one of `v2_blob`, `v2_manifest`, `v2_upload`, `v2_other`, `api`,
`ui`, `static`, `ops`. Repository names are never used as labels. Go runtime
and process metrics (`go_*`, `process_*`) are included. Examples:

```promql
# manifest pushes and pulls per second
sum(rate(foundry_http_requests_total{route="v2_manifest",method="PUT",code="201"}[5m]))
sum(rate(foundry_http_requests_total{route="v2_manifest",method="GET",code="200"}[5m]))
# server error ratio
sum(rate(foundry_http_requests_total{code=~"5.."}[5m])) / sum(rate(foundry_http_requests_total[5m]))
# time since the last successful GC
time() - foundry_gc_last_success_timestamp_seconds
```

## Architecture

```
┌───────────────────────────────────┐
│           Go HTTP Server          │
│   Registry API + Web UI + CLI API │
└────────────┬──────────────────────┘
             │
      ┌──────┴──────┐
      ▼             ▼
┌──────────┐  ┌────────────────┐
│  SQLite  │  │ Blob Filesystem│
│ Metadata │  │  data/blobs/   │
└──────────┘  └────────────────┘
```

Everything runs in one process. `data/` is the only state:

```
data/
├── registry.db            SQLite metadata (+ -wal/-shm files)
├── blobs/sha256/ab/ab…    blob content, sharded by first 2 hex chars
└── uploads/<uuid>.upload  in-progress chunked uploads
```

Back up by stopping the server and running `cp -r data/ backup/`. Copying while
the server runs can capture an inconsistent SQLite WAL.

## Development

Requires Go 1.26+ and [golangci-lint](https://golangci-lint.run/) v2
(`brew install golangci-lint`). Run `make help` to list all targets.

| Target | Description |
|--------|-------------|
| **Build & run** | |
| `make build` | Build `foundry-registry` and `regctl` (default target) |
| `make run` | Build, then run the registry with `DATA_DIR` and `ADDR` |
| `make tidy` | `go mod tidy` |
| **Code quality** | |
| `make fmt` | Format code (gofmt + goimports, via golangci-lint) |
| `make fmt-check` | Fail if any file is not formatted |
| `make vet` | `go vet ./...` |
| `make lint` | golangci-lint with [.golangci.yml](.golangci.yml) |
| `make lint-fix` | golangci-lint, applying automatic fixes |
| `make check` | `fmt-check` + `vet` + `lint` + `test-race`. Run before pushing |
| **Tests** | |
| `make test` | All tests |
| `make test-race` | All tests with the race detector |
| `make test-integration` | End-to-end HTTP tests only (verbose) |
| `make test-push` | Docker push/pull of `nginx` through a running registry |
| **colima** (see [Docker in a VM](#docker-in-a-vm-colima-docker-desktop)) | |
| `make colima-forward` | Let the colima VM reach this registry as `localhost:<port>` |
| `make colima-forward-down` | Remove the forward |
| `make colima-forward-status` | Show whether the forward is active |
| **Cleanup** | |
| `make clean` | Remove built binaries (keeps registry data) |
| `make clean-data` | **Delete `DATA_DIR`**, after a `y/N` confirmation |

Variables can be overridden on the command line:

| Variable | Default | Used by |
|----------|---------|---------|
| `ADDR` | `:8080` | `run`, `test-push`; its port is used by `colima-forward*` |
| `DATA_DIR` | `./data` | `run`, `clean-data` |
| `GOLANGCI_LINT` | `golangci-lint` | `fmt`, `fmt-check`, `lint`, `lint-fix` |

Example, serving on port 5000 for colima's Docker:

```bash
make run ADDR=:5000                # terminal 1 (stays in the foreground)
make colima-forward ADDR=:5000     # terminal 2
docker push localhost:5000/nginx:latest
```

CI ([.github/workflows/go.yml](.github/workflows/go.yml)) currently runs
`make build` and `make test` on pushes and pull requests to `main`.

See [docs/DEVELOPMENT_SPEC.md](docs/DEVELOPMENT_SPEC.md) for the design,
invariants and roadmap.
