// Package handlers implements the Docker Registry HTTP API v2.
//
// Spec: https://distribution.github.io/distribution/spec/api/
package handlers

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/foundry/registry/internal/registry/services"
	"github.com/foundry/registry/internal/registry/storage"
)

// RegistryHandler implements the OCI Distribution Spec v2 endpoints.
type RegistryHandler struct {
	repo    *services.RepositoryService
	blobs   *storage.FilesystemBlobStore
	uploads *storage.UploadStore
}

func NewRegistryHandler(
	repo *services.RepositoryService,
	blobs *storage.FilesystemBlobStore,
	uploads *storage.UploadStore,
) *RegistryHandler {
	return &RegistryHandler{repo: repo, blobs: blobs, uploads: uploads}
}

// Mount registers all registry routes on r.
func (h *RegistryHandler) Mount(r chi.Router) {
	r.Get("/v2/", h.ping)

	// Blob routes
	r.Head("/v2/{name:.+}/blobs/{digest}", h.headBlob)
	r.Get("/v2/{name:.+}/blobs/{digest}", h.getBlob)
	r.Delete("/v2/{name:.+}/blobs/{digest}", h.deleteBlob)

	// Upload routes
	r.Post("/v2/{name:.+}/blobs/uploads/", h.startUpload)
	r.Get("/v2/{name:.+}/blobs/uploads/{uuid}", h.getUploadStatus)
	r.Patch("/v2/{name:.+}/blobs/uploads/{uuid}", h.patchUpload)
	r.Put("/v2/{name:.+}/blobs/uploads/{uuid}", h.putUpload)
	r.Delete("/v2/{name:.+}/blobs/uploads/{uuid}", h.deleteUpload)

	// Manifest routes
	r.Head("/v2/{name:.+}/manifests/{reference}", h.headManifest)
	r.Get("/v2/{name:.+}/manifests/{reference}", h.getManifest)
	r.Put("/v2/{name:.+}/manifests/{reference}", h.putManifest)
	r.Delete("/v2/{name:.+}/manifests/{reference}", h.deleteManifest)

	// Tags
	r.Get("/v2/{name:.+}/tags/list", h.listTags)
}

// --------------------------------------------------------------------------
// Ping
// --------------------------------------------------------------------------

