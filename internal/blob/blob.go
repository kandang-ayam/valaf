// Package blob stores binary evidence (dashboard snapshot PNGs) outside the
// database. The backend is chosen dynamically by config: if S3/MinIO is
// configured it is used, otherwise a local volume — exactly as documented in
// .env.example. valaf serves these bytes through an authenticated endpoint, so
// the store only needs put/open/delete, not public URLs.
package blob

import (
	"context"
	"io"
)

// Store is the pluggable blob backend port.
type Store interface {
	// Backend reports the configured backend name ("local" | "s3"), recorded on
	// the attachments row so old objects always resolve to where they were put.
	Backend() string
	// Put stores data under key with the given content type.
	Put(ctx context.Context, key, contentType string, data []byte) error
	// Open returns a reader for a previously stored object.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes an object (idempotent).
	Delete(ctx context.Context, key string) error
}

// Config selects and configures the backend.
type Config struct {
	// S3 — when Endpoint+Bucket are set, the s3 backend is used.
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3UseSSL    bool
	// LocalDir is the base directory for the local backend (default when S3 unset).
	LocalDir string
}

// New returns the S3 store when S3 is configured, otherwise the local store.
func New(cfg Config) (Store, error) {
	if cfg.S3Endpoint != "" && cfg.S3Bucket != "" {
		return newS3(cfg)
	}
	dir := cfg.LocalDir
	if dir == "" {
		dir = "/var/lib/valaf"
	}
	return newLocal(dir)
}
