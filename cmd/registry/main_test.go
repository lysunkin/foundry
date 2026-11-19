package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIsLoopback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"127.1.2.3:8080", true},
		{"[::1]:8080", true},
		{"localhost:8080", true},
		{":8080", false},
		{"0.0.0.0:8080", false},
		{"[::]:8080", false},
		{"192.168.1.10:8080", false},
		{"example.com:8080", false},
		{"garbage", false},
	}
	for _, tc := range tests {
		if got := isLoopback(tc.addr); got != tc.want {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestEnvBool(t *testing.T) {
	tests := []struct {
		name, val     string
		fallback      bool
		want, wantErr bool
	}{
		{"unset uses fallback", "", true, true, false},
		{"false", "false", true, false, false},
		{"zero", "0", true, false, false},
		{"true", "true", false, true, false},
		{"invalid", "yes please", true, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FOUNDRY_TEST_BOOL", tc.val)
			got, err := envBool("FOUNDRY_TEST_BOOL", tc.fallback)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("envBool(%q) = %v, %v; want %v, err=%v", tc.val, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// startServer serves h on a loopback port with request contexts derived from
// a cancellable base, as run() does.
func startServer(t *testing.T, h http.Handler) (*http.Server, string, context.CancelFunc) {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	t.Cleanup(cancel)
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: time.Second,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
	go srv.Serve(ln) //nolint:errcheck // returns ErrServerClosed after shutdown
	return srv, "http://" + ln.Addr().String(), cancel
}

func TestShutdownIdle(t *testing.T) {
	t.Parallel()
	srv, _, cancel := startServer(t, http.NotFoundHandler())
	if err := shutdown(t.Context(), srv, cancel, time.Second, time.Second); err != nil {
		t.Fatalf("shutdown = %v, want nil", err)
	}
}

// A request still running when the drain period expires has its context
// cancelled, and shutdown waits for it to return.
func TestShutdownCancelsStuckRequests(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	finished := make(chan error, 1)
	srv, url, cancel := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done() // a long GC, until cancelled
		finished <- r.Context().Err()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	go func() {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/api/gc", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	<-started

	if err := shutdown(t.Context(), srv, cancel, 10*time.Millisecond, 5*time.Second); err != nil {
		t.Fatalf("shutdown = %v, want nil", err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("handler context err = %v, want context.Canceled", err)
		}
	default:
		t.Fatal("shutdown returned before the cancelled handler finished")
	}
}

// If a handler ignores cancellation, shutdown gives up after the abort period
// and reports it.
func TestShutdownReportsUnresponsiveHandlers(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv, url, cancel := startServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release // ignores its context
	}))
	go func() {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	<-started

	err := shutdown(t.Context(), srv, cancel, 10*time.Millisecond, 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown = %v, want DeadlineExceeded", err)
	}
}

func TestNewLogger(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		format, level string
		wantErr       bool
	}{
		{"text", "info", false},
		{"json", "debug", false},
		{"json", "WARN", false},
		{"yaml", "info", true},
		{"text", "verbose", true},
		{"", "info", true},
	} {
		var buf bytes.Buffer
		logger, err := newLogger(&buf, tc.format, tc.level)
		if (err != nil) != tc.wantErr {
			t.Errorf("newLogger(%q, %q) error = %v, want error %t", tc.format, tc.level, err, tc.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		logger.Error("probe")
		if !strings.Contains(buf.String(), "probe") {
			t.Errorf("newLogger(%q, %q) wrote %q", tc.format, tc.level, buf.String())
		}
	}
}

func TestParseSize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"0", 0, false},
		{"1048576", 1 << 20, false},
		{"512MiB", 512 << 20, false},
		{"10GB", 10_000_000_000, false},
		{"10 gb", 10_000_000_000, false},
		{"1KiB", 1024, false},
		{"7b", 7, false},
		{"2TiB", 2 << 40, false},
		{"", 0, true},
		{"GB", 0, true},
		{"-1", 0, true},
		{"1.5GB", 0, true},
		{"10XB", 0, true},
		{"10G", 0, true},
		{"9999999TiB", 0, true},
		{"99999999999999999999", 0, true},
	} {
		got, err := parseSize(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("parseSize(%q) = %d, %v; want %d, error %t", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}
