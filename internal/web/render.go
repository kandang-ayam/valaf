package web

import (
	"bytes"
	"embed"
	"fmt"
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
	"sparkline": sparkline,
}

// sparkline renders a compact inline SVG line from a series of values.
func sparkline(vals []float64) template.HTML {
	if len(vals) < 2 {
		return ""
	}
	const w, h = 140.0, 26.0
	min, max := vals[0], vals[0]
	for _, v := range vals {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	span := max - min
	if span == 0 {
		span = 1
	}
	var b bytes.Buffer
	for i, v := range vals {
		x := float64(i) / float64(len(vals)-1) * w
		y := h - ((v-min)/span)*(h-4) - 2
		if i == 0 {
			fmt.Fprintf(&b, "M%.1f,%.1f", x, y)
		} else {
			fmt.Fprintf(&b, " L%.1f,%.1f", x, y)
		}
	}
	svg := fmt.Sprintf(
		`<svg class="spark" viewBox="0 0 %g %g" width="%g" height="%g" preserveAspectRatio="none">`+
			`<path d="%s" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/></svg>`,
		w, h, w, h, b.String())
	return template.HTML(svg) //nolint:gosec // values are numbers we formatted
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
