package web

import (
	"errors"
	"net/http"

	"github.com/valaf/valaf/internal/export"
	"github.com/valaf/valaf/internal/store"
)

// exportIncident streams a notebook in the requested format. Nothing is stored —
// it is rendered from the live rows on each request.
func (s *Server) exportIncident(w http.ResponseWriter, r *http.Request) {
	exp, ok := export.For(r.PathValue("format"))
	if !ok {
		http.Error(w, "unsupported export format", http.StatusNotFound)
		return
	}

	nb, err := s.read.LoadNotebook(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotebookNotFound) {
			http.NotFound(w, r)
			return
		}
		s.log.ErrorContext(r.Context(), "export: load notebook failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", exp.ContentType())
	w.Header().Set("Content-Disposition", `attachment; filename="`+export.Filename(nb.Incident, exp.Ext())+`"`)
	if err := exp.Render(w, nb); err != nil {
		// Headers/body may be partially written; just log.
		s.log.ErrorContext(r.Context(), "export: render failed", "format", exp.Ext(), "error", err)
	}
}
