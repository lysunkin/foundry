package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ---------------------------------------------------------------------------
// GET /api/repos
// ---------------------------------------------------------------------------

func TestIntegration_API_ListRepos(t *testing.T) {
	rs := newRegistryServer(t)

	// Empty initially.
	resp := rs.do(t, http.MethodGet, "/api/repos", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)

	// Push to two repos.
	for _, repo := range []string{"alpha", "beta"} {
		config := []byte(`{"arch":"amd64"}`)
		d := pushBlob(t, rs, repo, config)
		m := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: d, Size: len(config)}, nil)
		pushManifest(t, rs, repo, "latest", mediaTypeOCIManifest, m)
	}

	resp = rs.do(t, http.MethodGet, "/api/repos", nil, nil)
	mustStatus(t, resp, http.StatusOK)

	var repos []struct {
		Name string `json:"name"`
	}
	json.Unmarshal(readBody(t, resp), &repos)

	if len(repos) != 2 {
		t.Errorf("expected 2 repos, got %d", len(repos))
	}
	// Must be sorted by name.
	if repos[0].Name != "alpha" || repos[1].Name != "beta" {
		t.Errorf("repos not sorted: %+v", repos)
	}
}

// ---------------------------------------------------------------------------
// GET /api/repos/{name}/tags
// ---------------------------------------------------------------------------

func TestIntegration_API_ListTags(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "api-tags-repo"

	config := []byte(`{"arch":"amd64"}`)
	d := pushBlob(t, rs, repo, config)
	m := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: d, Size: len(config)}, nil)

	for _, tag := range []string{"v1", "v2", "latest"} {
		pushManifest(t, rs, repo, tag, mediaTypeOCIManifest, m)
	}

	resp := rs.do(t, http.MethodGet, fmt.Sprintf("/api/repos/%s/tags", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)

	var tags []struct {
		Tag            string `json:"tag"`
		ManifestDigest string `json:"manifest_digest"`
	}
	json.Unmarshal(readBody(t, resp), &tags)

	if len(tags) != 3 {
		t.Errorf("expected 3 tags, got %d: %+v", len(tags), tags)
	}
	for _, tag := range tags {
		if tag.ManifestDigest == "" {
			t.Errorf("tag %q has empty manifest_digest", tag.Tag)
		}
	}
}

