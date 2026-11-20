// Package metrics exposes Prometheus metrics for HTTP traffic and garbage
// collection. Labels are bounded: no repository names, users or digests.
package metrics

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/foundry/registry/internal/registry/handlers"
	"github.com/foundry/registry/internal/registry/services"
)

// Route classes, the values of the "route" label.
const (
	RouteV2Blob     = "v2_blob"
	RouteV2Manifest = "v2_manifest"
	RouteV2Upload   = "v2_upload"
	RouteV2Other    = "v2_other"
	RouteAPI        = "api"
	RouteUI         = "ui"
	RouteStatic     = "static"
	RouteOps        = "ops"
)

// httpBuckets span quick metadata requests to multi-minute blob transfers.
var httpBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// Metrics holds the registry's metrics in its own registry (not the global
// default), so tests and multiple servers do not collide.
type Metrics struct {
	reg *prometheus.Registry

	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	bytesIn  prometheus.Counter
	bytesOut prometheus.Counter

	gcRuns        *prometheus.CounterVec
	gcDeleted     prometheus.Counter
	gcFreed       prometheus.Counter
	gcDuration    prometheus.Histogram
	gcLastSuccess prometheus.Gauge
}

var _ handlers.RequestObserver = (*Metrics)(nil)

// New registers all metrics, plus the Go runtime and process collectors.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	f := promauto.With(reg)
	return &Metrics{
		reg: reg,
		requests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "foundry_http_requests_total",
			Help: "HTTP requests by route class, method and status code.",
		}, []string{"route", "method", "code"}),
		duration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "foundry_http_request_duration_seconds",
			Help:    "HTTP request duration by route class and method.",
			Buckets: httpBuckets,
		}, []string{"route", "method"}),
		bytesIn: f.NewCounter(prometheus.CounterOpts{
			Name: "foundry_blob_bytes_received_total",
			Help: "Request body bytes received by blob upload endpoints.",
		}),
		bytesOut: f.NewCounter(prometheus.CounterOpts{
			Name: "foundry_blob_bytes_sent_total",
			Help: "Response body bytes sent by blob download endpoints.",
		}),
		gcRuns: f.NewCounterVec(prometheus.CounterOpts{
			Name: "foundry_gc_runs_total",
			Help: "Garbage collection runs by result (success, error, cancelled).",
		}, []string{"result"}),
		gcDeleted: f.NewCounter(prometheus.CounterOpts{
			Name: "foundry_gc_deleted_blobs_total",
			Help: "Blobs deleted by garbage collection (dry runs excluded).",
		}),
		gcFreed: f.NewCounter(prometheus.CounterOpts{
			Name: "foundry_gc_freed_bytes_total",
			Help: "Bytes freed by garbage collection (dry runs excluded).",
		}),
		gcDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "foundry_gc_duration_seconds",
			Help:    "Garbage collection run duration.",
			Buckets: prometheus.ExponentialBuckets(0.01, 4, 9), // 10ms .. ~11min
		}),
		gcLastSuccess: f.NewGauge(prometheus.GaugeOpts{
			Name: "foundry_gc_last_success_timestamp_seconds",
			Help: "Unix time of the last successful garbage collection run.",
		}),
	}
}

// ObserveStorage exports the bytes stored (read from used at scrape time)
// and the quota (0: unlimited).
func (m *Metrics) ObserveStorage(used func() int64, quota int64) {
	f := promauto.With(m.reg)
	f.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "foundry_storage_used_bytes",
		Help: "Bytes stored in blobs and in-flight uploads (the database is not counted).",
	}, func() float64 { return float64(used()) })
	f.NewGauge(prometheus.GaugeOpts{
		Name: "foundry_storage_quota_bytes",
		Help: "Storage quota in bytes; 0 means unlimited.",
	}).Set(float64(quota))
}

// Handler serves the metrics in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// ObserveRequest records a finished HTTP request.
func (m *Metrics) ObserveRequest(s handlers.RequestStats) {
	route := Route(s.Path)
	method := methodLabel(s.Method)
	m.requests.WithLabelValues(route, method, strconv.Itoa(s.Status)).Inc()
	m.duration.WithLabelValues(route, method).Observe(s.Duration.Seconds())
	switch {
	case route == RouteV2Upload:
		m.bytesIn.Add(float64(s.BytesIn))
	case route == RouteV2Blob && s.Method == http.MethodGet:
		m.bytesOut.Add(float64(s.BytesOut))
	}
}

// Route classifies a request path. For /v2 it mirrors the dispatcher in
// handlers/routing.go: the last of /manifests/ or /blobs/ wins, so
// repository names containing those words are classified correctly.
func Route(path string) string {
	switch {
	case path == "/healthz" || path == "/readyz" || path == "/metrics":
		return RouteOps
	case strings.HasPrefix(path, "/static/"):
		return RouteStatic
	case path == "/api" || strings.HasPrefix(path, "/api/"):
		return RouteAPI
	case path == "/v2" || strings.HasPrefix(path, "/v2/"):
		return v2Route(path)
	default:
		return RouteUI
	}
}

func v2Route(path string) string {
	manifests := strings.LastIndex(path, "/manifests/")
	blobs := strings.LastIndex(path, "/blobs/")
	switch {
	case manifests > blobs:
		return RouteV2Manifest
	case blobs >= 0 && strings.HasPrefix(path[blobs:], "/blobs/uploads"):
		return RouteV2Upload
	case blobs >= 0:
		return RouteV2Blob
	default:
		return RouteV2Other
	}
}

var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodOptions: true,
}

// methodLabel bounds the method label: clients can send arbitrary methods.
func methodLabel(m string) string {
	if knownMethods[m] {
		return m
	}
	return "other"
}

// collector is what InstrumentCollector wraps (handlers.Collector).
type collector interface {
	Collect(ctx context.Context, opts services.GCOptions) (*services.GCResult, error)
}

// InstrumentedCollector records GC metrics around another collector.
type InstrumentedCollector struct {
	next collector
	m    *Metrics
	now  func() time.Time
}

var _ handlers.Collector = (*InstrumentedCollector)(nil)

// InstrumentCollector returns c wrapped so that every run is recorded.
func (m *Metrics) InstrumentCollector(c collector) *InstrumentedCollector {
	return &InstrumentedCollector{next: c, m: m, now: time.Now}
}

// Collect runs the wrapped collector and records the run.
func (c *InstrumentedCollector) Collect(ctx context.Context, opts services.GCOptions) (*services.GCResult, error) {
	start := c.now()
	res, err := c.next.Collect(ctx, opts)
	c.m.gcDuration.Observe(c.now().Sub(start).Seconds())
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		c.m.gcRuns.WithLabelValues("cancelled").Inc()
	case err != nil:
		c.m.gcRuns.WithLabelValues("error").Inc()
	default:
		c.m.gcRuns.WithLabelValues("success").Inc()
		c.m.gcLastSuccess.Set(float64(c.now().Unix()))
		if !res.DryRun {
			c.m.gcDeleted.Add(float64(res.DeletedBlobs))
			c.m.gcFreed.Add(float64(res.FreedBytes))
		}
	}
	return res, err
}
