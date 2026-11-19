// Package registry wires all components into a single HTTP server.
package registry

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/foundry/registry/internal/auth"
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

	// Users enables HTTP Basic authentication when non-nil.
	Users *auth.Htpasswd
	// AnonymousPull allows unauthenticated reads (pull, browse) when Users
	// is set. The zero value requires authentication for everything.
	AnonymousPull bool
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
	// Outermost, so it sees the server's own ResponseWriter.
	r.Use(handlers.AbortReadsOnCancel)
	r.Use(handlers.RequestID)
	r.Use(handlers.AccessLog(nil))
	r.Use(handlers.Recover)
	// CSRF: reject unsafe cross-site browser requests. Always on; non-browser
	// clients (docker, regctl, curl) send no Sec-Fetch-Site/Origin and pass.
	r.Use(http.NewCrossOriginProtection().Handler)
	r.Use(handlers.RequireAuth(handlers.AuthConfig{Users: cfg.Users, AnonymousRead: cfg.AnonymousPull}))

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	gc := services.NewCollector(repo, blobs, uploads)
	handlers.NewRegistryHandler(repo, blobs, uploads).Mount(r)
	handlers.NewAPIHandler(repo, gc, gcOpts).Mount(r)
	handlers.NewWebHandler(repo, gc, gcOpts, tmpls).Mount(r)

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
//
// Every request context derives from base, so cancelling base cancels all
// in-flight requests (used when graceful shutdown times out).
func (s *Server) HTTPServer(base context.Context) *http.Server {
	return &http.Server{
		BaseContext:       func(net.Listener) context.Context { return base },
		Addr:              s.cfg.Addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
