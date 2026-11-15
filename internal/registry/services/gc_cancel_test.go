package services_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
)

// countingContext reports cancellation from its limit-th Err() call on
// (limit < 0: never), and counts calls. It lets a test cancel GC part-way
// through deterministically, without hooks in production code.
type countingContext struct {
	context.Context
	mu    sync.Mutex
	calls int
	limit int
	done  chan struct{}
}

func newCountingContext(t *testing.T, limit int) *countingContext {
	return &countingContext{Context: t.Context(), limit: limit, done: make(chan struct{})}
}

func (c *countingContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.limit >= 0 && c.calls >= c.limit {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
		return context.Canceled
	}
	return nil
}

func (c *countingContext) Done() <-chan struct{} { return c.done }

// gcFixture stores n unreferenced blobs, each with a DB row.
func gcFixture(t *testing.T, n int) (*services.RepositoryService, *storage.FilesystemBlobStore, []string) {
	t.Helper()
	svc, blobs := newGCFixtures(t)
	digests := make([]string, n)
	for i := range n {
		data := fmt.Appendf(nil, "orphan blob %d", i)
		digests[i] = putBlob(t, blobs, data)
		if err := svc.SaveBlob(t.Context(), digests[i], int64(len(data))); err != nil {
			t.Fatal(err)
		}
	}
	return svc, blobs, digests
}

func TestGC_CancelledBeforeStartDeletesNothing(t *testing.T) {
	svc, blobs, digests := gcFixture(t, 3)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := services.GarbageCollect(ctx, svc, blobs, nil, services.GCOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if result != nil {
		t.Errorf("result = %+v, want nil", result)
	}
	for _, d := range digests {
		if ok, _ := blobs.Exists(d); !ok {
			t.Errorf("blob %s deleted by cancelled GC", d)
		}
		if ok, _ := svc.BlobExists(t.Context(), d); !ok {
			t.Errorf("blob row %s deleted by cancelled GC", d)
		}
	}
}

// A GC cancelled part-way stops early and never leaves a blob file without
// its DB row (a row without file is allowed: the next GC removes it).
func TestGC_CancelledMidSweepStopsAndStaysConsistent(t *testing.T) {
	const n = 20

	// Count context checks in a complete run over an identical fixture.
	svc, blobs, _ := gcFixture(t, n)
	full := newCountingContext(t, -1)
	if _, err := services.GarbageCollect(full, svc, blobs, nil, services.GCOptions{}); err != nil {
		t.Fatalf("uncancelled GC: %v", err)
	}

	svc, blobs, digests := gcFixture(t, n)
	// The sweep is the tail of the run (uploads are skipped: UploadTTL is 0)
	// and checks the context at least once per blob, so cancelling n/2 checks
	// before the end lands inside the sweep. Earlier checks (mark phase,
	// directory walk) depend on implementation details and are not assumed.
	ctx := newCountingContext(t, full.calls-n/2)
	_, err := services.GarbageCollect(ctx, svc, blobs, nil, services.GCOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	deleted := 0
	for _, d := range digests {
		file, _ := blobs.Exists(d)
		row, rowErr := svc.BlobExists(t.Context(), d)
		if rowErr != nil {
			t.Fatal(rowErr)
		}
		if file && !row {
			t.Errorf("blob %s: file kept but row deleted", d)
		}
		if !file {
			deleted++
		}
	}
	if deleted == 0 || deleted == n {
		t.Errorf("deleted %d of %d blobs, want a partial sweep", deleted, n)
	}

	// The next GC finishes the job.
	if _, err := services.GarbageCollect(t.Context(), svc, blobs, nil, services.GCOptions{}); err != nil {
		t.Fatalf("follow-up GC: %v", err)
	}
	for _, d := range digests {
		file, _ := blobs.Exists(d)
		row, _ := svc.BlobExists(t.Context(), d)
		if file || row {
			t.Errorf("blob %s survived follow-up GC (file=%v row=%v)", d, file, row)
		}
	}
}

// D7: a cancelled PutManifest writes nothing (no repository, manifest or tag).
func TestPutManifest_CancelledWritesNothing(t *testing.T) {
	svc, blobs := newGCFixtures(t)
	cfg := []byte(`{}`)
	cfgDigest := putBlob(t, blobs, cfg)
	if err := svc.SaveBlob(t.Context(), cfgDigest, int64(len(cfg))); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",` +
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + cfgDigest + `","size":2},"layers":[]}`)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := svc.PutManifest(ctx, "cancelled/repo", "v1", "application/vnd.oci.image.manifest.v1+json", manifest)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PutManifest err = %v, want context.Canceled", err)
	}
	if _, err := svc.GetRepository(t.Context(), "cancelled/repo"); !errors.Is(err, services.ErrNotFound) {
		t.Errorf("GetRepository err = %v, want ErrNotFound", err)
	}
}
