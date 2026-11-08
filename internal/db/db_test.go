package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// A database created with only migration 001 (the original schema) must
// upgrade cleanly, preserving data, and allow the same manifest in two repos.
func TestMigration002_UpgradesExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	v1, _ := migrations.ReadFile("migrations/001_init.sql")
	mustExec(t, old, string(v1))
	mustExec(t, old, `INSERT INTO schema_migrations (version) VALUES (1)`)
	mustExec(t, old, `INSERT INTO repositories (id, name) VALUES (1, 'a'), (2, 'b')`)
	mustExec(t, old, `INSERT INTO manifests (digest, repository_id, content, size) VALUES ('sha256:m', 1, x'7b7d', 2)`)
	mustExec(t, old, `INSERT INTO tags (repository_id, tag, manifest_digest) VALUES (1, 'latest', 'sha256:m')`)
	old.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	defer d.Close()

	var tag string
	if err := d.QueryRow(`SELECT tag FROM tags WHERE repository_id = 1`).Scan(&tag); err != nil || tag != "latest" {
		t.Fatalf("tag not preserved: %q %v", tag, err)
	}
	// Same digest in repo 2 is now allowed.
	mustExec(t, d, `INSERT INTO manifests (repository_id, digest, content, size) VALUES (2, 'sha256:m', x'7b7d', 2)`)
	// Cascades: deleting repo 1 removes its manifest and tag only.
	mustExec(t, d, `DELETE FROM repositories WHERE id = 1`)
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM manifests`).Scan(&n)
	if n != 1 {
		t.Errorf("manifests after cascade = %d, want 1", n)
	}
	d.QueryRow(`SELECT COUNT(*) FROM tags`).Scan(&n)
	if n != 0 {
		t.Errorf("tags after cascade = %d, want 0", n)
	}
}

func mustExec(t *testing.T, d *sql.DB, q string) {
	t.Helper()
	if _, err := d.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
