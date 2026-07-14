package web

import (
	"errors"
	"io"
	"net/http"

	"github.com/valaf/valaf/internal/store"
)

// attachment streams a stored blob (e.g. a dashboard snapshot PNG) to an
// authenticated user. The bytes live in the blob store, never in a public
// bucket, so RBAC still applies to evidence images.
func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	if s.attachments == nil || s.blob == nil {
		http.Error(w, "attachments not available", http.StatusNotImplemented)
		return
	}
	ctx := r.Context()

	att, err := s.attachments.Get(ctx, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrAttachmentNotFound) {
			http.NotFound(w, r)
			return
		}
		s.log.ErrorContext(ctx, "attachment lookup failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rc, err := s.blob.Open(ctx, att.StorageKey)
	if err != nil {
		s.log.ErrorContext(ctx, "attachment open failed", "key", att.StorageKey, "error", err)
		http.Error(w, "attachment unavailable", http.StatusBadGateway)
		return
	}
	defer rc.Close()

	if att.MimeType != "" {
		w.Header().Set("Content-Type", att.MimeType)
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = io.Copy(w, rc)
}
