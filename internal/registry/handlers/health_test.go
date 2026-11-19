package handlers_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/foundry/registry/internal/registry/handlers"
)

type fakePinger struct{ err error }

func (p fakePinger) Ping(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		panic("readiness ping without a deadline")
	}
	return p.err
}

func TestHealth(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, method, path string
		pingErr            error
		wantCode           int
	}{
		{"live", http.MethodGet, "/healthz", nil, http.StatusOK},
		{"live ignores db", http.MethodGet, "/healthz", errBackend, http.StatusOK},
		{"live HEAD", http.MethodHead, "/healthz", nil, http.StatusOK},
		{"ready", http.MethodGet, "/readyz", nil, http.StatusOK},
		{"not ready", http.MethodGet, "/readyz", errBackend, http.StatusServiceUnavailable},
		{"ready HEAD", http.MethodHead, "/readyz", nil, http.StatusOK},
		{"POST not allowed", http.MethodPost, "/healthz", nil, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := chi.NewRouter()
			// Auth with anonymous reads off: probes must still pass.
			r.Use(handlers.RequireAuth(handlers.AuthConfig{Users: newUsers(t)}))
			handlers.NewHealthHandler(fakePinger{err: tc.pingErr}).Mount(r)

			rec := serveFake(t, r, tc.method, tc.path, nil)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body %q", rec.Code, tc.wantCode, rec.Body)
			}
			if tc.wantCode == http.StatusServiceUnavailable && rec.Body.String() != "not ready\n" {
				t.Errorf("body = %q leaks detail", rec.Body)
			}
		})
	}
}
