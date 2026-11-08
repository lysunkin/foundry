package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/foundry/registry/internal/registry/services"
)

// seed pushes a manifest+tag so subsequent API tests have data to work with.
func seedManifest(t *testing.T, ts *testServer, repo, tag string) (digest string) {
	t.Helper()
	content := []byte(`{"schemaVersion":2,"mediaType":"` + ociManifestMediaType + `","config":{"digest":"sha256:` + strings.Repeat("a", 64) + `"},"layers":[]}`)
	d := services.ComputeDigest(content)
	ts.svc.SaveManifest(repo, d, ociManifestMediaType, content)
	ts.svc.SaveTag(repo, tag, d)
	return d
}

// ---------------------------------------------------------------------------
// GET /api/repos
// ---------------------------------------------------------------------------

func TestAPIListRepos_Empty(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(t, http.MethodGet, "/api/repos", nil, nil)
	mustStatus(t, rr, http.StatusOK)
	// Returns null or [] — both are valid empty JSON.
}

func TestAPIListRepos(t *testing.T) {
	ts := newTestServer(t)
	ts.svc.EnsureRepository("alpha")
	ts.svc.EnsureRepository("beta")

	rr := ts.do(t, http.MethodGet, "/api/repos", nil, nil)
	mustStatus(t, rr, http.StatusOK)

	var repos []struct {
		Name string `json:"name"`
	}
	decodeJSON(t, rr, &repos)
	if len(repos) != 2 {
		t.Errorf("expected 2 repos, got %d: %s", len(repos), rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GET /api/repos/{name}/tags
// ---------------------------------------------------------------------------

func TestAPIListTags(t *testing.T) {
	ts := newTestServer(t)
	seedManifest(t, ts, "myrepo", "latest")
	seedManifest(t, ts, "myrepo", "v1")

	rr := ts.do(t, http.MethodGet, "/api/repos/myrepo/tags", nil, nil)
	mustStatus(t, rr, http.StatusOK)

	var tags []struct {
		Tag string `json:"tag"`
	}
	decodeJSON(t, rr, &tags)
	if len(tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(tags))
	}
}

func TestAPIListTags_RepoNotFound(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(t, http.MethodGet, "/api/repos/ghost/tags", nil, nil)
	mustStatus(t, rr, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// GET /api/repos/{name}/manifests/{reference}
// ---------------------------------------------------------------------------

func TestAPIGetManifest(t *testing.T) {
	ts := newTestServer(t)
	d := seedManifest(t, ts, "repo", "latest")

	rr := ts.do(t, http.MethodGet, "/api/repos/repo/manifests/latest", nil, nil)
	mustStatus(t, rr, http.StatusOK)

	var resp struct {
		Digest    string          `json:"digest"`
		MediaType string          `json:"media_type"`
		Size      int64           `json:"size"`
		Content   json.RawMessage `json:"content"`
	}
	decodeJSON(t, rr, &resp)

	if resp.Digest != d {
		t.Errorf("digest = %s, want %s", resp.Digest, d)
	}
	if resp.MediaType != ociManifestMediaType {
		t.Errorf("media_type = %s, want %s", resp.MediaType, ociManifestMediaType)
	}
	if len(resp.Content) == 0 {
		t.Error("content should not be empty")
	}
}

func TestAPIGetManifest_NotFound(t *testing.T) {
	ts := newTestServer(t)
	ts.svc.EnsureRepository("repo")

	rr := ts.do(t, http.MethodGet, "/api/repos/repo/manifests/notexist", nil, nil)
	mustStatus(t, rr, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// DELETE /api/repos/{name}
// ---------------------------------------------------------------------------

func TestAPIDeleteRepo(t *testing.T) {
	ts := newTestServer(t)
	seedManifest(t, ts, "todelete", "v1")

	rr := ts.do(t, http.MethodDelete, "/api/repos/todelete", nil, nil)
	mustStatus(t, rr, http.StatusOK)

	var resp map[string]string
	decodeJSON(t, rr, &resp)
	if resp["status"] != "deleted" {
		t.Errorf("status = %s, want deleted", resp["status"])
	}

	rr = ts.do(t, http.MethodGet, "/api/repos/todelete/tags", nil, nil)
	mustStatus(t, rr, http.StatusNotFound)
}

func TestAPIDeleteRepo_NotFound(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(t, http.MethodDelete, "/api/repos/ghost", nil, nil)
	mustStatus(t, rr, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// DELETE /api/tags/{name}/{tag}
// ---------------------------------------------------------------------------

func TestAPIDeleteTag(t *testing.T) {
	ts := newTestServer(t)
	d := seedManifest(t, ts, "repo", "v1")
	_ = d

	rr := ts.do(t, http.MethodDelete, "/api/tags/repo/v1", nil, nil)
	mustStatus(t, rr, http.StatusOK)

	tags, _ := ts.svc.ListTags("repo")
	if len(tags) != 0 {
		t.Errorf("expected 0 tags, got %d", len(tags))
	}
}

func TestAPIDeleteTag_NotFound(t *testing.T) {
	ts := newTestServer(t)
	ts.svc.EnsureRepository("repo")

	rr := ts.do(t, http.MethodDelete, "/api/tags/repo/ghost", nil, nil)
	mustStatus(t, rr, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// DELETE /api/manifests/{name}/{digest}
// ---------------------------------------------------------------------------

func TestAPIDeleteManifest(t *testing.T) {
	ts := newTestServer(t)
	d := seedManifest(t, ts, "repo", "v1")

	rr := ts.do(t, http.MethodDelete, "/api/manifests/repo/"+d, nil, nil)
	mustStatus(t, rr, http.StatusOK)

	// Tag should also be gone (cascade).
	tags, _ := ts.svc.ListTags("repo")
	if len(tags) != 0 {
		t.Errorf("expected 0 tags after manifest delete, got %d", len(tags))
	}
}

func TestAPIDeleteManifest_NotFound(t *testing.T) {
	ts := newTestServer(t)
	ts.svc.EnsureRepository("repo")

	rr := ts.do(t, http.MethodDelete, "/api/manifests/repo/sha256:"+strings.Repeat("0", 64), nil, nil)
	mustStatus(t, rr, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// POST /api/gc
// ---------------------------------------------------------------------------

func TestAPIGC(t *testing.T) {
	ts := newTestServer(t)

	// Put an orphaned blob (no manifest in DB references it).
	orphanData := bytes.Repeat([]byte("z"), 256)
	ts.blobs.Put(bytes.NewReader(orphanData))

	rr := ts.do(t, http.MethodPost, "/api/gc", nil, nil)
	mustStatus(t, rr, http.StatusOK)

	var result struct {
		ScannedBlobs int      `json:"scanned_blobs"`
		DeletedBlobs int      `json:"deleted_blobs"`
		Errors       []string `json:"errors"`
	}
	decodeJSON(t, rr, &result)

	if result.ScannedBlobs < 1 {
		t.Errorf("ScannedBlobs = %d, want ≥ 1", result.ScannedBlobs)
	}
	if result.DeletedBlobs < 1 {
		t.Errorf("DeletedBlobs = %d, want ≥ 1 (orphan blob not collected)", result.DeletedBlobs)
	}
	if len(result.Errors) != 0 {
		t.Errorf("GC errors: %v", result.Errors)
	}
}
