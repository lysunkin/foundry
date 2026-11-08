package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/foundry/registry/internal/oci"
)

var (
	// ErrBlobNotFound is returned when a blob does not exist.
	ErrBlobNotFound = errors.New("blob not found")
	// ErrInvalidDigest is returned for malformed or unsupported digests.
	ErrInvalidDigest = errors.New("invalid digest")
	// ErrDigestMismatch is returned when content does not hash to the expected digest.
	ErrDigestMismatch = errors.New("digest mismatch")
	// ErrSizeMismatch is returned when content length differs from the expected size.
	ErrSizeMismatch = errors.New("size mismatch")
)

// BlobInfo describes a stored blob.
type BlobInfo struct {
	Size    int64
	ModTime time.Time
}

// BlobStore defines content-addressable blob operations.
type BlobStore interface {
	// Put stores r under its sha256 digest. Returns the final digest and size.
	Put(r io.Reader) (digest string, size int64, err error)
	// PutVerified is Put, but fails with ErrDigestMismatch (and stores nothing)
	// unless the content hashes to expected.
	PutVerified(r io.Reader, expected string) (size int64, err error)
	// Get opens the blob for reading. Caller must close it.
	Get(digest string) (io.ReadSeekCloser, int64, error)
	// Stat returns blob metadata.
	Stat(digest string) (BlobInfo, error)
	// Exists reports whether the blob is present.
	Exists(digest string) (bool, error)
	// Delete removes the blob.
	Delete(digest string) error
}

var _ BlobStore = (*FilesystemBlobStore)(nil)

// FilesystemBlobStore stores blobs on disk, sharded by the first two hex chars
// of the digest — identical to Git's object layout.
//
//	data/blobs/sha256/ab/abcdef1234...
//
// Writes go to a temp file in the store root and are atomically renamed into
// place, so readers never observe partial blobs. Because blobs are
// content-addressed, concurrent writers of the same digest produce identical
// files and the last rename wins harmlessly; no locking is required.
type FilesystemBlobStore struct {
	root string
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
	return s.put(r, "")
}

// PutVerified stores r only if it hashes to expected.
func (s *FilesystemBlobStore) PutVerified(r io.Reader, expected string) (int64, error) {
	if !oci.ValidDigest(expected) {
		return 0, ErrInvalidDigest
	}
	_, size, err := s.put(r, expected)
	return size, err
}

func (s *FilesystemBlobStore) put(r io.Reader, expected string) (string, int64, error) {
	tmp, err := os.CreateTemp(s.root, "upload-*.tmp")
	if err != nil {
		return "", 0, fmt.Errorf("blob tmp: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		tmp.Close()
		return "", 0, fmt.Errorf("blob write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", 0, fmt.Errorf("blob sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("blob close: %w", err)
	}

	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if expected != "" && digest != expected {
		return "", 0, fmt.Errorf("%w: expected %s, got %s", ErrDigestMismatch, expected, digest)
	}

	if err := s.commit(tmpName, digest); err != nil {
		return "", 0, err
	}
	committed = true
	return digest, size, nil
}

// PutAt verifies the file at tempPath against digest (and expectedSize, when
// expectedSize >= 0) and moves it into the store. Used to finalise chunked
// uploads. On ErrDigestMismatch/ErrSizeMismatch the temp file is left in place
// for the caller to clean up.
func (s *FilesystemBlobStore) PutAt(digest string, tempPath string, expectedSize int64) error {
	if !oci.ValidDigest(digest) {
		return ErrInvalidDigest
	}
	f, err := os.Open(tempPath)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	f.Close()
	if err != nil {
		return err
	}
	if expectedSize >= 0 && n != expectedSize {
		return fmt.Errorf("%w: expected %d bytes, got %d", ErrSizeMismatch, expectedSize, n)
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != digest {
		return fmt.Errorf("%w: expected %s, got %s", ErrDigestMismatch, digest, got)
	}
	return s.commit(tempPath, digest)
}

func (s *FilesystemBlobStore) commit(src, digest string) error {
	dest, err := s.blobPath(digest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("blob mkdir: %w", err)
	}
	// Already present — deduplicate (content is identical by definition).
	if _, err := os.Stat(dest); err == nil {
		return os.Remove(src)
	}
	if err := os.Rename(src, dest); err != nil {
		return fmt.Errorf("blob rename: %w", err)
	}
	return nil
}

// Get opens a blob for reading. The returned reader is seekable so callers can
// serve HTTP Range requests.
func (s *FilesystemBlobStore) Get(digest string) (io.ReadSeekCloser, int64, error) {
	path, err := s.blobPath(digest)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
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

// Stat returns size and modification time of a blob.
func (s *FilesystemBlobStore) Stat(digest string) (BlobInfo, error) {
	path, err := s.blobPath(digest)
	if err != nil {
		return BlobInfo{}, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return BlobInfo{}, ErrBlobNotFound
		}
		return BlobInfo{}, err
	}
	return BlobInfo{Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

func (s *FilesystemBlobStore) Exists(digest string) (bool, error) {
	_, err := s.Stat(digest)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrBlobNotFound):
		return false, nil
	default:
		return false, err
	}
}

// Delete removes a blob. Returns ErrBlobNotFound if it does not exist.
func (s *FilesystemBlobStore) Delete(digest string) error {
	path, err := s.blobPath(digest)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrBlobNotFound
		}
		return err
	}
	return nil
}

// AllDigests returns every stored digest (used by GC). Files that do not look
// like blobs (temp files, stray files) are ignored.
func (s *FilesystemBlobStore) AllDigests() ([]string, error) {
	var digests []string
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		// rel looks like "ab/abcdef123..."
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 2 {
			return nil
		}
		digest := "sha256:" + parts[1]
		if oci.ValidDigest(digest) && strings.HasPrefix(parts[1], parts[0]) {
			digests = append(digests, digest)
		}
		return nil
	})
	return digests, err
}

// RemoveStaleTemp deletes temp files older than maxAge left behind by
// interrupted Put calls. Returns the number of files removed.
func (s *FilesystemBlobStore) RemoveStaleTemp(maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(s.root, e.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}

// blobPath maps a validated digest to its on-disk location. Validation here
// is the single choke point that prevents path traversal via crafted digests.
func (s *FilesystemBlobStore) blobPath(digest string) (string, error) {
	if !oci.ValidDigest(digest) {
		return "", ErrInvalidDigest
	}
	hex := strings.TrimPrefix(digest, "sha256:")
	return filepath.Join(s.root, hex[:2], hex), nil
}
