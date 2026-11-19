package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// ErrQuotaExceeded is returned by writes that would take stored bytes past
// the configured quota.
var ErrQuotaExceeded = errors.New("storage quota exceeded")

// Usage tracks the bytes stored under data/blobs and data/uploads and
// optionally enforces a quota on them. The stores reserve bytes before every
// write and release them when they remove a file, so the count equals the
// bytes on disk and concurrent writers together never exceed the quota.
//
// Known drift: if two writers commit the same digest at the same instant,
// both renames succeed and one file replaces the other while both stay
// counted. The count can only be too high, and a restart recomputes it.
//
// A nil *Usage is valid and tracks nothing.
type Usage struct {
	limit  int64 // 0: unlimited
	used   atomic.Int64
	warned atomic.Bool // usage is at or above warnRatio of limit
}

// warnRatio is the share of the quota at which a warning is logged.
const warnRatio = 0.9

// NewUsage returns a Usage with quota limit bytes (0 for no quota).
func NewUsage(limit int64) *Usage {
	return &Usage{limit: limit}
}

// Init sets the usage to the size of all files under dataDir/blobs and
// dataDir/uploads. Call it once at startup, before serving.
func (u *Usage) Init(ctx context.Context, dataDir string) error {
	start := time.Now()
	var total int64
	for _, dir := range []string{filepath.Join(dataDir, "blobs"), filepath.Join(dataDir, "uploads")} {
		err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			total += info.Size()
			return nil
		})
		if err != nil {
			return fmt.Errorf("measure storage usage: %w", err)
		}
	}
	u.used.Store(total)
	slog.InfoContext(ctx, "storage usage measured", "used_bytes", total, "quota_bytes", u.limit,
		"duration_ms", time.Since(start).Milliseconds())
	if u.limit > 0 && total > u.limit {
		slog.WarnContext(ctx, "storage usage exceeds the quota; writes are rejected until GC frees space",
			"used_bytes", total, "quota_bytes", u.limit)
	}
	u.checkWarn(total)
	return nil
}

// Used returns the bytes currently stored.
func (u *Usage) Used() int64 {
	if u == nil {
		return 0
	}
	return u.used.Load()
}

// Limit returns the quota in bytes, 0 if unlimited.
func (u *Usage) Limit() int64 {
	if u == nil {
		return 0
	}
	return u.limit
}

// reserve accounts for n bytes about to be written, or fails with
// ErrQuotaExceeded without changing the usage.
func (u *Usage) reserve(n int64) error {
	if u == nil || n <= 0 {
		return nil
	}
	for {
		cur := u.used.Load()
		next := cur + n
		if u.limit > 0 && next > u.limit {
			return ErrQuotaExceeded
		}
		if u.used.CompareAndSwap(cur, next) {
			u.checkWarn(next)
			return nil
		}
	}
}

// release accounts for n bytes removed from disk.
func (u *Usage) release(n int64) {
	if u == nil || n <= 0 {
		return
	}
	u.checkWarn(u.used.Add(-n))
}

// checkWarn logs once when used crosses warnRatio of the quota and re-arms
// when it drops back below.
func (u *Usage) checkWarn(used int64) {
	if u.limit <= 0 {
		return
	}
	high := float64(used) >= warnRatio*float64(u.limit)
	if high && u.warned.CompareAndSwap(false, true) {
		slog.Warn("storage usage is above 90% of the quota", "used_bytes", used, "quota_bytes", u.limit)
	} else if !high && u.warned.CompareAndSwap(true, false) {
		slog.Info("storage usage is back below 90% of the quota", "used_bytes", used, "quota_bytes", u.limit)
	}
}

// removeFile removes path and, if that succeeded, releases size bytes.
func (u *Usage) removeFile(path string, size int64) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	u.release(size)
	return nil
}

// quotaWriter reserves bytes before writing them. The first free bytes are
// not reserved: they overwrite data that is already counted (an upload chunk
// re-sent at an offset below the file's current size).
type quotaWriter struct {
	w     io.Writer
	u     *Usage
	free  int64
	added int64 // bytes reserved and written
}

func (q *quotaWriter) Write(p []byte) (int, error) {
	overwrite := min(q.free, int64(len(p)))
	grow := int64(len(p)) - overwrite
	if err := q.u.reserve(grow); err != nil {
		return 0, err
	}
	n, err := q.w.Write(p)
	written := int64(n)
	q.free -= min(overwrite, written)
	grown := max(written-overwrite, 0)
	q.u.release(grow - grown) // reserved but not written
	q.added += grown
	return n, err
}

// Option configures a store.
type Option func(*storeOptions)

type storeOptions struct {
	usage *Usage
}

// WithUsage makes a store account its files in u.
func WithUsage(u *Usage) Option {
	return func(o *storeOptions) { o.usage = u }
}

func applyOptions(opts []Option) storeOptions {
	var o storeOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}
