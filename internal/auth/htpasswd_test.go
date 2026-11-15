package auth_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foundry/registry/internal/auth"
)

// Generated with `htpasswd -nbB alice secret` and `htpasswd -nbB -C 4 bob hunter2`.
const (
	aliceLine = "alice:$2y$05$3ke9XuFROHt.q0GkxCRo..y3m5s1Z4YuOAG2bn1mz4HM0dpwg3PyK"
	bobLine   = "bob:$2y$04$qaZvSltyCA22y7vnxil7G.q2/2GPnD3UtddfryaBJBfcY1/vE4MrK"
)

func TestParseHtpasswdErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"empty file", "", "no users defined"},
		{"only comments", "# nothing\n\n", "no users defined"},
		{"missing colon", "alice\n", "line 1: expected user:hash"},
		{"empty user", ":$2y$05$abc\n", "line 1: expected user:hash"},
		{"empty hash", "alice:\n", "line 1: expected user:hash"},
		{"apr1 md5", "carol:$apr1$ldlUCPB5$HsGaKzvBsvNvzXjvt3PLl0\n", `line 1: user "carol": not a bcrypt hash`},
		{"sha1", "dave:{SHA}EfatjsUqKYSrqv18O1FlA3hcIHI=\n", `line 1: user "dave": not a bcrypt hash`},
		{"plaintext", "erin:password\n", `line 1: user "erin": not a bcrypt hash`},
		{"duplicate", aliceLine + "\n# c\n" + aliceLine + "\n", `line 3: duplicate user "alice"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := auth.ParseHtpasswd(strings.NewReader(tc.content))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	content := "# users\n\n  " + aliceLine + "  \r\n" + bobLine // CRLF, padding, no trailing newline
	h, err := auth.ParseHtpasswd(strings.NewReader(content))
	if err != nil {
		t.Fatalf("ParseHtpasswd: %v", err)
	}
	tests := []struct {
		name, user, pass string
		want             bool
	}{
		{"alice correct", "alice", "secret", true},
		{"bob correct", "bob", "hunter2", true},
		{"wrong password", "alice", "hunter2", false},
		{"empty password", "alice", "", false},
		{"unknown user", "mallory", "secret", false},
		{"empty user", "", "", false},
		{"case-sensitive user", "Alice", "secret", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := h.Verify(tc.user, tc.pass); got != tc.want {
				t.Errorf("Verify(%q, %q) = %v, want %v", tc.user, tc.pass, got, tc.want)
			}
		})
	}
}

func TestLoadHtpasswd(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "htpasswd")
	if err := os.WriteFile(path, []byte(aliceLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := auth.LoadHtpasswd(path)
	if err != nil {
		t.Fatalf("LoadHtpasswd: %v", err)
	}
	if !h.Verify("alice", "secret") {
		t.Error("Verify(alice) = false, want true")
	}

	if _, err := auth.LoadHtpasswd(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("LoadHtpasswd(missing) = nil error, want error")
	}
}
