package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Ping
// ---------------------------------------------------------------------------

func TestIntegration_Ping(t *testing.T) {
	rs := newRegistryServer(t)

	resp := rs.do(t, http.MethodGet, "/v2/", nil, nil)
	mustStatus(t, resp, http.StatusOK)

	if v := resp.Header.Get("Docker-Distribution-Api-Version"); v != "registry/2.0" {
		t.Errorf("Docker-Distribution-Api-Version = %q, want registry/2.0", v)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// ---------------------------------------------------------------------------
// Blob: monolithic push → HEAD → GET → DELETE
// ---------------------------------------------------------------------------

func TestIntegration_Blob_MonolithicRoundTrip(t *testing.T) {
	rs := newRegistryServer(t)
	data := []byte("monolithic layer data for integration test")
	repo := "integration-monolithic"
	d := sha256Digest(data)

	// HEAD — not present yet.
	resp := rs.do(t, http.MethodHead, fmt.Sprintf("/v2/%s/blobs/%s", repo, d), nil, nil)
	mustStatus(t, resp, http.StatusNotFound)

	// POST with ?digest — monolithic push.
	resp = rs.do(t, http.MethodPost,
		fmt.Sprintf("/v2/%s/blobs/uploads/?digest=%s", repo, d),
		bytes.NewReader(data),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	mustStatus(t, resp, http.StatusCreated)

	gotDigest := resp.Header.Get("Docker-Content-Digest")
	if gotDigest != d {
		t.Errorf("Docker-Content-Digest = %q, want %q", gotDigest, d)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Error("Location header missing after monolithic push")
	}

	// HEAD — present now.
	resp = rs.do(t, http.MethodHead, fmt.Sprintf("/v2/%s/blobs/%s", repo, d), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	if resp.Header.Get("Docker-Content-Digest") != d {
		t.Errorf("HEAD Docker-Content-Digest mismatch")
	}
	if resp.Header.Get("Content-Length") == "" {
		t.Error("HEAD missing Content-Length")
	}

	// GET — body matches.
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/blobs/%s", repo, d), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	body := readBody(t, resp)
	if !bytes.Equal(body, data) {
		t.Errorf("GET body mismatch: got %d bytes, want %d", len(body), len(data))
	}
	if resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", resp.Header.Get("Content-Type"))
	}

	// DELETE.
	resp = rs.do(t, http.MethodDelete, fmt.Sprintf("/v2/%s/blobs/%s", repo, d), nil, nil)
	mustStatus(t, resp, http.StatusAccepted)

	// HEAD — gone.
	resp = rs.do(t, http.MethodHead, fmt.Sprintf("/v2/%s/blobs/%s", repo, d), nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Blob: monolithic push with wrong digest is rejected
// ---------------------------------------------------------------------------

func TestIntegration_Blob_MonolithicDigestMismatch(t *testing.T) {
	rs := newRegistryServer(t)
	wrongDigest := sha256Digest([]byte("something else entirely"))

	resp := rs.do(t, http.MethodPost,
		fmt.Sprintf("/v2/repo/blobs/uploads/?digest=%s", wrongDigest),
		bytes.NewReader([]byte("actual content")),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	mustStatus(t, resp, http.StatusBadRequest)

	var errBody struct {
		Errors []struct{ Code string } `json:"errors"`
	}
	json.Unmarshal(readBody(t, resp), &errBody)
	if len(errBody.Errors) == 0 || errBody.Errors[0].Code != "DIGEST_INVALID" {
		t.Errorf("expected DIGEST_INVALID, got %s", errBody.Errors)
	}
}

// ---------------------------------------------------------------------------
// Blob: chunked upload (POST → PATCH × 2 → PUT)
// ---------------------------------------------------------------------------

func TestIntegration_Blob_ChunkedUpload(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "integration-chunked"

	chunk1 := bytes.Repeat([]byte("A"), 512)
	chunk2 := bytes.Repeat([]byte("B"), 512)
	allData := append(append([]byte{}, chunk1...), chunk2...)
	d := sha256Digest(allData)

	// POST — start session.
	resp := rs.do(t, http.MethodPost, fmt.Sprintf("/v2/%s/blobs/uploads/", repo), nil, nil)
	mustStatus(t, resp, http.StatusAccepted)

	loc := resp.Header.Get("Location")
	uid := resp.Header.Get("Docker-Upload-UUID")
	if loc == "" || uid == "" {
		t.Fatalf("Location=%q UUID=%q: missing headers after POST", loc, uid)
	}
	// Fresh session: Range header must be absent.
	if r := resp.Header.Get("Range"); r != "" {
		t.Errorf("Range should be absent on fresh session, got %q", r)
	}

	// PATCH chunk 1.
	resp = rs.do(t, http.MethodPatch, loc,
		bytes.NewReader(chunk1),
		map[string]string{
			"Content-Type":   "application/octet-stream",
			"Content-Range":  fmt.Sprintf("0-%d", len(chunk1)-1),
			"Content-Length": fmt.Sprintf("%d", len(chunk1)),
		},
	)
	mustStatus(t, resp, http.StatusAccepted)
	wantRange := fmt.Sprintf("0-%d", len(chunk1)-1)
	if r := resp.Header.Get("Range"); r != wantRange {
		t.Errorf("after chunk1 Range = %q, want %q", r, wantRange)
	}

	// PATCH chunk 2.
	resp = rs.do(t, http.MethodPatch, loc,
		bytes.NewReader(chunk2),
		map[string]string{
			"Content-Type":   "application/octet-stream",
			"Content-Range":  fmt.Sprintf("%d-%d", len(chunk1), len(allData)-1),
			"Content-Length": fmt.Sprintf("%d", len(chunk2)),
		},
	)
	mustStatus(t, resp, http.StatusAccepted)
	wantRange = fmt.Sprintf("0-%d", len(allData)-1)
	if r := resp.Header.Get("Range"); r != wantRange {
		t.Errorf("after chunk2 Range = %q, want %q", r, wantRange)
	}

	// PUT — finalise with digest.
	putURL := fmt.Sprintf("%s?digest=%s", loc, d)
	resp = rs.do(t, http.MethodPut, putURL, nil, map[string]string{"Content-Length": "0"})
	mustStatus(t, resp, http.StatusCreated)
	if resp.Header.Get("Docker-Content-Digest") != d {
		t.Errorf("PUT Docker-Content-Digest = %q, want %q", resp.Header.Get("Docker-Content-Digest"), d)
	}

	// GET — verify assembled content.
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/blobs/%s", repo, d), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	body := readBody(t, resp)
	if !bytes.Equal(body, allData) {
		t.Errorf("assembled blob size %d, want %d", len(body), len(allData))
	}
}

// ---------------------------------------------------------------------------
// Blob: chunked — PATCH with wrong offset is rejected
// ---------------------------------------------------------------------------

func TestIntegration_Blob_ChunkedWrongOffset(t *testing.T) {
	rs := newRegistryServer(t)

	resp := rs.do(t, http.MethodPost, "/v2/repo/blobs/uploads/", nil, nil)
	loc := resp.Header.Get("Location")

	// Send a PATCH claiming data starts at offset 100 on a fresh session.
	resp = rs.do(t, http.MethodPatch, loc,
		bytes.NewReader([]byte("data")),
		map[string]string{"Content-Range": "100-103"},
	)
	mustStatus(t, resp, http.StatusRequestedRangeNotSatisfiable)
}

// ---------------------------------------------------------------------------
// Blob: GET upload status
// ---------------------------------------------------------------------------

func TestIntegration_Blob_UploadStatus(t *testing.T) {
	rs := newRegistryServer(t)

	// Start upload.
	resp := rs.do(t, http.MethodPost, "/v2/myrepo/blobs/uploads/", nil, nil)
	loc := resp.Header.Get("Location")

	// GET status — offset 0.
	resp = rs.do(t, http.MethodGet, loc, nil, nil)
	mustStatus(t, resp, http.StatusNoContent)
	// Range must be absent when offset is 0.
	if r := resp.Header.Get("Range"); r != "" {
		t.Errorf("Range = %q on empty upload, want absent", r)
	}

	// PATCH some data.
	chunk := []byte("hello")
	resp = rs.do(t, http.MethodPatch, loc, bytes.NewReader(chunk),
		map[string]string{"Content-Length": fmt.Sprintf("%d", len(chunk))})
	mustStatus(t, resp, http.StatusAccepted)

	// GET status again — offset advances.
	resp = rs.do(t, http.MethodGet, loc, nil, nil)
	mustStatus(t, resp, http.StatusNoContent)
	wantRange := fmt.Sprintf("0-%d", len(chunk)-1)
	if r := resp.Header.Get("Range"); r != wantRange {
		t.Errorf("Range = %q, want %q", r, wantRange)
	}
}

// ---------------------------------------------------------------------------
// Blob: DELETE upload session
// ---------------------------------------------------------------------------

func TestIntegration_Blob_DeleteUpload(t *testing.T) {
	rs := newRegistryServer(t)

	resp := rs.do(t, http.MethodPost, "/v2/myrepo/blobs/uploads/", nil, nil)
	loc := resp.Header.Get("Location")

	resp = rs.do(t, http.MethodDelete, loc, nil, nil)
	mustStatus(t, resp, http.StatusNoContent)

	// Status request must now 404.
	resp = rs.do(t, http.MethodGet, loc, nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Manifest: push by tag → GET → HEAD → DELETE
// ---------------------------------------------------------------------------

func TestIntegration_Manifest_TagRoundTrip(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "integration-manifest-tag"

	// Push a config blob first.
	configData := []byte(`{"architecture":"arm64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	configDigest := pushBlob(t, rs, repo, configData)

	manifest := buildManifest(
		ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: configDigest, Size: len(configData)},
		[]ociDescriptor{},
	)
	expectedDigest := sha256Digest(manifest)

	// PUT by tag.
	gotDigest := pushManifest(t, rs, repo, "latest", mediaTypeOCIManifest, manifest)
	if gotDigest != expectedDigest {
		t.Errorf("pushed digest = %s, want %s", gotDigest, expectedDigest)
	}

	// HEAD by tag.
	resp := rs.do(t, http.MethodHead, fmt.Sprintf("/v2/%s/manifests/latest", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	if resp.Header.Get("Docker-Content-Digest") != expectedDigest {
		t.Errorf("HEAD digest mismatch")
	}
	if resp.Header.Get("Content-Type") != mediaTypeOCIManifest {
		t.Errorf("HEAD Content-Type = %q, want %q", resp.Header.Get("Content-Type"), mediaTypeOCIManifest)
	}
	// HEAD must return no body.
	body := readBody(t, resp)
	if len(body) != 0 {
		t.Errorf("HEAD body should be empty, got %d bytes", len(body))
	}

	// GET by tag.
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/manifests/latest", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	body = readBody(t, resp)
	if !bytes.Equal(body, manifest) {
		t.Errorf("GET manifest body mismatch")
	}
	if resp.Header.Get("Docker-Content-Digest") != expectedDigest {
		t.Errorf("GET digest header mismatch")
	}

	// DELETE by digest.
	resp = rs.do(t, http.MethodDelete, fmt.Sprintf("/v2/%s/manifests/%s", repo, expectedDigest), nil, nil)
	mustStatus(t, resp, http.StatusAccepted)

	// GET after delete must 404.
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/manifests/latest", repo), nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Manifest: push by digest (no tag)
// ---------------------------------------------------------------------------

func TestIntegration_Manifest_PushByDigest(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "integration-by-digest"

	configData := []byte(`{"architecture":"amd64"}`)
	configDigest := pushBlob(t, rs, repo, configData)
	manifest := buildManifest(
		ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: configDigest, Size: len(configData)},
		nil,
	)
	d := sha256Digest(manifest)

	// PUT by digest — no tag should be created.
	pushManifest(t, rs, repo, d, mediaTypeOCIManifest, manifest)

	// GET by digest succeeds.
	resp := rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/manifests/%s", repo, d), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)

	// Tags list must be empty (no tag was set).
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/tags/list", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	var tagResp struct {
		Tags []string `json:"tags"`
	}
	json.Unmarshal(readBody(t, resp), &tagResp)
	if len(tagResp.Tags) != 0 {
		t.Errorf("expected no tags after digest-only push, got %v", tagResp.Tags)
	}
}

// ---------------------------------------------------------------------------
// Manifest: tag update (push same tag twice with different content)
// ---------------------------------------------------------------------------

func TestIntegration_Manifest_TagUpdate(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "integration-tag-update"

	mkManifest := func(arch string) ([]byte, string) {
		config := []byte(`{"architecture":"` + arch + `"}`)
		configDigest := pushBlob(t, rs, repo, config)
		m := buildManifest(
			ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: configDigest, Size: len(config)},
			nil,
		)
		return m, sha256Digest(m)
	}

	m1, d1 := mkManifest("amd64")
	m2, d2 := mkManifest("arm64")

	pushManifest(t, rs, repo, "latest", mediaTypeOCIManifest, m1)
	pushManifest(t, rs, repo, "latest", mediaTypeOCIManifest, m2)

	// latest must now resolve to d2.
	resp := rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/manifests/latest", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	if resp.Header.Get("Docker-Content-Digest") != d2 {
		t.Errorf("latest = %s, want %s (d2)", resp.Header.Get("Docker-Content-Digest"), d2)
	}
	readBody(t, resp)

	// d1 manifest must still be retrievable by digest.
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/manifests/%s", repo, d1), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
}

// ---------------------------------------------------------------------------
// Tags list
// ---------------------------------------------------------------------------

func TestIntegration_Tags_List(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "integration-tags-list"

	configData := []byte(`{"arch":"amd64"}`)
	configDigest := pushBlob(t, rs, repo, configData)
	manifest := buildManifest(
		ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: configDigest, Size: len(configData)},
		nil,
	)

	for _, tag := range []string{"v1.0", "v1.1", "latest"} {
		pushManifest(t, rs, repo, tag, mediaTypeOCIManifest, manifest)
	}

	resp := rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/tags/list", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)

	var result struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	json.Unmarshal(readBody(t, resp), &result)

	if result.Name != repo {
		t.Errorf("name = %q, want %q", result.Name, repo)
	}
	if len(result.Tags) != 3 {
		t.Errorf("tags = %v, want 3 items", result.Tags)
	}
}

func TestIntegration_Tags_ListRepoNotFound(t *testing.T) {
	rs := newRegistryServer(t)
	resp := rs.do(t, http.MethodGet, "/v2/no-such-repo/tags/list", nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
}

// ---------------------------------------------------------------------------
// OCI Image Index push (Apple container CLI scenario)
// ---------------------------------------------------------------------------

func TestIntegration_OCIImageIndex_PushPull(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "integration-oci-index"

	// 1. Push the config blob.
	configData := []byte(`{"architecture":"arm64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	configDigest := pushBlob(t, rs, repo, configData)

	// 2. Push a layer blob.
	layerData := bytes.Repeat([]byte{0x1f, 0x8b}, 256) // fake gzip header
	layerDigest := pushBlob(t, rs, repo, layerData)

	// 3. Build and push the platform manifest by digest.
	platformManifest := buildManifest(
		ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: configDigest, Size: len(configData)},
		[]ociDescriptor{
			{MediaType: mediaTypeOCILayer, Digest: layerDigest, Size: len(layerData)},
		},
	)
	platformDigest := pushManifest(t, rs, repo, sha256Digest(platformManifest), mediaTypeOCIManifest, platformManifest)

	// 4. Build and push the OCI Image Index by tag.
	index := buildIndex([]ociDescriptor{
		{MediaType: mediaTypeOCIManifest, Digest: platformDigest, Size: len(platformManifest)},
	})
	indexDigest := pushManifest(t, rs, repo, "latest", mediaTypeOCIIndex, index)

	// 5. GET index by tag — must be the index media type.
	resp := rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/manifests/latest", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != mediaTypeOCIIndex {
		t.Errorf("Content-Type = %q, want %q", ct, mediaTypeOCIIndex)
	}
	body := readBody(t, resp)

	var gotIndex struct {
		Manifests []ociDescriptor `json:"manifests"`
	}
	if err := json.Unmarshal(body, &gotIndex); err != nil {
		t.Fatalf("unmarshal index: %v", err)
	}
	if len(gotIndex.Manifests) != 1 || gotIndex.Manifests[0].Digest != platformDigest {
		t.Errorf("index manifests = %+v, want platform digest %s", gotIndex.Manifests, platformDigest)
	}

	// 6. GET platform manifest by digest — must also be retrievable.
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/manifests/%s", repo, platformDigest), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)

	// 7. HEAD index by digest.
	resp = rs.do(t, http.MethodHead, fmt.Sprintf("/v2/%s/manifests/%s", repo, indexDigest), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
}

// ---------------------------------------------------------------------------
// Docker manifest v2 push
// ---------------------------------------------------------------------------

func TestIntegration_DockerManifestV2_Push(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "integration-docker-v2"

	configData := []byte(`{"architecture":"amd64","os":"linux"}`)
	configDigest := pushBlob(t, rs, repo, configData)

	layerData := bytes.Repeat([]byte("layer"), 100)
	layerDigest := pushBlob(t, rs, repo, layerData)

	manifest := []byte(fmt.Sprintf(`{
		"schemaVersion": 2,
		"mediaType": "%s",
		"config": {"mediaType":"application/vnd.docker.container.image.v1+json","digest":"%s","size":%d},
		"layers": [{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"%s","size":%d}]
	}`, mediaTypeManifestV2, configDigest, len(configData), layerDigest, len(layerData)))

	d := pushManifest(t, rs, repo, "v1", mediaTypeManifestV2, manifest)

	resp := rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/manifests/v1", repo), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	if resp.Header.Get("Docker-Content-Digest") != d {
		t.Errorf("digest mismatch on GET")
	}
	body := readBody(t, resp)
	if resp.Header.Get("Content-Type") != mediaTypeManifestV2 {
		t.Errorf("Content-Type = %q, want %q", resp.Header.Get("Content-Type"), mediaTypeManifestV2)
	}
	if len(body) == 0 {
		t.Error("manifest body is empty")
	}
}

// ---------------------------------------------------------------------------
// Error body format (OCI spec)
// ---------------------------------------------------------------------------

func TestIntegration_ErrorFormat_BlobUnknown(t *testing.T) {
	rs := newRegistryServer(t)
	missingDigest := sha256Digest([]byte("not stored"))

	resp := rs.do(t, http.MethodGet, fmt.Sprintf("/v2/repo/blobs/%s", missingDigest), nil, nil)
	mustStatus(t, resp, http.StatusNotFound)

	var body struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(readBody(t, resp), &body); err != nil {
		t.Fatalf("parse error body: %v", err)
	}
	if len(body.Errors) == 0 {
		t.Fatal("expected errors array, got empty")
	}
	if body.Errors[0].Code != "BLOB_UNKNOWN" {
		t.Errorf("code = %q, want BLOB_UNKNOWN", body.Errors[0].Code)
	}
	if body.Errors[0].Message == "" {
		t.Error("error message should not be empty")
	}
}

func TestIntegration_ErrorFormat_ManifestUnknown(t *testing.T) {
	rs := newRegistryServer(t)

	// Create the repo first so we get a proper MANIFEST_UNKNOWN (not NAME_UNKNOWN).
	rs.do(t, http.MethodPost, "/v2/myrepo/blobs/uploads/", nil, nil)

	resp := rs.do(t, http.MethodGet, "/v2/myrepo/manifests/notexist", nil, nil)
	mustStatus(t, resp, http.StatusNotFound)

	var body struct {
		Errors []struct{ Code string } `json:"errors"`
	}
	json.Unmarshal(readBody(t, resp), &body)
	if len(body.Errors) == 0 || body.Errors[0].Code != "MANIFEST_UNKNOWN" {
		t.Errorf("expected MANIFEST_UNKNOWN, got %+v", body.Errors)
	}
}

// ---------------------------------------------------------------------------
// Multiple repositories are isolated
// ---------------------------------------------------------------------------

func TestIntegration_MultiRepo_Isolation(t *testing.T) {
	rs := newRegistryServer(t)

	configA := []byte(`{"arch":"amd64"}`)
	configB := []byte(`{"arch":"arm64"}`)

	dA := pushBlob(t, rs, "repo-a", configA)
	dB := pushBlob(t, rs, "repo-b", configB)

	mA := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: dA, Size: len(configA)}, nil)
	mB := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: dB, Size: len(configB)}, nil)

	pushManifest(t, rs, "repo-a", "latest", mediaTypeOCIManifest, mA)
	pushManifest(t, rs, "repo-b", "latest", mediaTypeOCIManifest, mB)

	// repo-a:latest must return mA's digest.
	dExpA := sha256Digest(mA)
	resp := rs.do(t, http.MethodGet, "/v2/repo-a/manifests/latest", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	if resp.Header.Get("Docker-Content-Digest") != dExpA {
		t.Errorf("repo-a digest mismatch")
	}
	readBody(t, resp)

	// repo-b:latest must return mB's digest.
	dExpB := sha256Digest(mB)
	resp = rs.do(t, http.MethodGet, "/v2/repo-b/manifests/latest", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	if resp.Header.Get("Docker-Content-Digest") != dExpB {
		t.Errorf("repo-b digest mismatch")
	}
	readBody(t, resp)
}

// ---------------------------------------------------------------------------
// Static asset serving
// ---------------------------------------------------------------------------

func TestIntegration_StaticAssets(t *testing.T) {
	rs := newRegistryServer(t)

	resp := rs.do(t, http.MethodGet, "/static/css/main.css", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	body := readBody(t, resp)
	if len(body) == 0 {
		t.Error("main.css should not be empty")
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
}

// ---------------------------------------------------------------------------
// Web UI renders HTML
// ---------------------------------------------------------------------------

func TestIntegration_WebUI_Dashboard(t *testing.T) {
	rs := newRegistryServer(t)

	resp := rs.do(t, http.MethodGet, "/", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	body := readBody(t, resp)

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !bytes.Contains(body, []byte("Foundry")) {
		t.Errorf("dashboard HTML should contain 'Foundry'")
	}
}

func TestIntegration_WebUI_RepoPage(t *testing.T) {
	rs := newRegistryServer(t)
	repo := "myapp"

	configData := []byte(`{"arch":"amd64"}`)
	configDigest := pushBlob(t, rs, repo, configData)
	manifest := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: configDigest, Size: len(configData)}, nil)
	pushManifest(t, rs, repo, "v1", mediaTypeOCIManifest, manifest)

	resp := rs.do(t, http.MethodGet, "/ui/repo/"+repo, nil, nil)
	mustStatus(t, resp, http.StatusOK)
	body := readBody(t, resp)

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if len(body) == 0 {
		t.Error("repo page body should not be empty")
	}
}
