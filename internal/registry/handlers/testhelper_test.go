package handlers_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/foundry/registry/internal/db"
	"github.com/foundry/registry/internal/registry/handlers"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
)

// testServer wires all handlers onto a single chi router backed by real
// SQLite + real filesystem stores, all in temp directories.
type testServer struct {
	router http.Handler
	svc    *services.RepositoryService
	blobs  *storage.FilesystemBlobStore
	ups    *storage.UploadStore
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	dataDir := t.TempDir()
	blobs, err := storage.NewFilesystemBlobStore(dataDir)
	if err != nil {
		t.Fatalf("NewFilesystemBlobStore: %v", err)
	}
	ups, err := storage.NewUploadStore(dataDir)
	if err != nil {
		t.Fatalf("NewUploadStore: %v", err)
	}

	svc := services.NewRepositoryService(database)
	r := chi.NewRouter()

	handlers.NewRegistryHandler(svc, blobs, ups).Mount(r)
	handlers.NewAPIHandler(svc, blobs).Mount(r)

	return &testServer{router: r, svc: svc, blobs: blobs, ups: ups}
}

// do performs an HTTP request against the test server.
func (ts *testServer) do(t *testing.T, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	return rr
}

// mustStatus fails the test if the response code doesn't match.
func mustStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, want, rr.Body.String())
	}
}

// mustHeader fails if the response header is absent or doesn't match.
func mustHeader(t *testing.T, rr *httptest.ResponseRecorder, key, want string) {
	t.Helper()
	got := rr.Header().Get(key)
	if got != want {
		t.Errorf("header %s = %q, want %q", key, got, want)
	}
}

// decodeJSON unmarshals the response body into v.
func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.NewDecoder(bytes.NewReader(rr.Body.Bytes())).Decode(v); err != nil {
		t.Fatalf("decode JSON: %v (body: %s)", err, rr.Body.String())
	}
}
