package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrBlobNotFound is returned when a blob does not exist.
var ErrBlobNotFound = errors.New("blob not found")

// BlobStore defines content-addressable blob operations.
type BlobStore interface {
	// Put stores r under its sha256 digest. Returns the final digest and size.
	Put(r io.Reader) (digest string, size int64, err error)
	// Get opens the blob for reading. Caller must close it.
	Get(digest string) (io.ReadCloser, int64, error)
	// Exists reports whether the blob is present.
	Exists(digest string) (bool, error)
	// Delete removes the blob.
	Delete(digest string) error
}

// FilesystemBlobStore stores blobs on disk, sharded by the first two hex chars
// of the digest — identical to Git's object layout.
//
//   data/blobs/sha256/ab/abcdef1234...
type FilesystemBlobStore struct {
	root  string
	mu    sync.Map // per-digest mutexes to prevent duplicate concurrent writes
}

// NewFilesystemBlobStore creates a store rooted at dir/blobs.
func NewFilesystemBlobStore(dir string) (*FilesystemBlobStore, error) {
	root := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("blob store init: %w", err)
	}
	return &FilesystemBlobStore{root: root}, nil
}

// Put streams r to a temp file, computes its sha256, then atomically renames
// it to the final path. This is safe for concurrent and interrupted writes.
func (s *FilesystemBlobStore) Put(r io.Reader) (string, int64, error) {
	// Write to a temp file while hashing.
	tmp, err := os.CreateTemp(s.root, "upload-*.tmp")
	if err != nil {
		return "", 0, fmt.Errorf("blob tmp: %w", err)
	}
	tmpName := tmp.Name()

	h := sha256.New()
	mw := io.MultiWriter(tmp, h)

	size, err := io.Copy(mw, r)
	if err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", 0, fmt.Errorf("blob write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", 0, fmt.Errorf("blob sync: %w", err)
	}
	tmp.Close()

	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))

	// Per-digest lock so two concurrent identical pushes don't race.
	actual, _ := s.mu.LoadOrStore(digest, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	dest := s.blobPath(digest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		os.Remove(tmpName)
		return "", 0, fmt.Errorf("blob mkdir: %w", err)
	}

	// Already exists — deduplicate.
	if _, err := os.Stat(dest); err == nil {
		os.Remove(tmpName)
		return digest, size, nil
	}

	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return "", 0, fmt.Errorf("blob rename: %w", err)
	}
	return digest, size, nil
}

// PutAt stores already-known data at a specific digest path (used during
// chunked upload assembly). It still verifies the digest.
func (s *FilesystemBlobStore) PutAt(digest string, tempPath string, expectedSize int64) error {
	dest := s.blobPath(digest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("blob mkdir: %w", err)
	}
	// Verify digest.
	f, err := os.Open(tempPath)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return err
	}
	f.Close()

	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != digest {
		return fmt.Errorf("digest mismatch: expected %s got %s", digest, got)
	}

	return os.Rename(tempPath, dest)
}

func (s *FilesystemBlobStore) Get(digest string) (io.ReadCloser, int64, error) {
	path := s.blobPath(digest)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, ErrBlobNotFound
		}
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

func (s *FilesystemBlobStore) Exists(digest string) (bool, error) {
	_, err := os.Stat(s.blobPath(digest))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (s *FilesystemBlobStore) Delete(digest string) error {
	return os.Remove(s.blobPath(digest))
}

// AllDigests returns every stored digest (used by GC).
func (s *FilesystemBlobStore) AllDigests() ([]string, error) {
	var digests []string
	err := filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(d.Name(), ".tmp") {
			return nil
		}
		// Reconstruct digest from the shard path.
		rel, _ := filepath.Rel(s.root, path)
		// rel looks like "ab/abcdef123..."
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) == 2 {
			digests = append(digests, "sha256:"+parts[1])
		}
		return nil
	})
	return digests, err
}

func (s *FilesystemBlobStore) blobPath(digest string) string {
	// Strip algorithm prefix.
	hex := strings.TrimPrefix(digest, "sha256:")
	shard := hex[:2]
	return filepath.Join(s.root, shard, hex)
}
