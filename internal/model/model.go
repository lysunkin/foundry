package model

import "time"

// Repository represents a named image repository.
type Repository struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Blob represents a content-addressable layer or config blob.
type Blob struct {
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// Manifest represents an OCI/Docker image manifest stored raw.
type Manifest struct {
	Digest       string    `json:"digest"`
	RepositoryID int64     `json:"repository_id"`
	MediaType    string    `json:"media_type"`
	Content      []byte    `json:"-"`
	Size         int64     `json:"size"`
	CreatedAt    time.Time `json:"created_at"`
}

// Tag maps a mutable name to a manifest digest within a repository.
type Tag struct {
	RepositoryID   int64     `json:"repository_id"`
	RepositoryName string    `json:"repository_name,omitempty"`
	Tag            string    `json:"tag"`
	ManifestDigest string    `json:"manifest_digest"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Upload tracks an in-progress blob upload session.
type Upload struct {
	ID           string    `json:"id"`
	RepositoryID int64     `json:"repository_id"`
	Offset       int64     `json:"offset"`
	TempPath     string    `json:"temp_path"`
	CreatedAt    time.Time `json:"created_at"`
}
