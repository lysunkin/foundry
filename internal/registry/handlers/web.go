package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/foundry/registry/internal/oci"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/ui"
)

// WebHandler renders the HTML UI.
type WebHandler struct {
	repo      WebMetadata
	gc        Collector
	gcOpts    services.GCOptions
	templates *ui.Templates
}

// NewWebHandler returns a handler for the web UI. gcOpts are the options for
// the GC button.
func NewWebHandler(repo WebMetadata, gc Collector, gcOpts services.GCOptions, tmpls *ui.Templates) *WebHandler {
	return &WebHandler{repo: repo, gc: gc, gcOpts: gcOpts, templates: tmpls}
}

func (h *WebHandler) Mount(r chi.Router) {
	r.Get("/", h.dashboard)
	r.With(RequireHTMX).Post("/ui/gc", h.runGC)
	// Repository names may contain '/', see routing.go.
	r.HandleFunc("/ui/repo/*", nameRouter(
		route{suffix: "/tags", methods: map[string]http.HandlerFunc{http.MethodGet: h.repoTagsFragment}},
		route{marker: "/tag/", tailParam: "tag", methods: map[string]http.HandlerFunc{http.MethodGet: h.tagPage}},
		route{methods: map[string]http.HandlerFunc{http.MethodGet: h.repoPage}},
	))
}

func (h *WebHandler) dashboard(w http.ResponseWriter, r *http.Request) {
	repos, err := h.repo.ListRepositorySummaries(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, "dashboard.html", map[string]any{"Repos": repos})
}

func (h *WebHandler) repoPage(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tags, err := h.listTags(r.Context(), name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, "repo.html", map[string]any{"Name": name, "Tags": tags})
}

func (h *WebHandler) repoTagsFragment(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tags, err := h.listTags(r.Context(), name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, "tags_fragment.html", map[string]any{"Name": name, "Tags": tags})
}

func (h *WebHandler) tagPage(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tag := chi.URLParam(r, "tag")
	if !oci.ValidName(name) {
		http.NotFound(w, r)
		return
	}
	m, err := h.repo.GetManifest(r.Context(), name, tag)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, "tag.html", map[string]any{
		"Repo":     name,
		"Tag":      tag,
		"Manifest": m,
		"Content":  string(m.Content),
	})
}

func (h *WebHandler) runGC(w http.ResponseWriter, r *http.Request) {
	result, err := h.gc.Collect(r.Context(), h.gcOpts)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, "gc_result.html", result)
}

func (h *WebHandler) listTags(ctx context.Context, name string) (any, error) {
	if !oci.ValidName(name) {
		return nil, services.ErrNotFound
	}
	return h.repo.ListTags(ctx, name)
}

func (h *WebHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, services.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if requestCancelled(r) {
		http.Error(w, "request cancelled", http.StatusServiceUnavailable)
		return
	}
	slog.ErrorContext(r.Context(), "server error", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (h *WebHandler) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	if h.templates == nil {
		http.Error(w, "templates not loaded", http.StatusInternalServerError)
		return
	}
	if err := h.templates.Render(w, name, data); err != nil {
		slog.ErrorContext(r.Context(), "render template", "template", name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
