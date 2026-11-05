package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// dockerManifest is a minimal struct for extracting blob digests from a manifest.
type dockerManifest struct {
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		Digest string `json:"digest"`
	} `json:"layers"`
	// OCI index / manifest list
	Manifests []struct {
		Digest string `json:"digest"`
	} `json:"manifests"`
	// Legacy v1
	FsLayers []struct {
		BlobSum string `json:"blobSum"`
	} `json:"fsLayers"`
}

// extractDigests parses manifest JSON and adds all referenced digests to the set.
func extractDigests(content []byte, set map[string]bool) {
	var m dockerManifest
	if err := json.Unmarshal(content, &m); err != nil {
		return
	}
	if m.Config.Digest != "" {
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
func ParseManifestMediaType(data []byte) string {
	var m struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "application/vnd.docker.distribution.manifest.v2+json"
	}
	if m.MediaType == "" {
		return "application/vnd.oci.image.manifest.v1+json"
	}
	return m.MediaType
}

// VerifyDigest checks that ComputeDigest(data) == digest.
func VerifyDigest(data []byte, digest string) error {
	got := ComputeDigest(data)
	if got != digest {
		return fmt.Errorf("digest mismatch: expected %s got %s", digest, got)
	}
	return nil
}

// unused – keep compiler happy until GC service calls it
var _ = hex.EncodeToString
