package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/foundry/registry/internal/oci"
)

// registryError writes an OCI-spec error response.
func registryError(w http.ResponseWriter, status int, code, message string) {
	jsonResponse(w, status, map[string]any{
		"errors": []map[string]string{
			{"code": code, "message": message},
		},
	})
}

// jsonError writes a plain JSON error (admin API format).
func jsonError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

// jsonResponse serialises v to JSON and writes it with status.
func jsonResponse(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("encode json response", "err", err)
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	body = append(body, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

// serverError logs err and returns a generic OCI UNKNOWN error. Internal
// details are never sent to the client.
func serverError(w http.ResponseWriter, r *http.Request, err error) {
	if requestCancelled(r) {
		registryError(w, http.StatusServiceUnavailable, oci.ErrCodeUnknown, "request cancelled")
		return
	}
	slog.ErrorContext(r.Context(), "server error", "method", r.Method, "path", r.URL.Path, "err", err)
	registryError(w, http.StatusInternalServerError, oci.ErrCodeUnknown, "internal server error")
}

// apiServerError is serverError for the admin JSON API.
func apiServerError(w http.ResponseWriter, r *http.Request, err error) {
	if requestCancelled(r) {
		jsonError(w, http.StatusServiceUnavailable, "request cancelled")
		return
	}
	slog.ErrorContext(r.Context(), "server error", "method", r.Method, "path", r.URL.Path, "err", err)
	jsonError(w, http.StatusInternalServerError, "internal server error")
}

// requestCancelled reports whether r's context is done (client disconnected,
// client timeout, or server shutdown) and, if so, logs it at info level. Errors
// caused by cancellation are not server faults: callers respond 503 instead
// of 500 and skip the error log. The client has usually gone already; the
// status matters when the server cancelled (shutdown) and the client is still
// connected.
func requestCancelled(r *http.Request) bool {
	if r.Context().Err() == nil {
		return false
	}
	slog.InfoContext(r.Context(), "request cancelled", "method", r.Method, "path", r.URL.Path, "cause", context.Cause(r.Context()))
	return true
}
