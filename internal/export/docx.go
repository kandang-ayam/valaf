package export

import (
	"archive/zip"
	"fmt"
	"io"
	"strings"

	"github.com/valaf/valaf/internal/store"
)

// docxExporter writes a minimal, valid Office Open XML (.docx) document by hand —
// a zip of a few XML parts. Deliberately no third-party library (unioffice is
// AGPL/commercial, which clashes with valaf's MIT license). Text-only for now;
// snapshot image embedding follows when the dashboard collector lands.
type docxExporter struct{}

func (docxExporter) ContentType() string {
	return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
}
func (docxExporter) Ext() string { return "docx" }

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

const relsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

func (docxExporter) Render(w io.Writer, nb *store.Notebook) error {
	zw := zip.NewWriter(w)

	parts := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", relsXML},
		{"word/document.xml", documentXML(nb)},
	}
	for _, p := range parts {
		f, err := zw.Create(p.name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(f, p.body); err != nil {
			return err
		}
	}
	return zw.Close()
}

func documentXML(nb *store.Notebook) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)

	inc := nb.Incident
	heading(&b, 1, inc.Title)
	meta := fmt.Sprintf("Severity: %s   Status: %s", inc.Severity, inc.Status)
	if inc.TriageVerdict != "" {
		meta += "   Triage: " + inc.TriageVerdict
	}
	para(&b, meta)
	para(&b, "Opened: "+inc.OpenedAt.Format("2006-01-02 15:04 MST"))

	if a := nb.Analysis; a != nil {
		heading(&b, 2, "AI analysis")
		switch a.Status {
		case "ok":
			if a.Summary != "" {
				para(&b, a.Summary)
			}
			for _, t := range a.Timeline {
				para(&b, "• "+t.At+"  "+t.Text)
			}
		case "failed":
			para(&b, "Analysis failed — the notebook still holds all collected evidence.")
		default:
			para(&b, "No AI provider configured — published with evidence only.")
		}
		for _, g := range a.Gaps {
			para(&b, "Gap: "+g)
		}
	}

	if len(nb.Observations) > 0 {
		heading(&b, 2, "Observations")
		for _, o := range nb.Observations {
			para(&b, "• "+o.Body+refsSuffix(o.CiteRefs))
		}
	}

	if len(nb.Hypotheses) > 0 {
		heading(&b, 2, "Ranked hypotheses")
		for _, h := range nb.Hypotheses {
			title := fmt.Sprintf("%d. %s", h.Rank, h.Title)
			if h.Verdict != "none" && h.Verdict != "" {
				title += " — " + strings.ToUpper(h.Verdict)
			}
			heading(&b, 3, title)
			if h.Rationale != "" {
				para(&b, h.Rationale)
			}
			if len(h.SupportingRefs) > 0 {
				para(&b, "Supporting: "+strings.Join(h.SupportingRefs, ", "))
			}
			if len(h.ContradictingRefs) > 0 {
				para(&b, "Contradicting: "+strings.Join(h.ContradictingRefs, ", "))
			}
			for _, c := range h.Checks {
				para(&b, "Check: "+c)
			}
			if h.VerdictNote != "" {
				para(&b, "Note: "+h.VerdictNote)
			}
		}
	}

	if r := nb.Resolution; r != nil {
		heading(&b, 2, "Resolution")
		para(&b, "Root cause: "+r.RootCause)
		if r.ActionsTaken != "" {
			para(&b, "Actions taken: "+r.ActionsTaken)
		}
		if r.UltimateSolution != "" {
			para(&b, "Solution / prevention: "+r.UltimateSolution)
		}
		if r.Notes != "" {
			para(&b, "Notes: "+r.Notes)
		}
		if r.ResolvedBy != "" {
			para(&b, "Resolved by "+r.ResolvedBy+" · "+r.ResolvedAt.Format("2006-01-02 15:04 MST"))
		}
	}

	heading(&b, 2, "Evidence")
	for _, e := range nb.Evidence {
		heading(&b, 3, fmt.Sprintf("%s — %s / %s (%s)%s", e.Ref, e.Collector, e.Kind, e.Status, invalidSuffix(e)))
		if e.Error != "" {
			para(&b, e.Error)
		}
		para(&b, "Request: "+strings.TrimSpace(e.Request))
		if strings.TrimSpace(e.Result) != "" {
			para(&b, "Result: "+strings.TrimSpace(e.Result))
		}
	}

	b.WriteString(`<w:sectPr/></w:body></w:document>`)
	return b.String()
}

// heading writes a bold paragraph sized by level (direct formatting — no styles
// part needed). Levels 1..3 map to 18/14/12pt.
func heading(b *strings.Builder, level int, text string) {
	sz := map[int]int{1: 36, 2: 28, 3: 24}[level]
	if sz == 0 {
		sz = 24
	}
	fmt.Fprintf(b, `<w:p><w:pPr><w:spacing w:before="180" w:after="60"/></w:pPr>`+
		`<w:r><w:rPr><w:b/><w:sz w:val="%d"/></w:rPr><w:t xml:space="preserve">%s</w:t></w:r></w:p>`,
		sz, escapeXML(text))
}

func para(b *strings.Builder, text string) {
	fmt.Fprintf(b, `<w:p><w:r><w:t xml:space="preserve">%s</w:t></w:r></w:p>`, escapeXML(text))
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}
