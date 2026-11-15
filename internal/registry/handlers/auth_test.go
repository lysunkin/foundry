package handlers_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/foundry/registry/internal/auth"
	"github.com/foundry/registry/internal/registry/handlers"
)

func newUsers(t *testing.T) *auth.Htpasswd {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	h, err := auth.ParseHtpasswd(strings.NewReader("alice:" + string(hash)))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestRequireAuth(t *testing.T) {
	t.Parallel()
	users := newUsers(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

	const (
		none  = ""
		good  = "good"
		bad   = "bad"
		other = "other" // non-Basic scheme
	)
	tests := []struct {
		name      string
		anonRead  bool
		method    string
		path      string
		creds     string
		wantAllow bool
	}{
		// Anonymous read enabled.
		{"anon: ping challenges", true, http.MethodGet, "/v2/", none, false},
		{"anon: pull manifest", true, http.MethodGet, "/v2/lib/app/manifests/v1", none, true},
		{"anon: head blob", true, http.MethodHead, "/v2/lib/app/blobs/sha256:x", none, true},
		{"anon: catalog", true, http.MethodGet, "/v2/_catalog", none, true},
		{"anon: start upload", true, http.MethodPost, "/v2/lib/app/blobs/uploads/", none, false},
		{"anon: put manifest", true, http.MethodPut, "/v2/lib/app/manifests/v1", none, false},
		{"anon: delete blob", true, http.MethodDelete, "/v2/lib/app/blobs/sha256:x", none, false},
		{"anon: api list", true, http.MethodGet, "/api/repos", none, true},
		{"anon: api delete", true, http.MethodDelete, "/api/repos/lib/app", none, false},
		{"anon: api gc", true, http.MethodPost, "/api/gc", none, false},
		{"anon: dashboard", true, http.MethodGet, "/", none, true},
		{"anon: ui gc", true, http.MethodPost, "/ui/gc", none, false},
		{"anon: static", true, http.MethodGet, "/static/css/main.css", none, true},
		{"anon: bad creds on read", true, http.MethodGet, "/v2/lib/app/manifests/v1", bad, false},
		{"anon: non-basic scheme", true, http.MethodGet, "/v2/lib/app/manifests/v1", other, false},
		{"anon: good creds ping", true, http.MethodGet, "/v2/", good, true},
		{"anon: good creds push", true, http.MethodPut, "/v2/lib/app/manifests/v1", good, true},

		// Anonymous read disabled.
		{"closed: pull manifest", false, http.MethodGet, "/v2/lib/app/manifests/v1", none, false},
		{"closed: api list", false, http.MethodGet, "/api/repos", none, false},
		{"closed: dashboard", false, http.MethodGet, "/", none, false},
		{"closed: static", false, http.MethodGet, "/static/css/main.css", none, true},
		{"closed: good creds pull", false, http.MethodGet, "/v2/lib/app/manifests/v1", good, true},
		{"closed: good creds api delete", false, http.MethodDelete, "/api/repos/lib/app", good, true},
		{"closed: good creds ui gc", false, http.MethodPost, "/ui/gc", good, true},
		{"closed: bad creds", false, http.MethodGet, "/api/repos", bad, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := handlers.RequireAuth(handlers.AuthConfig{Users: users, AnonymousRead: tc.anonRead})(ok)
			req := httptest.NewRequest(tc.method, tc.path, nil)
			switch tc.creds {
			case good:
				req.Header.Set("Authorization", basic("alice", "secret"))
			case bad:
				req.Header.Set("Authorization", basic("alice", "wrong"))
			case other:
				req.Header.Set("Authorization", "Bearer abc")
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if tc.wantAllow {
				mustStatus(t, rr, http.StatusTeapot)
				return
			}
			mustStatus(t, rr, http.StatusUnauthorized)
			mustHeader(t, rr, "WWW-Authenticate", `Basic realm="foundry"`)
		})
	}
}

func TestRequireAuthErrorFormats(t *testing.T) {
	t.Parallel()
	h := handlers.RequireAuth(handlers.AuthConfig{Users: newUsers(t)})(http.NotFoundHandler())
	tests := []struct {
		path, wantCT, wantBody string
	}{
		{"/v2/", "application/json", `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`},
		{"/api/repos", "application/json", `{"error":"unauthorized"}`},
		{"/ui/repo/x", "text/plain; charset=utf-8", "unauthorized"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
			mustStatus(t, rr, http.StatusUnauthorized)
			mustHeader(t, rr, "Content-Type", tc.wantCT)
			if got := strings.TrimSpace(rr.Body.String()); got != tc.wantBody {
				t.Errorf("body = %s, want %s", got, tc.wantBody)
			}
		})
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/", nil))
	mustHeader(t, rr, "Docker-Distribution-Api-Version", "registry/2.0")
}

func TestRequireAuthDisabled(t *testing.T) {
	t.Parallel()
	h := handlers.RequireAuth(handlers.AuthConfig{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(m, "/v2/", nil))
		mustStatus(t, rr, http.StatusTeapot)
	}
}

func TestRequireHTMX(t *testing.T) {
	t.Parallel()
	h := handlers.RequireHTMX(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	tests := []struct {
		name, header string
		want         int
	}{
		{"htmx", "true", http.StatusTeapot},
		{"missing", "", http.StatusForbidden},
		{"wrong value", "1", http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/ui/gc", nil)
			if tc.header != "" {
				req.Header.Set("HX-Request", tc.header)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			mustStatus(t, rr, tc.want)
		})
	}
}
