package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/foundry/registry/internal/oci"
)

type descriptor struct {
	MediaType string   `json:"mediaType"`
	Digest    string   `json:"digest"`
	URLs      []string `json:"urls"`
}

// isForeign reports whether a layer is a "foreign"/non-distributable layer that
// clients fetch from external URLs and therefore never push to the registry.
func (d descriptor) isForeign() bool {
	return len(d.URLs) > 0 ||
		strings.Contains(d.MediaType, "foreign") ||
		strings.Contains(d.MediaType, "nondistributable")
}

// manifestDoc is a minimal union of the manifest formats the registry accepts.
type manifestDoc struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        *descriptor  `json:"config"`
	Layers        []descriptor `json:"layers"`
	// OCI index / Docker manifest list
	Manifests []descriptor `json:"manifests"`
	// Legacy schema 1
	FsLayers []struct {
		BlobSum string `json:"blobSum"`
	} `json:"fsLayers"`
}

// ManifestRefs lists what a manifest points at.
type ManifestRefs struct {
	// Blobs are config and layer digests that must exist in the blob store.
	Blobs []string
	// Manifests are child manifest digests (for indexes / manifest lists) that
	// must exist in the same repository.
	Manifests []string
}

// ParseManifest validates manifest JSON and returns the referenced digests.
// It returns an error wrapping ErrManifestInvalid for malformed input.
func ParseManifest(content []byte) (*ManifestRefs, error) {
	var m manifestDoc
	if err := json.Unmarshal(content, &m); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrManifestInvalid, err)
	}
	refs := &ManifestRefs{}
	switch m.SchemaVersion {
	case 1:
		// Legacy, deprecated format; stored as-is without reference checks.
		return refs, nil
	case 2:
	default:
		return nil, fmt.Errorf("%w: unsupported schemaVersion %d", ErrManifestInvalid, m.SchemaVersion)
	}

	check := func(d string) error {
		if !oci.ValidDigest(d) {
			return fmt.Errorf("%w: invalid descriptor digest %q", ErrManifestInvalid, d)
		}
		return nil
	}
	if m.Config != nil {
		if err := check(m.Config.Digest); err != nil {
			return nil, err
		}
		refs.Blobs = append(refs.Blobs, m.Config.Digest)
	}
	for _, l := range m.Layers {
		if err := check(l.Digest); err != nil {
			return nil, err
		}
		if !l.isForeign() {
			refs.Blobs = append(refs.Blobs, l.Digest)
		}
	}
	for _, c := range m.Manifests {
		if err := check(c.Digest); err != nil {
			return nil, err
		}
		refs.Manifests = append(refs.Manifests, c.Digest)
	}
	if m.Config == nil && len(m.Manifests) == 0 {
		return nil, fmt.Errorf("%w: manifest has neither config nor manifests", ErrManifestInvalid)
	}
	return refs, nil
}

// extractDigests parses manifest JSON and adds all referenced digests to the
// set. It is deliberately lenient (used for GC marking): unparsable content
// contributes nothing rather than failing the whole GC run.
func extractDigests(content []byte, set map[string]bool) {
	var m manifestDoc
	if err := json.Unmarshal(content, &m); err != nil {
		return
	}
	if m.Config != nil && m.Config.Digest != "" {
		set[m.Config.Digest] = true
	}
	for _, l := range m.Layers {
		if l.Digest != "" {
			set[l.Digest] = true
		}
	}
	for _, ms := range m.Manifests {
		if ms.Digest != "" {
			set[ms.Digest] = true
		}
	}
	for _, fl := range m.FsLayers {
		if fl.BlobSum != "" {
			set[fl.BlobSum] = true
		}
	}
}

// ComputeDigest computes the sha256 digest of data and returns "sha256:<hex>".
func ComputeDigest(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

// ParseManifestMediaType extracts the mediaType field from raw manifest JSON.
// When absent it infers OCI index vs. OCI manifest from the document shape.
func ParseManifestMediaType(data []byte) string {
	var m struct {
		MediaType string          `json:"mediaType"`
		Manifests json.RawMessage `json:"manifests"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return oci.MediaTypeDockerManifest
	}
	if m.MediaType != "" {
		return m.MediaType
	}
	if len(m.Manifests) > 0 {
		return oci.MediaTypeOCIIndex
	}
	return oci.MediaTypeOCIManifest
}

// ResolveManifestMediaType picks the media type to store for a pushed
// manifest: the document's own mediaType field wins, then the request
// Content-Type, then inference from the document shape.
func ResolveManifestMediaType(contentType string, content []byte) string {
	var m struct {
		MediaType string `json:"mediaType"`
	}
	if json.Unmarshal(content, &m) == nil && m.MediaType != "" {
		return m.MediaType
	}
	if ct := oci.BaseMediaType(contentType); ct != "" && ct != "application/json" && ct != "application/octet-stream" {
		return ct
	}
	return ParseManifestMediaType(content)
}

// VerifyDigest checks that ComputeDigest(data) == digest.
func VerifyDigest(data []byte, digest string) error {
	got := ComputeDigest(data)
	if got != digest {
		return fmt.Errorf("digest mismatch: expected %s got %s", digest, got)
	}
	return nil
}
