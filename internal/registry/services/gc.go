package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/foundry/registry/internal/model"
	"github.com/foundry/registry/internal/registry/storage"
)

// GCOptions tunes garbage collection.
type GCOptions struct {
	// GracePeriod protects blobs younger than this from collection. Clients
	// upload layers *before* pushing the manifest that references them, so a
	// GC running mid-push would otherwise delete fresh layers. Zero disables
	// the protection (useful in tests).
	GracePeriod time.Duration
	// UploadTTL expires upload sessions (and their temp files) older than
	// this. Zero disables upload cleanup.
	UploadTTL time.Duration
	// DryRun reports what would be deleted without deleting anything.
	DryRun bool
}

// GCResult summarises what was removed.
type GCResult struct {
	ScannedBlobs   int      `json:"scanned_blobs"`
	DeletedBlobs   int      `json:"deleted_blobs"`
	FreedBytes     int64    `json:"freed_bytes"`
	DeletedUploads int      `json:"deleted_uploads"`
	DryRun         bool     `json:"dry_run"`
	Errors         []string `json:"errors"`
}

// gcLock serialises GC against manifest writes. GC holds the write lock for
// its whole mark-and-sweep; PutManifest holds a read lock. Without it, a
// manifest committed after GC's mark phase could have the blobs it just
// referenced swept. It also ensures only one GC runs at a time.
var gcLock sync.RWMutex

// gcMetadata is the metadata GC reads and deletes.
type gcMetadata interface {
	ReferencedBlobDigests(ctx context.Context) (map[string]bool, error)
	AllBlobDigests(ctx context.Context) ([]string, error)
	GetBlob(ctx context.Context, digest string) (*model.Blob, error)
	DeleteBlob(ctx context.Context, digest string) error
	UploadIDsCreatedBefore(ctx context.Context, t time.Time) ([]string, error)
	DeleteUpload(ctx context.Context, id string) error
}

// gcBlobs is the blob storage GC sweeps.
type gcBlobs interface {
	AllDigests(ctx context.Context) ([]string, error)
	Stat(digest string) (storage.BlobInfo, error)
	Delete(digest string) error
	RemoveStaleTemp(ctx context.Context, maxAge time.Duration) (int, error)
}

// gcUploads holds the files of upload sessions GC expires.
type gcUploads interface {
	Delete(id string) error
}

// Collector runs garbage collection over one registry's metadata and stores.
type Collector struct {
	repo    gcMetadata
	blobs   gcBlobs
	uploads gcUploads // nil: upload files are left for the caller
}

// NewCollector returns a Collector. uploads may be nil, in which case expired
// upload sessions lose their DB row but not their file.
func NewCollector(repo gcMetadata, blobs gcBlobs, uploads *storage.UploadStore) *Collector {
	c := &Collector{repo: repo, blobs: blobs}
	// Assign only a non-nil pointer: a nil *UploadStore stored in the
	// interface would compare != nil and be called.
	if uploads != nil {
		c.uploads = uploads
	}
	return c
}

