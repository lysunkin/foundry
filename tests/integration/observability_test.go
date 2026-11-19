package integration

import (
	"net/http"
	"testing"
)

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
