package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/foundry/registry/internal/logging"
)

func TestNew_AddsRequestID(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log, err := logging.New(&buf, logging.FormatJSON, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := logging.WithRequestID(t.Context(), "req-1")
	log.With("component", "test").InfoContext(ctx, "hello", "n", 1)
	log.InfoContext(context.Background(), "no id")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	for k, want := range map[string]any{"msg": "hello", "request_id": "req-1", "component": "test", "n": 1.0} {
		if rec[k] != want {
			t.Errorf("%s = %v, want %v", k, rec[k], want)
		}
	}
	if strings.Contains(lines[1], "request_id") {
		t.Errorf("record without request ID has one: %s", lines[1])
	}
}

func TestNew_FormatAndLevel(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log, err := logging.New(&buf, logging.FormatText, slog.LevelWarn)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hidden")
	log.Warn("shown", "k", "v")
	if got := buf.String(); strings.Contains(got, "hidden") || !strings.Contains(got, "msg=shown k=v") {
		t.Errorf("output = %q", got)
	}

	if _, err := logging.New(&buf, "xml", slog.LevelInfo); err == nil {
		t.Error("New accepted format xml")
	}
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"debug", slog.LevelDebug, false},
		{"INFO", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"warning", 0, true},
		{"", 0, true},
		{"info+2", 0, true},
	} {
		got, err := logging.ParseLevel(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v, err %t", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}
