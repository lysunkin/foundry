package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/foundry/registry/internal/registry"
)

// Probes carry no credentials, so health endpoints answer even when every
// other request needs authentication.
func TestHealthEndpoints_NoAuthRequired(t *testing.T) {
	_, anon := newAuthServer(t, false)
	for _, path := range []string{"/healthz", "/readyz"} {
		resp := anon.do(t, http.MethodGet, path, nil, nil)
		mustStatus(t, resp, http.StatusOK)
		if got := string(readBody(t, resp)); got != "ok\n" {
			t.Errorf("%s body = %q", path, got)
		}
	}
}

func TestRequestIDHeader(t *testing.T) {
	rs := newRegistryServer(t)

	for _, path := range []string{"/v2/", "/api/repos", "/", "/v2/missing/manifests/latest"} {
		resp := rs.do(t, http.MethodGet, path, nil, nil)
		readBody(t, resp)
		if resp.Header.Get("X-Request-Id") == "" {
			t.Errorf("%s: no X-Request-Id", path)
		}
	}

	resp := rs.do(t, http.MethodGet, "/v2/", nil, map[string]string{"X-Request-Id": "trace-42"})
	readBody(t, resp)
	if got := resp.Header.Get("X-Request-Id"); got != "trace-42" {
		t.Errorf("X-Request-Id = %q, want the client's trace-42", got)
	}
}

func withMetrics(c *registry.Config) { c.Metrics = true }

func TestMetrics_DisabledByDefault(t *testing.T) {
	rs := newRegistryServer(t)
	resp := rs.do(t, http.MethodGet, "/metrics", nil, nil)
	mustStatus(t, resp, http.StatusNotFound)
	readBody(t, resp)
}

// metricValue returns the value of the series line starting with series
// (name plus labels exactly as exposed), or -1.
func metricValue(t *testing.T, exposition, series string) float64 {
	t.Helper()
	for line := range strings.Lines(exposition) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), series+" ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(rest, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		return v
	}
	return -1
}

func TestMetrics_PushPullAndGC(t *testing.T) {
	rs := newRegistryServerWithConfig(t, withMetrics)

	layer := bytes.Repeat([]byte("x"), 4096)
	layerDigest := pushBlob(t, rs, nestedRepo, layer)
	config := []byte(`{"metrics":true}`)
	configDigest := pushBlob(t, rs, nestedRepo, config)
	manifest := buildManifest(
		ociDescriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: configDigest, Size: len(config)},
		[]ociDescriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: layerDigest, Size: len(layer)}},
	)
	pushManifest(t, rs, nestedRepo, "v1", "application/vnd.oci.image.manifest.v1+json", manifest)

	resp := rs.do(t, http.MethodGet, "/v2/"+nestedRepo+"/manifests/v1", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
	resp = rs.do(t, http.MethodGet, fmt.Sprintf("/v2/%s/blobs/%s", nestedRepo, layerDigest), nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
	resp = rs.do(t, http.MethodPost, "/api/gc", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)

	resp = rs.do(t, http.MethodGet, "/metrics", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	out := string(readBody(t, resp))
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}

	for series, want := range map[string]float64{
		`foundry_http_requests_total{code="201",method="PUT",route="v2_manifest"}`: 1,
		`foundry_http_requests_total{code="200",method="GET",route="v2_manifest"}`: 1,
		`foundry_http_requests_total{code="201",method="POST",route="v2_upload"}`:  2,
		`foundry_blob_bytes_received_total`:                                        float64(len(layer) + len(config)),
		`foundry_blob_bytes_sent_total`:                                            float64(len(layer)),
		`foundry_gc_runs_total{result="success"}`:                                  1,
	} {
		if got := metricValue(t, out, series); got != want {
			t.Errorf("%s = %v, want %v", series, got, want)
		}
	}
	if strings.Contains(out, nestedRepo) {
		t.Error("repository name exported as a label value")
	}
}

func TestMetrics_FollowReadAuth(t *testing.T) {
	authed, anon := newAuthServer(t, false, withMetrics)
	mustChallenge(t, anon.do(t, http.MethodGet, "/metrics", nil, nil))
	resp := authed.do(t, http.MethodGet, "/metrics", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)

	_, anon = newAuthServer(t, true, withMetrics)
	resp = anon.do(t, http.MethodGet, "/metrics", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
}
