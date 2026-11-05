-- Repositories
CREATE TABLE IF NOT EXISTS repositories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    UNIQUE NOT NULL,
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Blobs
CREATE TABLE IF NOT EXISTS blobs (
    digest     TEXT PRIMARY KEY,
    size       INTEGER NOT NULL,
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Manifests
CREATE TABLE IF NOT EXISTS manifests (
    digest        TEXT PRIMARY KEY,
    repository_id INTEGER NOT NULL REFERENCES repositories(id),
    media_type    TEXT NOT NULL DEFAULT '',
    content       BLOB NOT NULL,
    size          INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Tags
CREATE TABLE IF NOT EXISTS tags (
    repository_id   INTEGER NOT NULL REFERENCES repositories(id),
    tag             TEXT    NOT NULL,
    manifest_digest TEXT    NOT NULL REFERENCES manifests(digest),
    updated_at      DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(repository_id, tag)
);

-- In-progress uploads
CREATE TABLE IF NOT EXISTS uploads (
    id            TEXT PRIMARY KEY,
    repository_id INTEGER NOT NULL REFERENCES repositories(id),
    offset        INTEGER NOT NULL DEFAULT 0,
    temp_path     TEXT    NOT NULL,
    created_at    DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Schema version tracking
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
