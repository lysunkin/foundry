// Package auth verifies user credentials against an htpasswd file.
//
// Only bcrypt entries (as written by `htpasswd -B`) are accepted. It has no
// knowledge of HTTP; the handlers package maps results to status codes.
package auth

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Htpasswd holds bcrypt password hashes keyed by username. It is immutable
// after construction and safe for concurrent use.
type Htpasswd struct {
	users map[string][]byte
	// dummy is compared against when the user is unknown, so that response
	// time does not reveal which usernames exist.
	dummy []byte
}

// LoadHtpasswd reads and parses the htpasswd file at path.
func LoadHtpasswd(path string) (*Htpasswd, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open htpasswd: %w", err)
	}
	defer f.Close()
	h, err := ParseHtpasswd(f)
	if err != nil {
		return nil, fmt.Errorf("htpasswd %s: %w", path, err)
	}
	return h, nil
}

// ParseHtpasswd parses htpasswd content: one "user:bcrypt-hash" per line.
// Blank lines and lines starting with '#' are ignored. Malformed lines,
// non-bcrypt hashes, duplicate users and an empty file are errors.
func ParseHtpasswd(r io.Reader) (*Htpasswd, error) {
	users := make(map[string][]byte)
	maxCost := bcrypt.MinCost
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		user, hash, ok := strings.Cut(line, ":")
		if !ok || user == "" || hash == "" {
			return nil, fmt.Errorf("line %d: expected user:hash", n)
		}
		cost, err := bcrypt.Cost([]byte(hash))
		if err != nil {
			return nil, fmt.Errorf("line %d: user %q: not a bcrypt hash (use htpasswd -B): %w", n, user, err)
		}
		if _, dup := users[user]; dup {
			return nil, fmt.Errorf("line %d: duplicate user %q", n, user)
		}
		users[user] = []byte(hash)
		maxCost = max(maxCost, cost)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if len(users) == 0 {
		return nil, errors.New("no users defined")
	}

	// Match the most expensive real entry so unknown users are never faster.
	dummy, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), maxCost)
	if err != nil {
		return nil, fmt.Errorf("generate dummy hash: %w", err)
	}
	return &Htpasswd{users: users, dummy: dummy}, nil
}

// Verify reports whether password is correct for user.
func (h *Htpasswd) Verify(user, password string) bool {
	hash, ok := h.users[user]
	if !ok {
		_ = bcrypt.CompareHashAndPassword(h.dummy, []byte(password)) // timing only; result irrelevant
		return false
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}
