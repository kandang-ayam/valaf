// Package web serves valaf's HTTP surface: the webhook intake, the notebook UI,
// and operational endpoints. Handlers are added per feature slice.
package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/valaf/valaf/internal/blob"
	"github.com/valaf/valaf/internal/core"
	"github.com/valaf/valaf/internal/store"
)

type Server struct {
	pool        *pgxpool.Pool
	log         *slog.Logger
	intake      *core.Service
	sources     *store.SourceRepo
	read        *store.ReadRepo
	users       *store.UserRepo
	sessions    *store.SessionRepo
	review      *store.ReviewRepo
	attachments *store.AttachmentRepo
	blob        blob.Store
	authCfg     AuthConfig
	adapters    map[string]core.IntakeAdapter // keyed by intake_sources.source_type
}

func New(pool *pgxpool.Pool, log *slog.Logger, intake *core.Service, sources *store.SourceRepo, read *store.ReadRepo, users *store.UserRepo, sessions *store.SessionRepo, review *store.ReviewRepo, attachments *store.AttachmentRepo, blobStore blob.Store, authCfg AuthConfig, adapters map[string]core.IntakeAdapter) *Server {
	return &Server{
		pool: pool, log: log, intake: intake, sources: sources, read: read,
		users: users, sessions: sessions, review: review,
		attachments: attachments, blob: blobStore, authCfg: authCfg, adapters: adapters,
	}
}

// Handler builds the HTTP routes. Uses the stdlib mux (Go 1.22+ patterns);
// chi middleware will be layered in when routes multiply.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("POST /webhook/{source}", s.webhook)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)

	// Notebook UI (requires an authenticated user)
	mux.HandleFunc("GET /incidents/{id}", s.requireUser(s.incidentDetail))
	mux.HandleFunc("GET /incidents/{id}/export/{format}", s.requireUser(s.exportIncident))
	mux.HandleFunc("GET /attachments/{id}", s.requireUser(s.attachment))
	mux.HandleFunc("GET /{$}", s.requireUser(s.incidentList))

	// Review actions (engineer role, CSRF-checked, audit-logged)
	mux.HandleFunc("POST /hypotheses/{id}/verdict", s.requireRole("engineer", s.hypothesisVerdict))
	mux.HandleFunc("POST /evidence/{id}/flag", s.requireRole("engineer", s.flagEvidence))
	mux.HandleFunc("POST /incidents/{id}/resolution", s.requireRole("engineer", s.saveResolution))

	// withUser resolves the principal for every request (nil = anonymous).
	return s.withUser(mux)
}

// healthz is liveness: the process is up. It never touches the database.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, "ok")
}

// readyz is readiness: the process can serve traffic, i.e. the database
// answers. Returns 503 when it does not, so a load balancer drains this node.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.pool.Ping(ctx); err != nil {
		s.log.WarnContext(ctx, "readiness check failed", "error", err)
		writeText(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeText(w, http.StatusOK, "ready")
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
