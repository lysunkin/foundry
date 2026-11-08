// Command foundry-registry runs the Foundry OCI registry.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

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
	flag.Parse()

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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
