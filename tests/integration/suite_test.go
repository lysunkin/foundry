// Package integration runs end-to-end tests against a fully wired registry
// server started with net/http/httptest. Tests exercise the complete OCI
// Distribution Spec v2 flow over real HTTP — no mocking, no httptest.NewRecorder.
//
// Run with:
//
//	go test -count=1 ./tests/integration/...
//
// or via the Makefile:
//
//	make test-integration
package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/foundry/registry/internal/db"
	"github.com/foundry/registry/internal/registry"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
	"github.com/foundry/registry/internal/ui"
	"github.com/foundry/registry/web"
)

// ---------------------------------------------------------------------------
// Test server bootstrap
// ---------------------------------------------------------------------------

// registryServer is a fully wired test server with convenience helpers.
type registryServer struct {
	*httptest.Server
	client *http.Client
}

// newRegistryServer starts a real httptest.Server backed by a temp SQLite DB
// and temp filesystem stores. It is closed automatically via t.Cleanup.
func newRegistryServer(t *testing.T) *registryServer {
	t.Helper()

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "registry.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	blobs, err := storage.NewFilesystemBlobStore(dataDir)
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}
	ups, err := storage.NewUploadStore(dataDir)
	if err != nil {
		t.Fatalf("upload store: %v", err)
	}

	svc := services.NewRepositoryService(database)

	tmpls, err := ui.Load()
	if err != nil {
		t.Fatalf("templates: %v", err)
	}

	cfg := registry.Config{Addr: "127.0.0.1:0", DataDir: dataDir, DBPath: dbPath}
	srv, err := registry.New(cfg, svc, blobs, ups, tmpls, web.StaticFS)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	return &registryServer{
		Server: ts,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// url builds an absolute URL for the given path.
func (rs *registryServer) url(path string) string {
	return rs.URL + path
}

// do executes a request and returns the response. Caller must close body.
func (rs *registryServer) do(t *testing.T, method, path string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, rs.url(path), body)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := rs.client.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	return resp
}

// mustStatus checks the response status. On success it does NOT close the body
// (caller handles it). On failure it drains + closes the body and marks the
// test as failed so callers can continue for additional assertions.
func mustStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Errorf("%s %s: status %d, want %d — body: %s",
			resp.Request.Method, resp.Request.URL.Path,
			resp.StatusCode, want, body)
	}
}

// readBody drains and closes resp.Body, returning the bytes.
func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// Digest helpers
// ---------------------------------------------------------------------------

func sha256Digest(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// Minimal OCI manifest builders
// ---------------------------------------------------------------------------

const (
	mediaTypeManifestV2  = "application/vnd.docker.distribution.manifest.v2+json"
	mediaTypeOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIIndex    = "application/vnd.oci.image.index.v1+json"
	mediaTypeOCIConfig   = "application/vnd.oci.image.config.v1+json"
	mediaTypeOCILayer    = "application/vnd.oci.image.layer.v1.tar+gzip"
)

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int    `json:"size"`
}

func buildManifest(config ociDescriptor, layers []ociDescriptor) []byte {
	type manifest struct {
		SchemaVersion int             `json:"schemaVersion"`
		MediaType     string          `json:"mediaType"`
		Config        ociDescriptor   `json:"config"`
		Layers        []ociDescriptor `json:"layers"`
	}
	m := manifest{
		SchemaVersion: 2,
		MediaType:     mediaTypeOCIManifest,
		Config:        config,
		Layers:        layers,
	}
	if m.Layers == nil {
		m.Layers = []ociDescriptor{}
	}
	b, _ := json.Marshal(m)
	return b
}

func buildIndex(manifests []ociDescriptor) []byte {
	type index struct {
		SchemaVersion int             `json:"schemaVersion"`
		MediaType     string          `json:"mediaType"`
		Manifests     []ociDescriptor `json:"manifests"`
	}
	b, _ := json.Marshal(index{SchemaVersion: 2, MediaType: mediaTypeOCIIndex, Manifests: manifests})
	return b
}

// pushBlob uploads data as a monolithic blob. Returns the digest.
func pushBlob(t *testing.T, rs *registryServer, repo string, data []byte) string {
	t.Helper()
	d := sha256Digest(data)
	resp := rs.do(t, http.MethodPost,
		fmt.Sprintf("/v2/%s/blobs/uploads/?digest=%s", repo, d),
		bytes.NewReader(data),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	mustStatus(t, resp, http.StatusCreated)
	return d
}

// pushManifest uploads manifest JSON under reference (tag or digest).
func pushManifest(t *testing.T, rs *registryServer, repo, reference, mediaType string, content []byte) string {
	t.Helper()
	resp := rs.do(t, http.MethodPut,
		fmt.Sprintf("/v2/%s/manifests/%s", repo, reference),
		bytes.NewReader(content),
		map[string]string{"Content-Type": mediaType},
	)
	mustStatus(t, resp, http.StatusCreated)
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		t.Error("PUT manifest: Docker-Content-Digest header missing")
	}
	return d
}
