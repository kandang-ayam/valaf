package web

import (
	"net/http"

	"github.com/valaf/valaf/internal/store"
)

// canEdit reports whether the current user may perform review actions.
func canEdit(r *http.Request) bool {
	u := currentUser(r)
	return u != nil && roleAtLeast(u.Role, "engineer")
}

// hypothesisVerdict records confirm/reject/clear on a hypothesis and re-renders
// the hypotheses card.
func (s *Server) hypothesisVerdict(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	verdict := r.PostFormValue("verdict")
	switch verdict {
	case "confirmed", "rejected", "none":
	default:
		http.Error(w, "invalid verdict", http.StatusBadRequest)
		return
	}

	incidentID, err := s.review.SetHypothesisVerdict(r.Context(), r.PathValue("id"), verdict, r.PostFormValue("note"), currentUser(r).ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "set verdict failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderNotebookFragment(w, r, incidentID, "hypotheses_card")
}

// flagEvidence marks an evidence item invalid and re-renders the evidence card.
func (s *Server) flagEvidence(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	comment := r.PostFormValue("comment")
	if comment == "" {
		http.Error(w, "a comment is required to flag evidence", http.StatusBadRequest)
		return
	}

	incidentID, err := s.review.FlagEvidence(r.Context(), r.PathValue("id"), comment, currentUser(r).ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "flag evidence failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderNotebookFragment(w, r, incidentID, "evidence_card")
}

// saveResolution writes the resolution, marks the incident resolved, and (for
// HTMX) redirects to the refreshed detail page.
func (s *Server) saveResolution(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	rootCause := r.PostFormValue("root_cause")
	if rootCause == "" {
		http.Error(w, "root cause is required", http.StatusBadRequest)
		return
	}

	err := s.review.SaveResolution(r.Context(), id, store.ResolutionInput{
		RootCause:        rootCause,
		ActionsTaken:     r.PostFormValue("actions_taken"),
		UltimateSolution: r.PostFormValue("ultimate_solution"),
		Notes:            r.PostFormValue("notes"),
	}, currentUser(r).ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "save resolution failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	target := "/incidents/" + id
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// renderNotebookFragment reloads the notebook and renders a single card
// template (used as the HTMX swap response).
func (s *Server) renderNotebookFragment(w http.ResponseWriter, r *http.Request, incidentID, fragment string) {
	nb, err := s.read.LoadNotebook(r.Context(), incidentID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "reload notebook failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, fragment, detailData{baseData: s.base(r, nb.Incident.Title), Notebook: nb, CanEdit: canEdit(r)})
}
