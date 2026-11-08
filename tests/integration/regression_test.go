package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// pushImage pushes a config blob plus manifest and returns the manifest digest.
func pushImage(t *testing.T, rs *registryServer, repo, tag string, cfg []byte) string {
	t.Helper()
	d := pushBlob(t, rs, repo, cfg)
	m := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: d, Size: len(cfg)}, nil)
	return pushManifest(t, rs, repo, tag, mediaTypeOCIManifest, m)
}

// Each page must render its own "content" block (previously every page
// rendered the last-parsed template's block).
func TestRegression_WebPagesRenderOwnContent(t *testing.T) {
	rs := newRegistryServer(t)
	pushImage(t, rs, "web", "v1", []byte(`{"a":1}`))

	cases := map[string]string{
		"/":                   "<h1>Repositories</h1>",
		"/ui/repo/web":        ">v1</a>",
		"/ui/repo/web/tag/v1": "Raw Manifest",
	}
	for path, want := range cases {
		resp := rs.do(t, http.MethodGet, path, nil, nil)
		mustStatus(t, resp, http.StatusOK)
		body := string(readBody(t, resp))
		if !strings.Contains(body, want) {
			t.Errorf("GET %s: body missing %q", path, want)
		}
		if path != "/ui/repo/web/tag/v1" && strings.Contains(body, "Raw Manifest") {
			t.Errorf("GET %s rendered the tag page content", path)
		}
	}
}

