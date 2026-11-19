package handlers

import (
	"context"
	"io"

	"github.com/foundry/registry/internal/model"
	"github.com/foundry/registry/internal/registry/services"
)

// The interfaces below list only what each handler calls, so tests can use
// small fakes. *services.RepositoryService, *storage.FilesystemBlobStore,
// *storage.UploadStore and *services.Collector satisfy them in production.
//
// Implementations must return services.ErrNotFound / storage.ErrBlobNotFound
// (possibly wrapped) for missing data; any other error is reported as a 5xx.

// RegistryMetadata is the metadata the /v2 handler reads and writes.
type RegistryMetadata interface {
	EnsureRepository(ctx context.Context, name string) (*model.Repository, error)
	ListRepositoryNames(ctx context.Context, n int, last string) ([]string, error)

	SaveBlob(ctx context.Context, digest string, size int64) error
	BlobExists(ctx context.Context, digest string) (bool, error)
	DeleteBlob(ctx context.Context, digest string) error

	PutManifest(ctx context.Context, repoName, reference, mediaType string, content []byte) (string, error)
	GetManifest(ctx context.Context, repoName, reference string) (*model.Manifest, error)
	DeleteManifest(ctx context.Context, repoName, digest string) error
	ListTags(ctx context.Context, repoName string) ([]*model.Tag, error)
	DeleteTag(ctx context.Context, repoName, tag string) error

	SaveUpload(ctx context.Context, id, tempPath string, repoID, offset int64) error
	GetRepoUpload(ctx context.Context, repoName, id string) (*model.Upload, error)
	UpdateUploadOffset(ctx context.Context, id string, offset int64) error
	DeleteUpload(ctx context.Context, id string) error
}

// APIMetadata is the metadata the /api handler reads and writes.
type APIMetadata interface {
	ListRepositories(ctx context.Context) ([]*model.Repository, error)
	DeleteRepository(ctx context.Context, name string) error
	ListTags(ctx context.Context, repoName string) ([]*model.Tag, error)
	DeleteTag(ctx context.Context, repoName, tag string) error
	GetManifest(ctx context.Context, repoName, reference string) (*model.Manifest, error)
	DeleteManifest(ctx context.Context, repoName, digest string) error
}

// WebMetadata is the metadata the web UI reads.
type WebMetadata interface {
	ListRepositorySummaries(ctx context.Context) ([]services.RepositorySummary, error)
	ListTags(ctx context.Context, repoName string) ([]*model.Tag, error)
	GetManifest(ctx context.Context, repoName, reference string) (*model.Manifest, error)
}

// BlobStore is the content-addressed blob storage used by /v2.
type BlobStore interface {
	Get(digest string) (io.ReadSeekCloser, int64, error)
	Exists(digest string) (bool, error)
	Delete(digest string) error
	PutVerified(ctx context.Context, r io.Reader, expected string) (int64, error)
	PutAt(digest, tempPath string, expectedSize int64) error
}

// UploadStore holds in-progress chunked upload sessions.
type UploadStore interface {
	// Lock serialises operations on one session; call the result to unlock.
	Lock(id string) func()
	Create(id string) (string, error)
	Append(ctx context.Context, id string, offset int64, r io.Reader) (int64, error)
	Path(id string) string
	Delete(id string) error
}

// Collector runs garbage collection.
type Collector interface {
	Collect(ctx context.Context, opts services.GCOptions) (*services.GCResult, error)
}
