package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// readyTimeout bounds the readiness check's dependency probe.
const readyTimeout = 2 * time.Second

// Pinger reports whether a dependency is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler serves /healthz (liveness) and /readyz (readiness).
type HealthHandler struct {
	db Pinger
}

// NewHealthHandler returns a HealthHandler whose readiness depends on db.
func NewHealthHandler(db Pinger) *HealthHandler {
	return &HealthHandler{db: db}
}

// Mount registers the health routes on r. RequireAuth exempts them.
func (h *HealthHandler) Mount(r chi.Router) {
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		r.MethodFunc(m, "/healthz", h.live)
		r.MethodFunc(m, "/readyz", h.ready)
	}
}

// live reports that the process is serving HTTP. It deliberately checks no
// dependencies: a failing database must not get the process restarted.
func (h *HealthHandler) live(w http.ResponseWriter, _ *http.Request) {
	plain(w, http.StatusOK, "ok")
}

// ready reports whether the database answers within readyTimeout.
func (h *HealthHandler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := h.db.Ping(ctx); err != nil {
		slog.WarnContext(r.Context(), "not ready: database ping failed", "err", err)
		plain(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	plain(w, http.StatusOK, "ok")
}

func plain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte(body + "\n"))
}
