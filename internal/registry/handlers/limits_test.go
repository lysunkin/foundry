package handlers_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/foundry/registry/internal/db"
	"github.com/foundry/registry/internal/oci"
	"github.com/foundry/registry/internal/registry/handlers"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
)

type limitServer struct {
	router http.Handler
	usage  *storage.Usage
	blobs  *storage.FilesystemBlobStore
	svc    *services.RepositoryService
}

// newLimitServer wires the /v2 handler with real stores, a maximum blob
// size and a quota (0: none).
func newLimitServer(t *testing.T, maxBlobSize, quota int64) *limitServer {
	t.Helper()
	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	dir := t.TempDir()
	usage := storage.NewUsage(quota)
	blobs, err := storage.NewFilesystemBlobStore(dir, storage.WithUsage(usage))
	if err != nil {
		t.Fatal(err)
	}
	ups, err := storage.NewUploadStore(dir, storage.WithUsage(usage))
	if err != nil {
		t.Fatal(err)
	}
	svc := services.NewRepositoryService(database)
	r := chi.NewRouter()
	handlers.NewRegistryHandler(svc, blobs, ups, maxBlobSize).Mount(r)
	return &limitServer{router: r, usage: usage, blobs: blobs, svc: svc}
}

// body is a request body; stream hides its length (Content-Length -1), as
// with chunked transfer encoding.
type body struct {
	data   []byte
	stream bool
}

func (s *limitServer) do(t *testing.T, method, path string, b body) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(b.data))
	if b.stream {
		req.Body = io.NopCloser(bytes.NewReader(b.data))
		req.ContentLength = -1
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec
}

// startSession opens a chunked upload and returns its URL.
func (s *limitServer) startSession(t *testing.T) string {
	t.Helper()
	rec := s.do(t, http.MethodPost, "/v2/team/app/blobs/uploads/", body{})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start upload: %d %s", rec.Code, rec.Body)
	}
	return rec.Header().Get("Location")
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// want413 asserts a 413 SIZE_INVALID with message msg that closes the
// connection.
func want413(t *testing.T, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body %s", rec.Code, rec.Body)
	}
	var e struct {
		Errors []struct{ Code, Message string } `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || len(e.Errors) != 1 ||
		e.Errors[0].Code != oci.ErrCodeSizeInvalid || e.Errors[0].Message != msg {
		t.Errorf("body = %s, want SIZE_INVALID %q", rec.Body, msg)
	}
	if rec.Header().Get("Connection") != "close" {
		t.Error("413 does not close the connection")
	}
}

func TestMaxBlobSize_Monolithic(t *testing.T) {
	t.Parallel()
	exact := bytes.Repeat([]byte("e"), 100)
	over := bytes.Repeat([]byte("o"), 101)
	for _, tc := range []struct {
		name   string
		data   []byte
		stream bool
		want   int
	}{
		{"exactly the limit", exact, false, http.StatusCreated},
		{"exactly the limit, streamed", exact, true, http.StatusCreated},
		{"over, declared length", over, false, http.StatusRequestEntityTooLarge},
		{"over, streamed", over, true, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newLimitServer(t, 100, 0)
			rec := s.do(t, http.MethodPost, "/v2/team/app/blobs/uploads/?digest="+sha(tc.data), body{tc.data, tc.stream})
			if tc.want == http.StatusRequestEntityTooLarge {
				want413(t, rec, "blob exceeds maximum size")
				if s.usage.Used() != 0 {
					t.Errorf("rejected blob left %d bytes", s.usage.Used())
				}
				return
			}
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestMaxBlobSize_Chunked(t *testing.T) {
	t.Parallel()
	chunk := bytes.Repeat([]byte("c"), 60)
	for _, tc := range []struct {
		name   string
		method string // of the second request
		stream bool
	}{
		{"patch, declared length", http.MethodPatch, false},
		{"patch, streamed", http.MethodPatch, true},
		{"final put, declared length", http.MethodPut, false},
		{"final put, streamed", http.MethodPut, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newLimitServer(t, 100, 0)
			loc := s.startSession(t)
			if rec := s.do(t, http.MethodPatch, loc, body{data: chunk}); rec.Code != http.StatusAccepted {
				t.Fatalf("first chunk: %d %s", rec.Code, rec.Body)
			}

			// 60 + 60 > 100: the offset counts towards the limit.
			target := loc
			if tc.method == http.MethodPut {
				target += "?digest=" + sha(append(chunk, chunk...))
			}
			want413(t, s.do(t, tc.method, target, body{chunk, tc.stream}), "blob exceeds maximum size")

			if rec := s.do(t, http.MethodGet, loc, body{}); rec.Code != http.StatusNotFound {
				t.Errorf("session after 413: status %d, want 404 (discarded)", rec.Code)
			}
			if s.usage.Used() != 0 {
				t.Errorf("discarded session left %d bytes", s.usage.Used())
			}
		})
	}
}

func TestStorageQuota(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte("q"), 150)

	t.Run("monolithic", func(t *testing.T) {
		t.Parallel()
		s := newLimitServer(t, 0, 100)
		want413(t, s.do(t, http.MethodPost, "/v2/team/app/blobs/uploads/?digest="+sha(data), body{data, true}),
			"storage quota exceeded")
		if s.usage.Used() != 0 {
			t.Errorf("usage = %d after rejected push", s.usage.Used())
		}
	})

	t.Run("chunked", func(t *testing.T) {
		t.Parallel()
		s := newLimitServer(t, 0, 100)
		loc := s.startSession(t)
		want413(t, s.do(t, http.MethodPatch, loc, body{data: data}), "storage quota exceeded")
		if rec := s.do(t, http.MethodGet, loc, body{}); rec.Code != http.StatusNotFound {
			t.Errorf("session after 413: status %d, want 404", rec.Code)
		}
		if s.usage.Used() != 0 {
			t.Errorf("usage = %d after discarded session", s.usage.Used())
		}
	})
}

// Mounting copies nothing, so it is allowed even for a blob over the limit.
func TestMaxBlobSize_MountUnaffected(t *testing.T) {
	t.Parallel()
	s := newLimitServer(t, 10, 0)
	big := bytes.Repeat([]byte("m"), 50)
	// Store it directly, as if pushed before the limit was configured.
	digest, _, err := s.blobs.Put(t.Context(), bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.svc.SaveBlob(t.Context(), digest, int64(len(big))); err != nil {
		t.Fatal(err)
	}
	rec := s.do(t, http.MethodPost, "/v2/other/repo/blobs/uploads/?mount="+digest+"&from=team/app", body{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("mount: %d %s, want 201", rec.Code, rec.Body)
	}
}
