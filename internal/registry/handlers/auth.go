package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/foundry/registry/internal/auth"
	"github.com/foundry/registry/internal/oci"
)

// basicChallenge is sent with every 401. Docker, containerd, regctl and
// browsers all understand a plain Basic challenge.
const basicChallenge = `Basic realm="foundry"`

// AuthConfig controls RequireAuth.
type AuthConfig struct {
	// Users verifies credentials. Nil disables authentication entirely.
	Users *auth.Htpasswd
	// AnonymousRead allows unauthenticated GET/HEAD requests (pulls and
	// browsing), except GET /v2/, which must challenge so that clients
	// discover that the registry supports authentication.
	AnonymousRead bool
}

// RequireAuth returns middleware enforcing HTTP Basic authentication:
//
//   - /static/, /healthz and /readyz are always public.
//   - Invalid credentials are rejected, even on requests that would be
//     allowed anonymously, so that `docker login` fails on a bad password.
//   - Valid credentials grant full access (there is a single role).
//   - Without credentials, GET/HEAD are allowed when AnonymousRead is set;
//     everything else is rejected.
//
// Rejections are 401 with a Basic challenge, in the error format of the
// matching API (/v2: OCI, /api: JSON, otherwise text).
func RequireAuth(cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if cfg.Users == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/static/") || healthPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			if _, hasAuth := r.Header["Authorization"]; hasAuth {
				user, pass, ok := r.BasicAuth()
				if ok && cfg.Users.Verify(user, pass) {
					setRequestUser(r.Context(), user)
					next.ServeHTTP(w, r)
					return
				}
				slog.WarnContext(r.Context(), "auth: rejected credentials",
					"user", user, "remote_addr", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
				unauthorized(w, r)
				return
			}
			if cfg.AnonymousRead && isRead(r.Method) && r.URL.Path != "/v2/" {
				next.ServeHTTP(w, r)
				return
			}
			unauthorized(w, r)
		})
	}
}

// healthPaths are probed by orchestrators, which carry no credentials.
var healthPaths = map[string]bool{"/healthz": true, "/readyz": true}

// RequireHTMX rejects requests that lack the HX-Request header htmx sends.
// Browsers cannot attach custom headers to cross-site "simple" requests
// (form posts) without a CORS preflight, so this is a CSRF defence for UI
// endpoints that only htmx calls. It complements http.CrossOriginProtection.
func RequireHTMX(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("HX-Request") != "true" {
			http.Error(w, "forbidden: missing HX-Request header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func unauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", basicChallenge)
	switch {
	case r.URL.Path == "/v2" || strings.HasPrefix(r.URL.Path, "/v2/"):
		// apiVersionHeader is not reached; set its headers here, since
		// clients probe for the version header on /v2/.
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		registryError(w, http.StatusUnauthorized, oci.ErrCodeUnauthorized, "authentication required")
	case strings.HasPrefix(r.URL.Path, "/api/"):
		jsonError(w, http.StatusUnauthorized, "unauthorized")
	default:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}
