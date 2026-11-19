package storage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foundry/registry/internal/registry/storage"
)

// diskBytes sums the sizes of all files the usage counter is meant to track.
func diskBytes(t *testing.T, dataDir string) int64 {
	t.Helper()
	var total int64
	for _, dir := range []string{"blobs", "uploads"} {
		err := filepath.WalkDir(filepath.Join(dataDir, dir), func(_ string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return total
}

type usageFixture struct {
	dir     string
	usage   *storage.Usage
	blobs   *storage.FilesystemBlobStore
	uploads *storage.UploadStore
}

func newUsageFixture(t *testing.T, limit int64) *usageFixture {
	t.Helper()
	f := &usageFixture{dir: t.TempDir(), usage: storage.NewUsage(limit)}
	var err error
	if f.blobs, err = storage.NewFilesystemBlobStore(f.dir, storage.WithUsage(f.usage)); err != nil {
		t.Fatal(err)
	}
	if f.uploads, err = storage.NewUploadStore(f.dir, storage.WithUsage(f.usage)); err != nil {
		t.Fatal(err)
	}
	return f
}

// check asserts that the counter matches the bytes on disk.
func (f *usageFixture) check(t *testing.T, step string) {
	t.Helper()
	if got, want := f.usage.Used(), diskBytes(t, f.dir); got != want {
		t.Fatalf("%s: usage = %d, disk = %d", step, got, want)
	}
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestUsage_TracksDisk(t *testing.T) {
	t.Parallel()
	f := newUsageFixture(t, 0)
	ctx := t.Context()
	a := bytes.Repeat([]byte("a"), 1000)
	b := bytes.Repeat([]byte("b"), 300)

	da, _, err := f.blobs.Put(ctx, bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	f.check(t, "put")
	if f.usage.Used() != 1000 {
		t.Fatalf("usage after put = %d", f.usage.Used())
	}

	if _, _, err := f.blobs.Put(ctx, bytes.NewReader(a)); err != nil {
		t.Fatal(err)
	}
	f.check(t, "dedup put")

	if _, err := f.blobs.PutVerified(ctx, bytes.NewReader(b), digestOf(a)); !errors.Is(err, storage.ErrDigestMismatch) {
		t.Fatalf("mismatch err = %v", err)
	}
	f.check(t, "digest mismatch")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := f.blobs.Put(cancelled, bytes.NewReader(b)); err == nil {
		t.Fatal("cancelled put succeeded")
	}
	f.check(t, "cancelled put")

	// Chunked upload, including a chunk re-sent over already-written bytes.
	path, err := f.uploads.Create("u1")
	if err != nil {
		t.Fatal(err)
	}
	off, err := f.uploads.Append(ctx, "u1", 0, bytes.NewReader(b[:200]))
	if err != nil || off != 200 {
		t.Fatalf("append = %d, %v", off, err)
	}
	f.check(t, "append")
	if off, err = f.uploads.Append(ctx, "u1", 100, bytes.NewReader(b[100:])); err != nil || off != 300 {
		t.Fatalf("overlapping append = %d, %v", off, err)
	}
	f.check(t, "overlapping append")
	if err := f.blobs.PutAt(digestOf(b), path, off); err != nil {
		t.Fatal(err)
	}
	f.check(t, "put at")

	// Second session for the same content: PutAt dedups.
	path2, _ := f.uploads.Create("u2")
	if _, err := f.uploads.Append(ctx, "u2", 0, bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	if err := f.blobs.PutAt(digestOf(b), path2, int64(len(b))); err != nil {
		t.Fatal(err)
	}
	f.check(t, "dedup put at")

	// Abandoned session, then deleted.
	f.uploads.Create("u3")
	f.uploads.Append(ctx, "u3", 0, bytes.NewReader(b))
	f.check(t, "abandoned session")
	if err := f.uploads.Delete("u3"); err != nil {
		t.Fatal(err)
	}
	if err := f.uploads.Delete("u3"); err != nil {
		t.Fatal(err)
	}
	f.check(t, "upload delete (twice)")

	if err := f.blobs.Delete(da); err != nil {
		t.Fatal(err)
	}
	if err := f.blobs.Delete(da); !errors.Is(err, storage.ErrBlobNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	f.check(t, "blob delete")
	if f.usage.Used() != 300 {
		t.Fatalf("final usage = %d, want 300", f.usage.Used())
	}

	// A stray temp file (crash leftover) is measured by Init and released
	// by RemoveStaleTemp.
	stray := filepath.Join(f.dir, "blobs", "sha256", "upload-stray.tmp")
	if err := os.WriteFile(stray, make([]byte, 50), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(stray, old, old)
	if err := f.usage.Init(ctx, f.dir); err != nil {
		t.Fatal(err)
	}
	f.check(t, "init")
	if n, err := f.blobs.RemoveStaleTemp(ctx, time.Minute); err != nil || n != 1 {
		t.Fatalf("RemoveStaleTemp = %d, %v", n, err)
	}
	f.check(t, "remove stale temp")
}

func TestUsage_Quota(t *testing.T) {
	t.Parallel()
	f := newUsageFixture(t, 1000)
	ctx := t.Context()

	if _, _, err := f.blobs.Put(ctx, bytes.NewReader(make([]byte, 600))); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.blobs.Put(ctx, bytes.NewReader(bytes.Repeat([]byte("x"), 500)))
	if !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Fatalf("over-quota put err = %v", err)
	}
	f.check(t, "rejected put")

	f.uploads.Create("u")
	if _, err := f.uploads.Append(ctx, "u", 0, bytes.NewReader(make([]byte, 400))); err != nil {
		t.Fatalf("append to exactly the quota: %v", err)
	}
	if _, err := f.uploads.Append(ctx, "u", 400, bytes.NewReader([]byte{1})); !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Fatalf("append over quota err = %v", err)
	}
	// Overwriting counted bytes needs no new quota.
	if _, err := f.uploads.Append(ctx, "u", 0, bytes.NewReader(make([]byte, 400))); err != nil {
		t.Fatalf("overwrite at full quota: %v", err)
	}
	f.check(t, "full")

	f.uploads.Delete("u")
	if _, _, err := f.blobs.Put(ctx, bytes.NewReader(make([]byte, 7))); err != nil {
		t.Fatalf("put after freeing space: %v", err)
	}
	f.check(t, "freed")
}

// Concurrent writers racing for the last bytes of the quota never exceed it.
func TestUsage_ConcurrentWritersRespectQuota(t *testing.T) {
	t.Parallel()
	const limit, writers, size = 10_000, 50, 1_000
	f := newUsageFixture(t, limit)

	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			// Distinct content so nothing dedups.
			data := bytes.Repeat([]byte{byte(i)}, size)
			_, _, err := f.blobs.Put(t.Context(), bytes.NewReader(data))
			if err != nil && !errors.Is(err, storage.ErrQuotaExceeded) {
				t.Errorf("put: %v", err)
			}
		})
	}
	wg.Wait()
	if used := f.usage.Used(); used > limit {
		t.Fatalf("usage %d exceeds quota %d", used, limit)
	}
	f.check(t, "after race")
}

func TestUsage_InitOverQuotaStartsAndRejects(t *testing.T) {
	t.Parallel()
	f := newUsageFixture(t, 0)
	if _, _, err := f.blobs.Put(t.Context(), strings.NewReader("0123456789")); err != nil {
		t.Fatal(err)
	}

	u := storage.NewUsage(5)
	if err := u.Init(t.Context(), f.dir); err != nil {
		t.Fatalf("Init over quota: %v", err)
	}
	blobs, err := storage.NewFilesystemBlobStore(f.dir, storage.WithUsage(u))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := blobs.Put(t.Context(), strings.NewReader("y")); !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Fatalf("put while over quota = %v", err)
	}
}

func TestUsage_NilIsNoop(t *testing.T) {
	t.Parallel()
	var u *storage.Usage
	if u.Used() != 0 || u.Limit() != 0 {
		t.Error("nil usage reports values")
	}
	blobs, err := storage.NewFilesystemBlobStore(t.TempDir(), storage.WithUsage(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := blobs.Put(t.Context(), strings.NewReader("ok")); err != nil {
		t.Fatal(err)
	}
}

// Not parallel: it swaps the default logger.
func TestUsage_WarnsOnceNearQuota(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newUsageFixture(t, 100)
	ctx := t.Context()
	put := func(n int, b byte) string {
		t.Helper()
		d, _, err := f.blobs.Put(ctx, bytes.NewReader(bytes.Repeat([]byte{b}, n)))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	count := func() int { return strings.Count(logs.String(), "above 90% of the quota") }

	put(89, 'a')
	if count() != 0 {
		t.Fatalf("warned below 90%%: %s", logs.String())
	}
	d := put(5, 'b') // 94
	put(5, 'c')      // 99
	if count() != 1 {
		t.Fatalf("want exactly one warning, got %d: %s", count(), logs.String())
	}
	if err := f.blobs.Delete(d); err != nil { // 94: still above
		t.Fatal(err)
	}
	if err := f.blobs.Delete(digestOf(bytes.Repeat([]byte{'c'}, 5))); err != nil { // 89: re-arm
		t.Fatal(err)
	}
	put(5, 'd') // 94 again
	if count() != 2 {
		t.Fatalf("warning did not re-arm: %s", logs.String())
	}
}
