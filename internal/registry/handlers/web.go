package handlers

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/foundry/registry/internal/registry/services"
)

// WebHandler renders the HTML UI.
type WebHandler struct {
	repo      *services.RepositoryService
	blobs     interface{ AllDigests() ([]string, error) }
	templates *template.Template
}

func NewWebHandler(repo *services.RepositoryService, tmpls *template.Template) *WebHandler {
	return &WebHandler{repo: repo, templates: tmpls}
}

func (h *WebHandler) Mount(r chi.Router) {
	r.Get("/", h.dashboard)
	r.Get("/ui/repo/{name:.+}/tags", h.repoTagsFragment)
	r.Get("/ui/repo/{name:.+}", h.repoPage)
	r.Get("/ui/repo/{name:.+}/tag/{tag}", h.tagPage)
}

// --------------------------------------------------------------------------

type dashboardData struct {
	Repos interface{}
	Now   time.Time
}

func (h *WebHandler) dashboard(w http.ResponseWriter, r *http.Request) {
	repos, err := h.repo.ListRepositories()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Enrich with tag counts.
	type repoVM struct {
		ID        int64
		Name      string
		TagCount  int
		CreatedAt time.Time
	}
	var vms []repoVM
	for _, repo := range repos {
		tags, _ := h.repo.ListTags(repo.Name)
		vms = append(vms, repoVM{
			ID:        repo.ID,
			Name:      repo.Name,
			TagCount:  len(tags),
			CreatedAt: repo.CreatedAt,
		})
	}

	h.render(w, "dashboard.html", map[string]interface{}{
		"Repos": vms,
		"Now":   time.Now(),
	})
}

func (h *WebHandler) repoPage(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tags, err := h.repo.ListTags(name)
	if err != nil {
		if err == services.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, "repo.html", map[string]interface{}{
		"Name": name,
		"Tags": tags,
	})
}

func (h *WebHandler) repoTagsFragment(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tags, err := h.repo.ListTags(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, "tags_fragment.html", map[string]interface{}{
		"Name": name,
		"Tags": tags,
	})
}

func (h *WebHandler) tagPage(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tag := chi.URLParam(r, "tag")

	m, err := h.repo.GetManifest(name, tag)
	if err != nil {
		if err == services.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Decode manifest layers for display.
	var manifestJSON map[string]json.RawMessage
	json.Unmarshal(m.Content, &manifestJSON)

	h.render(w, "tag.html", map[string]interface{}{
		"Repo":     name,
		"Tag":      tag,
		"Manifest": m,
		"Content":  string(m.Content),
	})
}

func (h *WebHandler) render(w http.ResponseWriter, name string, data interface{}) {
	if h.templates == nil {
		http.Error(w, "templates not loaded", http.StatusInternalServerError)
		return
	}
	// Clone the template set so each request gets its own copy of block
	// overrides — without this, concurrent renders of different pages can
	// race on the shared "content" block definition.
	clone, err := h.templates.Clone()
	if err != nil {
		log.Printf("template clone error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := clone.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template %s error: %v", name, err)
	}
}
