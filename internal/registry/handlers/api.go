// Package handlers - Admin JSON API for regctl and programmatic access.
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
)

// APIHandler provides the /api/... admin endpoints.
type APIHandler struct {
	repo    *services.RepositoryService
	blobs   *storage.FilesystemBlobStore
}

func NewAPIHandler(repo *services.RepositoryService, blobs *storage.FilesystemBlobStore) *APIHandler {
	return &APIHandler{repo: repo, blobs: blobs}
}

func (h *APIHandler) Mount(r chi.Router) {
	r.Get("/api/repos", h.listRepos)
	r.Get("/api/repos/{name:.+}/tags", h.listTags)
	r.Get("/api/repos/{name:.+}/manifests/{reference}", h.getManifest)
	r.Delete("/api/repos/{name:.+}", h.deleteRepo)
	r.Delete("/api/tags/{name:.+}/{tag}", h.deleteTag)
	r.Delete("/api/manifests/{name:.+}/{digest}", h.deleteManifest)

	r.Post("/api/gc", h.runGC)
}

func (h *APIHandler) listRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := h.repo.ListRepositories()
	if err != nil {
		serverError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, repos)
}

func (h *APIHandler) listTags(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tags, err := h.repo.ListTags(name)
	if err != nil {
		if err == services.ErrNotFound {
			jsonError(w, http.StatusNotFound, "repository not found")
			return
		}
		serverError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, tags)
}

func (h *APIHandler) getManifest(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	reference := chi.URLParam(r, "reference")

	m, err := h.repo.GetManifest(name, reference)
	if err != nil {
		if err == services.ErrNotFound {
			jsonError(w, http.StatusNotFound, "manifest not found")
			return
		}
		serverError(w, err)
		return
	}

	// Return metadata + raw content as base64 in JSON.
	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"digest":     m.Digest,
		"media_type": m.MediaType,
		"size":       m.Size,
		"created_at": m.CreatedAt,
		"content":    json.RawMessage(m.Content),
	})
}

func (h *APIHandler) deleteRepo(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := h.repo.DeleteRepository(name); err != nil {
		if err == services.ErrNotFound {
			jsonError(w, http.StatusNotFound, "repository not found")
			return
		}
		serverError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *APIHandler) deleteTag(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tag := chi.URLParam(r, "tag")

	if err := h.repo.DeleteTag(name, tag); err != nil {
		if err == services.ErrNotFound {
			jsonError(w, http.StatusNotFound, "tag not found")
			return
		}
		serverError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *APIHandler) deleteManifest(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	digest := chi.URLParam(r, "digest")

	if err := h.repo.DeleteManifest(name, digest); err != nil {
		if err == services.ErrNotFound {
			jsonError(w, http.StatusNotFound, "manifest not found")
			return
		}
		serverError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *APIHandler) runGC(w http.ResponseWriter, r *http.Request) {
	result, err := services.GarbageCollect(h.repo, h.blobs)
	if err != nil {
		serverError(w, fmt.Errorf("gc failed: %w", err))
		return
	}
	jsonResponse(w, http.StatusOK, result)
}
