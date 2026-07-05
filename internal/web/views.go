package web

import (
	"errors"
	"net/http"

	"github.com/valaf/valaf/internal/store"
)

// baseData is embedded in every page's data so the layout can render the header
// (current user, theme, CSRF).
type baseData struct {
	Title string
	User  *AuthUser
	CSRF  string
}

func (s *Server) base(r *http.Request, title string) baseData {
	u := currentUser(r)
	csrf := ""
	if u != nil {
		csrf = u.CSRF
	}
	return baseData{Title: title, User: u, CSRF: csrf}
}

type listData struct {
	baseData
	Query     string
	Incidents []store.IncidentSummary
}

type detailData struct {
	baseData
	Notebook *store.Notebook
	CanEdit  bool
}

type loginData struct {
	baseData
	Error string
}

// incidentList renders the incident list, filtered by ?q=.
func (s *Server) incidentList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	incidents, err := s.read.ListIncidents(r.Context(), query)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list incidents failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "incidents_list", listData{baseData: s.base(r, "Incidents"), Query: query, Incidents: incidents})
}

// incidentDetail renders one notebook.
func (s *Server) incidentDetail(w http.ResponseWriter, r *http.Request) {
	nb, err := s.read.LoadNotebook(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotebookNotFound) {
			http.Error(w, "incident not found", http.StatusNotFound)
			return
		}
		s.log.ErrorContext(r.Context(), "load notebook failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "incident_detail", detailData{baseData: s.base(r, nb.Incident.Title), Notebook: nb, CanEdit: canEdit(r)})
}
