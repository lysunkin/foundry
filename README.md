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
- Graceful shutdown on SIGINT/SIGTERM

> **Security:** there is no authentication or TLS. Anyone who can reach the
> port can push, pull and **delete**. Run it on a trusted machine or network,
> or bind to loopback (`-addr 127.0.0.1:8080`).

## Quick Start

```bash
make build
./foundry-registry -data ./data -addr :8080
```

Open http://localhost:8080 in your browser.

### Server flags

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `-addr` | `FOUNDRY_ADDR` | `:8080` | Listen address |
| `-data` | `FOUNDRY_DATA` | `./data` | Data directory |
| `-gc-grace` | | `1h` | GC never deletes unreferenced blobs younger than this (protects in-flight pushes) |
| `-upload-ttl` | | `24h` | GC expires upload sessions older than this |

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

Requires Go 1.26+ and [golangci-lint](https://golangci-lint.run/) v2.

```bash
make help              # list targets
make build             # build foundry-registry and regctl
make test              # all tests
make test-race         # all tests with the race detector
make test-integration  # end-to-end HTTP tests only
make fmt / fmt-check   # format / verify formatting
make lint              # golangci-lint (.golangci.yml)
make check             # fmt-check + vet + lint + test-race (CI gate)
make clean             # remove binaries (keeps data)
make clean-data        # delete ./data (asks for confirmation)
```

See [docs/DEVELOPMENT_SPEC.md](docs/DEVELOPMENT_SPEC.md) for the design,
invariants and roadmap.
