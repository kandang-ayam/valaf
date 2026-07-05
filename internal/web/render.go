package web

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"time"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

var funcs = template.FuncMap{
	"shorttime": shortTime,
	"fulltime":  fullTime,
}

var templates = template.Must(template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/*.html"))

// render writes a named template, buffering first so a template error becomes a
// clean 500 instead of a half-written page.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("template render failed", "template", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func toTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	}
	return time.Time{}, false
}

func shortTime(v any) string {
	if t, ok := toTime(v); ok {
		return t.Local().Format("Jan 2, 15:04")
	}
	return ""
}

func fullTime(v any) string {
	if t, ok := toTime(v); ok {
		return t.Local().Format("2006-01-02 15:04 MST")
	}
	return ""
}
