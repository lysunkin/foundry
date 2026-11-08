package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/foundry/registry/internal/oci"
	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
)

// APIHandler provides the /api/... admin endpoints.
type APIHandler struct {
	repo    *services.RepositoryService
	blobs   *storage.FilesystemBlobStore
	uploads *storage.UploadStore
	gcOpts  services.GCOptions
}

func NewAPIHandler(
	repo *services.RepositoryService,
	blobs *storage.FilesystemBlobStore,
	uploads *storage.UploadStore,
	gcOpts services.GCOptions,
) *APIHandler {
	return &APIHandler{repo: repo, blobs: blobs, uploads: uploads, gcOpts: gcOpts}
}

func (h *APIHandler) Mount(r chi.Router) {
	r.Get("/api/repos", h.listRepos)
	// Repository names may contain '/', see routing.go.
	r.HandleFunc("/api/repos/*", nameRouter(
		route{suffix: "/tags", methods: map[string]http.HandlerFunc{
			http.MethodGet: h.withName(h.listTags),
		}},
		route{marker: "/manifests/", tailParam: "reference", methods: map[string]http.HandlerFunc{
			http.MethodGet: h.withName(h.getManifest),
		}},
		route{methods: map[string]http.HandlerFunc{
			http.MethodDelete: h.withName(h.deleteRepo),
		}},
	))
	r.HandleFunc("/api/tags/*", nameRouter(
		route{marker: "/", tailParam: "tag", methods: map[string]http.HandlerFunc{
			http.MethodDelete: h.withName(h.deleteTag),
		}},
	))
	r.HandleFunc("/api/manifests/*", nameRouter(
		route{marker: "/", tailParam: "digest", methods: map[string]http.HandlerFunc{
			http.MethodDelete: h.withName(h.deleteManifest),
		}},
	))

	r.Post("/api/gc", h.runGC)
}

func (h *APIHandler) withName(next nameHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if !oci.ValidName(name) {
			jsonError(w, http.StatusBadRequest, "invalid repository name")
			return
		}
		next(w, r, name)
	}
}

func (h *APIHandler) listRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := h.repo.ListRepositories()
	if err != nil {
		apiServerError(w, r, err)
		return
	}
	jsonResponse(w, http.StatusOK, repos)
}

func (h *APIHandler) listTags(w http.ResponseWriter, r *http.Request, name string) {
	tags, err := h.repo.ListTags(name)
	if err != nil {
		h.notFoundOr(w, r, err, "repository not found")
		return
	}
	jsonResponse(w, http.StatusOK, tags)
}

func (h *APIHandler) getManifest(w http.ResponseWriter, r *http.Request, name string) {
	reference := chi.URLParam(r, "reference")
	m, err := h.repo.GetManifest(name, reference)
	if err != nil {
		h.notFoundOr(w, r, err, "manifest not found")
		return
	}

	// Manifest content is JSON; embed it verbatim when valid.
	var content any = json.RawMessage(m.Content)
	if !json.Valid(m.Content) {
		content = string(m.Content)
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"digest":     m.Digest,
		"media_type": m.MediaType,
		"size":       m.Size,
		"created_at": m.CreatedAt,
		"content":    content,
	})
}

func (h *APIHandler) deleteRepo(w http.ResponseWriter, r *http.Request, name string) {
	if err := h.repo.DeleteRepository(name); err != nil {
		h.notFoundOr(w, r, err, "repository not found")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *APIHandler) deleteTag(w http.ResponseWriter, r *http.Request, name string) {
	tag := chi.URLParam(r, "tag")
	if err := h.repo.DeleteTag(name, tag); err != nil {
		h.notFoundOr(w, r, err, "tag not found")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *APIHandler) deleteManifest(w http.ResponseWriter, r *http.Request, name string) {
	digest := chi.URLParam(r, "digest")
	if !oci.ValidDigest(digest) {
		jsonError(w, http.StatusBadRequest, "invalid digest")
		return
	}
	if err := h.repo.DeleteManifest(name, digest); err != nil {
		h.notFoundOr(w, r, err, "manifest not found")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// runGC runs garbage collection. ?dry_run=true reports without deleting.
func (h *APIHandler) runGC(w http.ResponseWriter, r *http.Request) {
	opts := h.gcOpts
	opts.DryRun = r.URL.Query().Get("dry_run") == "true"
	result, err := services.GarbageCollect(h.repo, h.blobs, h.uploads, opts)
	if err != nil {
		apiServerError(w, r, err)
		return
	}
	jsonResponse(w, http.StatusOK, result)
}

func (h *APIHandler) notFoundOr(w http.ResponseWriter, r *http.Request, err error, msg string) {
	if errors.Is(err, services.ErrNotFound) {
		jsonError(w, http.StatusNotFound, msg)
		return
	}
	apiServerError(w, r, err)
}
