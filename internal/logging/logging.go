// Package logging builds the server's structured logger and carries the
// request ID through contexts so that every record logged with a request's
// context is attributed to it.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Supported output formats.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// New returns a logger writing format ("text" or "json") records at or above
// level to w. Records logged with a context carrying a request ID (see
// WithRequestID) get a request_id attribute.
func New(w io.Writer, format string, level slog.Level) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch format {
	case FormatText:
		h = slog.NewTextHandler(w, opts)
	case FormatJSON:
		h = slog.NewJSONHandler(w, opts)
	default:
		return nil, fmt.Errorf("invalid log format %q (want %s or %s)", format, FormatText, FormatJSON)
	}
	return slog.New(contextHandler{h}), nil
}

// ParseLevel parses debug, info, warn or error (case-insensitive).
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("invalid log level %q (want debug, info, warn or error)", s)
}

type requestIDKey struct{}

// WithRequestID returns a copy of ctx carrying the request ID id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID carried by ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// contextHandler adds the context's request ID to each record.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
