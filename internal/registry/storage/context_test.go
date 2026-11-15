package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/foundry/registry/internal/registry/storage"
)

// cancelAfterFirstRead yields one chunk, cancelling the context while doing
// so, then would yield more data forever. It simulates a request cancelled
// mid-upload.
type cancelAfterFirstRead struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelAfterFirstRead) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		r.cancel()
	}
	return copy(p, bytes.Repeat([]byte("x"), len(p))), nil
}

func TestBlobStore_Put_CancelledMidStream(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := storage.NewFilesystemBlobStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		put  func(ctx context.Context, r io.Reader) error
	}{
		{"Put", func(ctx context.Context, r io.Reader) error {
			_, _, err := s.Put(ctx, r)
			return err
		}},
		{"PutVerified", func(ctx context.Context, r io.Reader) error {
			_, err := s.PutVerified(ctx, r, sha256Digest([]byte("anything")))
			return err
		}},
	}
	for _, tc := range tests {
		ctx, cancel := context.WithCancel(t.Context())
		r := &cancelAfterFirstRead{cancel: cancel}
		if err := tc.put(ctx, r); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: err = %v, want context.Canceled", tc.name, err)
		}
		if r.reads != 1 {
			t.Errorf("%s: %d reads after cancellation, want 1", tc.name, r.reads)
		}
	}

	// F3: nothing committed and no temp file left behind.
	all, err := s.AllDigests(t.Context())
	if err != nil || len(all) != 0 {
		t.Errorf("AllDigests = %v, %v; want empty", all, err)
	}
	tmps, _ := filepath.Glob(filepath.Join(dir, "blobs", "sha256", "*.tmp"))
	if len(tmps) != 0 {
		t.Errorf("temp files left behind: %v", tmps)
	}
}

func TestBlobStore_ListingsHonourCancellation(t *testing.T) {
	t.Parallel()
	s := newBlobStore(t)
	if _, _, err := s.Put(t.Context(), bytes.NewReader([]byte("a blob"))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.AllDigests(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("AllDigests err = %v, want context.Canceled", err)
	}
	if _, err := s.RemoveStaleTemp(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("RemoveStaleTemp err = %v, want context.Canceled", err)
	}
}

func TestUploadStore_Append_CancelledMidStream(t *testing.T) {
	t.Parallel()
	s, err := storage.NewUploadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.Create("cancelled")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	r := &cancelAfterFirstRead{cancel: cancel}

	if _, err := s.Append(ctx, "cancelled", 0, r); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append err = %v, want context.Canceled", err)
	}
	if r.reads != 1 {
		t.Errorf("%d reads after cancellation, want 1", r.reads)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("upload file should remain for the caller to discard: %v", err)
	}
}
