package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// ErrInvalidUploadID is returned for upload IDs containing unsafe characters.
var ErrInvalidUploadID = errors.New("invalid upload id")

// Upload IDs are generated as UUIDs; accept any short token of safe characters
// so an ID can never escape the uploads directory.
var uploadIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// UploadStore manages temporary files for chunked blob uploads.
type UploadStore struct {
	dir   string
	locks sync.Map // upload id -> *sync.Mutex
}

func NewUploadStore(dir string) (*UploadStore, error) {
	p := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(p, 0o755); err != nil {
		return nil, fmt.Errorf("upload store init: %w", err)
	}
	return &UploadStore{dir: p}, nil
}

// Lock serialises operations on a single upload session (e.g. two concurrent
// PATCH requests for the same UUID). The returned func releases the lock.
func (s *UploadStore) Lock(id string) func() {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Create makes a new temp file for the given upload ID.
func (s *UploadStore) Create(id string) (string, error) {
	path, err := s.safePath(id)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// Append writes data to the upload, starting at offset. Returns the new offset.
func (s *UploadStore) Append(id string, offset int64, r io.Reader) (int64, error) {
	path, err := s.safePath(id)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("upload append open: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("upload seek: %w", err)
	}
	n, err := io.Copy(f, r)
	if err != nil {
		return 0, fmt.Errorf("upload write: %w", err)
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("upload sync: %w", err)
	}
	return offset + n, nil
}

// Path returns the filesystem path for an upload ID. Callers must only pass
// IDs that have been validated (e.g. obtained from Create or the database).
func (s *UploadStore) Path(id string) string {
	return s.path(id)
}

// Delete removes the temp file and releases the per-upload lock entry.
// Deleting a non-existent upload is not an error.
func (s *UploadStore) Delete(id string) error {
	path, err := s.safePath(id)
	if err != nil {
		return err
	}
	s.locks.Delete(id)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ValidUploadID reports whether id is a well-formed upload session ID.
func ValidUploadID(id string) bool { return uploadIDRe.MatchString(id) }

func (s *UploadStore) safePath(id string) (string, error) {
	if !ValidUploadID(id) {
		return "", ErrInvalidUploadID
	}
	return s.path(id), nil
}

func (s *UploadStore) path(id string) string {
	return filepath.Join(s.dir, id+".upload")
}
