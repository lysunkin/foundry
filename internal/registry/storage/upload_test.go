package storage_test

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/foundry/registry/internal/registry/storage"
)

func newUploadStore(t *testing.T) *storage.UploadStore {
	t.Helper()
	s, err := storage.NewUploadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewUploadStore: %v", err)
	}
	return s
}

func TestUploadStore_CreateAndPath(t *testing.T) {
	s := newUploadStore(t)
	path, err := s.Create("abc123")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if path != s.Path("abc123") {
		t.Errorf("Create returned %s, Path returned %s", path, s.Path("abc123"))
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestUploadStore_Append_SingleChunk(t *testing.T) {
	s := newUploadStore(t)
	s.Create("upload1")

	data := []byte("hello world")
	newOffset, err := s.Append("upload1", 0, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if newOffset != int64(len(data)) {
		t.Errorf("newOffset = %d, want %d", newOffset, len(data))
	}

	got, _ := os.ReadFile(s.Path("upload1"))
	if !bytes.Equal(got, data) {
		t.Errorf("file content = %q, want %q", got, data)
	}
}

func TestUploadStore_Append_MultipleChunks(t *testing.T) {
	s := newUploadStore(t)
	s.Create("chunked")

	chunk1 := []byte("AAAA")
	chunk2 := []byte("BBBB")
	chunk3 := []byte("CCCC")

	off, _ := s.Append("chunked", 0, bytes.NewReader(chunk1))
	off, _ = s.Append("chunked", off, bytes.NewReader(chunk2))
	off, _ = s.Append("chunked", off, bytes.NewReader(chunk3))

	want := append(append(chunk1, chunk2...), chunk3...)
	got, _ := os.ReadFile(s.Path("chunked"))
	if !bytes.Equal(got, want) {
		t.Errorf("assembled content = %q, want %q", got, want)
	}
	if off != int64(len(want)) {
		t.Errorf("final offset = %d, want %d", off, len(want))
	}
}

func TestUploadStore_Append_ReturnedOffsetIsAbsolute(t *testing.T) {
	s := newUploadStore(t)
	s.Create("offtest")

	data := make([]byte, 1024)
	off, err := s.Append("offtest", 0, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if off != 1024 {
		t.Errorf("expected offset 1024, got %d", off)
	}
}

func TestUploadStore_Append_SeeksToCorrectPosition(t *testing.T) {
	// Write chunk1, then overwrite from the middle — simulates a retry.
	s := newUploadStore(t)
	s.Create("seek")

	// Fill with 8 bytes of 'A'.
	s.Append("seek", 0, bytes.NewReader(bytes.Repeat([]byte("A"), 8)))

	// "Retry" last 4 bytes with 'B'.
	s.Append("seek", 4, bytes.NewReader(bytes.Repeat([]byte("B"), 4)))

	want := append(bytes.Repeat([]byte("A"), 4), bytes.Repeat([]byte("B"), 4)...)
	got, _ := os.ReadFile(s.Path("seek"))
	if !bytes.Equal(got, want) {
		t.Errorf("seek content = %q, want %q", got, want)
	}
}

func TestUploadStore_Delete(t *testing.T) {
	s := newUploadStore(t)
	path, _ := s.Create("del")

	if err := s.Delete("del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be gone after Delete")
	}
}

func TestUploadStore_Append_EmptyReader(t *testing.T) {
	s := newUploadStore(t)
	s.Create("empty")

	off, err := s.Append("empty", 0, bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Append empty: %v", err)
	}
	if off != 0 {
		t.Errorf("offset after empty append = %d, want 0", off)
	}
}

func TestUploadStore_Create_ReturnsCorrectPath(t *testing.T) {
	s := newUploadStore(t)
	id := "my-upload-uuid"
	path, err := s.Create(id)
	if err != nil {
		t.Fatal(err)
	}
	// Path must be consistent.
	if s.Path(id) != path {
		t.Errorf("Path(%q) = %s, Create returned %s", id, s.Path(id), path)
	}
	// File must be readable.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	if len(data) != 0 {
		t.Errorf("new upload file should be empty, got %d bytes", len(data))
	}
}
