package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Open with a cancelled context fails without applying migrations, and a
// later Open succeeds from scratch.
func TestOpen_CancelledContext(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cancel.db")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if d, err := Open(ctx, path); !errors.Is(err, context.Canceled) {
		if d != nil {
			d.Close()
		}
		t.Fatalf("Open err = %v, want context.Canceled", err)
	}

	d, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open after cancelled attempt: %v", err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("applied migrations = %d, want 2", n)
	}
}
