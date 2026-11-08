package handlers

import (
	"encoding/json"
	"log"
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
		log.Printf("json encode error: %v", err)
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
	log.Printf("server error: %s %q: %v", r.Method, r.URL.Path, err) //nolint:gosec // G706: %q escapes control chars
	registryError(w, http.StatusInternalServerError, oci.ErrCodeUnknown, "internal server error")
}

// apiServerError is serverError for the admin JSON API.
func apiServerError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("server error: %s %q: %v", r.Method, r.URL.Path, err) //nolint:gosec // G706: %q escapes control chars
	jsonError(w, http.StatusInternalServerError, "internal server error")
}
