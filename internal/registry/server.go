// Package registry wires all components into a single HTTP server.
package registry

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/foundry/registry/internal/registry/handlers"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
)

// Config holds runtime configuration for the server.
type Config struct {
	Addr    string // e.g. ":8080"
	DataDir string // e.g. "./data"
	DBPath  string // e.g. "./data/registry.db"
}

// Server wraps the HTTP server and all its dependencies.
type Server struct {
	cfg     Config
	router  chi.Router
	repo    *services.RepositoryService
	blobs   *storage.FilesystemBlobStore
	uploads *storage.UploadStore
}

// New creates a fully wired Server.
func New(
	cfg Config,
	repo *services.RepositoryService,
	blobs *storage.FilesystemBlobStore,
	uploads *storage.UploadStore,
	tmpls *template.Template,
	staticFS embed.FS,
) *Server {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// Static assets — the embed FS root is "static/", served at /static/.
	staticSub, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Registry API v2
	registryH := handlers.NewRegistryHandler(repo, blobs, uploads)
	registryH.Mount(r)

	// Admin API
	apiH := handlers.NewAPIHandler(repo, blobs)
	apiH.Mount(r)

	// Web UI
	webH := handlers.NewWebHandler(repo, tmpls)
	webH.Mount(r)

	return &Server{
		cfg:     cfg,
		router:  r,
		repo:    repo,
		blobs:   blobs,
		uploads: uploads,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) Handler() http.Handler {
	return s.router
}
