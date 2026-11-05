package services

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/foundry/registry/internal/model"
)

var ErrNotFound = errors.New("not found")

// RepositoryService handles CRUD for repositories, blobs, manifests, and tags.
type RepositoryService struct {
	db *sql.DB
}

func NewRepositoryService(db *sql.DB) *RepositoryService {
	return &RepositoryService{db: db}
}

// --- Repositories ---

func (s *RepositoryService) EnsureRepository(name string) (*model.Repository, error) {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO repositories (name) VALUES (?)`, name)
	if err != nil {
		return nil, fmt.Errorf("ensure repo: %w", err)
	}
	return s.GetRepository(name)
}

func (s *RepositoryService) GetRepository(name string) (*model.Repository, error) {
	row := s.db.QueryRow(`SELECT id, name, created_at FROM repositories WHERE name = ?`, name)
	var r model.Repository
	if err := row.Scan(&r.ID, &r.Name, &r.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &r, nil
}

func (s *RepositoryService) ListRepositories() ([]*model.Repository, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM repositories ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []*model.Repository
	for rows.Next() {
		var r model.Repository
		if err := rows.Scan(&r.ID, &r.Name, &r.CreatedAt); err != nil {
			return nil, err
		}
		repos = append(repos, &r)
	}
	return repos, rows.Err()
}

func (s *RepositoryService) DeleteRepository(name string) error {
	repo, err := s.GetRepository(name)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM tags WHERE repository_id = ?`, repo.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM manifests WHERE repository_id = ?`, repo.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM repositories WHERE id = ?`, repo.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// --- Blobs ---

func (s *RepositoryService) SaveBlob(digest string, size int64) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO blobs (digest, size) VALUES (?, ?)`,
		digest, size)
	return err
}

func (s *RepositoryService) GetBlob(digest string) (*model.Blob, error) {
	row := s.db.QueryRow(`SELECT digest, size, created_at FROM blobs WHERE digest = ?`, digest)
	var b model.Blob
	if err := row.Scan(&b.Digest, &b.Size, &b.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &b, nil
}

func (s *RepositoryService) BlobExists(digest string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM blobs WHERE digest = ?`, digest).Scan(&count)
	return count > 0, err
}

func (s *RepositoryService) AllBlobDigests() ([]string, error) {
	rows, err := s.db.Query(`SELECT digest FROM blobs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var digests []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		digests = append(digests, d)
	}
	return digests, rows.Err()
}

func (s *RepositoryService) DeleteBlob(digest string) error {
	_, err := s.db.Exec(`DELETE FROM blobs WHERE digest = ?`, digest)
	return err
}

// --- Manifests ---

func (s *RepositoryService) SaveManifest(repoName, digest, mediaType string, content []byte) error {
	repo, err := s.EnsureRepository(repoName)
	if err != nil {
		return err
	}
	size := int64(len(content))
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO manifests (digest, repository_id, media_type, content, size) VALUES (?, ?, ?, ?, ?)`,
		digest, repo.ID, mediaType, content, size)
	return err
}

func (s *RepositoryService) GetManifest(repoName, reference string) (*model.Manifest, error) {
	repo, err := s.GetRepository(repoName)
	if err != nil {
		return nil, err
	}

	var row *sql.Row

	if isDigest(reference) {
		row = s.db.QueryRow(
			`SELECT digest, repository_id, media_type, content, size, created_at FROM manifests WHERE digest = ? AND repository_id = ?`,
			reference, repo.ID)
	} else {
		// It's a tag name — join through tags.
		row = s.db.QueryRow(`
			SELECT m.digest, m.repository_id, m.media_type, m.content, m.size, m.created_at
			FROM manifests m
			JOIN tags t ON t.manifest_digest = m.digest
			WHERE t.repository_id = ? AND t.tag = ?`,
			repo.ID, reference)
	}

	var m model.Manifest
	if err := row.Scan(&m.Digest, &m.RepositoryID, &m.MediaType, &m.Content, &m.Size, &m.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (s *RepositoryService) DeleteManifest(repoName, digest string) error {
	repo, err := s.GetRepository(repoName)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM tags WHERE repository_id = ? AND manifest_digest = ?`, repo.ID, digest); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM manifests WHERE repository_id = ? AND digest = ?`, repo.ID, digest)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *RepositoryService) AllManifestDigests() ([]string, error) {
	rows, err := s.db.Query(`SELECT digest FROM manifests`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var digests []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		digests = append(digests, d)
	}
	return digests, rows.Err()
}

// --- Tags ---

func (s *RepositoryService) SaveTag(repoName, tag, manifestDigest string) error {
	repo, err := s.EnsureRepository(repoName)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO tags (repository_id, tag, manifest_digest)
		VALUES (?, ?, ?)
		ON CONFLICT(repository_id, tag) DO UPDATE SET
			manifest_digest = excluded.manifest_digest,
			updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')`,
		repo.ID, tag, manifestDigest)
	return err
}

func (s *RepositoryService) ListTags(repoName string) ([]*model.Tag, error) {
	repo, err := s.GetRepository(repoName)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT repository_id, tag, manifest_digest, updated_at
		FROM tags WHERE repository_id = ? ORDER BY tag`, repo.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []*model.Tag
	for rows.Next() {
		var t model.Tag
		if err := rows.Scan(&t.RepositoryID, &t.Tag, &t.ManifestDigest, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.RepositoryName = repoName
		tags = append(tags, &t)
	}
	return tags, rows.Err()
}

func (s *RepositoryService) DeleteTag(repoName, tag string) error {
	repo, err := s.GetRepository(repoName)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM tags WHERE repository_id = ? AND tag = ?`, repo.ID, tag)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Uploads ---

func (s *RepositoryService) SaveUpload(id, tempPath string, repoID, offset int64) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO uploads (id, repository_id, offset, temp_path) VALUES (?, ?, ?, ?)`,
		id, repoID, offset, tempPath)
	return err
}

func (s *RepositoryService) GetUpload(id string) (*model.Upload, error) {
	row := s.db.QueryRow(
		`SELECT id, repository_id, offset, temp_path, created_at FROM uploads WHERE id = ?`, id)
	var u model.Upload
	if err := row.Scan(&u.ID, &u.RepositoryID, &u.Offset, &u.TempPath, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (s *RepositoryService) UpdateUploadOffset(id string, offset int64) error {
	_, err := s.db.Exec(`UPDATE uploads SET offset = ? WHERE id = ?`, offset, id)
	return err
}

func (s *RepositoryService) DeleteUpload(id string) error {
	_, err := s.db.Exec(`DELETE FROM uploads WHERE id = ?`, id)
	return err
}

// --- GC ---

// ReferencedBlobDigests returns all blobs referenced by any stored manifest.
func (s *RepositoryService) ReferencedBlobDigests() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT content FROM manifests`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	referenced := make(map[string]bool)
	for rows.Next() {
		var content []byte
		if err := rows.Scan(&content); err != nil {
			return nil, err
		}
		extractDigests(content, referenced)
	}
	return referenced, rows.Err()
}

// isDigest reports whether ref looks like sha256:...
func isDigest(ref string) bool {
	return len(ref) > 7 && ref[:7] == "sha256:"
}
