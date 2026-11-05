package services_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/foundry/registry/internal/db"
	"github.com/foundry/registry/internal/registry/services"
)

// newService opens a fresh in-process SQLite DB with migrations applied
// and returns a RepositoryService wired to it.
func newService(t *testing.T) *services.RepositoryService {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return services.NewRepositoryService(database)
}

// ---------------------------------------------------------------------------
// Repositories
// ---------------------------------------------------------------------------

func TestRepository_EnsureAndGet(t *testing.T) {
	svc := newService(t)

	repo, err := svc.EnsureRepository("myapp")
	if err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	if repo.Name != "myapp" {
		t.Errorf("Name = %q, want %q", repo.Name, "myapp")
	}
	if repo.ID == 0 {
		t.Error("ID should not be zero")
	}
}

func TestRepository_EnsureIdempotent(t *testing.T) {
	svc := newService(t)

	r1, _ := svc.EnsureRepository("app")
	r2, err := svc.EnsureRepository("app")
	if err != nil {
		t.Fatalf("second EnsureRepository: %v", err)
	}
	if r1.ID != r2.ID {
		t.Errorf("IDs differ: %d vs %d", r1.ID, r2.ID)
	}
}

func TestRepository_GetNotFound(t *testing.T) {
	svc := newService(t)

	_, err := svc.GetRepository("nonexistent")
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRepository_List(t *testing.T) {
	svc := newService(t)

	names := []string{"zebra", "alpha", "middle"}
	for _, n := range names {
		svc.EnsureRepository(n)
	}

	repos, err := svc.ListRepositories()
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(repos))
	}
	// Should be ordered by name.
	want := []string{"alpha", "middle", "zebra"}
	for i, r := range repos {
		if r.Name != want[i] {
			t.Errorf("repos[%d].Name = %q, want %q", i, r.Name, want[i])
		}
	}
}

func TestRepository_ListEmpty(t *testing.T) {
	svc := newService(t)
	repos, err := svc.ListRepositories()
	if err != nil {
		t.Fatal(err)
	}
	if repos != nil && len(repos) != 0 {
		t.Errorf("expected nil or empty, got %v", repos)
	}
}

