package services

import (
	"fmt"
	"log"

	"github.com/foundry/registry/internal/registry/storage"
)

// GCResult summarises what was removed.
type GCResult struct {
	ScannedBlobs  int
	DeletedBlobs  int
	FreedBytes    int64
	Errors        []string
}

// GarbageCollect runs a mark-and-sweep GC over the blob store.
//
//  1. Mark  – collect every digest referenced by any manifest in the DB.
//  2. Sweep – list every digest on disk, delete those not in the marked set.
func GarbageCollect(repo *RepositoryService, blobs *storage.FilesystemBlobStore) (*GCResult, error) {
	result := &GCResult{}

	// Phase 1: mark
	referenced, err := repo.ReferencedBlobDigests()
	if err != nil {
		return nil, fmt.Errorf("gc mark: %w", err)
	}
	// Also mark every digest that still has a DB row (in case manifest parsing missed some).
	dbDigests, err := repo.AllBlobDigests()
	if err != nil {
		return nil, fmt.Errorf("gc mark db: %w", err)
	}
	for _, d := range dbDigests {
		referenced[d] = true
	}

	log.Printf("gc: %d referenced blobs", len(referenced))

	// Phase 2: sweep
	onDisk, err := blobs.AllDigests()
	if err != nil {
		return nil, fmt.Errorf("gc sweep list: %w", err)
	}

	for _, digest := range onDisk {
		result.ScannedBlobs++
		if referenced[digest] {
			continue
		}
		// Get size before deleting for accounting.
		rc, size, err := blobs.Get(digest)
		if err == nil {
			rc.Close()
			result.FreedBytes += size
		}
		if err := blobs.Delete(digest); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("delete %s: %v", digest, err))
			continue
		}
		result.DeletedBlobs++
		log.Printf("gc: deleted blob %s", digest)
	}

	log.Printf("gc: complete – deleted %d blobs, freed %d bytes", result.DeletedBlobs, result.FreedBytes)
	return result, nil
}
