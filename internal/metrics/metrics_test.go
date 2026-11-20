package metrics_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/foundry/registry/internal/metrics"
	"github.com/foundry/registry/internal/registry/handlers"
	"github.com/foundry/registry/internal/registry/services"
)

const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

func TestRoute(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, want string }{
		{"/healthz", metrics.RouteOps},
		{"/readyz", metrics.RouteOps},
		{"/metrics", metrics.RouteOps},
		{"/static/js/htmx.min.js", metrics.RouteStatic},
		{"/api/repos", metrics.RouteAPI},
		{"/api/gc", metrics.RouteAPI},
		{"/", metrics.RouteUI},
		{"/ui/repo/team/app", metrics.RouteUI},
		{"/apiary", metrics.RouteUI},
		{"/v2/", metrics.RouteV2Other},
		{"/v2/_catalog", metrics.RouteV2Other},
		{"/v2/team/app/tags/list", metrics.RouteV2Other},
		{"/v2/team/app/manifests/latest", metrics.RouteV2Manifest},
		{"/v2/team/app/blobs/" + digest, metrics.RouteV2Blob},
		{"/v2/team/app/blobs/uploads/", metrics.RouteV2Upload},
		{"/v2/team/app/blobs/uploads/abc-123", metrics.RouteV2Upload},
		// Repository names may contain the route words; the last marker wins.
		{"/v2/blobs/manifests/app/manifests/v1", metrics.RouteV2Manifest},
		{"/v2/x/manifests/y/blobs/" + digest, metrics.RouteV2Blob},
		{"/v2/x/blobs/uploads/y/manifests/v1", metrics.RouteV2Manifest},
	} {
		if got := metrics.Route(tc.path); got != tc.want {
			t.Errorf("Route(%q) = %s, want %s", tc.path, got, tc.want)
		}
	}
}

// hasLine reports whether the exposition text contains line exactly.
func hasLine(out, line string) bool {
	for l := range strings.Lines(out) {
		if strings.TrimSuffix(l, "\n") == line {
			return true
		}
	}
	return false
}

// scrape returns the exposition text of m.
func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status %d", rec.Code)
	}
	return rec.Body.String()
}

func TestObserveRequest(t *testing.T) {
	t.Parallel()
	m := metrics.New()
	for _, s := range []handlers.RequestStats{
		{Method: http.MethodPatch, Path: "/v2/a/blobs/uploads/u1", Status: 202, BytesIn: 100, Duration: time.Second},
		{Method: http.MethodPut, Path: "/v2/a/blobs/uploads/u1", Status: 201, BytesIn: 50},
		{Method: http.MethodGet, Path: "/v2/a/blobs/" + digest, Status: 200, BytesOut: 70},
		{Method: http.MethodHead, Path: "/v2/a/blobs/" + digest, Status: 200, BytesOut: 0},
		{Method: http.MethodGet, Path: "/v2/a/manifests/v1", Status: 200, BytesIn: 999, BytesOut: 999},
		{Method: http.MethodGet, Path: "/v2/a/manifests/v1", Status: 404},
		{Method: "BREW", Path: "/", Status: 405},
	} {
		m.ObserveRequest(s)
	}

	out := scrape(t, m)
	for _, want := range []string{
		`foundry_blob_bytes_received_total 150`,
		`foundry_blob_bytes_sent_total 70`,
		`foundry_http_requests_total{code="202",method="PATCH",route="v2_upload"} 1`,
		`foundry_http_requests_total{code="200",method="GET",route="v2_manifest"} 1`,
		`foundry_http_requests_total{code="404",method="GET",route="v2_manifest"} 1`,
		`foundry_http_requests_total{code="405",method="other",route="ui"} 1`,
		`foundry_http_request_duration_seconds_count{method="PATCH",route="v2_upload"} 1`,
	} {
		if !hasLine(out, want) {
			t.Errorf("scrape lacks %q", want)
		}
	}
	if !strings.Contains(out, "\ngo_goroutines ") {
		t.Error("Go runtime collector missing")
	}
	if strings.Contains(out, `"a"`) || strings.Contains(out, "BREW") {
		t.Errorf("unbounded label value exported:\n%s", out)
	}
}

type fakeCollector struct {
	res *services.GCResult
	err error
}

func (f fakeCollector) Collect(context.Context, services.GCOptions) (*services.GCResult, error) {
	return f.res, f.err
}

func TestInstrumentCollector(t *testing.T) {
	t.Parallel()
	m := metrics.New()
	run := func(c fakeCollector) {
		t.Helper()
		res, err := m.InstrumentCollector(c).Collect(t.Context(), services.GCOptions{})
		if res != c.res || !errors.Is(err, c.err) {
			t.Errorf("decorator changed the result: %v, %v", res, err)
		}
	}
	run(fakeCollector{res: &services.GCResult{DeletedBlobs: 2, FreedBytes: 300}})
	run(fakeCollector{res: &services.GCResult{DeletedBlobs: 5, FreedBytes: 999, DryRun: true}})
	run(fakeCollector{err: errors.New("disk on fire")})
	run(fakeCollector{err: fmt.Errorf("gc cancelled: %w", context.Canceled)})
	run(fakeCollector{err: fmt.Errorf("gc cancelled: %w", context.DeadlineExceeded)})

	out := scrape(t, m)
	for _, want := range []string{
		`foundry_gc_runs_total{result="success"} 2`,
		`foundry_gc_runs_total{result="error"} 1`,
		`foundry_gc_runs_total{result="cancelled"} 2`,
		`foundry_gc_deleted_blobs_total 2`,
		`foundry_gc_freed_bytes_total 300`,
		`foundry_gc_duration_seconds_count 5`,
	} {
		if !hasLine(out, want) {
			t.Errorf("scrape lacks %q", want)
		}
	}
	if !strings.Contains(out, "\nfoundry_gc_last_success_timestamp_seconds 1.") {
		t.Errorf("last-success timestamp not set:\n%s", out)
	}
}
