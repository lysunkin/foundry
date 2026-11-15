package storage

import (
	"context"
	"io"
)

// copyContext is io.Copy that stops with ctx.Err() once ctx is done. It checks
// before every Read; a Read already blocked in src is not interrupted, but for
// request bodies net/http unblocks it when the request is cancelled.
func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, readerFunc(func(p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return src.Read(p)
	}))
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
