package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// Requests served by registry.Server.HTTPServer derive their context from its
// base context. Once that is cancelled (as on shutdown after the drain
// period), GC aborts: it deletes nothing and answers 503 rather than 500.
func TestContext_CancelledBaseAbortsGC(t *testing.T) {
	// Seed an orphan blob that any GC run would delete.
	base, cancel := context.WithCancel(t.Context())
	rs := startRegistryServer(t, nil, base)
	orphan := pushBlob(t, rs, "ctx/repo", []byte("orphan blob"))

	cancel()

	resp := rs.do(t, http.MethodPost, "/api/gc", nil, nil)
	mustStatus(t, resp, http.StatusServiceUnavailable)
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(readBody(t, resp), &body); err != nil || body.Error != "request cancelled" {
		t.Errorf("body error = %q (%v), want %q", body.Error, err, "request cancelled")
	}

	// The orphan survived. Blob HEAD reads only the file, never the database,
	// so it still answers on this server.
	resp = rs.do(t, http.MethodHead, "/v2/ctx/repo/blobs/"+orphan, nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
}
