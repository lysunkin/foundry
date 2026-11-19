package handlers_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/foundry/registry/internal/oci"
	"github.com/foundry/registry/internal/registry/handlers"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
	"github.com/foundry/registry/internal/ui"
)

// errBackend stands in for a database or disk failure. Its text must never
// reach the client.
var errBackend = errors.New("backend exploded: secret detail")

const testDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

func serveFake(t *testing.T, h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// /v2 must distinguish "not found" from "failed" (spec §5.4): a backend
// error is 500 UNKNOWN with a generic message, never 404 and never the
// underlying error text.
func TestRegistryHandler_BackendErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		meta     error
		blobs    error
		path     string
		wantCode int
		wantOCI  string
	}{
		{"manifest db error", errBackend, nil, "/v2/team/app/manifests/latest", http.StatusInternalServerError, oci.ErrCodeUnknown},
		{"manifest not found", services.ErrNotFound, nil, "/v2/team/app/manifests/latest", http.StatusNotFound, oci.ErrCodeManifestUnknown},
		{"tags db error", errBackend, nil, "/v2/team/app/tags/list", http.StatusInternalServerError, oci.ErrCodeUnknown},
		{"tags not found", services.ErrNotFound, nil, "/v2/team/app/tags/list", http.StatusNotFound, oci.ErrCodeNameUnknown},
		{"upload db error", errBackend, nil, "/v2/team/app/blobs/uploads/abc", http.StatusInternalServerError, oci.ErrCodeUnknown},
		{"upload not found", services.ErrNotFound, nil, "/v2/team/app/blobs/uploads/abc", http.StatusNotFound, oci.ErrCodeBlobUploadUnknown},
		{"blob disk error", nil, errBackend, "/v2/team/app/blobs/" + testDigest, http.StatusInternalServerError, oci.ErrCodeUnknown},
		{"blob not found", nil, fmt.Errorf("open: %w", storage.ErrBlobNotFound), "/v2/team/app/blobs/" + testDigest, http.StatusNotFound, oci.ErrCodeBlobUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := chi.NewRouter()
			handlers.NewRegistryHandler(fakeMeta{err: tc.meta}, fakeBlobs{err: tc.blobs}, fakeUploads{}).Mount(r)

			rec := serveFake(t, r, http.MethodGet, tc.path, nil)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.wantCode, rec.Body)
			}
			var body struct {
				Errors []struct{ Code, Message string } `json:"errors"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 {
				t.Fatalf("body %q is not one OCI error (%v)", rec.Body, err)
			}
			if body.Errors[0].Code != tc.wantOCI {
				t.Errorf("code = %s, want %s", body.Errors[0].Code, tc.wantOCI)
			}
			if strings.Contains(rec.Body.String(), "secret detail") {
				t.Errorf("internal error leaked to client: %s", rec.Body)
			}
		})
	}
}

func TestAPIHandler_BackendErrors(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/repos", "/api/repos/team/app/tags", "/api/repos/team/app/manifests/latest"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			r := chi.NewRouter()
			handlers.NewAPIHandler(fakeMeta{err: errBackend}, &fakeCollector{}, services.GCOptions{}).Mount(r)

			rec := serveFake(t, r, http.MethodGet, path, nil)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"internal server error"}` {
				t.Errorf("body = %s", got)
			}
		})
	}
}

func TestAPIHandler_GC(t *testing.T) {
	t.Parallel()
	defaults := services.GCOptions{GracePeriod: 7, UploadTTL: 9}
	for _, tc := range []struct {
		name     string
		query    string
		gc       *fakeCollector
		wantCode int
		wantBody string
		wantDry  bool
	}{
		{"success", "", &fakeCollector{result: gcResult(false)}, http.StatusOK, `"deleted_blobs":1`, false},
		{"dry run", "?dry_run=true", &fakeCollector{result: gcResult(true)}, http.StatusOK, `"dry_run":true`, true},
		{"failure", "", &fakeCollector{err: errBackend}, http.StatusInternalServerError, `{"error":"internal server error"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := chi.NewRouter()
			handlers.NewAPIHandler(fakeMeta{}, tc.gc, defaults).Mount(r)

			rec := serveFake(t, r, http.MethodPost, "/api/gc"+tc.query, nil)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.wantCode, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body %s does not contain %s", rec.Body, tc.wantBody)
			}
			want := defaults
			want.DryRun = tc.wantDry
			if tc.gc.got == nil || *tc.gc.got != want {
				t.Errorf("collector options = %+v, want %+v", tc.gc.got, want)
			}
		})
	}
}

func TestWebHandler_GC(t *testing.T) {
	t.Parallel()
	tmpls, err := ui.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		gc       *fakeCollector
		wantCode int
		wantBody string
	}{
		{"success", &fakeCollector{result: gcResult(false)}, http.StatusOK, "42"},
		{"failure", &fakeCollector{err: errBackend}, http.StatusInternalServerError, "internal server error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := chi.NewRouter()
			handlers.NewWebHandler(fakeMeta{}, tc.gc, services.GCOptions{}, tmpls).Mount(r)

			rec := serveFake(t, r, http.MethodPost, "/ui/gc", map[string]string{"HX-Request": "true"})
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.wantCode, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body %q does not contain %q", rec.Body, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "secret detail") {
				t.Errorf("internal error leaked to client: %s", rec.Body)
			}
		})
	}
}

func TestWebHandler_BackendErrors(t *testing.T) {
	t.Parallel()
	tmpls, err := ui.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		err      error
		path     string
		wantCode int
	}{
		{"dashboard db error", errBackend, "/", http.StatusInternalServerError},
		{"repo db error", errBackend, "/ui/repo/team/app", http.StatusInternalServerError},
		{"repo not found", services.ErrNotFound, "/ui/repo/team/app", http.StatusNotFound},
		{"tag db error", errBackend, "/ui/repo/team/app/tag/v1", http.StatusInternalServerError},
		{"tag not found", services.ErrNotFound, "/ui/repo/team/app/tag/v1", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := chi.NewRouter()
			handlers.NewWebHandler(fakeMeta{err: tc.err}, &fakeCollector{}, services.GCOptions{}, tmpls).Mount(r)

			rec := serveFake(t, r, http.MethodGet, tc.path, nil)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.wantCode, rec.Body)
			}
		})
	}
}