func TestIntegration_API_ListTags_NotFound(t *testing.T) {
	rs := newRegistryServer(t)
	resp := rs.do(t, http.MethodGet, "/api/repos/nonexistent/tags", nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
}

// ---------------------------------------------------------------------------
// GET /api/repos/{name}/manifests/{reference}
// ---------------------------------------------------------------------------

func TestIntegration_API_GetManifest(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "api-manifest-repo"

	config := []byte(`{"arch":"arm64"}`)
	configDigest := pushBlob(t, rs, repo, config)
	manifest := buildManifest(
		ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: configDigest, Size: len(config)},
		nil,
	)
	d := pushManifest(t, rs, repo, "latest", mediaTypeOCIManifest, manifest)

	resp := rs.do(t, http.MethodGet, fmt.Sprintf("/api/repos/%s/manifests/latest", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)

	var result struct {
		Digest    string          `json:"digest"`
		MediaType string          `json:"media_type"`
		Size      int64           `json:"size"`
		Content   json.RawMessage `json:"content"`
	}
	json.Unmarshal(readBody(t, resp), &result)

	if result.Digest != d {
		t.Errorf("digest = %q, want %q", result.Digest, d)
	}
	if result.MediaType != mediaTypeOCIManifest {
		t.Errorf("media_type = %q, want %q", result.MediaType, mediaTypeOCIManifest)
	}
	if result.Size != int64(len(manifest)) {
		t.Errorf("size = %d, want %d", result.Size, len(manifest))
	}
	if len(result.Content) == 0 {
		t.Error("content should not be empty")
	}
	// Content must be valid JSON.
	var raw map[string]interface{}
	if err := json.Unmarshal(result.Content, &raw); err != nil {
		t.Errorf("content is not valid JSON: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DELETE /api/repos/{name}
// ---------------------------------------------------------------------------

func TestIntegration_API_DeleteRepo(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "delete-me-repo"

	config := []byte(`{"arch":"amd64"}`)
	d := pushBlob(t, rs, repo, config)
	m := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: d, Size: len(config)}, nil)
	pushManifest(t, rs, repo, "latest", mediaTypeOCIManifest, m)

	// Confirm it exists.
	resp := rs.do(t, http.MethodGet, fmt.Sprintf("/api/repos/%s/tags", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)

	// Delete the repo.
	resp = rs.do(t, http.MethodDelete, fmt.Sprintf("/api/repos/%s", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	var status map[string]string
	json.Unmarshal(readBody(t, resp), &status)
	if status["status"] != "deleted" {
		t.Errorf("status = %q, want deleted", status["status"])
	}

	// Tags endpoint must 404 now.
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/api/repos/%s/tags", repo), nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
}

func TestIntegration_API_DeleteRepo_NotFound(t *testing.T) {
	rs := newRegistryServer(t)
	resp := rs.do(t, http.MethodDelete, "/api/repos/ghost", nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
}

// ---------------------------------------------------------------------------
// DELETE /api/tags/{name}/{tag}
// ---------------------------------------------------------------------------

func TestIntegration_API_DeleteTag(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "tag-delete-repo"

	config := []byte(`{"arch":"amd64"}`)
	configDigest := pushBlob(t, rs, repo, config)
	manifest := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: configDigest, Size: len(config)}, nil)
	pushManifest(t, rs, repo, "v1", mediaTypeOCIManifest, manifest)
	pushManifest(t, rs, repo, "latest", mediaTypeOCIManifest, manifest)

	// Delete just "v1".
	resp := rs.do(t, http.MethodDelete, fmt.Sprintf("/api/tags/%s/v1", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)

	// v1 tag must be gone from the registry API too.
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/tags/list", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	var tagResp struct{ Tags []string }
	json.Unmarshal(readBody(t, resp), &tagResp)
	for _, tag := range tagResp.Tags {
		if tag == "v1" {
			t.Error("v1 should have been deleted")
		}
	}
	// "latest" must still be there.
	found := false
	for _, tag := range tagResp.Tags {
		if tag == "latest" {
			found = true
		}
	}
	if !found {
		t.Error("latest tag should still exist")
	}
}

func TestIntegration_API_DeleteTag_NotFound(t *testing.T) {
	rs := newRegistryServer(t)

	// Create the repo so the error is TAG-not-found, not repo-not-found.
	config := []byte(`{"arch":"amd64"}`)
	d := pushBlob(t, rs, "repo", config)
	m := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: d, Size: len(config)}, nil)
	pushManifest(t, rs, "repo", "existing", mediaTypeOCIManifest, m)

	resp := rs.do(t, http.MethodDelete, "/api/tags/repo/ghost", nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
}

// ---------------------------------------------------------------------------
// DELETE /api/manifests/{name}/{digest}
// ---------------------------------------------------------------------------

func TestIntegration_API_DeleteManifest(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "manifest-delete-repo"

	config := []byte(`{"arch":"amd64"}`)
	configDigest := pushBlob(t, rs, repo, config)
	manifest := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: configDigest, Size: len(config)}, nil)
	d := pushManifest(t, rs, repo, "latest", mediaTypeOCIManifest, manifest)

	resp := rs.do(t, http.MethodDelete, fmt.Sprintf("/api/manifests/%s/%s", repo, d), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)

	// Manifest must be gone from the registry API.
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/manifests/latest", repo), nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
}

func TestIntegration_API_DeleteManifest_NotFound(t *testing.T) {
	rs := newRegistryServer(t)

	config := []byte(`{"arch":"amd64"}`)
	d := pushBlob(t, rs, "repo", config)
	m := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: d, Size: len(config)}, nil)
	pushManifest(t, rs, "repo", "v1", mediaTypeOCIManifest, m)

	fakeDigest := sha256Digest([]byte("not stored"))
	resp := rs.do(t, http.MethodDelete, fmt.Sprintf("/api/manifests/repo/%s", fakeDigest), nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
}

// ---------------------------------------------------------------------------
// POST /api/gc
// ---------------------------------------------------------------------------

func TestIntegration_API_GC(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "gc-test-repo"

	// Push a blob that will remain referenced.
	config := []byte(`{"arch":"amd64"}`)
	configDigest := pushBlob(t, rs, repo, config)
	manifest := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: configDigest, Size: len(config)}, nil)
	pushManifest(t, rs, repo, "latest", mediaTypeOCIManifest, manifest)

	// Push an orphan blob via the monolithic API, then delete the only manifest
	// that would reference it — but we never created such a manifest, so it's
	// immediately an orphan (it's stored on disk but not in the DB blobs table
	// and not referenced by any manifest).
	orphan := bytes.Repeat([]byte("orphaned"), 64)
	orphanDigest := sha256Digest(orphan)

	orphanResp := rs.do(t, http.MethodPost,
		fmt.Sprintf("/v2/%s/blobs/uploads/?digest=%s", repo, orphanDigest),
		bytes.NewReader(orphan),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	mustStatus(t, orphanResp, http.StatusCreated)
	readBody(t, orphanResp) // drain

	// Build and push a manifest referencing the orphan blob, then delete it.
	orphanManifest := buildManifest(
		ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: orphanDigest, Size: len(orphan)},
		nil,
	)
	orphanManifestDigest := pushManifest(t, rs, repo, "orphan-tag", mediaTypeOCIManifest, orphanManifest)

	// Delete the manifest — now orphanDigest blob has no manifest in DB.
	delResp := rs.do(t, http.MethodDelete,
		fmt.Sprintf("/api/manifests/%s/%s", repo, orphanManifestDigest),
		nil, nil)
	mustStatus(t, delResp, http.StatusOK)
	readBody(t, delResp)

	// The orphan blob now has no referencing manifest, so GC must collect it
	// (from disk and from the DB) while keeping the still-referenced config.
	resp := rs.do(t, http.MethodPost, "/api/gc", nil, nil)
	mustStatus(t, resp, http.StatusOK)

	var result struct {
		ScannedBlobs int      `json:"scanned_blobs"`
		DeletedBlobs int      `json:"deleted_blobs"`
		FreedBytes   int64    `json:"freed_bytes"`
		Errors       []string `json:"errors"`
	}
	json.Unmarshal(readBody(t, resp), &result)

	if result.ScannedBlobs != 2 {
		t.Errorf("ScannedBlobs = %d, want 2", result.ScannedBlobs)
	}
	if result.DeletedBlobs != 1 {
		t.Errorf("DeletedBlobs = %d, want 1", result.DeletedBlobs)
	}
	if result.FreedBytes != int64(len(orphan)) {
		t.Errorf("FreedBytes = %d, want %d", result.FreedBytes, len(orphan))
	}
	if len(result.Errors) != 0 {
		t.Errorf("GC errors: %v", result.Errors)
	}

	// Orphan is gone; referenced config is still served.
	resp = rs.do(t, http.MethodHead, fmt.Sprintf("/v2/%s/blobs/%s", repo, orphanDigest), nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
	resp = rs.do(t, http.MethodHead, fmt.Sprintf("/v2/%s/blobs/%s", repo, configDigest), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
}
