package handlers

import (
	"errors"
	"io"
	"net/http"

	"github.com/foundry/registry/internal/oci"
	"github.com/foundry/registry/internal/registry/storage"
)

// errBlobTooLarge is returned by the body reader once a blob upload passes
// the maximum blob size.
var errBlobTooLarge = errors.New("blob exceeds maximum size")

// limitBody returns r.Body limited so that a blob already offset bytes long
// cannot grow past h.maxBlobSize. If the declared Content-Length already
// exceeds the limit it answers 413 itself, without reading, and returns
// false.
func (h *RegistryHandler) limitBody(w http.ResponseWriter, r *http.Request, offset int64) (io.Reader, bool) {
	if h.maxBlobSize <= 0 {
		return r.Body, true
	}
	remaining := max(h.maxBlobSize-offset, 0)
	if r.ContentLength > remaining {
		blobLimitError(w, errBlobTooLarge)
		return nil, false
	}
	return &limitedReader{r: r.Body, remaining: remaining}, true
}

// limitedReader fails with errBlobTooLarge as soon as more than remaining
// bytes arrive (io.LimitReader would silently truncate instead).
type limitedReader struct {
	r         io.Reader
	remaining int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	// Ask for one byte more than allowed so an oversized body is detected
	// rather than truncated.
	if int64(len(p)) > l.remaining+1 {
		p = p[:l.remaining+1]
	}
	n, err := l.r.Read(p)
	if int64(n) > l.remaining {
		return 0, errBlobTooLarge
	}
	l.remaining -= int64(n)
	return n, err
}

func isBlobLimitError(err error) bool {
	return errors.Is(err, errBlobTooLarge) || errors.Is(err, storage.ErrQuotaExceeded)
}

// blobLimitError answers 413 SIZE_INVALID if err is a size or quota
// rejection and reports whether it did. The connection is closed after the
// response: the client may still be sending a large body, and draining it
// would defeat the early abort.
func blobLimitError(w http.ResponseWriter, err error) bool {
	msg := ""
	switch {
	case errors.Is(err, errBlobTooLarge):
		msg = "blob exceeds maximum size"
	case errors.Is(err, storage.ErrQuotaExceeded):
		msg = "storage quota exceeded"
	default:
		return false
	}
	w.Header().Set("Connection", "close")
	registryError(w, http.StatusRequestEntityTooLarge, oci.ErrCodeSizeInvalid, msg)
	return true
}
