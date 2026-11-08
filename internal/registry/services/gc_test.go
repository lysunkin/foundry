package services_test

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/foundry/registry/internal/db"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
)

func newGCFixtures(t *testing.T) (*services.RepositoryService, *storage.FilesystemBlobStore) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "gc.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	blobDir := t.TempDir()
	blobs, err := storage.NewFilesystemBlobStore(blobDir)
	if err != nil {
		t.Fatalf("NewFilesystemBlobStore: %v", err)
	}

	svc := services.NewRepositoryService(database)
	return svc, blobs
}

func putBlob(t *testing.T, blobs *storage.FilesystemBlobStore, data []byte) string {
	t.Helper()
	digest, _, err := blobs.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	return digest
}

func TestGC_DeletesUnreferencedBlobs(t *testing.T) {
	svc, blobs := newGCFixtures(t)

	// Create two blobs on disk.
	referenced := putBlob(t, blobs, []byte("referenced layer"))
	orphan := putBlob(t, blobs, []byte("orphaned layer"))

	// Save the referenced blob in the DB.
	svc.SaveBlob(referenced, int64(len("referenced layer")))

	// Build a manifest that references only the first blob.
	manifestJSON := []byte(`{
		"schemaVersion":2,
		"mediaType":"application/vnd.oci.image.manifest.v1+json",
		"config":{"digest":"` + referenced + `"},
		"layers":[]
	}`)
	digest := services.ComputeDigest(manifestJSON)
	svc.SaveManifest("repo", digest, "application/vnd.oci.image.manifest.v1+json", manifestJSON)

	result, err := services.GarbageCollect(svc, blobs, nil, services.GCOptions{})
	if err != nil {
		t.Fatalf("GarbageCollect: %v", err)
	}

	if result.ScannedBlobs != 2 {
		t.Errorf("ScannedBlobs = %d, want 2", result.ScannedBlobs)
	}
	if result.DeletedBlobs != 1 {
		t.Errorf("DeletedBlobs = %d, want 1", result.DeletedBlobs)
	}

	// Orphan should be gone.
	ok, _ := blobs.Exists(orphan)
	if ok {
		t.Error("orphaned blob should have been deleted")
	}
	// Referenced blob should still exist.
	ok, _ = blobs.Exists(referenced)
	if !ok {
		t.Error("referenced blob should not have been deleted")
	}
}

func TestGC_NoOrphans(t *testing.T) {
	svc, blobs := newGCFixtures(t)

	data := []byte("clean blob")
	d := putBlob(t, blobs, data)
	svc.SaveBlob(d, int64(len(data)))
	m := []byte(`{"schemaVersion":2,"config":{"digest":"` + d + `"},"layers":[]}`)
	svc.SaveManifest("repo", services.ComputeDigest(m), "application/vnd.oci.image.manifest.v1+json", m)

	result, err := services.GarbageCollect(svc, blobs, nil, services.GCOptions{})
	if err != nil {
		t.Fatalf("GarbageCollect: %v", err)
	}
	if result.DeletedBlobs != 0 {
		t.Errorf("expected 0 deletions, got %d", result.DeletedBlobs)
	}
	if result.ScannedBlobs != 1 {
		t.Errorf("expected 1 scanned blob, got %d", result.ScannedBlobs)
	}
}

func TestGC_EmptyStore(t *testing.T) {
	svc, blobs := newGCFixtures(t)

	result, err := services.GarbageCollect(svc, blobs, nil, services.GCOptions{})
	if err != nil {
		t.Fatalf("GarbageCollect on empty store: %v", err)
	}
	if result.ScannedBlobs != 0 {
		t.Errorf("ScannedBlobs = %d, want 0", result.ScannedBlobs)
	}
	if result.DeletedBlobs != 0 {
		t.Errorf("DeletedBlobs = %d, want 0", result.DeletedBlobs)
	}
	if len(result.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", result.Errors)
	}
}

func TestGC_FreedBytesAccounting(t *testing.T) {
	svc, blobs := newGCFixtures(t)

	smallBlob := bytes.Repeat([]byte("x"), 100)
	largeBlob := bytes.Repeat([]byte("y"), 512)

	// Both orphans — no DB rows, no manifests referencing them.
	putBlob(t, blobs, smallBlob)
	putBlob(t, blobs, largeBlob)

	result, err := services.GarbageCollect(svc, blobs, nil, services.GCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedBlobs != 2 {
		t.Errorf("DeletedBlobs = %d, want 2", result.DeletedBlobs)
	}
	if result.FreedBytes != int64(len(smallBlob)+len(largeBlob)) {
		t.Errorf("FreedBytes = %d, want %d", result.FreedBytes, len(smallBlob)+len(largeBlob))
	}
}

func TestGC_ManifestReferencedBlobProtected(t *testing.T) {
	svc, blobs := newGCFixtures(t)

	configData := []byte(`{"architecture":"arm64"}`)
	configDigest := putBlob(t, blobs, configData)

	// Note: configDigest blob is NOT in the blobs DB table — only referenced by manifest JSON.
	manifestJSON := []byte(`{
		"schemaVersion":2,
		"mediaType":"application/vnd.oci.image.manifest.v1+json",
		"config":{"digest":"` + configDigest + `"},
		"layers":[]
	}`)
	md := services.ComputeDigest(manifestJSON)
	svc.SaveManifest("repo", md, "application/vnd.oci.image.manifest.v1+json", manifestJSON)

	result, err := services.GarbageCollect(svc, blobs, nil, services.GCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// config blob is referenced in manifest JSON — GC should protect it.
	if result.DeletedBlobs != 0 {
		t.Errorf("DeletedBlobs = %d, want 0 (manifest-referenced blob protected)", result.DeletedBlobs)
	}
}

func TestGC_GracePeriodProtectsFreshBlobs(t *testing.T) {
	svc, blobs := newGCFixtures(t)
	d := putBlob(t, blobs, []byte("fresh, not yet referenced"))
	svc.SaveBlob(d, 25)

	res, err := services.GarbageCollect(svc, blobs, nil, services.GCOptions{GracePeriod: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedBlobs != 0 {
		t.Errorf("fresh blob deleted despite grace period")
	}
	if ok, _ := blobs.Exists(d); !ok {
		t.Error("fresh blob missing")
	}
}

func TestGC_DryRunDeletesNothing(t *testing.T) {
	svc, blobs := newGCFixtures(t)
	d := putBlob(t, blobs, []byte("orphan"))

	res, err := services.GarbageCollect(svc, blobs, nil, services.GCOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedBlobs != 1 || !res.DryRun {
		t.Errorf("dry run result = %+v", res)
	}
	if ok, _ := blobs.Exists(d); !ok {
		t.Error("dry run deleted the blob")
	}
}

func TestGC_RemovesDBRowOfCollectedBlob(t *testing.T) {
	svc, blobs := newGCFixtures(t)
	d := putBlob(t, blobs, []byte("pushed then abandoned"))
	svc.SaveBlob(d, 21)

	if _, err := services.GarbageCollect(svc, blobs, nil, services.GCOptions{}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := svc.BlobExists(d); ok {
		t.Error("blob DB row should be removed with the file")
	}
}
