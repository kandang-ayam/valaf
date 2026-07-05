package export

import (
	"fmt"
	"io"
	"strings"

	"github.com/valaf/valaf/internal/store"
)

type markdownExporter struct{}

func (markdownExporter) ContentType() string { return "text/markdown; charset=utf-8" }
func (markdownExporter) Ext() string         { return "md" }

func (markdownExporter) Render(w io.Writer, nb *store.Notebook) error {
	var b strings.Builder
	inc := nb.Incident

	fmt.Fprintf(&b, "# %s\n\n", inc.Title)
	fmt.Fprintf(&b, "**Severity:** %s  **Status:** %s", inc.Severity, inc.Status)
	if inc.TriageVerdict != "" {
		fmt.Fprintf(&b, "  **Triage:** %s", inc.TriageVerdict)
	}
	fmt.Fprintf(&b, "\n\n**Opened:** %s\n", inc.OpenedAt.Format("2006-01-02 15:04 MST"))
	if len(inc.EntityBag) > 0 {
		b.WriteString("**Entities:** ")
		first := true
		for k, v := range inc.EntityBag {
			if !first {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s=`%s`", k, v)
			first = false
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if a := nb.Analysis; a != nil {
		b.WriteString("## AI analysis\n\n")
		switch a.Status {
		case "ok":
			if a.Summary != "" {
				fmt.Fprintf(&b, "%s\n\n", a.Summary)
			}
			if len(a.Timeline) > 0 {
				b.WriteString("**Timeline**\n\n")
				for _, t := range a.Timeline {
					fmt.Fprintf(&b, "- `%s` %s\n", t.At, t.Text)
				}
				b.WriteString("\n")
			}
		case "failed":
			fmt.Fprintf(&b, "_Analysis failed — the notebook still holds all collected evidence._\n\n")
		default:
			fmt.Fprintf(&b, "_No AI provider configured — published with evidence only._\n\n")
		}
		if len(a.Gaps) > 0 {
			b.WriteString("**Evidence gaps**\n\n")
			for _, g := range a.Gaps {
				fmt.Fprintf(&b, "- %s\n", g)
			}
			b.WriteString("\n")
		}
	}

	if len(nb.Observations) > 0 {
		b.WriteString("## Observations\n\n")
		for _, o := range nb.Observations {
			fmt.Fprintf(&b, "- %s%s\n", o.Body, refsSuffix(o.CiteRefs))
		}
		b.WriteString("\n")
	}

	if len(nb.Hypotheses) > 0 {
		b.WriteString("## Ranked hypotheses\n\n")
		for _, h := range nb.Hypotheses {
			fmt.Fprintf(&b, "### %d. %s", h.Rank, h.Title)
			if h.Verdict != "none" && h.Verdict != "" {
				fmt.Fprintf(&b, " — **%s**", h.Verdict)
			}
			b.WriteString("\n\n")
			if h.Rationale != "" {
				fmt.Fprintf(&b, "%s\n\n", h.Rationale)
			}
			if len(h.SupportingRefs) > 0 {
				fmt.Fprintf(&b, "- Supporting: %s\n", strings.Join(h.SupportingRefs, ", "))
			}
			if len(h.ContradictingRefs) > 0 {
				fmt.Fprintf(&b, "- Contradicting: %s\n", strings.Join(h.ContradictingRefs, ", "))
			}
			for _, c := range h.Checks {
				fmt.Fprintf(&b, "- Check: %s\n", c)
			}
			if h.VerdictNote != "" {
				fmt.Fprintf(&b, "- Note: %s\n", h.VerdictNote)
			}
			b.WriteString("\n")
		}
	}

	if r := nb.Resolution; r != nil {
		b.WriteString("## Resolution\n\n")
		fmt.Fprintf(&b, "**Root cause:** %s\n\n", r.RootCause)
		if r.ActionsTaken != "" {
			fmt.Fprintf(&b, "**Actions taken:** %s\n\n", r.ActionsTaken)
		}
		if r.UltimateSolution != "" {
			fmt.Fprintf(&b, "**Solution / prevention:** %s\n\n", r.UltimateSolution)
		}
		if r.Notes != "" {
			fmt.Fprintf(&b, "**Notes:** %s\n\n", r.Notes)
		}
		if r.ResolvedBy != "" {
			fmt.Fprintf(&b, "_Resolved by %s · %s_\n\n", r.ResolvedBy, r.ResolvedAt.Format("2006-01-02 15:04 MST"))
		}
	}

	b.WriteString("## Evidence\n\n")
	for _, e := range nb.Evidence {
		fmt.Fprintf(&b, "### %s — %s / %s (%s)%s\n\n", e.Ref, e.Collector, e.Kind, e.Status, invalidSuffix(e))
		if e.Error != "" {
			fmt.Fprintf(&b, "> %s\n\n", e.Error)
		}
		fmt.Fprintf(&b, "Request:\n\n```json\n%s\n```\n\n", strings.TrimSpace(e.Request))
		if strings.TrimSpace(e.Result) != "" {
			fmt.Fprintf(&b, "Result:\n\n```json\n%s\n```\n\n", strings.TrimSpace(e.Result))
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func refsSuffix(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	return " [" + strings.Join(refs, ", ") + "]"
}

func invalidSuffix(e store.EvidenceView) string {
	if e.IsValid {
		return ""
	}
	return " — FLAGGED INVALID: " + e.InvalidComment
}
