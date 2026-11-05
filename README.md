# Foundry Registry

A local Docker/OCI container registry in a single Go binary.

## Features

- Full OCI Distribution Spec v2 (push, pull, delete, tag listing)
- Chunked and monolithic blob uploads
- SQLite metadata (WAL mode, migrations auto-applied on startup)
- Content-addressable filesystem blob store (Git-style shard layout)
- Mark-and-sweep garbage collector
- Server-rendered web UI with HTMX (no React, no Node.js)
- Admin JSON API
- `regctl` companion CLI

## Quick Start

```bash
make build
./foundry-registry -data ./data -addr :8080
```

Open http://localhost:8080 in your browser.

## Docker Setup

Add to your Docker daemon config (`/etc/docker/daemon.json` or Docker Desktop):

```json
{
  "insecure-registries": ["localhost:8080"]
}
```

Then push:

```bash
docker tag nginx localhost:8080/nginx:latest
docker push localhost:8080/nginx:latest
docker pull localhost:8080/nginx:latest
```

## CLI

```bash
# List all repositories
regctl repos

# List tags
regctl tags nginx

# Inspect a manifest
regctl inspect nginx:latest

# Delete a tag
regctl delete nginx:latest

# Run garbage collection
regctl gc
```

Set `FOUNDRY_URL` or use `-registry` to point at a non-default address.

## API

### Docker Registry v2

| Method | Path | Description |
|--------|------|-------------|
| GET    | /v2/ | Ping / version check |
| HEAD/GET | /v2/{name}/blobs/{digest} | Blob existence / download |
| POST   | /v2/{name}/blobs/uploads/ | Start upload |
| PATCH  | /v2/{name}/blobs/uploads/{uuid} | Append chunk |
| PUT    | /v2/{name}/blobs/uploads/{uuid} | Complete upload |
| HEAD/GET | /v2/{name}/manifests/{ref} | Manifest |
| PUT    | /v2/{name}/manifests/{ref} | Push manifest |
| DELETE | /v2/{name}/manifests/{ref} | Delete manifest |
| GET    | /v2/{name}/tags/list | List tags |

### Admin API

| Method | Path | Description |
|--------|------|-------------|
| GET    | /api/repos | List repositories |
| GET    | /api/repos/{name}/tags | List tags |
| GET    | /api/repos/{name}/manifests/{ref} | Get manifest |
| DELETE | /api/repos/{name} | Delete repository |
| DELETE | /api/tags/{name}/{tag} | Delete tag |
| DELETE | /api/manifests/{name}/{digest} | Delete manifest |
| POST   | /api/gc | Run garbage collection |

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

Everything runs in one process. `data/` is the only state — back it up with `cp -r`.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `FOUNDRY_ADDR` | `:8080` | Listen address |
| `FOUNDRY_DATA` | `./data` | Data directory |
| `FOUNDRY_URL`  | `http://localhost:8080` | Used by regctl |
