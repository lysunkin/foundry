// Package oci holds OCI Distribution Spec constants and input validators that
// are shared by the storage, service, and HTTP layers.
//
// Spec: https://github.com/opencontainers/distribution-spec/blob/main/spec.md
package oci

import (
	"mime"
	"regexp"
	"strings"
)

// Media types the registry knows about.
const (
	MediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeOCIManifest        = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIIndex           = "application/vnd.oci.image.index.v1+json"
)

// MediaTypeDockerManifestV1Signed is the deprecated schema 1 manifest type.
const MediaTypeDockerManifestV1Signed = "application/vnd.docker.distribution.manifest.v1+prettyjws"

// IsManifestMediaType reports whether mt is a manifest type this registry
// will store and serve. Restricting this set matters: the media type is
// echoed back as Content-Type, so accepting arbitrary values (e.g.
// "text/html") would let a client plant content the browser renders on the
// registry's (and web UI's) origin.
func IsManifestMediaType(mt string) bool {
	switch mt {
	case MediaTypeDockerManifest, MediaTypeDockerManifestList,
		MediaTypeOCIManifest, MediaTypeOCIIndex, MediaTypeDockerManifestV1Signed,
		"application/vnd.docker.distribution.manifest.v1+json":
		return true
	}
	return false
}

// Error codes defined by the distribution spec.
const (
	ErrCodeBlobUnknown         = "BLOB_UNKNOWN"
	ErrCodeBlobUploadInvalid   = "BLOB_UPLOAD_INVALID"
	ErrCodeBlobUploadUnknown   = "BLOB_UPLOAD_UNKNOWN"
	ErrCodeDigestInvalid       = "DIGEST_INVALID"
	ErrCodeManifestBlobUnknown = "MANIFEST_BLOB_UNKNOWN"
	ErrCodeManifestInvalid     = "MANIFEST_INVALID"
	ErrCodeManifestUnknown     = "MANIFEST_UNKNOWN"
	ErrCodeNameInvalid         = "NAME_INVALID"
	ErrCodeNameUnknown         = "NAME_UNKNOWN"
	ErrCodeSizeInvalid         = "SIZE_INVALID"
	ErrCodeUnsupported         = "UNSUPPORTED"
	ErrCodeUnknown             = "UNKNOWN"
)

// MaxManifestSize bounds manifest bodies (same limit as distribution/distribution).
const MaxManifestSize = 4 << 20 // 4 MiB

// MaxNameLength is the maximum repository name length (including slashes).
const MaxNameLength = 255

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*)*$`)
	tagRe    = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)
	digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ValidName reports whether name is a valid repository name.
func ValidName(name string) bool {
	return len(name) <= MaxNameLength && nameRe.MatchString(name)
}

// ValidTag reports whether tag is a valid tag.
func ValidTag(tag string) bool { return tagRe.MatchString(tag) }

// ValidDigest reports whether d is a supported, well-formed digest.
// Only sha256 is supported.
func ValidDigest(d string) bool { return digestRe.MatchString(d) }

// IsDigestReference reports whether ref has the shape of a digest
// ("<algorithm>:<encoded>") rather than a tag. Tags cannot contain ':'.
func IsDigestReference(ref string) bool { return strings.Contains(ref, ":") }

// BaseMediaType strips parameters (e.g. "; charset=utf-8") from a
// Content-Type header value. It returns "" for an empty or unparsable value.
func BaseMediaType(contentType string) string {
	if contentType == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return mt
}
