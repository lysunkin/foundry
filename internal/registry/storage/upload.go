package storage

import (
	"context"
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
	usage *Usage   // nil: no accounting
	locks sync.Map // upload id -> *sync.Mutex
}

// NewUploadStore creates a store for upload sessions in dir/uploads.
func NewUploadStore(dir string, opts ...Option) (*UploadStore, error) {
	p := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(p, 0o755); err != nil {
		return nil, fmt.Errorf("upload store init: %w", err)
	}
	return &UploadStore{dir: p, usage: applyOptions(opts).usage}, nil
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
// The copy stops with ctx.Err() once ctx is done; the session's offset is then
// unknown and the caller must discard or re-validate it.
func (s *UploadStore) Append(ctx context.Context, id string, offset int64, r io.Reader) (int64, error) {
	path, err := s.safePath(id)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("upload append open: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("upload stat: %w", err)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("upload seek: %w", err)
	}
	// Bytes below the current file size overwrite counted data.
	qw := &quotaWriter{w: f, u: s.usage, free: max(info.Size()-offset, 0)}
	n, err := copyContext(ctx, qw, r)
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
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.usage.removeFile(path, info.Size()); err != nil && !errors.Is(err, fs.ErrNotExist) {
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
