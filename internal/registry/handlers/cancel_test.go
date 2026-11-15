package handlers_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/foundry/registry/internal/registry/handlers"
)

// When the server cancels a request (BaseContext cancelled on shutdown), a
// handler blocked reading a stalled request body must be released.
func TestAbortReadsOnCancel_UnblocksStalledBody(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	readErr := make(chan error, 1)
	h := handlers.AbortReadsOnCancel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 3)
		if _, err := io.ReadFull(r.Body, buf); err != nil {
			readErr <- err
			return
		}
		close(started)
		_, err := r.Body.Read(buf) // the client sends nothing more
		readErr <- err
	}))

	base, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	defer cancel()
	srv := httptest.NewUnstartedServer(h)
	srv.Config.BaseContext = func(net.Listener) context.Context { return base }
	srv.Start()
	defer srv.Close()

	body, bodyW := io.Pipe()
	defer bodyW.Close()
	go func() {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, srv.URL, body)
		if resp, err := srv.Client().Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	if _, err := bodyW.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	<-started

	cancel()
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("body read succeeded, want an error after cancellation")
		}
	case <-time.After(5 * time.Second): // failure guard only, not synchronisation
		t.Fatal("handler still blocked reading the body 5s after cancellation")
	}
}

// Requests that finish normally are unaffected, including the next request
// on the same keep-alive connection.
func TestAbortReadsOnCancel_KeepAliveUnaffected(t *testing.T) {
	t.Parallel()
	h := handlers.AbortReadsOnCancel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Write(b)
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	for i := range 3 {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader("ping"))
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(got) != "ping" {
			t.Fatalf("request %d: %d %q", i, resp.StatusCode, got)
		}
	}
}
