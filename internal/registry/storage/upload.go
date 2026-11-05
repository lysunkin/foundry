package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// UploadStore manages temporary files for chunked blob uploads.
type UploadStore struct {
	dir string
}

func NewUploadStore(dir string) (*UploadStore, error) {
	p := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(p, 0o755); err != nil {
		return nil, fmt.Errorf("upload store init: %w", err)
	}
	return &UploadStore{dir: p}, nil
}

// Create makes a new temp file for the given upload ID.
func (s *UploadStore) Create(id string) (string, error) {
	path := s.path(id)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	f.Close()
	return path, nil
}

// Append writes data to the upload, starting at offset. Returns the new offset.
func (s *UploadStore) Append(id string, offset int64, r io.Reader) (int64, error) {
	path := s.path(id)
	f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
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

// Path returns the filesystem path for an upload ID.
func (s *UploadStore) Path(id string) string {
	return s.path(id)
}

// Delete removes the temp file.
func (s *UploadStore) Delete(id string) error {
	return os.Remove(s.path(id))
}

func (s *UploadStore) path(id string) string {
	return filepath.Join(s.dir, id+".upload")
}
