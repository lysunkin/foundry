// Package handlers implements the HTTP surface of the registry: the OCI
// Distribution API (/v2), the admin JSON API (/api), and the web UI.
//
// Spec: https://github.com/opencontainers/distribution-spec/blob/main/spec.md
package handlers

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/foundry/registry/internal/oci"
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
	r.Group(func(r chi.Router) {
		r.Use(apiVersionHeader)

		r.Get("/v2/", h.ping)
		r.Get("/v2/_catalog", h.catalog)

		// Everything else is /v2/<name>/... where <name> may contain '/'.
		// Order matters: "/blobs/uploads/" must be tried before "/blobs/".
		r.HandleFunc("/v2/*", nameRouter(
			route{suffix: "/tags/list", methods: map[string]http.HandlerFunc{
				http.MethodGet: h.withName(h.listTags),
			}},
			route{marker: "/blobs/uploads/", tailParam: "uuid", allowEmptyTail: true, methods: map[string]http.HandlerFunc{
				http.MethodPost:   h.withName(h.uploadsRoot(h.startUpload)),
				http.MethodGet:    h.withName(h.uploadSession(h.getUploadStatus)),
				http.MethodPatch:  h.withName(h.uploadSession(h.patchUpload)),
				http.MethodPut:    h.withName(h.uploadSession(h.putUpload)),
				http.MethodDelete: h.withName(h.uploadSession(h.deleteUpload)),
			}},
			// HEAD and GET share a handler; ServeContent omits the body for HEAD.
			route{marker: "/blobs/", tailParam: "digest", methods: map[string]http.HandlerFunc{
				http.MethodHead:   h.withName(h.getBlob),
				http.MethodGet:    h.withName(h.getBlob),
				http.MethodDelete: h.withName(h.deleteBlob),
			}},
			route{marker: "/manifests/", tailParam: "reference", methods: map[string]http.HandlerFunc{
				http.MethodHead:   h.withName(h.getManifest),
				http.MethodGet:    h.withName(h.getManifest),
				http.MethodPut:    h.withName(h.putManifest),
				http.MethodDelete: h.withName(h.deleteManifest),
			}},
		))
	})
}

// uploadsRoot only accepts ".../blobs/uploads/" with no session ID.
func (h *RegistryHandler) uploadsRoot(next nameHandler) nameHandler {
	return func(w http.ResponseWriter, r *http.Request, name string) {
		if chi.URLParam(r, "uuid") != "" {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		next(w, r, name)
	}
}

// uploadSession requires ".../blobs/uploads/<uuid>".
func (h *RegistryHandler) uploadSession(next nameHandler) nameHandler {
	return func(w http.ResponseWriter, r *http.Request, name string) {
		if chi.URLParam(r, "uuid") == "" {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		next(w, r, name)
	}
}

// apiVersionHeader advertises Registry v2 support on every /v2 response.
func apiVersionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

type nameHandler func(w http.ResponseWriter, r *http.Request, name string)

// withName validates the {name} path parameter before invoking next.
func (h *RegistryHandler) withName(next nameHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if !oci.ValidName(name) {
			registryError(w, http.StatusBadRequest, oci.ErrCodeNameInvalid, "invalid repository name")
			return
		}
		next(w, r, name)
	}
}

// --------------------------------------------------------------------------
// Ping & catalog
// --------------------------------------------------------------------------

func (h *RegistryHandler) ping(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, struct{}{})
}

func (h *RegistryHandler) catalog(w http.ResponseWriter, r *http.Request) {
	n, last, ok := paginationParams(w, r)
	if !ok {
		return
	}
	// Fetch one extra to know whether there is a next page.
	fetch := 0
	if n > 0 {
		fetch = n + 1
	}
	names, err := h.repo.ListRepositoryNames(fetch, last)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if n > 0 && len(names) > n {
		names = names[:n]
		setNextLink(w, "/v2/_catalog", n, names[len(names)-1])
	}
	jsonResponse(w, http.StatusOK, map[string]any{"repositories": names})
}

// --------------------------------------------------------------------------
// Blobs
// --------------------------------------------------------------------------

func (h *RegistryHandler) digestParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	d := chi.URLParam(r, "digest")
	if !oci.ValidDigest(d) {
		registryError(w, http.StatusBadRequest, oci.ErrCodeDigestInvalid, "invalid digest")
		return "", false
	}
	return d, true
}