func (h *RegistryHandler) ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{}`))
}

// --------------------------------------------------------------------------
// Blobs
// --------------------------------------------------------------------------

func (h *RegistryHandler) headBlob(w http.ResponseWriter, r *http.Request) {
	digest := chi.URLParam(r, "digest")
	blob, err := h.repo.GetBlob(digest)
	if err != nil {
		registryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(blob.Size, 10))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
}

func (h *RegistryHandler) getBlob(w http.ResponseWriter, r *http.Request) {
	digest := chi.URLParam(r, "digest")
	rc, size, err := h.blobs.Get(digest)
	if err != nil {
		registryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
	io.Copy(w, rc)
}

func (h *RegistryHandler) deleteBlob(w http.ResponseWriter, r *http.Request) {
	digest := chi.URLParam(r, "digest")
	if err := h.blobs.Delete(digest); err != nil {
		registryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}
	h.repo.DeleteBlob(digest)
	w.WriteHeader(http.StatusAccepted)
}

// --------------------------------------------------------------------------
// Uploads (chunked)
// --------------------------------------------------------------------------

func (h *RegistryHandler) startUpload(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	// Single-shot monolithic push: ?digest=sha256:...
	if digest := r.URL.Query().Get("digest"); digest != "" {
		h.handleMonolithicPush(w, r, name, digest)
		return
	}

	repo, err := h.repo.EnsureRepository(name)
	if err != nil {
		serverError(w, err)
		return
	}

	id := uuid.New().String()
	tmpPath, err := h.uploads.Create(id)
	if err != nil {
		serverError(w, err)
		return
	}
	if err := h.repo.SaveUpload(id, tmpPath, repo.ID, 0); err != nil {
		serverError(w, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id))
	w.Header().Set("Docker-Upload-UUID", id)
	w.Header().Set("OCI-Chunk-Min-Length", "0")
	// Per spec: Range is omitted (or 0-0) for a fresh session with no data yet.
	w.WriteHeader(http.StatusAccepted)
}

func (h *RegistryHandler) handleMonolithicPush(w http.ResponseWriter, r *http.Request, name, digest string) {
	repo, err := h.repo.EnsureRepository(name)
	if err != nil {
		serverError(w, err)
		return
	}
	_ = repo

	actualDigest, size, err := h.blobs.Put(r.Body)
	if err != nil {
		serverError(w, err)
		return
	}
	if actualDigest != digest {
		registryError(w, http.StatusBadRequest, "DIGEST_INVALID", "digest mismatch")
		return
	}
	if err := h.repo.SaveBlob(actualDigest, size); err != nil {
		serverError(w, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

func (h *RegistryHandler) getUploadStatus(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	id := chi.URLParam(r, "uuid")

	upload, err := h.repo.GetUpload(id)
	if err != nil {
		registryError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "blob upload unknown")
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id))
	w.Header().Set("Docker-Upload-UUID", id)
	if upload.Offset > 0 {
		w.Header().Set("Range", fmt.Sprintf("0-%d", upload.Offset-1))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RegistryHandler) patchUpload(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	id := chi.URLParam(r, "uuid")

	upload, err := h.repo.GetUpload(id)
	if err != nil {
		registryError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "blob upload unknown")
		return
	}

	// Validate Content-Range if provided.
	startOffset := upload.Offset
	if cr := r.Header.Get("Content-Range"); cr != "" {
		var start, end int64
		if _, err := fmt.Sscanf(cr, "%d-%d", &start, &end); err == nil {
			if start != upload.Offset {
				registryError(w, http.StatusRequestedRangeNotSatisfiable, "RANGE_INVALID", "invalid range")
				return
			}
			startOffset = start
		}
	}

	newOffset, err := h.uploads.Append(id, startOffset, r.Body)
	if err != nil {
		serverError(w, err)
		return
	}
	if err := h.repo.UpdateUploadOffset(id, newOffset); err != nil {
		serverError(w, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id))
	w.Header().Set("Docker-Upload-UUID", id)
	if newOffset > 0 {
		w.Header().Set("Range", fmt.Sprintf("0-%d", newOffset-1))
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *RegistryHandler) putUpload(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	id := chi.URLParam(r, "uuid")
	digest := r.URL.Query().Get("digest")

	if digest == "" {
		registryError(w, http.StatusBadRequest, "DIGEST_INVALID", "digest query param required")
		return
	}

	upload, err := h.repo.GetUpload(id)
	if err != nil {
		registryError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "blob upload unknown")
		return
	}

	// If the PUT carries a body, append it first.
	if r.ContentLength != 0 {
		newOffset, err := h.uploads.Append(id, upload.Offset, r.Body)
		if err != nil {
			serverError(w, err)
			return
		}
		upload.Offset = newOffset
	}

	// Stat the temp file to get the final size before PutAt moves it.
	tempPath := h.uploads.Path(id)
	fi, err := os.Stat(tempPath)
	if err != nil {
		serverError(w, fmt.Errorf("stat upload temp: %w", err))
		return
	}
	size := fi.Size()

	if err := h.blobs.PutAt(digest, tempPath, upload.Offset); err != nil {
		log.Printf("putUpload PutAt error: %v", err)
		registryError(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
		h.uploads.Delete(id)
		h.repo.DeleteUpload(id)
		return
	}

	if err := h.repo.SaveBlob(digest, size); err != nil {
		serverError(w, err)
		return
	}
	h.repo.DeleteUpload(id)

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

func (h *RegistryHandler) deleteUpload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "uuid")
	h.uploads.Delete(id)
	h.repo.DeleteUpload(id)
	w.WriteHeader(http.StatusNoContent)
}

// --------------------------------------------------------------------------
// Manifests
// --------------------------------------------------------------------------

func (h *RegistryHandler) headManifest(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	reference := chi.URLParam(r, "reference")

	m, err := h.repo.GetManifest(name, reference)
	if err != nil {
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	mediaType := m.MediaType
	if mediaType == "" {
		mediaType = "application/vnd.docker.distribution.manifest.v2+json"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.Header().Set("Docker-Content-Digest", m.Digest)
	w.WriteHeader(http.StatusOK)
}

func (h *RegistryHandler) getManifest(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	reference := chi.URLParam(r, "reference")

	m, err := h.repo.GetManifest(name, reference)
	if err != nil {
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}

	mediaType := m.MediaType
	if mediaType == "" {
		mediaType = "application/vnd.docker.distribution.manifest.v2+json"
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.Header().Set("Docker-Content-Digest", m.Digest)
	w.WriteHeader(http.StatusOK)
	w.Write(m.Content)
}

func (h *RegistryHandler) putManifest(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	reference := chi.URLParam(r, "reference")

	content, err := io.ReadAll(r.Body)
	if err != nil {
		serverError(w, err)
		return
	}

	digest := services.ComputeDigest(content)
	mediaType := r.Header.Get("Content-Type")
	if mediaType == "" {
		mediaType = services.ParseManifestMediaType(content)
	}

	if err := h.repo.SaveManifest(name, digest, mediaType, content); err != nil {
		serverError(w, err)
		return
	}

	// If reference is a tag, save the tag mapping.
	if !strings.HasPrefix(reference, "sha256:") {
		if err := h.repo.SaveTag(name, reference, digest); err != nil {
			serverError(w, err)
			return
		}
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

func (h *RegistryHandler) deleteManifest(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	reference := chi.URLParam(r, "reference")

	if err := h.repo.DeleteManifest(name, reference); err != nil {
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// --------------------------------------------------------------------------
// Tags
// --------------------------------------------------------------------------

func (h *RegistryHandler) listTags(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tags, err := h.repo.ListTags(name)
	if err != nil {
		if err == services.ErrNotFound {
			registryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
			return
		}
		serverError(w, err)
		return
	}

	tagNames := make([]string, len(tags))
	for i, t := range tags {
		tagNames[i] = t.Tag
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"name": name,
		"tags": tagNames,
	})
}
