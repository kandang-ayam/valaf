package web

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/valaf/valaf/internal/core"
	"github.com/valaf/valaf/internal/store"
)

// maxWebhookBytes caps request bodies to protect memory; alert payloads are small.
const maxWebhookBytes = 5 << 20 // 5 MiB

type webhookResponse struct {
	IncidentID string `json:"incident_id,omitempty"`
	Created    bool   `json:"created,omitempty"`
	Dropped    bool   `json:"dropped,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Alerts     int    `json:"alerts,omitempty"`
	Error      string `json:"error,omitempty"`
}

// webhook authenticates the source, normalizes the payload, and ingests it.
// Path: POST /webhook/{source}, where {source} is intake_sources.name.
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("source")

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, webhookResponse{Error: "cannot read body"})
		return
	}

	src, err := s.sources.FindActive(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrSourceNotFound) {
			// Same response as a bad token — don't reveal which sources exist.
			writeJSON(w, http.StatusUnauthorized, webhookResponse{Error: "unauthorized"})
			return
		}
		s.log.ErrorContext(ctx, "intake source lookup failed", "source", name, "error", err)
		writeJSON(w, http.StatusInternalServerError, webhookResponse{Error: "internal error"})
		return
	}

	if !authenticate(src, r, body) {
		writeJSON(w, http.StatusUnauthorized, webhookResponse{Error: "unauthorized"})
		return
	}
	if err := s.sources.TouchLastSeen(ctx, src.ID); err != nil {
		s.log.WarnContext(ctx, "touch last_seen_at failed", "source", name, "error", err)
	}

	adapter, ok := s.adapters[src.SourceType]
	if !ok {
		writeJSON(w, http.StatusNotImplemented, webhookResponse{Error: "unsupported source type: " + src.SourceType})
		return
	}

	res, err := s.intake.Ingest(ctx, adapter, body)
	if err != nil {
		if errors.Is(err, core.ErrBadPayload) {
			writeJSON(w, http.StatusBadRequest, webhookResponse{Error: err.Error()})
			return
		}
		s.log.ErrorContext(ctx, "intake ingest failed", "source", name, "error", err)
		writeJSON(w, http.StatusInternalServerError, webhookResponse{Error: "internal error"})
		return
	}

	if res.Dropped {
		// 200 (not an error) so Alertmanager doesn't retry filtered noise.
		writeJSON(w, http.StatusOK, webhookResponse{Dropped: true, Reason: res.Reason})
		return
	}
	writeJSON(w, http.StatusAccepted, webhookResponse{
		IncidentID: res.IncidentID,
		Created:    res.Created,
		Alerts:     res.Alerts,
	})
}

func authenticate(src store.Source, r *http.Request, body []byte) bool {
	switch src.AuthMethod {
	case "shared_token":
		return src.VerifySharedToken(bearerToken(r))
	case "hmac":
		return src.VerifyHMAC(body, r.Header.Get("X-Valaf-Signature"))
	default:
		return false
	}
}

// bearerToken reads the token from "Authorization: Bearer <t>" or X-Valaf-Token.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if t, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(t)
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Valaf-Token"))
}
