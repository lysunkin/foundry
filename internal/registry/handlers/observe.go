package handlers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/foundry/registry/internal/logging"
)

// RequestIDHeader carries the request ID in requests and responses.
const RequestIDHeader = "X-Request-Id"

// validRequestID bounds what a client may choose as request ID, so it is safe
// to log and echo.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestID assigns each request an ID: the client's X-Request-Id if it is
// well-formed, otherwise a new UUID. The ID is stored in the context (see
// logging.RequestID) and echoed in the response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}

// RequestStats describes a finished request.
type RequestStats struct {
	Method   string
	Path     string
	Status   int
	BytesIn  int64 // request body bytes read by the handler
	BytesOut int64 // response body bytes written
	Duration time.Duration
}

// RequestObserver receives the stats of every finished request (e.g. to
// record metrics).
type RequestObserver interface {
	ObserveRequest(RequestStats)
}

// quietPaths are probed or scraped frequently; their access log records are
// logged at debug level.
var quietPaths = map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true}

// AccessLog logs one record per request (method, path, status, bytes,
// duration, remote address, request ID and, if authenticated, user) and
// reports it to obs, which may be nil. Query strings and bodies are never
// logged.
//
// It must run inside AbortReadsOnCancel and RequestID, and outside Recover
// and RequireAuth.
func AccessLog(obs RequestObserver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			info := &requestInfo{}
			rw := &responseWriter{ResponseWriter: w}
			ctx := context.WithValue(r.Context(), requestInfoKey{}, info)
			r = r.WithContext(ctx)
			var body *countingReader
			if r.Body != nil {
				body = &countingReader{ReadCloser: r.Body}
				r.Body = body
			}

			next.ServeHTTP(rw, r)

			stats := RequestStats{
				Method:   r.Method,
				Path:     r.URL.Path,
				Status:   rw.status(),
				BytesOut: rw.bytes,
				Duration: time.Since(start),
			}
			if body != nil {
				stats.BytesIn = body.n
			}
			level := slog.LevelInfo
			if quietPaths[r.URL.Path] {
				level = slog.LevelDebug
			}
			attrs := []slog.Attr{
				slog.String("method", stats.Method),
				slog.String("path", stats.Path),
				slog.Int("status", stats.Status),
				slog.Int64("bytes", stats.BytesOut),
				slog.Int64("duration_ms", stats.Duration.Milliseconds()),
				slog.String("remote_addr", r.RemoteAddr),
			}
			if info.user != "" {
				attrs = append(attrs, slog.String("user", info.user))
			}
			slog.LogAttrs(ctx, level, "request", attrs...)
			if obs != nil {
				obs.ObserveRequest(stats)
			}
		})
	}
}

// Recover turns a handler panic into a logged error and a generic 500. It
// must run inside AccessLog so the 500 is logged and counted.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v) // net/http's deliberate abort; let it through
			}
			slog.ErrorContext(ctx, "panic serving request",
				"method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
			if rw, ok := w.(*responseWriter); ok && rw.wroteHeader {
				return // too late for an error response
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

// requestInfo collects per-request details for the access log that are only
// known deeper in the chain.
type requestInfo struct {
	user string // authenticated user, set by RequireAuth
}

type requestInfoKey struct{}

// setRequestUser records the authenticated user for the access log.
func setRequestUser(ctx context.Context, user string) {
	if info, ok := ctx.Value(requestInfoKey{}).(*requestInfo); ok {
		info.user = user
	}
}

// responseWriter records the status code and body size.
type responseWriter struct {
	http.ResponseWriter
	code        int
	bytes       int64
	wroteHeader bool
}

func (w *responseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.code = code
		w.wroteHeader = code >= 200 // 1xx are informational; a final header follows
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// ReadFrom keeps the underlying writer's sendfile fast path for blob
// downloads (http.ServeContent copies with io.Copy).
func (w *responseWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	var n int64
	var err error
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(r)
	} else {
		n, err = io.Copy(struct{ io.Writer }{w.ResponseWriter}, r)
	}
	w.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer (Flush,
// deadlines).
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// status is the response status; a handler that writes nothing sends 200.
func (w *responseWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

// countingReader counts request body bytes read.
type countingReader struct {
	io.ReadCloser
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.n += int64(n)
	return n, err
}
