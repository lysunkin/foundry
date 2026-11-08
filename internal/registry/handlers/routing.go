package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Repository names may contain slashes ("library/nginx"), which chi path
// parameters cannot match — chi's {name:.+} regexp is applied per path
// segment. Routes whose path contains a repository name are therefore
// registered as wildcards and split here on the *last* occurrence of a fixed
// route keyword, the same strategy distribution/distribution uses.

// route is one candidate pattern under a wildcard prefix.
type route struct {
	// marker separates name from the trailing part, e.g. "/manifests/".
	// The tail after the marker must not contain '/'. An empty marker means
	// "the whole remainder is the name".
	marker string
	// suffix, if set, must terminate the path exactly (e.g. "/tags/list");
	// used instead of marker for routes without a trailing parameter.
	suffix string
	// tailParam is the URL param name for the part after marker ("" if none).
	tailParam string
	// allowEmptyTail permits "<name><marker>" with nothing after (upload POST).
	allowEmptyTail bool
	// handlers by HTTP method.
	methods map[string]http.HandlerFunc
}

// match tries to split rest into name and tail for this route.
func (rt route) match(rest string) (name, tail string, ok bool) {
	switch {
	case rt.suffix != "":
		name, ok = strings.CutSuffix(rest, rt.suffix)
		return name, "", ok && name != ""
	case rt.marker == "":
		return rest, "", rest != "" && !strings.HasSuffix(rest, "/")
	}
	i := strings.LastIndex(rest, rt.marker)
	if i <= 0 {
		return "", "", false
	}
	name, tail = rest[:i], rest[i+len(rt.marker):]
	if strings.Contains(tail, "/") || (tail == "" && !rt.allowEmptyTail) {
		return "", "", false
	}
	return name, tail, true
}

// nameRouter returns a handler for "<prefix>*" that dispatches to the first
// matching route and populates chi URL params "name" and route.tailParam.
func nameRouter(routes ...route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := chi.URLParam(r, "*")
		for _, rt := range routes {
			name, tail, ok := rt.match(rest)
			if !ok {
				continue
			}
			h, allowed := rt.methods[r.Method]
			if !allowed {
				w.Header().Set("Allow", allowHeader(rt.methods))
				http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
				return
			}
			rctx := chi.RouteContext(r.Context())
			rctx.URLParams.Add("name", name)
			if rt.tailParam != "" {
				rctx.URLParams.Add(rt.tailParam, tail)
			}
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

func allowHeader(m map[string]http.HandlerFunc) string {
	order := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	var out []string
	for _, k := range order {
		if _, ok := m[k]; ok {
			out = append(out, k)
		}
	}
	return strings.Join(out, ", ")
}