// getBlob serves GET and HEAD. http.ServeContent provides Range, If-None-Match
// and HEAD handling for free.
func (h *RegistryHandler) getBlob(w http.ResponseWriter, r *http.Request, name string) {
	digest, ok := h.digestParam(w, r)
	if !ok {
		return
	}
	rc, _, err := h.blobs.Get(digest)
	if err != nil {
		if errors.Is(err, storage.ErrBlobNotFound) {
			registryError(w, http.StatusNotFound, oci.ErrCodeBlobUnknown, "blob unknown to registry")
			return
		}
		serverError(w, r, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("ETag", `"`+digest+`"`)
	w.Header().Set("Cache-Control", "max-age=31536000, immutable")
	http.ServeContent(w, r, "", time.Time{}, rc)
}

func (h *RegistryHandler) deleteBlob(w http.ResponseWriter, r *http.Request, name string) {
	digest, ok := h.digestParam(w, r)
	if !ok {
		return
	}
	err := h.blobs.Delete(digest)
	if err != nil && !errors.Is(err, storage.ErrBlobNotFound) {
		serverError(w, r, err)
		return
	}
	known, dbErr := h.repo.BlobExists(digest)
	if dbErr != nil {
		serverError(w, r, dbErr)
		return
	}
	if errors.Is(err, storage.ErrBlobNotFound) && !known {
		registryError(w, http.StatusNotFound, oci.ErrCodeBlobUnknown, "blob unknown to registry")
		return
	}
	if err := h.repo.DeleteBlob(digest); err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// --------------------------------------------------------------------------
// Uploads
// --------------------------------------------------------------------------

func uploadLocation(name, id string) string {
	return fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id)
}

func setUploadHeaders(w http.ResponseWriter, name, id string, offset int64) {
	w.Header().Set("Location", uploadLocation(name, id))
	w.Header().Set("Docker-Upload-UUID", id)
	if offset > 0 {
		w.Header().Set("Range", fmt.Sprintf("0-%d", offset-1))
	}
}

func (h *RegistryHandler) startUpload(w http.ResponseWriter, r *http.Request, name string) {
	q := r.URL.Query()

	// Cross-repository mount: blobs are stored globally, so any blob the
	// registry already has can be "mounted" instantly.
	if mount := q.Get("mount"); mount != "" && oci.ValidDigest(mount) {
		if ok, err := h.repo.BlobExists(mount); err == nil && ok {
			if exists, _ := h.blobs.Exists(mount); exists {
				if _, err := h.repo.EnsureRepository(name); err != nil {
					serverError(w, r, err)
					return
				}
				w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, mount))
				w.Header().Set("Docker-Content-Digest", mount)
				w.WriteHeader(http.StatusCreated)
				return
			}
		}
		// Fall through: spec says to start a regular upload session instead.
	}

	// Single-shot monolithic push: POST ?digest=sha256:...
	if digest := q.Get("digest"); digest != "" {
		h.monolithicPush(w, r, name, digest)
		return
	}

	repo, err := h.repo.EnsureRepository(name)
	if err != nil {
		serverError(w, r, err)
		return
	}

	id := uuid.NewString()
	tmpPath, err := h.uploads.Create(id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := h.repo.SaveUpload(id, tmpPath, repo.ID, 0); err != nil {
		h.discardUpload(id)
		serverError(w, r, err)
		return
	}

	setUploadHeaders(w, name, id, 0)
	w.Header().Set("OCI-Chunk-Min-Length", "0")
	w.WriteHeader(http.StatusAccepted)
}

func (h *RegistryHandler) monolithicPush(w http.ResponseWriter, r *http.Request, name, digest string) {
	if !oci.ValidDigest(digest) {
		registryError(w, http.StatusBadRequest, oci.ErrCodeDigestInvalid, "invalid digest")
		return
	}
	if _, err := h.repo.EnsureRepository(name); err != nil {
		serverError(w, r, err)
		return
	}
	// PutVerified only commits the blob if it hashes to digest, so a
	// mismatching push leaves nothing behind.
	size, err := h.blobs.PutVerified(r.Body, digest)
	if err != nil {
		if errors.Is(err, storage.ErrDigestMismatch) {
			registryError(w, http.StatusBadRequest, oci.ErrCodeDigestInvalid, "digest mismatch")
			return
		}
		serverError(w, r, err)
		return
	}
	if err := h.repo.SaveBlob(digest, size); err != nil {
		serverError(w, r, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

// loadUpload fetches the session for {uuid}, ensuring it belongs to name.
func (h *RegistryHandler) loadUpload(w http.ResponseWriter, r *http.Request, name string) (id string, offset int64, ok bool) {
	id = chi.URLParam(r, "uuid")
	if !storage.ValidUploadID(id) {
		registryError(w, http.StatusNotFound, oci.ErrCodeBlobUploadUnknown, "blob upload unknown")
		return "", 0, false
	}
	upload, err := h.repo.GetRepoUpload(name, id)
	if err != nil {
		if errors.Is(err, services.ErrNotFound) {
			registryError(w, http.StatusNotFound, oci.ErrCodeBlobUploadUnknown, "blob upload unknown")
			return "", 0, false
		}
		serverError(w, r, err)
		return "", 0, false
	}
	return id, upload.Offset, true
}

func (h *RegistryHandler) getUploadStatus(w http.ResponseWriter, r *http.Request, name string) {
	id, offset, ok := h.loadUpload(w, r, name)
	if !ok {
		return
	}
	setUploadHeaders(w, name, id, offset)
	w.WriteHeader(http.StatusNoContent)
}

// parseContentRange parses "<start>-<end>" (optionally prefixed "bytes ").
func parseContentRange(v string) (start, end int64, err error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "bytes ")
	a, b, found := strings.Cut(v, "-")
	if !found {
		return 0, 0, fmt.Errorf("malformed range %q", v)
	}
	if start, err = strconv.ParseInt(a, 10, 64); err != nil {
		return 0, 0, err
	}
	if end, err = strconv.ParseInt(b, 10, 64); err != nil {
		return 0, 0, err
	}
	if start < 0 || end < start {
		return 0, 0, fmt.Errorf("invalid range %q", v)
	}
	return start, end, nil
}

func (h *RegistryHandler) patchUpload(w http.ResponseWriter, r *http.Request, name string) {
	id := chi.URLParam(r, "uuid")
	if storage.ValidUploadID(id) {
		unlock := h.uploads.Lock(id)
		defer unlock()
	}
	id, offset, ok := h.loadUpload(w, r, name)
	if !ok {
		return
	}

	if cr := r.Header.Get("Content-Range"); cr != "" {
		start, _, err := parseContentRange(cr)
		if err != nil || start != offset {
			setUploadHeaders(w, name, id, offset)
			registryError(w, http.StatusRequestedRangeNotSatisfiable, oci.ErrCodeBlobUploadInvalid, "invalid content range")
			return
		}
	}

	newOffset, err := h.uploads.Append(id, offset, r.Body)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := h.repo.UpdateUploadOffset(id, newOffset); err != nil {
		serverError(w, r, err)
		return
	}

	setUploadHeaders(w, name, id, newOffset)
	w.WriteHeader(http.StatusAccepted)
}

func (h *RegistryHandler) putUpload(w http.ResponseWriter, r *http.Request, name string) {
	digest := r.URL.Query().Get("digest")
	if !oci.ValidDigest(digest) {
		registryError(w, http.StatusBadRequest, oci.ErrCodeDigestInvalid, "valid digest query parameter required")
		return
	}

	id := chi.URLParam(r, "uuid")
	if storage.ValidUploadID(id) {
		unlock := h.uploads.Lock(id)
		defer unlock()
	}
	id, offset, ok := h.loadUpload(w, r, name)
	if !ok {
		return
	}

	// The final PUT may carry the last chunk.
	if r.ContentLength != 0 {
		newOffset, err := h.uploads.Append(id, offset, r.Body)
		if err != nil {
			serverError(w, r, err)
			return
		}
		offset = newOffset
	}

	// The session is finished whatever happens next.
	cleanup := func() { h.discardUpload(id) }

	if err := h.blobs.PutAt(digest, h.uploads.Path(id), offset); err != nil {
		cleanup()
		if errors.Is(err, storage.ErrDigestMismatch) || errors.Is(err, storage.ErrSizeMismatch) {
			registryError(w, http.StatusBadRequest, oci.ErrCodeDigestInvalid, "uploaded content does not match digest")
			return
		}
		serverError(w, r, err)
		return
	}
	cleanup()

	if err := h.repo.SaveBlob(digest, offset); err != nil {
		serverError(w, r, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

// discardUpload removes an upload session's temp file and DB row. Failures are
// logged rather than returned: leftovers are reclaimed by GC (upload TTL).
func (h *RegistryHandler) discardUpload(id string) {
	if err := h.uploads.Delete(id); err != nil {
		log.Printf("discard upload %s: remove temp file: %v", id, err) //nolint:gosec // G706: id validated by storage.ValidUploadID
	}
	if err := h.repo.DeleteUpload(id); err != nil {
		log.Printf("discard upload %s: delete db row: %v", id, err) //nolint:gosec // G706: id validated by storage.ValidUploadID
	}
}

func (h *RegistryHandler) deleteUpload(w http.ResponseWriter, r *http.Request, name string) {
	id, _, ok := h.loadUpload(w, r, name)
	if !ok {
		return
	}
	if err := h.uploads.Delete(id); err != nil {
		serverError(w, r, err)
		return
	}
	if err := h.repo.DeleteUpload(id); err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --------------------------------------------------------------------------
// Manifests
// --------------------------------------------------------------------------

// referenceParam validates {reference} as either a tag or a digest.
func referenceParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	ref := chi.URLParam(r, "reference")
	if oci.IsDigestReference(ref) {
		if !oci.ValidDigest(ref) {
			registryError(w, http.StatusBadRequest, oci.ErrCodeDigestInvalid, "invalid digest")
			return "", false
		}
		return ref, true
	}
	if !oci.ValidTag(ref) {
		registryError(w, http.StatusBadRequest, oci.ErrCodeManifestInvalid, "invalid tag")
		return "", false
	}
	return ref, true
}

// getManifest serves GET and HEAD.
func (h *RegistryHandler) getManifest(w http.ResponseWriter, r *http.Request, name string) {
	reference, ok := referenceParam(w, r)
	if !ok {
		return
	}
	m, err := h.repo.GetManifest(name, reference)
	if err != nil {
		if errors.Is(err, services.ErrNotFound) {
			registryError(w, http.StatusNotFound, oci.ErrCodeManifestUnknown, "manifest unknown")
			return
		}
		serverError(w, r, err)
		return
	}

	mediaType := m.MediaType
	if mediaType == "" {
		mediaType = oci.MediaTypeDockerManifest
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.Header().Set("Docker-Content-Digest", m.Digest)
	w.Header().Set("ETag", `"`+m.Digest+`"`)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		// Safe: mediaType is restricted to manifest types on write
		// (oci.IsManifestMediaType) and responses carry nosniff.
		w.Write(m.Content) //nolint:gosec // G705, see above
	}
}

func (h *RegistryHandler) putManifest(w http.ResponseWriter, r *http.Request, name string) {
	reference, ok := referenceParam(w, r)
	if !ok {
		return
	}

	content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, oci.MaxManifestSize))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			registryError(w, http.StatusRequestEntityTooLarge, oci.ErrCodeSizeInvalid, "manifest too large")
			return
		}
		serverError(w, r, err)
		return
	}

	mediaType := services.ResolveManifestMediaType(r.Header.Get("Content-Type"), content)
	digest, err := h.repo.PutManifest(name, reference, mediaType, content)
	switch {
	case err == nil:
	case errors.Is(err, services.ErrDigestMismatch):
		registryError(w, http.StatusBadRequest, oci.ErrCodeDigestInvalid, "manifest content does not match digest")
		return
	case errors.Is(err, services.ErrManifestInvalid):
		registryError(w, http.StatusBadRequest, oci.ErrCodeManifestInvalid, err.Error())
		return
	case errors.Is(err, services.ErrManifestBlobUnknown):
		registryError(w, http.StatusBadRequest, oci.ErrCodeManifestBlobUnknown, err.Error())
		return
	default:
		serverError(w, r, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, digest))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

// deleteManifest deletes a manifest by digest, or (OCI 1.1) a tag by name.
func (h *RegistryHandler) deleteManifest(w http.ResponseWriter, r *http.Request, name string) {
	reference, ok := referenceParam(w, r)
	if !ok {
		return
	}
	var err error
	if oci.IsDigestReference(reference) {
		err = h.repo.DeleteManifest(name, reference)
	} else {
		err = h.repo.DeleteTag(name, reference)
	}
	if err != nil {
		if errors.Is(err, services.ErrNotFound) {
			registryError(w, http.StatusNotFound, oci.ErrCodeManifestUnknown, "manifest unknown")
			return
		}
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// --------------------------------------------------------------------------
// Tags
// --------------------------------------------------------------------------

func (h *RegistryHandler) listTags(w http.ResponseWriter, r *http.Request, name string) {
	n, last, ok := paginationParams(w, r)
	if !ok {
		return
	}
	tags, err := h.repo.ListTags(name)
	if err != nil {
		if errors.Is(err, services.ErrNotFound) {
			registryError(w, http.StatusNotFound, oci.ErrCodeNameUnknown, "repository name not known to registry")
			return
		}
		serverError(w, r, err)
		return
	}

	tagNames := make([]string, 0, len(tags))
	for _, t := range tags { // already sorted by tag
		if last == "" || t.Tag > last {
			tagNames = append(tagNames, t.Tag)
		}
	}
	if n > 0 && len(tagNames) > n {
		tagNames = tagNames[:n]
		setNextLink(w, fmt.Sprintf("/v2/%s/tags/list", name), n, tagNames[n-1])
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"name": name,
		"tags": tagNames,
	})
}

// paginationParams parses the spec's ?n=&last= parameters. n == 0 means
// "no limit".
func paginationParams(w http.ResponseWriter, r *http.Request) (n int, last string, ok bool) {
	q := r.URL.Query()
	if v := q.Get("n"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			registryError(w, http.StatusBadRequest, oci.ErrCodeUnsupported, "invalid n parameter")
			return 0, "", false
		}
		n = parsed
	}
	return n, q.Get("last"), true
}

func setNextLink(w http.ResponseWriter, path string, n int, last string) {
	q := url.Values{"n": {strconv.Itoa(n)}, "last": {last}}
	w.Header().Set("Link", fmt.Sprintf(`<%s?%s>; rel="next"`, path, q.Encode()))
}
