package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	"github.com/foundry/registry/internal/registry"
	"github.com/foundry/registry/internal/registry/storage"
)

// With a quota, pushes are rejected once it is reached; deleting an image and
// running GC frees the space for the next push. Usage is exported as metrics.
func TestStorageQuota_RejectThenGCFrees(t *testing.T) {
	const quota = 10_000
	usage := storage.NewUsage(quota)
	rs := newRegistryServerWithConfig(t, func(c *registry.Config) {
		c.Usage = usage
		c.Metrics = true
	})

	layer := bytes.Repeat([]byte("L"), 6_000)
	config := []byte(`{"quota":1}`)
	layerDigest := pushBlob(t, rs, nestedRepo, layer)
	configDigest := pushBlob(t, rs, nestedRepo, config)
	manifestDigest := pushManifest(t, rs, nestedRepo, "v1", "application/vnd.oci.image.manifest.v1+json", buildManifest(
		ociDescriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: configDigest, Size: len(config)},
		[]ociDescriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: layerDigest, Size: len(layer)}},
	))

	second := bytes.Repeat([]byte("S"), 6_000)
	resp := rs.do(t, http.MethodPost,
		fmt.Sprintf("/v2/%s/blobs/uploads/?digest=%s", nestedRepo, sha256Digest(second)),
		bytes.NewReader(second), nil)
	mustStatus(t, resp, http.StatusRequestEntityTooLarge)
	if b := string(readBody(t, resp)); !bytes.Contains([]byte(b), []byte(`"SIZE_INVALID"`)) {
		t.Errorf("body = %s", b)
	}

	resp = rs.do(t, http.MethodDelete, "/v2/"+nestedRepo+"/manifests/"+manifestDigest, nil, nil)
	mustStatus(t, resp, http.StatusAccepted)
	readBody(t, resp)
	resp = rs.do(t, http.MethodPost, "/api/gc", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
	if usage.Used() != 0 {
		t.Fatalf("usage after GC = %d, want 0", usage.Used())
	}

	pushBlob(t, rs, nestedRepo, second)

	resp = rs.do(t, http.MethodGet, "/metrics", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	out := string(readBody(t, resp))
	if got := metricValue(t, out, "foundry_storage_used_bytes"); got != float64(len(second)) {
		t.Errorf("foundry_storage_used_bytes = %v, want %d", got, len(second))
	}
	if got := metricValue(t, out, "foundry_storage_quota_bytes"); got != quota {
		t.Errorf("foundry_storage_quota_bytes = %v, want %d", got, quota)
	}
}

func TestMaxBlobSize_EndToEnd(t *testing.T) {
	rs := newRegistryServerWithConfig(t, func(c *registry.Config) { c.MaxBlobSize = 1024 })

	pushBlob(t, rs, nestedRepo, bytes.Repeat([]byte("k"), 1024))
	big := bytes.Repeat([]byte("b"), 1025)
	resp := rs.do(t, http.MethodPost,
		fmt.Sprintf("/v2/%s/blobs/uploads/?digest=%s", nestedRepo, sha256Digest(big)),
		bytes.NewReader(big), nil)
	mustStatus(t, resp, http.StatusRequestEntityTooLarge)
	readBody(t, resp)
}
