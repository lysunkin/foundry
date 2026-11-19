package handlers_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Errors caused by a cancelled request are answered with 503 in the API's
// error format and logged at info level, never as a server error.
//
// Not parallel: it captures the default logger.
func TestCancelledRequestIsNotAServerError(t *testing.T) {
	ts := newTestServer(t)
	orphan, _, err := ts.blobs.Put(t.Context(), bytes.NewReader([]byte("orphan")))
	if err != nil {
		t.Fatal(err)
	}

	logs := captureLogs(t)

	tests := []struct {
		name, method, path, wantBody string
	}{
		{"api gc", http.MethodPost, "/api/gc", `{"error":"request cancelled"}`},
		{"api list", http.MethodGet, "/api/repos", `{"error":"request cancelled"}`},
		{"v2 tags", http.MethodGet, "/v2/some/repo/tags/list", `{"errors":[{"code":"UNKNOWN","message":"request cancelled"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			req := httptest.NewRequestWithContext(ctx, tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, req)

			mustStatus(t, rr, http.StatusServiceUnavailable)
			if got := strings.TrimSpace(rr.Body.String()); got != tc.wantBody {
				t.Errorf("body = %s, want %s", got, tc.wantBody)
			}
		})
	}

	if ok, _ := ts.blobs.Exists(orphan); !ok {
		t.Error("cancelled GC deleted an orphan blob")
	}
	if errs := logRecords(t, logs, "server error"); len(errs) != 0 {
		t.Errorf("cancellation logged as server error:\n%s", logs)
	}
	cancelled := logRecords(t, logs, "request cancelled")
	if len(cancelled) != len(tests) {
		t.Errorf("%d 'request cancelled' records, want %d:\n%s", len(cancelled), len(tests), logs)
	}
	for _, rec := range cancelled {
		if rec["level"] != "INFO" {
			t.Errorf("cancellation logged at %v, want INFO", rec["level"])
		}
	}
}
