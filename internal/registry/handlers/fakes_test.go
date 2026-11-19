package handlers_test

import (
	"context"
	"io"

	"github.com/foundry/registry/internal/model"
	"github.com/foundry/registry/internal/registry/handlers"
	"github.com/foundry/registry/internal/registry/services"
)

// fakeMeta implements every handler metadata interface. Each method returns
// err, so a test can make the metadata layer fail as a whole.
type fakeMeta struct{ err error }

var (
	_ handlers.RegistryMetadata = fakeMeta{}
	_ handlers.APIMetadata      = fakeMeta{}
	_ handlers.WebMetadata      = fakeMeta{}
)

func (f fakeMeta) EnsureRepository(context.Context, string) (*model.Repository, error) {
	return nil, f.err
}

func (f fakeMeta) ListRepositoryNames(context.Context, int, string) ([]string, error) {
	return nil, f.err
}

func (f fakeMeta) ListRepositories(context.Context) ([]*model.Repository, error) {
	return nil, f.err
}

func (f fakeMeta) ListRepositorySummaries(context.Context) ([]services.RepositorySummary, error) {
	return nil, f.err
}
func (f fakeMeta) DeleteRepository(context.Context, string) error   { return f.err }
func (f fakeMeta) SaveBlob(context.Context, string, int64) error    { return f.err }
func (f fakeMeta) BlobExists(context.Context, string) (bool, error) { return false, f.err }
func (f fakeMeta) DeleteBlob(context.Context, string) error         { return f.err }

func (f fakeMeta) PutManifest(context.Context, string, string, string, []byte) (string, error) {
	return "", f.err
}

func (f fakeMeta) GetManifest(context.Context, string, string) (*model.Manifest, error) {
	return nil, f.err
}
func (f fakeMeta) DeleteManifest(context.Context, string, string) error { return f.err }
func (f fakeMeta) ListTags(context.Context, string) ([]*model.Tag, error) {
	return nil, f.err
}
func (f fakeMeta) DeleteTag(context.Context, string, string) error { return f.err }

func (f fakeMeta) SaveUpload(context.Context, string, string, int64, int64) error { return f.err }

func (f fakeMeta) GetRepoUpload(context.Context, string, string) (*model.Upload, error) {
	return nil, f.err
}
func (f fakeMeta) UpdateUploadOffset(context.Context, string, int64) error { return f.err }
func (f fakeMeta) DeleteUpload(context.Context, string) error              { return f.err }

// fakeBlobs is a BlobStore whose every operation fails with err.
type fakeBlobs struct{ err error }

var _ handlers.BlobStore = fakeBlobs{}

func (f fakeBlobs) Get(string) (io.ReadSeekCloser, int64, error) { return nil, 0, f.err }
func (f fakeBlobs) Exists(string) (bool, error)                  { return false, f.err }
func (f fakeBlobs) Delete(string) error                          { return f.err }
func (f fakeBlobs) PutVerified(context.Context, io.Reader, string) (int64, error) {
	return 0, f.err
}
func (f fakeBlobs) PutAt(string, string, int64) error { return f.err }

// fakeUploads is an UploadStore whose every operation fails with err.
type fakeUploads struct{ err error }

var _ handlers.UploadStore = fakeUploads{}

func (f fakeUploads) Lock(string) func()            { return func() {} }
func (f fakeUploads) Create(string) (string, error) { return "", f.err }
func (f fakeUploads) Append(context.Context, string, int64, io.Reader) (int64, error) {
	return 0, f.err
}
func (f fakeUploads) Path(string) string  { return "" }
func (f fakeUploads) Delete(string) error { return f.err }

// fakeCollector returns result/err and records the options it was given.
type fakeCollector struct {
	result *services.GCResult
	err    error
	got    *services.GCOptions
}

var _ handlers.Collector = (*fakeCollector)(nil)

func (f *fakeCollector) Collect(_ context.Context, opts services.GCOptions) (*services.GCResult, error) {
	f.got = &opts
	return f.result, f.err
}

// gcResult is a typical successful GC result.
func gcResult(dryRun bool) *services.GCResult {
	return &services.GCResult{ScannedBlobs: 3, DeletedBlobs: 1, FreedBytes: 42, DryRun: dryRun, Errors: []string{}}
}
