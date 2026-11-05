package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/foundry/registry/internal/db"
	"github.com/foundry/registry/internal/registry"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
	"github.com/foundry/registry/internal/ui"
	"github.com/foundry/registry/web"
)

func main() {
	addr := flag.String("addr", envOr("FOUNDRY_ADDR", ":8080"), "listen address")
	dataDir := flag.String("data", envOr("FOUNDRY_DATA", "./data"), "data directory")
	flag.Parse()

	// Ensure data directory exists.
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// Open database.
	dbPath := filepath.Join(*dataDir, "registry.db")
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Storage layer.
	blobStore, err := storage.NewFilesystemBlobStore(*dataDir)
	if err != nil {
		log.Fatalf("blob store: %v", err)
	}
	uploadStore, err := storage.NewUploadStore(*dataDir)
	if err != nil {
		log.Fatalf("upload store: %v", err)
	}

	// Service layer.
	repoSvc := services.NewRepositoryService(database)

	// Templates.
	tmpls, err := ui.Load()
	if err != nil {
		log.Fatalf("templates: %v", err)
	}

	// Build server.
	cfg := registry.Config{
		Addr:    *addr,
		DataDir: *dataDir,
		DBPath:  dbPath,
	}
	srv := registry.New(cfg, repoSvc, blobStore, uploadStore, tmpls, web.StaticFS)

	log.Printf("Foundry Registry listening on %s  (data: %s)", *addr, *dataDir)
	if err := http.ListenAndServe(*addr, srv); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
