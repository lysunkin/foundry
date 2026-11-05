package handlers_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func digest(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// Ping
// ---------------------------------------------------------------------------

func TestPing(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(t, http.MethodGet, "/v2/", nil, nil)

	mustStatus(t, rr, http.StatusOK)
	mustHeader(t, rr, "Docker-Distribution-Api-Version", "registry/2.0")
	mustHeader(t, rr, "Content-Type", "application/json")
}

// ---------------------------------------------------------------------------
// Blobs
// ---------------------------------------------------------------------------

func TestBlob_HeadNotFound(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(t, http.MethodHead, "/v2/myrepo/blobs/sha256:"+strings.Repeat("0", 64), nil, nil)
	mustStatus(t, rr, http.StatusNotFound)
}

func TestBlob_HeadFound(t *testing.T) {
	ts := newTestServer(t)
	data := []byte("test blob content")
	d := digest(data)

	ts.svc.SaveBlob(d, int64(len(data)))

	rr := ts.do(t, http.MethodHead, "/v2/myrepo/blobs/"+d, nil, nil)
	mustStatus(t, rr, http.StatusOK)
	mustHeader(t, rr, "Docker-Content-Digest", d)
	if cl := rr.Header().Get("Content-Length"); cl != fmt.Sprintf("%d", len(data)) {
		t.Errorf("Content-Length = %s, want %d", cl, len(data))
	}
}

func TestBlob_GetNotFound(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(t, http.MethodGet, "/v2/myrepo/blobs/sha256:"+strings.Repeat("0", 64), nil, nil)
	mustStatus(t, rr, http.StatusNotFound)

	var errResp struct {
		Errors []struct{ Code string } `json:"errors"`
	}
	decodeJSON(t, rr, &errResp)
	if len(errResp.Errors) == 0 || errResp.Errors[0].Code != "BLOB_UNKNOWN" {
		t.Errorf("expected BLOB_UNKNOWN error, got %s", rr.Body.String())
	}
}

func TestBlob_GetFound(t *testing.T) {
	ts := newTestServer(t)
	data := []byte("downloadable blob")
	d, _, _ := ts.blobs.Put(bytes.NewReader(data))
	ts.svc.SaveBlob(d, int64(len(data)))

	rr := ts.do(t, http.MethodGet, "/v2/myrepo/blobs/"+d, nil, nil)
	mustStatus(t, rr, http.StatusOK)
	mustHeader(t, rr, "Docker-Content-Digest", d)
	mustHeader(t, rr, "Content-Type", "application/octet-stream")

	if !bytes.Equal(rr.Body.Bytes(), data) {
		t.Error("blob body mismatch")
	}
}

func TestBlob_Delete(t *testing.T) {
	ts := newTestServer(t)
	data := []byte("delete me")
	d, _, _ := ts.blobs.Put(bytes.NewReader(data))
	ts.svc.SaveBlob(d, int64(len(data)))

	rr := ts.do(t, http.MethodDelete, "/v2/myrepo/blobs/"+d, nil, nil)
	mustStatus(t, rr, http.StatusAccepted)

	// Blob should be gone from filesystem.
	ok, _ := ts.blobs.Exists(d)
	if ok {
		t.Error("blob should be gone after DELETE")
	}
}

// ---------------------------------------------------------------------------
// Uploads — monolithic
// ---------------------------------------------------------------------------

func TestUpload_MonolithicPush(t *testing.T) {
	ts := newTestServer(t)
	data := []byte("monolithic blob data")
	d := digest(data)

	rr := ts.do(t, http.MethodPost,
		"/v2/myrepo/blobs/uploads/?digest="+d,
		bytes.NewReader(data),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	mustStatus(t, rr, http.StatusCreated)
	mustHeader(t, rr, "Docker-Content-Digest", d)

	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Error("Location header missing")
	}

	// Blob must be stored.
	ok, _ := ts.blobs.Exists(d)
	if !ok {
		t.Error("blob should exist after monolithic push")
	}
}

func TestUpload_MonolithicPush_DigestMismatch(t *testing.T) {
	ts := newTestServer(t)
	data := []byte("real content")
	wrongDigest := digest([]byte("different content"))

	rr := ts.do(t, http.MethodPost,
		"/v2/myrepo/blobs/uploads/?digest="+wrongDigest,
		bytes.NewReader(data),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	mustStatus(t, rr, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Uploads — chunked (POST → PATCH → PUT)
// ---------------------------------------------------------------------------

func TestUpload_ChunkedFlow(t *testing.T) {
	ts := newTestServer(t)

	// POST — start session
	rr := ts.do(t, http.MethodPost, "/v2/myrepo/blobs/uploads/", nil, nil)
	mustStatus(t, rr, http.StatusAccepted)

	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Fatal("Location header missing after POST")
	}
	uid := rr.Header().Get("Docker-Upload-UUID")
	if uid == "" {
		t.Fatal("Docker-Upload-UUID missing after POST")
	}

	// Split data into two chunks.
	chunk1 := []byte("hello ")
	chunk2 := []byte("world")
	allData := append(chunk1, chunk2...)
	d := digest(allData)

	// PATCH chunk 1
	rr = ts.do(t, http.MethodPatch, loc,
		bytes.NewReader(chunk1),
		map[string]string{
			"Content-Type":   "application/octet-stream",
			"Content-Length": fmt.Sprintf("%d", len(chunk1)),
		},
	)
	mustStatus(t, rr, http.StatusAccepted)

	rangeHdr := rr.Header().Get("Range")
	wantRange := fmt.Sprintf("0-%d", len(chunk1)-1)
	if rangeHdr != wantRange {
		t.Errorf("Range = %q after chunk1, want %q", rangeHdr, wantRange)
	}

	// PATCH chunk 2
	rr = ts.do(t, http.MethodPatch, loc,
		bytes.NewReader(chunk2),
		map[string]string{
			"Content-Type":   "application/octet-stream",
			"Content-Range":  fmt.Sprintf("%d-%d", len(chunk1), len(allData)-1),
			"Content-Length": fmt.Sprintf("%d", len(chunk2)),
		},
	)
	mustStatus(t, rr, http.StatusAccepted)

	// PUT — finalize
	putURL := fmt.Sprintf("%s?digest=%s", loc, d)
	rr = ts.do(t, http.MethodPut, putURL, nil, map[string]string{"Content-Length": "0"})
	mustStatus(t, rr, http.StatusCreated)
	mustHeader(t, rr, "Docker-Content-Digest", d)

	// Verify blob was stored correctly.
	rc, _, err := ts.blobs.Get(d)
	if err != nil {
		t.Fatalf("Get after chunked upload: %v", err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	buf.ReadFrom(rc)
	if !bytes.Equal(buf.Bytes(), allData) {
		t.Errorf("stored blob = %q, want %q", buf.Bytes(), allData)
	}
}

func TestUpload_StartSession_Headers(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(t, http.MethodPost, "/v2/myrepo/blobs/uploads/", nil, nil)
	mustStatus(t, rr, http.StatusAccepted)

	if rr.Header().Get("Location") == "" {
		t.Error("Location header missing")
	}
	if rr.Header().Get("Docker-Upload-UUID") == "" {
		t.Error("Docker-Upload-UUID header missing")
	}
	// Range must NOT be present for a fresh session.
	if r := rr.Header().Get("Range"); r != "" {
		t.Errorf("Range should be absent for fresh session, got %q", r)
	}
}

func TestUpload_Patch_InvalidRange(t *testing.T) {
	ts := newTestServer(t)

	// Start session.
	rr := ts.do(t, http.MethodPost, "/v2/myrepo/blobs/uploads/", nil, nil)
	loc := rr.Header().Get("Location")

	// PATCH with wrong Content-Range start offset.
	rr = ts.do(t, http.MethodPatch, loc,
		bytes.NewReader([]byte("data")),
		map[string]string{"Content-Range": "10-13"},
	)
	mustStatus(t, rr, http.StatusRequestedRangeNotSatisfiable)
}

func TestUpload_GetStatus_NotFound(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(t, http.MethodGet, "/v2/myrepo/blobs/uploads/no-such-uuid", nil, nil)
	mustStatus(t, rr, http.StatusNotFound)
}

func TestUpload_Put_NoDigest(t *testing.T) {
	ts := newTestServer(t)

	rr := ts.do(t, http.MethodPost, "/v2/myrepo/blobs/uploads/", nil, nil)
	loc := rr.Header().Get("Location")

	// PUT without ?digest= query param.
	rr = ts.do(t, http.MethodPut, loc, nil, nil)
	mustStatus(t, rr, http.StatusBadRequest)
}

func TestUpload_Delete(t *testing.T) {
	ts := newTestServer(t)

	rr := ts.do(t, http.MethodPost, "/v2/myrepo/blobs/uploads/", nil, nil)
	loc := rr.Header().Get("Location")

	rr = ts.do(t, http.MethodDelete, loc, nil, nil)
	mustStatus(t, rr, http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Manifests
// ---------------------------------------------------------------------------

const ociManifestMediaType = "application/vnd.oci.image.manifest.v1+json"

func sampleManifest() []byte {
	return []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("c", 64) + `","size":1},"layers":[]}`)
}

func TestManifest_PutByTag(t *testing.T) {
	ts := newTestServer(t)
	content := sampleManifest()
	d := digest(content)

	rr := ts.do(t, http.MethodPut, "/v2/myrepo/manifests/latest",
		bytes.NewReader(content),
		map[string]string{"Content-Type": ociManifestMediaType},
	)
	mustStatus(t, rr, http.StatusCreated)
	mustHeader(t, rr, "Docker-Content-Digest", d)

	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Error("Location header missing")
	}
}

func TestManifest_PutByDigest(t *testing.T) {
	ts := newTestServer(t)
	content := sampleManifest()
	d := digest(content)

	rr := ts.do(t, http.MethodPut, "/v2/myrepo/manifests/"+d,
		bytes.NewReader(content),
		map[string]string{"Content-Type": ociManifestMediaType},
	)
	mustStatus(t, rr, http.StatusCreated)
	mustHeader(t, rr, "Docker-Content-Digest", d)
}

func TestManifest_GetByTag(t *testing.T) {
	ts := newTestServer(t)
	content := sampleManifest()
	d := digest(content)

	ts.do(t, http.MethodPut, "/v2/myrepo/manifests/latest",
		bytes.NewReader(content),
		map[string]string{"Content-Type": ociManifestMediaType},
	)

	rr := ts.do(t, http.MethodGet, "/v2/myrepo/manifests/latest", nil, nil)
	mustStatus(t, rr, http.StatusOK)
	mustHeader(t, rr, "Docker-Content-Digest", d)
	mustHeader(t, rr, "Content-Type", ociManifestMediaType)

	if !bytes.Equal(rr.Body.Bytes(), content) {
		t.Error("manifest body mismatch")
	}
}

func TestManifest_GetByDigest(t *testing.T) {
	ts := newTestServer(t)
	content := sampleManifest()
	d := digest(content)

	ts.do(t, http.MethodPut, "/v2/myrepo/manifests/"+d,
		bytes.NewReader(content),
		map[string]string{"Content-Type": ociManifestMediaType},
	)

	rr := ts.do(t, http.MethodGet, "/v2/myrepo/manifests/"+d, nil, nil)
	mustStatus(t, rr, http.StatusOK)
	mustHeader(t, rr, "Docker-Content-Digest", d)
}

func TestManifest_HeadFound(t *testing.T) {
	ts := newTestServer(t)
	content := sampleManifest()
	d := digest(content)
	ts.svc.SaveManifest("repo", d, ociManifestMediaType, content)
	ts.svc.SaveTag("repo", "latest", d)

	rr := ts.do(t, http.MethodHead, "/v2/repo/manifests/latest", nil, nil)
	mustStatus(t, rr, http.StatusOK)
	mustHeader(t, rr, "Docker-Content-Digest", d)
	mustHeader(t, rr, "Content-Type", ociManifestMediaType)
	// HEAD must not have a body.
	if rr.Body.Len() != 0 {
		t.Errorf("HEAD response should have no body, got %d bytes", rr.Body.Len())
	}
}

func TestManifest_GetNotFound(t *testing.T) {
	ts := newTestServer(t)
	ts.svc.EnsureRepository("empty")

	rr := ts.do(t, http.MethodGet, "/v2/empty/manifests/latest", nil, nil)
	mustStatus(t, rr, http.StatusNotFound)

	var errResp struct {
		Errors []struct{ Code string } `json:"errors"`
	}
	decodeJSON(t, rr, &errResp)
	if len(errResp.Errors) == 0 || errResp.Errors[0].Code != "MANIFEST_UNKNOWN" {
		t.Errorf("expected MANIFEST_UNKNOWN, got %s", rr.Body.String())
	}
}

func TestManifest_DeleteByDigest(t *testing.T) {
	ts := newTestServer(t)
	content := sampleManifest()
	d := digest(content)
	ts.svc.SaveManifest("repo", d, ociManifestMediaType, content)
	ts.svc.SaveTag("repo", "v1", d)

	rr := ts.do(t, http.MethodDelete, "/v2/repo/manifests/"+d, nil, nil)
	mustStatus(t, rr, http.StatusAccepted)

	rr = ts.do(t, http.MethodGet, "/v2/repo/manifests/"+d, nil, nil)
	mustStatus(t, rr, http.StatusNotFound)
}

func TestManifest_PutUpdatesTag(t *testing.T) {
	ts := newTestServer(t)
	v1 := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:` + strings.Repeat("1", 64) + `"},"layers":[]}`)
	v2 := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:` + strings.Repeat("2", 64) + `"},"layers":[]}`)
	d2 := digest(v2)

	ts.do(t, http.MethodPut, "/v2/repo/manifests/latest", bytes.NewReader(v1),
		map[string]string{"Content-Type": ociManifestMediaType})
	ts.do(t, http.MethodPut, "/v2/repo/manifests/latest", bytes.NewReader(v2),
		map[string]string{"Content-Type": ociManifestMediaType})

	// GET latest must return v2.
	rr := ts.do(t, http.MethodGet, "/v2/repo/manifests/latest", nil, nil)
	mustStatus(t, rr, http.StatusOK)
	mustHeader(t, rr, "Docker-Content-Digest", d2)
}

// ---------------------------------------------------------------------------
// Tags list
// ---------------------------------------------------------------------------

func TestTagsList(t *testing.T) {
	ts := newTestServer(t)

	content := sampleManifest()
	d := digest(content)
	ts.svc.SaveManifest("myrepo", d, ociManifestMediaType, content)
	for _, tag := range []string{"v1", "v2", "latest"} {
		ts.svc.SaveTag("myrepo", tag, d)
	}

	rr := ts.do(t, http.MethodGet, "/v2/myrepo/tags/list", nil, nil)
	mustStatus(t, rr, http.StatusOK)

	var resp struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	decodeJSON(t, rr, &resp)

	if resp.Name != "myrepo" {
		t.Errorf("name = %q, want myrepo", resp.Name)
	}
	if len(resp.Tags) != 3 {
		t.Errorf("tags count = %d, want 3", len(resp.Tags))
	}
}

func TestTagsList_RepoNotFound(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(t, http.MethodGet, "/v2/ghost/tags/list", nil, nil)
	mustStatus(t, rr, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// OCI Image Index push flow (Apple container CLI scenario)
// ---------------------------------------------------------------------------

func TestOCIIndexPushFlow(t *testing.T) {
	ts := newTestServer(t)

	// 1. Push config blob (monolithic).
	configData := []byte(`{"architecture":"arm64","os":"linux"}`)
	configDigest := digest(configData)
	rr := ts.do(t, http.MethodPost,
		"/v2/hello-world/blobs/uploads/?digest="+configDigest,
		bytes.NewReader(configData),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	mustStatus(t, rr, http.StatusCreated)

	// 2. Push platform manifest by digest.
	platformManifest := []byte(`{
		"schemaVersion":2,
		"mediaType":"application/vnd.oci.image.manifest.v1+json",
		"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + configDigest + `","size":` + fmt.Sprintf("%d", len(configData)) + `},
		"layers":[]
	}`)
	platformDigest := digest(platformManifest)

	rr = ts.do(t, http.MethodPut, "/v2/hello-world/manifests/"+platformDigest,
		bytes.NewReader(platformManifest),
		map[string]string{"Content-Type": ociManifestMediaType},
	)
	mustStatus(t, rr, http.StatusCreated)
	mustHeader(t, rr, "Docker-Content-Digest", platformDigest)

	// 3. Push OCI index by tag, referencing the platform manifest.
	indexJSON := fmt.Sprintf(`{
		"schemaVersion":2,
		"mediaType":"application/vnd.oci.image.index.v1+json",
		"manifests":[{
			"mediaType":"application/vnd.oci.image.manifest.v1+json",
			"digest":"%s",
			"size":%d,
			"platform":{"os":"linux","architecture":"arm64"}
		}]
	}`, platformDigest, len(platformManifest))
	indexBytes := []byte(indexJSON)

	rr = ts.do(t, http.MethodPut, "/v2/hello-world/manifests/latest",
		bytes.NewReader(indexBytes),
		map[string]string{"Content-Type": "application/vnd.oci.image.index.v1+json"},
	)
	mustStatus(t, rr, http.StatusCreated)

	// 4. Verify platform manifest is retrievable by digest.
	rr = ts.do(t, http.MethodGet, "/v2/hello-world/manifests/"+platformDigest, nil, nil)
	mustStatus(t, rr, http.StatusOK)

	// 5. Verify index is retrievable by tag.
	rr = ts.do(t, http.MethodGet, "/v2/hello-world/manifests/latest", nil, nil)
	mustStatus(t, rr, http.StatusOK)
	mustHeader(t, rr, "Content-Type", "application/vnd.oci.image.index.v1+json")

	// 6. Tags list must include "latest".
	rr = ts.do(t, http.MethodGet, "/v2/hello-world/tags/list", nil, nil)
	mustStatus(t, rr, http.StatusOK)
	var tagResp struct {
		Tags []string `json:"tags"`
	}
	decodeJSON(t, rr, &tagResp)
	found := false
	for _, tag := range tagResp.Tags {
		if tag == "latest" {
			found = true
		}
	}
	if !found {
		t.Errorf("latest tag not in tags list: %v", tagResp.Tags)
	}
}
