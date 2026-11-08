package services

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/foundry/registry/internal/model"
	"github.com/foundry/registry/internal/oci"
)

var (
	// ErrNotFound is returned when a repository, manifest, tag, blob or upload does not exist.
	ErrNotFound = errors.New("not found")
	// ErrManifestInvalid is returned for malformed manifests.
	ErrManifestInvalid = errors.New("manifest invalid")
	// ErrManifestBlobUnknown is returned when a manifest references a blob or
	// child manifest the registry does not have.
	ErrManifestBlobUnknown = errors.New("manifest references unknown blob")
	// ErrDigestMismatch is returned when a manifest pushed by digest does not
	// hash to that digest.
	ErrDigestMismatch = errors.New("digest mismatch")
)

// sqliteTime is the timestamp layout used by the schema's DEFAULT clauses.
const sqliteTime = "2006-01-02T15:04:05Z"

// RepositoryService handles CRUD for repositories, blobs, manifests, and tags.
type RepositoryService struct {
	db *sql.DB
}

func NewRepositoryService(db *sql.DB) *RepositoryService {
	return &RepositoryService{db: db}
}

// queryer is satisfied by both *sql.DB and *sql.Tx.
type queryer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// --- Repositories ---

func (s *RepositoryService) EnsureRepository(name string) (*model.Repository, error) {
	return ensureRepository(s.db, name)
}

func ensureRepository(q queryer, name string) (*model.Repository, error) {
	if _, err := q.Exec(`INSERT OR IGNORE INTO repositories (name) VALUES (?)`, name); err != nil {
		return nil, fmt.Errorf("ensure repo: %w", err)
	}
	return getRepository(q, name)
}

func (s *RepositoryService) GetRepository(name string) (*model.Repository, error) {
	return getRepository(s.db, name)
}

func getRepository(q queryer, name string) (*model.Repository, error) {
	row := q.QueryRow(`SELECT id, name, created_at FROM repositories WHERE name = ?`, name)
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
	repos := []*model.Repository{}
	for rows.Next() {
		var r model.Repository
		if err := rows.Scan(&r.ID, &r.Name, &r.CreatedAt); err != nil {
			return nil, err
		}
		repos = append(repos, &r)
	}
	return repos, rows.Err()
}

// RepositorySummary is a repository plus aggregate counts, for list views.
type RepositorySummary struct {
	model.Repository
	TagCount int `json:"tag_count"`
}

