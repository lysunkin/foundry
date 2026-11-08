-- Manifests become repository-scoped: the same manifest digest may legitimately
-- exist in several repositories (e.g. `docker push a/x` and `docker push b/x`
-- of the same image). Previously `digest` was a global PRIMARY KEY, so pushing
-- to a second repository silently moved the manifest out of the first one.
--
-- Child rows now cascade on delete so repository/manifest deletion cannot
-- leave dangling tags or uploads.

CREATE TABLE manifests_new (
    repository_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    digest        TEXT    NOT NULL,
    media_type    TEXT    NOT NULL DEFAULT '',
    content       BLOB    NOT NULL,
    size          INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    PRIMARY KEY (repository_id, digest)
);

INSERT INTO manifests_new (repository_id, digest, media_type, content, size, created_at)
SELECT repository_id, digest, media_type, content, size, created_at FROM manifests;

CREATE TABLE tags_new (
    repository_id   INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    tag             TEXT    NOT NULL,
    manifest_digest TEXT    NOT NULL,
    updated_at      DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    PRIMARY KEY (repository_id, tag),
    FOREIGN KEY (repository_id, manifest_digest)
        REFERENCES manifests_new(repository_id, digest) ON DELETE CASCADE
);

-- Only keep tags whose manifest still lives in the same repository.
INSERT INTO tags_new (repository_id, tag, manifest_digest, updated_at)
SELECT t.repository_id, t.tag, t.manifest_digest, t.updated_at
FROM tags t
JOIN manifests_new m ON m.repository_id = t.repository_id AND m.digest = t.manifest_digest;

CREATE TABLE uploads_new (
    id            TEXT PRIMARY KEY,
    repository_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    offset        INTEGER NOT NULL DEFAULT 0,
    temp_path     TEXT    NOT NULL,
    created_at    DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

INSERT INTO uploads_new (id, repository_id, offset, temp_path, created_at)
SELECT id, repository_id, offset, temp_path, created_at FROM uploads;

DROP TABLE tags;
DROP TABLE uploads;
DROP TABLE manifests;

-- RENAME also rewrites the FK in tags_new to point at "manifests".
ALTER TABLE manifests_new RENAME TO manifests;
ALTER TABLE tags_new      RENAME TO tags;
ALTER TABLE uploads_new   RENAME TO uploads;

CREATE INDEX idx_tags_manifest      ON tags (repository_id, manifest_digest);
CREATE INDEX idx_uploads_created_at ON uploads (created_at);
CREATE INDEX idx_blobs_created_at   ON blobs (created_at);
