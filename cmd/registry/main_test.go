package main

import (
	"context"
	"errors"
	"net"
	"net/http"
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
