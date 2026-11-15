// Command foundry-registry runs the Foundry OCI registry.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/foundry/registry/internal/auth"
	"github.com/foundry/registry/internal/db"
	"github.com/foundry/registry/internal/registry"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
	"github.com/foundry/registry/internal/ui"
	"github.com/foundry/registry/web"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", envOr("FOUNDRY_ADDR", ":8080"), "listen address")
	dataDir := flag.String("data", envOr("FOUNDRY_DATA", "./data"), "data directory")
	gcGrace := flag.Duration("gc-grace", time.Hour, "GC keeps unreferenced blobs younger than this (protects in-flight pushes)")
	uploadTTL := flag.Duration("upload-ttl", 24*time.Hour, "GC expires upload sessions older than this")
	htpasswdPath := flag.String("auth-htpasswd", os.Getenv("FOUNDRY_AUTH_HTPASSWD"), "bcrypt htpasswd file; enables authentication")
	anonPullDefault, err := envBool("FOUNDRY_AUTH_ANONYMOUS_PULL", true)
	if err != nil {
		return err
	}
	anonPull := flag.Bool("auth-anonymous-pull", anonPullDefault, "with authentication enabled, allow unauthenticated pulls and browsing")
	flag.Parse()

	var users *auth.Htpasswd
	if *htpasswdPath != "" {
		if users, err = auth.LoadHtpasswd(*htpasswdPath); err != nil {
			return err
		}
		log.Printf("authentication enabled (anonymous pull: %t)", *anonPull)
		if !isLoopback(*addr) {
			log.Printf("WARNING: authentication is enabled on non-loopback address %s; credentials are sent in cleartext unless TLS is terminated in front of the registry", *addr)
		}
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		return err
	}

	dbPath := filepath.Join(*dataDir, "registry.db")
	database, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer database.Close()

	blobStore, err := storage.NewFilesystemBlobStore(*dataDir)
	if err != nil {
		return err
	}
	uploadStore, err := storage.NewUploadStore(*dataDir)
	if err != nil {
		return err
	}
	tmpls, err := ui.Load()
	if err != nil {
		return err
	}

	cfg := registry.Config{
		Addr:          *addr,
		DataDir:       *dataDir,
		DBPath:        dbPath,
		GCGracePeriod: *gcGrace,
		UploadTTL:     *uploadTTL,
		Users:         users,
		AnonymousPull: *anonPull,
	}
	srv, err := registry.New(cfg, services.NewRepositoryService(database), blobStore, uploadStore, tmpls, web.StaticFS)
	if err != nil {
		return err
	}
	httpSrv := srv.HTTPServer()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Foundry Registry listening on %s  (data: %s)", *addr, *dataDir)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Printf("shutting down…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

// envBool parses a boolean environment variable, failing on invalid values.
func envBool(key string, fallback bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean %q", key, v)
	}
	return b, nil
}

// isLoopback reports whether listen address addr binds only to loopback.
// An empty host (":8080") binds all interfaces.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
