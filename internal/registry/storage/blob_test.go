package storage_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/foundry/registry/internal/registry/storage"
)

// newBlobStore creates a FilesystemBlobStore backed by a temp directory.
func newBlobStore(t *testing.T) *storage.FilesystemBlobStore {
	t.Helper()
	store, err := storage.NewFilesystemBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemBlobStore: %v", err)
	}
	return store
}

func sha256Digest(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// Put
// ---------------------------------------------------------------------------

func TestBlobStore_Put_RoundTrip(t *testing.T) {
	s := newBlobStore(t)
	data := []byte("hello foundry")

	digest, size, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if size != int64(len(data)) {
		t.Errorf("size = %d, want %d", size, len(data))
	}
	want := sha256Digest(data)
	if digest != want {
		t.Errorf("digest = %s, want %s", digest, want)
	}

	rc, gotSize, err := s.Get(digest)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Errorf("content mismatch: got %q, want %q", got, data)
	}
	if gotSize != int64(len(data)) {
		t.Errorf("Get size = %d, want %d", gotSize, len(data))
	}
}

func TestBlobStore_Put_Deduplication(t *testing.T) {
	s := newBlobStore(t)
	data := []byte("deduplicate me")

	d1, sz1, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	d2, sz2, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if d1 != d2 {
		t.Errorf("digests differ: %s vs %s", d1, d2)
	}
	if sz1 != sz2 {
		t.Errorf("sizes differ: %d vs %d", sz1, sz2)
	}

	// Only one file should be on disk (no orphaned temp files).
	all, err := s.AllDigests()
	if err != nil {
		t.Fatalf("AllDigests: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 stored blob, got %d", len(all))
	}
}

func TestBlobStore_Put_ConcurrentSameBlob(t *testing.T) {
	s := newBlobStore(t)
	data := []byte("concurrent blob")
	want := sha256Digest(data)

	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			d, _, err := s.Put(bytes.NewReader(data))
			if err != nil {
				errs <- err
				return
			}
			if d != want {
				errs <- errors.New("wrong digest: " + d)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Put error: %v", err)
	}
}

func TestBlobStore_Put_EmptyBlob(t *testing.T) {
	s := newBlobStore(t)
	d, size, err := s.Put(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Put empty: %v", err)
	}
	if size != 0 {
		t.Errorf("size = %d, want 0", size)
	}
	want := sha256Digest(nil)
	if d != want {
		t.Errorf("digest = %s, want %s", d, want)
	}
}

// ---------------------------------------------------------------------------
// PutAt
// ---------------------------------------------------------------------------

func TestBlobStore_PutAt_Success(t *testing.T) {
	s := newBlobStore(t)
	data := []byte("chunk assembled")
	digest := sha256Digest(data)

	// Write to a temp file simulating a completed chunked upload.
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "upload.tmp")
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.PutAt(digest, tmpPath, int64(len(data))); err != nil {
		t.Fatalf("PutAt: %v", err)
	}

	// Temp file should be gone (renamed).
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp file should have been renamed away")
	}

	// Blob should be readable.
	rc, sz, err := s.Get(digest)
	if err != nil {
		t.Fatalf("Get after PutAt: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Errorf("content mismatch after PutAt")
	}
	if sz != int64(len(data)) {
		t.Errorf("size = %d, want %d", sz, len(data))
	}
}

func TestBlobStore_PutAt_DigestMismatch(t *testing.T) {
	s := newBlobStore(t)
	data := []byte("real content")
	wrongDigest := sha256Digest([]byte("different content"))

	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "bad.tmp")
	os.WriteFile(tmpPath, data, 0o644)

	err := s.PutAt(wrongDigest, tmpPath, int64(len(data)))
	if err == nil {
		t.Fatal("expected digest mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("error should mention digest mismatch, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestBlobStore_Get_NotFound(t *testing.T) {
	s := newBlobStore(t)
	_, _, err := s.Get("sha256:" + strings.Repeat("a", 64))
	if !errors.Is(err, storage.ErrBlobNotFound) {
		t.Errorf("expected ErrBlobNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Exists
// ---------------------------------------------------------------------------

func TestBlobStore_Exists(t *testing.T) {
	s := newBlobStore(t)
	data := []byte("exists test")

	ok, err := s.Exists(sha256Digest(data))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("blob should not exist yet")
	}

	digest, _, _ := s.Put(bytes.NewReader(data))

	ok, err = s.Exists(digest)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("blob should exist after Put")
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestBlobStore_Delete(t *testing.T) {
	s := newBlobStore(t)
	data := []byte("delete me")
	digest, _, _ := s.Put(bytes.NewReader(data))

	if err := s.Delete(digest); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	ok, err := s.Exists(digest)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("blob should not exist after Delete")
	}
}

// ---------------------------------------------------------------------------
// AllDigests
// ---------------------------------------------------------------------------

func TestBlobStore_AllDigests(t *testing.T) {
	s := newBlobStore(t)

	blobs := [][]byte{
		[]byte("blob one"),
		[]byte("blob two"),
		[]byte("blob three"),
	}
	want := make(map[string]bool)
	for _, b := range blobs {
		d, _, err := s.Put(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		want[d] = true
	}

	all, err := s.AllDigests()
	if err != nil {
		t.Fatalf("AllDigests: %v", err)
	}
	if len(all) != len(want) {
		t.Errorf("AllDigests len = %d, want %d", len(all), len(want))
	}
	for _, d := range all {
		if !want[d] {
			t.Errorf("unexpected digest in AllDigests: %s", d)
		}
	}
}

func TestBlobStore_AllDigests_IgnoresTmpFiles(t *testing.T) {
	store, err := storage.NewFilesystemBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Inject a stale .tmp file directly into the store root by
	// doing a Put and leaving a tmp file via a controlled path.
	// Easiest: just create a file with .tmp suffix in the shard dir.
	data := []byte("real blob")
	digest, _, _ := store.Put(bytes.NewReader(data))

	// Create a stale temp file in the same shard directory.
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	shard := hexDigest[:2]
	// Access the blob's directory via the store root (white-box via AllDigests).
	all, _ := store.AllDigests()
	_ = all // just confirm it works; detailed check below

	// Write a .tmp file and ensure AllDigests doesn't include it.
	// We need the root path — construct it from the fact that blob is at root/shard/hex.
	// We can find the directory by using t.TempDir-based path assumption.
	// Use a second store at a known path to be explicit.
	dir2 := t.TempDir()
	s2, _ := storage.NewFilesystemBlobStore(dir2)
	d2, _, _ := s2.Put(bytes.NewReader(data))
	h := strings.TrimPrefix(d2, "sha256:")
	shardDir := filepath.Join(dir2, "blobs", "sha256", shard)
	os.MkdirAll(shardDir, 0o755)
	os.WriteFile(filepath.Join(shardDir, "stale-upload-*.tmp"), []byte("garbage"), 0o644)

	all2, err := s2.AllDigests()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all2 {
		if strings.HasSuffix(d, ".tmp") {
			t.Errorf("AllDigests included .tmp file: %s", d)
		}
	}
	_ = h
}
