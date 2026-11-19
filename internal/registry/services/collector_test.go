package services_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/foundry/registry/internal/db"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
)

// Expired upload sessions are removed with or without an upload store. A nil
// *storage.UploadStore must behave like no store at all: before Collector,
// GC compared the pointer to nil; a typed nil inside an interface would not
// compare equal and would be dereferenced.
func TestCollector_ExpiresUploads(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		withStore bool
	}{
		{"nil upload store", false},
		{"with upload store", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "gc.db"))
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			t.Cleanup(func() { database.Close() })
			dataDir := t.TempDir()
			blobs, err := storage.NewFilesystemBlobStore(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			svc := services.NewRepositoryService(database)

			var uploads *storage.UploadStore // typed nil unless withStore
			path := filepath.Join(dataDir, "unused.upload")
			if tc.withStore {
				if uploads, err = storage.NewUploadStore(dataDir); err != nil {
					t.Fatal(err)
				}
				if path, err = uploads.Create("old"); err != nil {
					t.Fatal(err)
				}
			}
			repo, err := svc.EnsureRepository(t.Context(), "repo")
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.SaveUpload(t.Context(), "old", path, repo.ID, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(t.Context(), `UPDATE uploads SET created_at = '2000-01-01T00:00:00Z'`); err != nil {
				t.Fatal(err)
			}

			res, err := services.NewCollector(svc, blobs, uploads).Collect(t.Context(), services.GCOptions{UploadTTL: time.Hour})
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			if res.DeletedUploads != 1 || len(res.Errors) != 0 {
				t.Errorf("result = %+v, want 1 deleted upload and no errors", res)
			}
			if _, err := svc.GetUpload(t.Context(), "old"); !errors.Is(err, services.ErrNotFound) {
				t.Errorf("upload row still present (err %v)", err)
			}
			if tc.withStore {
				if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("upload file still present (err %v)", err)
				}
			}
		})
	}
}