func TestRepository_Delete(t *testing.T) {
	svc := newService(t)

	// Create repo with a manifest and tag.
	svc.EnsureRepository("todelete")
	digest := services.ComputeDigest([]byte(`{"schemaVersion":2}`))
	svc.SaveManifest("todelete", digest, "application/vnd.oci.image.manifest.v1+json", []byte(`{"schemaVersion":2}`))
	svc.SaveTag("todelete", "v1", digest)

	if err := svc.DeleteRepository("todelete"); err != nil {
		t.Fatalf("DeleteRepository: %v", err)
	}

	_, err := svc.GetRepository("todelete")
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestRepository_DeleteNotFound(t *testing.T) {
	svc := newService(t)
	err := svc.DeleteRepository("ghost")
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Blobs
// ---------------------------------------------------------------------------

func TestBlob_SaveAndGet(t *testing.T) {
	svc := newService(t)
	digest := "sha256:" + strings.Repeat("ab", 32)

	if err := svc.SaveBlob(digest, 1234); err != nil {
		t.Fatalf("SaveBlob: %v", err)
	}

	blob, err := svc.GetBlob(digest)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if blob.Digest != digest {
		t.Errorf("Digest = %s, want %s", blob.Digest, digest)
	}
	if blob.Size != 1234 {
		t.Errorf("Size = %d, want %d", blob.Size, 1234)
	}
}

func TestBlob_SaveIdempotent(t *testing.T) {
	svc := newService(t)
	digest := "sha256:" + strings.Repeat("cd", 32)

	// INSERT OR IGNORE — second call must not error.
	if err := svc.SaveBlob(digest, 100); err != nil {
		t.Fatalf("first SaveBlob: %v", err)
	}
	if err := svc.SaveBlob(digest, 200); err != nil {
		t.Fatalf("second SaveBlob: %v", err)
	}

	// Original size is preserved (INSERT OR IGNORE).
	blob, _ := svc.GetBlob(digest)
	if blob.Size != 100 {
		t.Errorf("size after duplicate save = %d, want 100", blob.Size)
	}
}

func TestBlob_GetNotFound(t *testing.T) {
	svc := newService(t)
	_, err := svc.GetBlob("sha256:" + strings.Repeat("00", 32))
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestBlob_Exists(t *testing.T) {
	svc := newService(t)
	digest := "sha256:" + strings.Repeat("dd", 32)

	ok, _ := svc.BlobExists(digest)
	if ok {
		t.Error("should not exist before save")
	}
	svc.SaveBlob(digest, 5)
	ok, _ = svc.BlobExists(digest)
	if !ok {
		t.Error("should exist after save")
	}
}

func TestBlob_Delete(t *testing.T) {
	svc := newService(t)
	digest := "sha256:" + strings.Repeat("ee", 32)
	svc.SaveBlob(digest, 10)

	if err := svc.DeleteBlob(digest); err != nil {
		t.Fatalf("DeleteBlob: %v", err)
	}
	ok, _ := svc.BlobExists(digest)
	if ok {
		t.Error("blob should not exist after delete")
	}
}

func TestBlob_AllDigests(t *testing.T) {
	svc := newService(t)
	for _, d := range []string{"sha256:" + strings.Repeat("11", 32), "sha256:" + strings.Repeat("22", 32)} {
		svc.SaveBlob(d, 1)
	}

	all, err := svc.AllBlobDigests()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 digests, got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// Manifests
// ---------------------------------------------------------------------------

const testMediaType = "application/vnd.oci.image.manifest.v1+json"

func testManifestContent() []byte {
	return []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config1"},"layers":[]}`)
}

func TestManifest_SaveAndGetByDigest(t *testing.T) {
	svc := newService(t)
	content := testManifestContent()
	digest := services.ComputeDigest(content)

	if err := svc.SaveManifest("myrepo", digest, testMediaType, content); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	m, err := svc.GetManifest("myrepo", digest)
	if err != nil {
		t.Fatalf("GetManifest by digest: %v", err)
	}
	if m.Digest != digest {
		t.Errorf("Digest = %s, want %s", m.Digest, digest)
	}
	if m.MediaType != testMediaType {
		t.Errorf("MediaType = %s, want %s", m.MediaType, testMediaType)
	}
	if string(m.Content) != string(content) {
		t.Errorf("Content mismatch")
	}
}

func TestManifest_SaveCreatesRepoAutomatically(t *testing.T) {
	svc := newService(t)
	content := testManifestContent()
	digest := services.ComputeDigest(content)

	// EnsureRepository is NOT called explicitly — SaveManifest must do it.
	if err := svc.SaveManifest("newrepo", digest, testMediaType, content); err != nil {
		t.Fatalf("SaveManifest on new repo: %v", err)
	}

	repo, err := svc.GetRepository("newrepo")
	if err != nil {
		t.Fatalf("repo not created: %v", err)
	}
	if repo.Name != "newrepo" {
		t.Errorf("Name = %s", repo.Name)
	}
}

func TestManifest_GetByTag(t *testing.T) {
	svc := newService(t)
	content := testManifestContent()
	digest := services.ComputeDigest(content)

	svc.SaveManifest("tagrepo", digest, testMediaType, content)
	svc.SaveTag("tagrepo", "latest", digest)

	m, err := svc.GetManifest("tagrepo", "latest")
	if err != nil {
		t.Fatalf("GetManifest by tag: %v", err)
	}
	if m.Digest != digest {
		t.Errorf("Digest via tag = %s, want %s", m.Digest, digest)
	}
}

func TestManifest_GetNotFound(t *testing.T) {
	svc := newService(t)
	svc.EnsureRepository("empty")

	_, err := svc.GetManifest("empty", "sha256:"+strings.Repeat("00", 32))
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound by digest, got %v", err)
	}

	_, err = svc.GetManifest("empty", "nonexistent-tag")
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound by tag, got %v", err)
	}
}

func TestManifest_GetRepoNotFound(t *testing.T) {
	svc := newService(t)
	_, err := svc.GetManifest("ghost", "latest")
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound for missing repo, got %v", err)
	}
}

func TestManifest_Delete(t *testing.T) {
	svc := newService(t)
	content := testManifestContent()
	digest := services.ComputeDigest(content)

	svc.SaveManifest("repo", digest, testMediaType, content)
	svc.SaveTag("repo", "v1", digest)

	if err := svc.DeleteManifest("repo", digest); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}

	_, err := svc.GetManifest("repo", digest)
	if err != services.ErrNotFound {
		t.Errorf("manifest should be gone, got %v", err)
	}
	// Tag should also be gone.
	tags, _ := svc.ListTags("repo")
	if len(tags) != 0 {
		t.Errorf("tags should be gone after manifest delete, got %d", len(tags))
	}
}

func TestManifest_Overwrite(t *testing.T) {
	svc := newService(t)
	content1 := []byte(`{"schemaVersion":2,"v":1}`)
	content2 := []byte(`{"schemaVersion":2,"v":2}`)
	d1 := services.ComputeDigest(content1)
	d2 := services.ComputeDigest(content2)

	svc.SaveManifest("repo", d1, testMediaType, content1)
	svc.SaveManifest("repo", d2, testMediaType, content2)

	digests, _ := svc.AllManifestDigests()
	if len(digests) != 2 {
		t.Errorf("expected 2 manifests, got %d", len(digests))
	}
}

// ---------------------------------------------------------------------------
// Tags
// ---------------------------------------------------------------------------

