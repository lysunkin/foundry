package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens (or creates) the SQLite database at path, applies migrations,
// and configures pragmas appropriate for a single-process registry.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=ON")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Single writer – avoid "database is locked" in production.
	db.SetMaxOpenConns(1)

	if err := runMigrations(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}
	return db, nil
}

func runMigrations(ctx context.Context, db *sql.DB) error {
	// Ensure the migrations table exists first.
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
	)`)
	if err != nil {
		return err
	}

	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}

	// Sort by filename so order is deterministic.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%d_", &version); err != nil {
			return fmt.Errorf("parse migration version from %s: %w", entry.Name(), err)
		}

		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue // already applied
		}

		content, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		slog.InfoContext(ctx, "applied migration", "migration", entry.Name())
	}
	return nil
}
