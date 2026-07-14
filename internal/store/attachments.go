package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAttachmentNotFound is returned when an attachment id does not exist.
var ErrAttachmentNotFound = errors.New("attachment not found")

// Attachment is a stored binary's pointer (backend + key + mime).
type Attachment struct {
	StorageBackend string
	StorageKey     string
	MimeType       string
}

type AttachmentRepo struct {
	pool *pgxpool.Pool
}

func NewAttachmentRepo(pool *pgxpool.Pool) *AttachmentRepo { return &AttachmentRepo{pool: pool} }

// Get returns where an attachment's bytes live, for streaming through the
// authenticated endpoint.
func (r *AttachmentRepo) Get(ctx context.Context, id string) (Attachment, error) {
	var a Attachment
	err := r.pool.QueryRow(ctx,
		`SELECT storage_backend::text, storage_key, mime_type FROM attachments WHERE id = $1`, id,
	).Scan(&a.StorageBackend, &a.StorageKey, &a.MimeType)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrAttachmentNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("get attachment: %w", err)
	}
	return a, nil
}