func TestTag_SaveAndList(t *testing.T) {
	svc := newService(t)
	content := testManifestContent()
	digest := services.ComputeDigest(content)
	svc.SaveManifest("repo", digest, testMediaType, content)

	for _, tag := range []string{"v2", "latest", "v1"} {
		if err := svc.SaveTag("repo", tag, digest); err != nil {
			t.Fatalf("SaveTag %s: %v", tag, err)
		}
	}

	tags, err := svc.ListTags("repo")
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 3 {
		t.Fatalf("expected 3 tags, got %d", len(tags))
	}
	// Ordered by tag name.
	want := []string{"latest", "v1", "v2"}
	for i, tag := range tags {
		if tag.Tag != want[i] {
			t.Errorf("tags[%d].Tag = %s, want %s", i, tag.Tag, want[i])
		}
		if tag.RepositoryName != "repo" {
			t.Errorf("RepositoryName = %s, want repo", tag.RepositoryName)
		}
	}
}

func TestTag_Upsert(t *testing.T) {
	svc := newService(t)
	c1 := []byte(`{"v":1}`)
	c2 := []byte(`{"v":2}`)
	d1 := services.ComputeDigest(c1)
	d2 := services.ComputeDigest(c2)
	svc.SaveManifest("repo", d1, testMediaType, c1)
	svc.SaveManifest("repo", d2, testMediaType, c2)

	svc.SaveTag("repo", "latest", d1)
	svc.SaveTag("repo", "latest", d2) // upsert

	tags, _ := svc.ListTags("repo")
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].ManifestDigest != d2 {
		t.Errorf("tag should point to d2, got %s", tags[0].ManifestDigest)
	}
}

func TestTag_ListRepoNotFound(t *testing.T) {
	svc := newService(t)
	_, err := svc.ListTags("ghost")
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestTag_Delete(t *testing.T) {
	svc := newService(t)
	content := testManifestContent()
	digest := services.ComputeDigest(content)
	svc.SaveManifest("repo", digest, testMediaType, content)
	svc.SaveTag("repo", "v1", digest)

	if err := svc.DeleteTag("repo", "v1"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}

	tags, _ := svc.ListTags("repo")
	if len(tags) != 0 {
		t.Errorf("expected 0 tags after delete, got %d", len(tags))
	}
}

func TestTag_DeleteNotFound(t *testing.T) {
	svc := newService(t)
	svc.EnsureRepository("repo")

	err := svc.DeleteTag("repo", "ghost")
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Uploads
// ---------------------------------------------------------------------------

func TestUpload_SaveAndGet(t *testing.T) {
	svc := newService(t)
	repo, _ := svc.EnsureRepository("repo")

	if err := svc.SaveUpload("uuid-1", "/tmp/upload-uuid-1.upload", repo.ID, 0); err != nil {
		t.Fatalf("SaveUpload: %v", err)
	}

	u, err := svc.GetUpload("uuid-1")
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if u.ID != "uuid-1" {
		t.Errorf("ID = %s, want uuid-1", u.ID)
	}
	if u.Offset != 0 {
		t.Errorf("Offset = %d, want 0", u.Offset)
	}
	if u.TempPath != "/tmp/upload-uuid-1.upload" {
		t.Errorf("TempPath = %s", u.TempPath)
	}
}

func TestUpload_UpdateOffset(t *testing.T) {
	svc := newService(t)
	repo, _ := svc.EnsureRepository("repo")
	svc.SaveUpload("uid", "/tmp/uid.upload", repo.ID, 0)

	if err := svc.UpdateUploadOffset("uid", 4096); err != nil {
		t.Fatalf("UpdateUploadOffset: %v", err)
	}

	u, _ := svc.GetUpload("uid")
	if u.Offset != 4096 {
		t.Errorf("Offset = %d, want 4096", u.Offset)
	}
}

func TestUpload_GetNotFound(t *testing.T) {
	svc := newService(t)
	_, err := svc.GetUpload("no-such-id")
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpload_Delete(t *testing.T) {
	svc := newService(t)
	repo, _ := svc.EnsureRepository("repo")
	svc.SaveUpload("del-uid", "/tmp/del.upload", repo.ID, 0)

	if err := svc.DeleteUpload("del-uid"); err != nil {
		t.Fatalf("DeleteUpload: %v", err)
	}

	_, err := svc.GetUpload("del-uid")
	if err != services.ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ReferencedBlobDigests
// ---------------------------------------------------------------------------

func TestReferencedBlobDigests(t *testing.T) {
	svc := newService(t)

	manifest := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"digest": "sha256:configaaa"},
		"layers": [
			{"digest": "sha256:layer111"},
			{"digest": "sha256:layer222"}
		]
	}`)
	digest := services.ComputeDigest(manifest)
	svc.SaveManifest("repo", digest, testMediaType, manifest)

	referenced, err := svc.ReferencedBlobDigests()
	if err != nil {
		t.Fatalf("ReferencedBlobDigests: %v", err)
	}

	for _, want := range []string{"sha256:configaaa", "sha256:layer111", "sha256:layer222"} {
		if !referenced[want] {
			t.Errorf("expected %s in referenced set", want)
		}
	}
}