// The same manifest pushed to two repositories must stay addressable in both,
// and deleting it from one must not affect the other.
func TestRegression_SameManifestInTwoRepos(t *testing.T) {
	rs := newRegistryServer(t)
	cfg := []byte(`{"shared":true}`)
	d := pushImage(t, rs, "a", "latest", cfg)
	if d2 := pushImage(t, rs, "b", "latest", cfg); d2 != d {
		t.Fatalf("digests differ: %s vs %s", d, d2)
	}

	for _, repo := range []string{"a", "b"} {
		resp := rs.do(t, http.MethodGet, "/v2/"+repo+"/manifests/"+d, nil, nil)
		mustStatus(t, resp, http.StatusOK)
		readBody(t, resp)
	}

	resp := rs.do(t, http.MethodDelete, "/v2/a/manifests/"+d, nil, nil)
	mustStatus(t, resp, http.StatusAccepted)
	readBody(t, resp)

	resp = rs.do(t, http.MethodGet, "/v2/b/manifests/latest", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
}

// Malformed digests are client errors, never panics/500s.
func TestRegression_InvalidDigestIs400(t *testing.T) {
	rs := newRegistryServer(t)
	for _, d := range []string{"sha256:a", "sha256:..", "md5:abc", "sha256:" + strings.Repeat("Z", 64)} {
		resp := rs.do(t, http.MethodGet, "/v2/x/blobs/"+d, nil, nil)
		mustStatus(t, resp, http.StatusBadRequest)
		readBody(t, resp)
	}
}

func TestRegression_InvalidRepoNameIs400(t *testing.T) {
	rs := newRegistryServer(t)
	resp := rs.do(t, http.MethodPost, "/v2/BAD..NAME/blobs/uploads/", nil, nil)
	mustStatus(t, resp, http.StatusBadRequest)
	if !bytes.Contains(readBody(t, resp), []byte("NAME_INVALID")) {
		t.Error("expected NAME_INVALID")
	}
}

func TestRegression_ManifestValidation(t *testing.T) {
	rs := newRegistryServer(t)
	missing := "sha256:" + strings.Repeat("0", 64)
	cases := []struct {
		ref, body, code string
	}{
		{"latest", `not json`, "MANIFEST_INVALID"},
		{"latest", `{"schemaVersion":2,"mediaType":"` + mediaTypeOCIManifest + `","config":{"digest":"` + missing + `"},"layers":[]}`, "MANIFEST_BLOB_UNKNOWN"},
		{"sha256:" + strings.Repeat("1", 64), `{"schemaVersion":2,"config":{"digest":"` + missing + `"}}`, "DIGEST_INVALID"},
	}
	for _, c := range cases {
		resp := rs.do(t, http.MethodPut, "/v2/x/manifests/"+c.ref, strings.NewReader(c.body),
			map[string]string{"Content-Type": mediaTypeOCIManifest})
		mustStatus(t, resp, http.StatusBadRequest)
		if b := readBody(t, resp); !bytes.Contains(b, []byte(c.code)) {
			t.Errorf("ref %s: want %s, got %s", c.ref, c.code, b)
		}
	}
}

func TestRegression_DeleteUnknownUploadIs404(t *testing.T) {
	rs := newRegistryServer(t)
	resp := rs.do(t, http.MethodDelete, "/v2/x/blobs/uploads/nope", nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
}

func TestRegression_TagsPagination(t *testing.T) {
	rs := newRegistryServer(t)
	d := pushImage(t, rs, "pg", "a", []byte(`{"p":1}`))
	for _, tag := range []string{"b", "c"} {
		m := rs.do(t, http.MethodGet, "/v2/pg/manifests/"+d, nil, nil)
		body := readBody(t, m)
		pushManifest(t, rs, "pg", tag, mediaTypeOCIManifest, body)
	}

	resp := rs.do(t, http.MethodGet, "/v2/pg/tags/list?n=2", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	link := resp.Header.Get("Link")
	var page struct{ Tags []string }
	json.Unmarshal(readBody(t, resp), &page)
	if fmt.Sprint(page.Tags) != "[a b]" || !strings.Contains(link, `rel="next"`) {
		t.Errorf("page 1 = %v, link %q", page.Tags, link)
	}

	resp = rs.do(t, http.MethodGet, "/v2/pg/tags/list?n=2&last=b", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	json.Unmarshal(readBody(t, resp), &page)
	if fmt.Sprint(page.Tags) != "[c]" || resp.Header.Get("Link") != "" {
		t.Errorf("page 2 = %v", page.Tags)
	}
}

func TestRegression_CatalogAndMount(t *testing.T) {
	rs := newRegistryServer(t)
	cfg := []byte(`{"m":1}`)
	d := pushBlob(t, rs, "src", cfg)

	resp := rs.do(t, http.MethodPost, "/v2/dst/blobs/uploads/?mount="+d+"&from=src", nil, nil)
	mustStatus(t, resp, http.StatusCreated)
	readBody(t, resp)

	resp = rs.do(t, http.MethodGet, "/v2/_catalog", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	var cat struct{ Repositories []string }
	json.Unmarshal(readBody(t, resp), &cat)
	if fmt.Sprint(cat.Repositories) != "[dst src]" {
		t.Errorf("catalog = %v", cat.Repositories)
	}
}

func TestRegression_BlobRangeRequest(t *testing.T) {
	rs := newRegistryServer(t)
	d := pushBlob(t, rs, "r", []byte("0123456789"))
	resp := rs.do(t, http.MethodGet, "/v2/r/blobs/"+d, nil, map[string]string{"Range": "bytes=2-4"})
	mustStatus(t, resp, http.StatusPartialContent)
	if b := readBody(t, resp); string(b) != "234" {
		t.Errorf("range body = %q", b)
	}
}

// Deleting a tag through the manifests endpoint (OCI 1.1) removes only the tag.
func TestRegression_DeleteTagViaManifestEndpoint(t *testing.T) {
	rs := newRegistryServer(t)
	d := pushImage(t, rs, "dt", "v1", []byte(`{"t":1}`))
	resp := rs.do(t, http.MethodDelete, "/v2/dt/manifests/v1", nil, nil)
	mustStatus(t, resp, http.StatusAccepted)
	readBody(t, resp)

	resp = rs.do(t, http.MethodGet, "/v2/dt/manifests/v1", nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
	resp = rs.do(t, http.MethodGet, "/v2/dt/manifests/"+d, nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
}

// Repository names containing '/' (e.g. "library/nginx") must work across the
// registry API, admin API and web UI. chi path params cannot span segments, so
// this was previously 404 everywhere.
func TestRegression_NestedRepositoryNames(t *testing.T) {
	rs := newRegistryServer(t)
	const repo = "org/team/app"

	// Chunked upload under a nested name.
	layer := []byte("nested layer data")
	resp := rs.do(t, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", nil, nil)
	mustStatus(t, resp, http.StatusAccepted)
	loc := resp.Header.Get("Location")
	readBody(t, resp)
	resp = rs.do(t, http.MethodPatch, loc, bytes.NewReader(layer), nil)
	mustStatus(t, resp, http.StatusAccepted)
	readBody(t, resp)
	ld := sha256Digest(layer)
	resp = rs.do(t, http.MethodPut, loc+"?digest="+ld, nil, nil)
	mustStatus(t, resp, http.StatusCreated)
	readBody(t, resp)

	cfg := []byte(`{"nested":true}`)
	cd := pushBlob(t, rs, repo, cfg)
	m := buildManifest(ociDescriptor{MediaType: mediaTypeOCIConfig, Digest: cd, Size: len(cfg)},
		[]ociDescriptor{{MediaType: mediaTypeOCILayer, Digest: ld, Size: len(layer)}})
	md := pushManifest(t, rs, repo, "v1", mediaTypeOCIManifest, m)

	for _, p := range []string{
		"/v2/" + repo + "/tags/list",
		"/v2/" + repo + "/manifests/v1",
		"/v2/" + repo + "/blobs/" + ld,
		"/api/repos/" + repo + "/tags",
		"/api/repos/" + repo + "/manifests/v1",
		"/ui/repo/" + repo,
		"/ui/repo/" + repo + "/tags",
		"/ui/repo/" + repo + "/tag/v1",
	} {
		resp := rs.do(t, http.MethodGet, p, nil, nil)
		mustStatus(t, resp, http.StatusOK)
		readBody(t, resp)
	}

	resp = rs.do(t, http.MethodDelete, "/api/tags/"+repo+"/v1", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
	resp = rs.do(t, http.MethodDelete, "/api/manifests/"+repo+"/"+md, nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
	resp = rs.do(t, http.MethodDelete, "/api/repos/"+repo, nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
	resp = rs.do(t, http.MethodGet, "/v2/"+repo+"/tags/list", nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
}

func TestRegression_WrongMethodIs405(t *testing.T) {
	rs := newRegistryServer(t)
	resp := rs.do(t, http.MethodPost, "/v2/x/manifests/latest", nil, nil)
	mustStatus(t, resp, http.StatusMethodNotAllowed)
	readBody(t, resp)
}