// Collect runs a mark-and-sweep GC over the blob store.
//
//  1. Mark  – collect every blob digest referenced by any manifest in any
//     repository.
//  2. Sweep – for every blob known on disk or in the DB that is unmarked and
//     older than the grace period, delete the file and its DB row.
//
// Untagged manifests are retained (and keep their blobs alive); delete them
// explicitly via the API to make their blobs collectable.
//
// GC stops at the next blob or upload once ctx is done and returns an error
// wrapping ctx.Err(). Work already done stays done; every step is safe to
// repeat, so the next run continues where this one stopped.
func (c *Collector) Collect(ctx context.Context, opts GCOptions) (*GCResult, error) {
	repo, blobs, uploads := c.repo, c.blobs, c.uploads
	gcLock.Lock()
	defer gcLock.Unlock()

	result := &GCResult{DryRun: opts.DryRun, Errors: []string{}}
	now := time.Now()
	cancelled := func() error {
		err := ctx.Err()
		if err != nil {
			slog.InfoContext(ctx, "gc cancelled", "deleted_blobs", result.DeletedBlobs,
				"freed_bytes", result.FreedBytes, "deleted_uploads", result.DeletedUploads)
			err = fmt.Errorf("gc cancelled: %w", err)
		}
		return err
	}
	if err := cancelled(); err != nil {
		return nil, err
	}

	// Phase 1: mark
	referenced, err := repo.ReferencedBlobDigests(ctx)
	if err != nil {
		return nil, fmt.Errorf("gc mark: %w", err)
	}
	slog.DebugContext(ctx, "gc mark done", "referenced_blobs", len(referenced))

	// Phase 2: sweep — candidates are the union of disk and DB.
	onDisk, err := blobs.AllDigests(ctx)
	if err != nil {
		return nil, fmt.Errorf("gc list disk: %w", err)
	}
	inDB, err := repo.AllBlobDigests(ctx)
	if err != nil {
		return nil, fmt.Errorf("gc list db: %w", err)
	}
	candidates := make(map[string]struct{}, len(onDisk)+len(inDB))
	for _, d := range onDisk {
		candidates[d] = struct{}{}
	}
	for _, d := range inDB {
		candidates[d] = struct{}{}
	}

	for digest := range candidates {
		if err := cancelled(); err != nil {
			return nil, err
		}
		result.ScannedBlobs++
		if referenced[digest] {
			continue
		}

		info, statErr := blobs.Stat(digest)
		onDiskNow := statErr == nil
		if statErr != nil && !errors.Is(statErr, storage.ErrBlobNotFound) {
			result.Errors = append(result.Errors, fmt.Sprintf("stat %s: %v", digest, statErr))
			continue
		}
		if onDiskNow && opts.GracePeriod > 0 && now.Sub(info.ModTime) < opts.GracePeriod {
			continue // too young — may belong to an in-flight push
		}
		if !onDiskNow && opts.GracePeriod > 0 {
			if b, err := repo.GetBlob(ctx, digest); err == nil && now.Sub(b.CreatedAt) < opts.GracePeriod {
				continue
			}
		}

		if opts.DryRun {
			result.DeletedBlobs++
			result.FreedBytes += info.Size
			continue
		}
		if onDiskNow {
			if err := blobs.Delete(digest); err != nil && !errors.Is(err, storage.ErrBlobNotFound) {
				result.Errors = append(result.Errors, fmt.Sprintf("delete %s: %v", digest, err))
				continue
			}
		}
		if err := repo.DeleteBlob(ctx, digest); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("delete db row %s: %v", digest, err))
			continue
		}
		result.DeletedBlobs++
		result.FreedBytes += info.Size
		slog.DebugContext(ctx, "gc deleted blob", "digest", digest, "size", info.Size)
	}

	if opts.UploadTTL > 0 && !opts.DryRun {
		ids, err := repo.UploadIDsCreatedBefore(ctx, now.Add(-opts.UploadTTL))
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("list stale uploads: %v", err))
		}
		for _, id := range ids {
			if err := cancelled(); err != nil {
				return nil, err
			}
			if err := repo.DeleteUpload(ctx, id); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("delete upload %s: %v", id, err))
				continue
			}
			if uploads != nil {
				if err := uploads.Delete(id); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("delete upload file %s: %v", id, err))
				}
			}
			result.DeletedUploads++
		}
		if _, err := blobs.RemoveStaleTemp(ctx, opts.UploadTTL); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("remove stale temp files: %v", err))
		}
	}

	slog.InfoContext(ctx, "gc complete", "scanned_blobs", result.ScannedBlobs, "deleted_blobs", result.DeletedBlobs,
		"freed_bytes", result.FreedBytes, "deleted_uploads", result.DeletedUploads,
		"errors", len(result.Errors), "dry_run", opts.DryRun)
	return result, nil
}
