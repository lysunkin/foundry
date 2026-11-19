package handlers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/foundry/registry/internal/logging"
	"github.com/foundry/registry/internal/registry/handlers"
)

// captureLogs routes slog's default logger to a buffer (JSON, debug level)
// for the rest of the test. Tests using it must not call t.Parallel: the
// default logger is process-wide, and parallel tests only start once all
// sequential tests have finished.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	logger, err := logging.New(&buf, logging.FormatJSON, slog.LevelDebug)
	if err != nil {
		t.Fatal(err)
	}
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// logRecords parses buf's JSON lines, keeping those with message msg.
func logRecords(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func TestRequestID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, incoming string
		keep           bool
	}{
		{"valid incoming kept", "abc.DEF_123-x", true},
		{"missing generated", "", false},
		{"invalid chars replaced", "bad id;drop", false},
		{"too long replaced", strings.Repeat("a", 65), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var seen string
			h := handlers.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = logging.RequestID(r.Context())
			}))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tc.incoming != "" {
				req.Header.Set(handlers.RequestIDHeader, tc.incoming)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get(handlers.RequestIDHeader)
			if got != seen {
				t.Errorf("header %q != context %q", got, seen)
			}
			if tc.keep {
				if got != tc.incoming {
					t.Errorf("ID = %q, want %q", got, tc.incoming)
				}
			} else if _, err := uuid.Parse(got); err != nil {
				t.Errorf("ID %q is not a generated UUID", got)
			}
		})
	}
}

type recordingObserver struct{ stats []handlers.RequestStats }

func (o *recordingObserver) ObserveRequest(s handlers.RequestStats) { o.stats = append(o.stats, s) }

func TestAccessLog(t *testing.T) {
	buf := captureLogs(t)
	obs := &recordingObserver{}
	app := http.NewServeMux()
	app.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		w.Write(append(b, b...))
	})
	app.HandleFunc("/healthz", func(http.ResponseWriter, *http.Request) {}) // writes nothing: 200
	h := handlers.RequestID(handlers.AccessLog(obs)(
		handlers.RequireAuth(handlers.AuthConfig{Users: newUsers(t), AnonymousRead: true})(app)))

	serve := func(method, target, body, authz string) {
		req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
		req.Header.Set(handlers.RequestIDHeader, "rid-1")
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	serve(http.MethodPost, "/echo?token=hidden", "abc", basic("alice", "secret"))
	serve(http.MethodGet, "/healthz", "", "")
	serve(http.MethodGet, "/nope", "", "")

	recs := logRecords(t, buf, "request")
	if len(recs) != 3 {
		t.Fatalf("got %d request records: %s", len(recs), buf)
	}
	want := map[string]any{
		"level": "INFO", "method": "POST", "path": "/echo", "status": 201.0,
		"bytes": 6.0, "user": "alice", "request_id": "rid-1", "remote_addr": "192.0.2.1:1234",
	}
	for k, v := range want {
		if recs[0][k] != v {
			t.Errorf("%s = %v, want %v", k, recs[0][k], v)
		}
	}
	if _, ok := recs[0]["duration_ms"]; !ok {
		t.Error("duration_ms missing")
	}
	if strings.Contains(buf.String(), "hidden") || strings.Contains(buf.String(), "secret") {
		t.Errorf("query string or credentials logged: %s", buf)
	}
	if recs[1]["level"] != "DEBUG" || recs[1]["status"] != 200.0 {
		t.Errorf("health record = %v, want DEBUG with status 200", recs[1])
	}
	if _, ok := recs[2]["user"]; ok || recs[2]["status"] != 404.0 {
		t.Errorf("anonymous record = %v, want status 404 and no user", recs[2])
	}

	if len(obs.stats) != 3 {
		t.Fatalf("observer got %d requests", len(obs.stats))
	}
	if s := obs.stats[0]; s.Method != http.MethodPost || s.Path != "/echo" || s.Status != 201 || s.BytesIn != 3 || s.BytesOut != 6 {
		t.Errorf("stats = %+v", s)
	}
}

func TestRecover(t *testing.T) {
	buf := captureLogs(t)
	for _, tc := range []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
		wantBody   string
	}{
		{"panic before writing", func(http.ResponseWriter, *http.Request) { panic("boom") },
			http.StatusInternalServerError, "internal server error\n"},
		{"panic after writing", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte("partial"))
			panic("boom")
		}, http.StatusAccepted, "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			obs := &recordingObserver{}
			h := handlers.AccessLog(obs)(handlers.Recover(tc.handler))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil))

			if rec.Code != tc.wantStatus || rec.Body.String() != tc.wantBody {
				t.Errorf("response = %d %q, want %d %q", rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if len(obs.stats) != 1 || obs.stats[0].Status != tc.wantStatus {
				t.Errorf("observed %+v, want status %d", obs.stats, tc.wantStatus)
			}
			panics := logRecords(t, buf, "panic serving request")
			if len(panics) != 1 || panics[0]["level"] != "ERROR" || !strings.Contains(panics[0]["stack"].(string), "goroutine") {
				t.Errorf("panic not logged with stack: %s", buf)
			}
		})
	}

	t.Run("ErrAbortHandler passes through", func(t *testing.T) {
		defer func() {
			if v := recover(); v == nil || !errors.Is(v.(error), http.ErrAbortHandler) {
				t.Errorf("recovered %v, want http.ErrAbortHandler", v)
			}
		}()
		h := handlers.Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil))
	})
}

// The access log's response wrapper must not hide the server's writer from
// http.ResponseController (used by AbortReadsOnCancel inside it and by
// streaming handlers).
func TestAccessLog_ResponseControllerReachesServerWriter(t *testing.T) {
	t.Parallel()
	errCh := make(chan error, 1)
	srv := httptest.NewServer(handlers.AccessLog(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("x"))
		errCh <- http.NewResponseController(w).Flush()
	})))
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := <-errCh; err != nil {
		t.Errorf("Flush through wrapper: %v", err)
	}
}