// ListRepositorySummaries returns all repositories with their tag counts in a
// single query.
func (s *RepositoryService) ListRepositorySummaries() ([]RepositorySummary, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.name, r.created_at, COUNT(t.tag)
		FROM repositories r
		LEFT JOIN tags t ON t.repository_id = r.id
		GROUP BY r.id
		ORDER BY r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepositorySummary
	for rows.Next() {
		var r RepositorySummary
		if err := rows.Scan(&r.ID, &r.Name, &r.CreatedAt, &r.TagCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRepositoryNames returns up to n repository names sorted lexically and
// strictly greater than last (n <= 0 means no limit). Used by /v2/_catalog.
func (s *RepositoryService) ListRepositoryNames(n int, last string) ([]string, error) {
	limit := -1
	if n > 0 {
		limit = n
	}
	rows, err := s.db.Query(`SELECT name FROM repositories WHERE name > ? ORDER BY name LIMIT ?`, last, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// DeleteRepository removes a repository and (via ON DELETE CASCADE) all its
// manifests, tags and upload sessions. Blobs are left for GC.
func (s *RepositoryService) DeleteRepository(name string) error {
	res, err := s.db.Exec(`DELETE FROM repositories WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Blobs ---

func (s *RepositoryService) SaveBlob(digest string, size int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO blobs (digest, size) VALUES (?, ?)`, digest, size)
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
	return s.queryStrings(`SELECT digest FROM blobs`)
}

func (s *RepositoryService) DeleteBlob(digest string) error {
	_, err := s.db.Exec(`DELETE FROM blobs WHERE digest = ?`, digest)
	return err
}

// --- Manifests ---

// PutManifest validates and stores a manifest and, if reference is a tag,
// points the tag at it — all in one transaction. It returns the manifest
// digest.
//
// Errors: ErrManifestInvalid, ErrDigestMismatch, ErrManifestBlobUnknown.
func (s *RepositoryService) PutManifest(repoName, reference, mediaType string, content []byte) (string, error) {
	digest := ComputeDigest(content)
	isDigestRef := oci.IsDigestReference(reference)
	if isDigestRef && reference != digest {
		return "", fmt.Errorf("%w: reference %s, content %s", ErrDigestMismatch, reference, digest)
	}

	if !oci.IsManifestMediaType(mediaType) {
		return "", fmt.Errorf("%w: unsupported media type %q", ErrManifestInvalid, mediaType)
	}
	refs, err := ParseManifest(content)
	if err != nil {
		return "", err
	}

	gcLock.RLock()
	defer gcLock.RUnlock()

	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	repo, err := ensureRepository(tx, repoName)
	if err != nil {
		return "", err
	}

	for _, d := range refs.Blobs {
		var one int
		err := tx.QueryRow(`SELECT 1 FROM blobs WHERE digest = ?`, d).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: %s", ErrManifestBlobUnknown, d)
		} else if err != nil {
			return "", err
		}
	}
	for _, d := range refs.Manifests {
		var one int
		err := tx.QueryRow(`SELECT 1 FROM manifests WHERE repository_id = ? AND digest = ?`, repo.ID, d).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: manifest %s", ErrManifestBlobUnknown, d)
		} else if err != nil {
			return "", err
		}
	}

	if err := upsertManifest(tx, repo.ID, digest, mediaType, content); err != nil {
		return "", err
	}
	if !isDigestRef {
		if err := upsertTag(tx, repo.ID, reference, digest); err != nil {
			return "", err
		}
	}
	return digest, tx.Commit()
}

// SaveManifest stores a manifest without reference validation. Prefer
// PutManifest for client-facing writes.
func (s *RepositoryService) SaveManifest(repoName, digest, mediaType string, content []byte) error {
	repo, err := s.EnsureRepository(repoName)
	if err != nil {
		return err
	}
	return upsertManifest(s.db, repo.ID, digest, mediaType, content)
}

func upsertManifest(q queryer, repoID int64, digest, mediaType string, content []byte) error {
	_, err := q.Exec(`
		INSERT INTO manifests (repository_id, digest, media_type, content, size)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(repository_id, digest) DO UPDATE SET media_type = excluded.media_type`,
		repoID, digest, mediaType, content, int64(len(content)))
	return err
}

func (s *RepositoryService) GetManifest(repoName, reference string) (*model.Manifest, error) {
	repo, err := s.GetRepository(repoName)
	if err != nil {
		return nil, err
	}

	var row *sql.Row
	if oci.IsDigestReference(reference) {
		row = s.db.QueryRow(`
			SELECT digest, repository_id, media_type, content, size, created_at
			FROM manifests WHERE repository_id = ? AND digest = ?`,
			repo.ID, reference)
	} else {
		row = s.db.QueryRow(`
			SELECT m.digest, m.repository_id, m.media_type, m.content, m.size, m.created_at
			FROM tags t
			JOIN manifests m ON m.repository_id = t.repository_id AND m.digest = t.manifest_digest
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

// DeleteManifest removes a manifest from a repository; tags pointing at it are
// removed by ON DELETE CASCADE.
func (s *RepositoryService) DeleteManifest(repoName, digest string) error {
	repo, err := s.GetRepository(repoName)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM manifests WHERE repository_id = ? AND digest = ?`, repo.ID, digest)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *RepositoryService) AllManifestDigests() ([]string, error) {
	return s.queryStrings(`SELECT DISTINCT digest FROM manifests`)
}

// --- Tags ---

func (s *RepositoryService) SaveTag(repoName, tag, manifestDigest string) error {
	repo, err := s.EnsureRepository(repoName)
	if err != nil {
		return err
	}
	return upsertTag(s.db, repo.ID, tag, manifestDigest)
}

func upsertTag(q queryer, repoID int64, tag, digest string) error {
	_, err := q.Exec(`
		INSERT INTO tags (repository_id, tag, manifest_digest)
		VALUES (?, ?, ?)
		ON CONFLICT(repository_id, tag) DO UPDATE SET
			manifest_digest = excluded.manifest_digest,
			updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')`,
		repoID, tag, digest)
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
	tags := []*model.Tag{}
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
	if n, _ := res.RowsAffected(); n == 0 {
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

// GetRepoUpload returns an upload session only if it belongs to repoName.
func (s *RepositoryService) GetRepoUpload(repoName, id string) (*model.Upload, error) {
	row := s.db.QueryRow(`
		SELECT u.id, u.repository_id, u.offset, u.temp_path, u.created_at
		FROM uploads u JOIN repositories r ON r.id = u.repository_id
		WHERE u.id = ? AND r.name = ?`, id, repoName)
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

// UploadIDsCreatedBefore returns IDs of upload sessions started before t.
func (s *RepositoryService) UploadIDsCreatedBefore(t time.Time) ([]string, error) {
	return s.queryStrings(`SELECT id FROM uploads WHERE created_at < ?`, t.UTC().Format(sqliteTime))
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

func (s *RepositoryService) queryStrings(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
