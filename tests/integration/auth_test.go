package integration

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/foundry/registry/internal/auth"
	"github.com/foundry/registry/internal/registry"
)

const nestedRepo = "team/app"

func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// newAuthServer starts a registry with user alice/secret. It returns a client
// that authenticates and one that doesn't, sharing the same server.
func newAuthServer(t *testing.T, anonymousPull bool) (authed, anon *registryServer) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	users, err := auth.ParseHtpasswd(strings.NewReader("alice:" + string(hash)))
	if err != nil {
		t.Fatal(err)
	}
	authed = newRegistryServerWithConfig(t, func(c *registry.Config) {
		c.Users = users
		c.AnonymousPull = anonymousPull
	})
	authed.auth = basicAuth("alice", "secret")
	a := *authed
	a.auth = ""
	return authed, &a
}

func mustChallenge(t *testing.T, resp *http.Response) {
	t.Helper()
	mustStatus(t, resp, http.StatusUnauthorized)
	if got := resp.Header.Get("WWW-Authenticate"); got != `Basic realm="foundry"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	readBody(t, resp)
}

// Unauthenticated push is rejected; after login it succeeds and the image is
// pullable anonymously (the default configuration).
func TestAuth_PushRequiresLogin_AnonymousPull(t *testing.T) {
	authed, anon := newAuthServer(t, true)

	// docker login probes GET /v2/: anonymous challenges, credentials pass.
	mustChallenge(t, anon.do(t, http.MethodGet, "/v2/", nil, nil))
	resp := authed.do(t, http.MethodGet, "/v2/", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)

	bad := *authed
	bad.auth = basicAuth("alice", "wrong")
	mustChallenge(t, bad.do(t, http.MethodGet, "/v2/", nil, nil))

	// Anonymous push: every write step is refused.
	resp = anon.do(t, http.MethodPost, "/v2/"+nestedRepo+"/blobs/uploads/", nil, nil)
	mustChallenge(t, resp)
	resp = anon.do(t, http.MethodPut, "/v2/"+nestedRepo+"/manifests/v1", strings.NewReader("{}"),
		map[string]string{"Content-Type": mediaTypeOCIManifest})
	mustChallenge(t, resp)

	// Authenticated push, anonymous pull.
	digest := pushImage(t, authed, nestedRepo, "v1", []byte(`{"auth":"test"}`))
	for _, path := range []string{
		"/v2/" + nestedRepo + "/manifests/v1",
		"/v2/" + nestedRepo + "/manifests/" + digest,
		"/v2/" + nestedRepo + "/tags/list",
		"/v2/_catalog",
		"/api/repos",
		"/",
		"/ui/repo/" + nestedRepo,
	} {
		resp := anon.do(t, http.MethodGet, path, nil, nil)
		mustStatus(t, resp, http.StatusOK)
		readBody(t, resp)
	}

	// Bad credentials are rejected even on reads.
	mustChallenge(t, bad.do(t, http.MethodGet, "/v2/"+nestedRepo+"/manifests/v1", nil, nil))

	// Deletes need credentials.
	mustChallenge(t, anon.do(t, http.MethodDelete, "/v2/"+nestedRepo+"/manifests/"+digest, nil, nil))
	mustChallenge(t, anon.do(t, http.MethodDelete, "/api/tags/"+nestedRepo+"/v1", nil, nil))
	resp = authed.do(t, http.MethodDelete, "/api/tags/"+nestedRepo+"/v1", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
}

func TestAuth_AnonymousPullDisabled(t *testing.T) {
	authed, anon := newAuthServer(t, false)
	pushImage(t, authed, nestedRepo, "v1", []byte(`{"auth":"closed"}`))

	for _, path := range []string{
		"/v2/" + nestedRepo + "/manifests/v1",
		"/v2/_catalog",
		"/api/repos",
		"/",
	} {
		mustChallenge(t, anon.do(t, http.MethodGet, path, nil, nil))

		resp := authed.do(t, http.MethodGet, path, nil, nil)
		mustStatus(t, resp, http.StatusOK)
		readBody(t, resp)
	}

	// Static assets stay public so the login prompt page can be styled.
	for _, path := range []string{"/static/css/main.css", "/static/js/htmx.min.js"} {
		resp := anon.do(t, http.MethodGet, path, nil, nil)
		mustStatus(t, resp, http.StatusOK)
		readBody(t, resp)
	}
}

func TestAuth_AdminAPIAndGC(t *testing.T) {
	authed, anon := newAuthServer(t, true)

	mustChallenge(t, anon.do(t, http.MethodPost, "/api/gc", nil, nil))
	mustChallenge(t, anon.do(t, http.MethodPost, "/ui/gc", nil, map[string]string{"HX-Request": "true"}))

	resp := authed.do(t, http.MethodPost, "/api/gc?dry_run=true", nil, nil)
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)

	resp = authed.do(t, http.MethodPost, "/ui/gc", nil, map[string]string{"HX-Request": "true"})
	mustStatus(t, resp, http.StatusOK)
	readBody(t, resp)
}

// CSRF protection applies with and without authentication.
func TestCSRF(t *testing.T) {
	authed, _ := newAuthServer(t, true)
	open := newRegistryServer(t)

	for name, rs := range map[string]*registryServer{"auth": authed, "no-auth": open} {
		t.Run(name, func(t *testing.T) {
			tests := []struct {
				name    string
				method  string
				path    string
				headers map[string]string
				want    int
			}{
				{"cross-site api gc", http.MethodPost, "/api/gc", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
				{"cross-site ui gc", http.MethodPost, "/ui/gc", map[string]string{"Sec-Fetch-Site": "cross-site", "HX-Request": "true"}, http.StatusForbidden},
				{"foreign origin delete", http.MethodDelete, "/api/repos/x", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
				{"cross-site upload", http.MethodPost, "/v2/x/blobs/uploads/", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
				{"ui gc without htmx header", http.MethodPost, "/ui/gc", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusForbidden},
				{"same-origin ui gc", http.MethodPost, "/ui/gc", map[string]string{"Sec-Fetch-Site": "same-origin", "HX-Request": "true"}, http.StatusOK},
				{"non-browser api gc", http.MethodPost, "/api/gc?dry_run=true", nil, http.StatusOK},
				{"cross-site read allowed", http.MethodGet, "/api/repos", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusOK},
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					resp := rs.do(t, tc.method, tc.path, nil, tc.headers)
					mustStatus(t, resp, tc.want)
					readBody(t, resp)
				})
			}
		})
	}
}
