package handlers

import (
	"encoding/json"
	"log"
	"net/http"
)

// registryError writes an OCI-spec error response.
func registryError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"errors": []map[string]string{
			{"code": code, "message": message},
		},
	})
}

// jsonError writes a plain JSON error.
func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// jsonResponse serialises v to JSON and writes it with status.
func jsonResponse(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("json encode error: %v", err)
	}
}

func serverError(w http.ResponseWriter, err error) {
	log.Printf("server error: %v", err)
	registryError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
}
