// Package export renders a notebook into downloadable formats on demand. Nothing
// is persisted — each export is generated from the live database rows and
// streamed to the caller (see the exports-on-demand decision).
package export

import (
	"io"
	"regexp"
	"strings"

	"github.com/valaf/valaf/internal/store"
)

// Exporter renders a notebook to a specific format.
type Exporter interface {
	ContentType() string
	Ext() string
	Render(w io.Writer, nb *store.Notebook) error
}

// For returns the exporter for a format name, if supported.
func For(format string) (Exporter, bool) {
	switch strings.ToLower(format) {
	case "json":
		return jsonExporter{}, true
	case "md", "markdown":
		return markdownExporter{}, true
	case "docx":
		return docxExporter{}, true
	default:
		return nil, false
	}
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Filename builds a stable, safe download name for an incident export.
func Filename(inc store.IncidentDetail, ext string) string {
	slug := slugRe.ReplaceAllString(strings.ToLower(inc.Title), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 60 {
		slug = slug[:60]
	}
	if slug == "" {
		slug = "incident"
	}
	short := inc.ID
	if len(short) > 8 {
		short = short[:8]
	}
	return "valaf-" + slug + "-" + short + "." + ext
}
