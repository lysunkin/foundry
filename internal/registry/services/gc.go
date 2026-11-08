package services

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

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

// GarbageCollect runs a mark-and-sweep GC over the blob store.
//
//  1. Mark  – collect every blob digest referenced by any manifest in any
//     repository.
//  2. Sweep – for every blob known on disk or in the DB that is unmarked and
//     older than the grace period, delete the file and its DB row.
//
// Untagged manifests are retained (and keep their blobs alive); delete them
// explicitly via the API to make their blobs collectable.
//
// uploads may be nil, in which case stale upload temp files are not removed.
func GarbageCollect(repo *RepositoryService, blobs *storage.FilesystemBlobStore, uploads *storage.UploadStore, opts GCOptions) (*GCResult, error) {
	gcLock.Lock()
	defer gcLock.Unlock()

	result := &GCResult{DryRun: opts.DryRun, Errors: []string{}}
	now := time.Now()

	// Phase 1: mark
	referenced, err := repo.ReferencedBlobDigests()
	if err != nil {
		return nil, fmt.Errorf("gc mark: %w", err)
	}
	log.Printf("gc: %d referenced blobs", len(referenced))

	// Phase 2: sweep — candidates are the union of disk and DB.
	onDisk, err := blobs.AllDigests()
	if err != nil {
		return nil, fmt.Errorf("gc list disk: %w", err)
	}
	inDB, err := repo.AllBlobDigests()
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
			if b, err := repo.GetBlob(digest); err == nil && now.Sub(b.CreatedAt) < opts.GracePeriod {
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
		if err := repo.DeleteBlob(digest); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("delete db row %s: %v", digest, err))
			continue
		}
		result.DeletedBlobs++
		result.FreedBytes += info.Size
		log.Printf("gc: deleted blob %s", digest)
	}

	if opts.UploadTTL > 0 && !opts.DryRun {
		ids, err := repo.UploadIDsCreatedBefore(now.Add(-opts.UploadTTL))
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("list stale uploads: %v", err))
		}
		for _, id := range ids {
			if err := repo.DeleteUpload(id); err != nil {
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
		if _, err := blobs.RemoveStaleTemp(opts.UploadTTL); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("remove stale temp files: %v", err))
		}
	}

	log.Printf("gc: complete – deleted %d blobs (%d bytes), %d stale uploads, dry_run=%v",
		result.DeletedBlobs, result.FreedBytes, result.DeletedUploads, opts.DryRun)
	return result, nil
}
