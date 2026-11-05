package services

import (
	"testing"
)

// ---------------------------------------------------------------------------
// ComputeDigest
// ---------------------------------------------------------------------------

func TestComputeDigest(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{
			name: "empty",
			data: []byte{},
			want: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name: "hello",
			data: []byte("hello"),
			want: "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
		},
		{
			name: "binary",
			data: []byte{0x00, 0x01, 0x02, 0x03},
			want: "sha256:054edec1d0211f624fed0cbca9d4f9400b0e491c43742af2c5b0abebf0c990d8",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeDigest(tc.data)
			if got != tc.want {
				t.Errorf("ComputeDigest = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestComputeDigest_HasSha256Prefix(t *testing.T) {
	d := ComputeDigest([]byte("test"))
	if len(d) < 7 || d[:7] != "sha256:" {
		t.Errorf("digest %q should start with sha256:", d)
	}
}

// ---------------------------------------------------------------------------
// VerifyDigest
// ---------------------------------------------------------------------------

func TestVerifyDigest_Match(t *testing.T) {
	data := []byte("verify me")
	digest := ComputeDigest(data)
	if err := VerifyDigest(data, digest); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestVerifyDigest_Mismatch(t *testing.T) {
	data := []byte("original")
	wrongDigest := ComputeDigest([]byte("different"))
	err := VerifyDigest(data, wrongDigest)
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
}

// ---------------------------------------------------------------------------
// ParseManifestMediaType
// ---------------------------------------------------------------------------

func TestParseManifestMediaType(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "docker v2 media type",
			input: `{"mediaType":"application/vnd.docker.distribution.manifest.v2+json","schemaVersion":2}`,
			want:  "application/vnd.docker.distribution.manifest.v2+json",
		},
		{
			name:  "oci manifest media type",
			input: `{"mediaType":"application/vnd.oci.image.manifest.v1+json","schemaVersion":2}`,
			want:  "application/vnd.oci.image.manifest.v1+json",
		},
		{
			name:  "oci index media type",
			input: `{"mediaType":"application/vnd.oci.image.index.v1+json"}`,
			want:  "application/vnd.oci.image.index.v1+json",
		},
		{
			name:  "missing mediaType field — OCI fallback",
			input: `{"schemaVersion":2,"config":{"digest":"sha256:abc"}}`,
			want:  "application/vnd.oci.image.manifest.v1+json",
		},
		{
			name:  "invalid JSON — docker fallback",
			input: `not json at all`,
			want:  "application/vnd.docker.distribution.manifest.v2+json",
		},
		{
			name:  "empty JSON object — OCI fallback",
			input: `{}`,
			want:  "application/vnd.oci.image.manifest.v1+json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseManifestMediaType([]byte(tc.input))
			if got != tc.want {
				t.Errorf("ParseManifestMediaType = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// extractDigests (internal — tested via exported surface through GC path,
// but we test the logic directly since this is a same-package test file).
// ---------------------------------------------------------------------------

func TestExtractDigests_DockerV2(t *testing.T) {
	manifest := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
		"config": {
			"mediaType": "application/vnd.docker.container.image.v1+json",
			"digest": "sha256:config111"
		},
		"layers": [
			{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "digest": "sha256:layer111"},
			{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "digest": "sha256:layer222"}
		]
	}`)

	set := make(map[string]bool)
	extractDigests(manifest, set)

	for _, want := range []string{"sha256:config111", "sha256:layer111", "sha256:layer222"} {
		if !set[want] {
			t.Errorf("expected digest %s in set", want)
		}
	}
}

func TestExtractDigests_OCIIndex(t *testing.T) {
	index := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": [
			{"digest": "sha256:platform_amd64"},
			{"digest": "sha256:platform_arm64"}
		]
	}`)

	set := make(map[string]bool)
	extractDigests(index, set)

	if !set["sha256:platform_amd64"] || !set["sha256:platform_arm64"] {
		t.Errorf("OCI index manifests not extracted: %v", set)
	}
}

func TestExtractDigests_LegacyV1(t *testing.T) {
	v1 := []byte(`{
		"schemaVersion": 1,
		"fsLayers": [
			{"blobSum": "sha256:legacy_layer1"},
			{"blobSum": "sha256:legacy_layer2"}
		]
	}`)

	set := make(map[string]bool)
	extractDigests(v1, set)

	if !set["sha256:legacy_layer1"] || !set["sha256:legacy_layer2"] {
		t.Errorf("legacy fsLayers not extracted: %v", set)
	}
}

func TestExtractDigests_InvalidJSON(t *testing.T) {
	set := make(map[string]bool)
	extractDigests([]byte("not json"), set)
	// Should not panic, should produce empty set.
	if len(set) != 0 {
		t.Errorf("expected empty set for invalid JSON, got %v", set)
	}
}

func TestExtractDigests_EmptyDigestsSkipped(t *testing.T) {
	manifest := []byte(`{
		"config": {"digest": ""},
		"layers": [{"digest": ""}, {"digest": "sha256:real"}]
	}`)
	set := make(map[string]bool)
	extractDigests(manifest, set)

	if set[""] {
		t.Error("empty digest should not be in set")
	}
	if !set["sha256:real"] {
		t.Error("real digest should be in set")
	}
}
