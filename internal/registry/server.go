// Package registry wires all components into a single HTTP server.
package registry

import (
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/foundry/registry/internal/registry/handlers"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
	"github.com/foundry/registry/internal/ui"
)

// Config holds runtime configuration for the server.
type Config struct {
	Addr    string // e.g. ":8080"
	DataDir string // e.g. "./data"
	DBPath  string // e.g. "./data/registry.db"

	// GC tuning; see services.GCOptions. Zero values disable the protection,
	// so production callers should set them explicitly (cmd/registry does).
	GCGracePeriod time.Duration
	UploadTTL     time.Duration
}

// Server wraps the HTTP router and its dependencies.
type Server struct {
	cfg    Config
	router chi.Router
}

// New creates a fully wired Server. staticFS must contain a "static" directory.
func New(
	cfg Config,
	repo *services.RepositoryService,
	blobs *storage.FilesystemBlobStore,
	uploads *storage.UploadStore,
	tmpls *ui.Templates,
	staticFS fs.FS,
) (*Server, error) {
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("static assets: %w", err)
	}
	gcOpts := services.GCOptions{GracePeriod: cfg.GCGracePeriod, UploadTTL: cfg.UploadTTL}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	handlers.NewRegistryHandler(repo, blobs, uploads).Mount(r)
	handlers.NewAPIHandler(repo, blobs, uploads, gcOpts).Mount(r)
	handlers.NewWebHandler(repo, blobs, uploads, gcOpts, tmpls).Mount(r)

	return &Server{cfg: cfg, router: r}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.router
}

// HTTPServer returns an *http.Server with production-safe timeouts.
//
// WriteTimeout and ReadTimeout are intentionally unset: blob uploads and
// downloads can legitimately take minutes. ReadHeaderTimeout guards against
// slowloris-style connections.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
