package main

import (
	"strings"
	"testing"
	"time"
)

func TestReadPassword(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"newline", "s3cret\n", "s3cret", false},
		{"crlf", "s3cret\r\n", "s3cret", false},
		{"no newline", "s3cret", "s3cret", false},
		{"first line only", "s3cret\nextra\n", "s3cret", false},
		{"keeps spaces", " pass word \n", " pass word ", false},
		{"empty", "", "", true},
		{"blank line", "\n", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := readPassword(strings.NewReader(tc.in))
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("readPassword(%q) = %q, %v; want %q, err=%v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestRequestTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		command  string
		timeout  time.Duration
		explicit bool
		want     time.Duration
	}{
		{"default", "repos", defaultTimeout, false, defaultTimeout},
		{"explicit", "tags", 5 * time.Second, true, 5 * time.Second},
		{"explicit none", "delete", 0, true, 0},
		{"gc default has none", "gc", defaultTimeout, false, 0},
		{"gc explicit", "gc", time.Minute, true, time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := requestTimeout(tc.command, tc.timeout, tc.explicit); got != tc.want {
				t.Errorf("requestTimeout(%q, %v, %v) = %v, want %v", tc.command, tc.timeout, tc.explicit, got, tc.want)
			}
		})
	}
}
