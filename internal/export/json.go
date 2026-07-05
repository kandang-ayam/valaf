package export

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/valaf/valaf/internal/core"
	"github.com/valaf/valaf/internal/store"
)

type jsonExporter struct{}

func (jsonExporter) ContentType() string { return "application/json; charset=utf-8" }
func (jsonExporter) Ext() string         { return "json" }

func (jsonExporter) Render(w io.Writer, nb *store.Notebook) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(buildDoc(nb))
}

// exportDoc is the machine-readable notebook shape for custom pipelines.
type exportDoc struct {
	Incident     incidentDoc      `json:"incident"`
	Analysis     *analysisDoc     `json:"analysis,omitempty"`
	Observations []observationDoc `json:"observations,omitempty"`
	Hypotheses   []hypothesisDoc  `json:"hypotheses,omitempty"`
	Evidence     []evidenceDoc    `json:"evidence,omitempty"`
	Resolution   *resolutionDoc   `json:"resolution,omitempty"`
	Alerts       []alertDoc       `json:"alerts,omitempty"`
}

type incidentDoc struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Status        string            `json:"status"`
	Severity      string            `json:"severity"`
	TriageVerdict string            `json:"triage_verdict,omitempty"`
	EntityBag     map[string]string `json:"entities,omitempty"`
	OpenedAt      time.Time         `json:"opened_at"`
	PublishedAt   *time.Time        `json:"published_at,omitempty"`
	ResolvedAt    *time.Time        `json:"resolved_at,omitempty"`
}

type analysisDoc struct {
	Provider string               `json:"provider,omitempty"`
	Model    string               `json:"model,omitempty"`
	Status   string               `json:"status"`
	Summary  string               `json:"summary,omitempty"`
	Timeline []core.TimelineEntry `json:"timeline,omitempty"`
	Gaps     []string             `json:"gaps,omitempty"`
	Error    string               `json:"error,omitempty"`
}

type observationDoc struct {
	Body  string   `json:"body"`
	Cites []string `json:"cites,omitempty"`
}

type hypothesisDoc struct {
	Rank          int      `json:"rank"`
	Title         string   `json:"title"`
	Rationale     string   `json:"rationale,omitempty"`
	Supporting    []string `json:"supporting,omitempty"`
	Contradicting []string `json:"contradicting,omitempty"`
	Checks        []string `json:"checks,omitempty"`
	Verdict       string   `json:"verdict"`
	VerdictNote   string   `json:"verdict_note,omitempty"`
}

type evidenceDoc struct {
	Ref            string          `json:"ref"`
	Collector      string          `json:"collector"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	Request        json.RawMessage `json:"request,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`
	Valid          bool            `json:"valid"`
	InvalidComment string          `json:"invalid_comment,omitempty"`
}

type resolutionDoc struct {
	RootCause        string    `json:"root_cause"`
	ActionsTaken     string    `json:"actions_taken,omitempty"`
	UltimateSolution string    `json:"ultimate_solution,omitempty"`
	Notes            string    `json:"notes,omitempty"`
	ResolvedBy       string    `json:"resolved_by,omitempty"`
	ResolvedAt       time.Time `json:"resolved_at"`
}

type alertDoc struct {
	Source      string            `json:"source"`
	Severity    string            `json:"severity"`
	Annotations map[string]string `json:"annotations,omitempty"`
	ReceivedAt  time.Time         `json:"received_at"`
}

func buildDoc(nb *store.Notebook) exportDoc {
	doc := exportDoc{
		Incident: incidentDoc{
			ID: nb.Incident.ID, Title: nb.Incident.Title, Status: nb.Incident.Status,
			Severity: nb.Incident.Severity, TriageVerdict: nb.Incident.TriageVerdict,
			EntityBag: nb.Incident.EntityBag, OpenedAt: nb.Incident.OpenedAt,
			PublishedAt: nb.Incident.PublishedAt, ResolvedAt: nb.Incident.ResolvedAt,
		},
	}
	if a := nb.Analysis; a != nil {
		doc.Analysis = &analysisDoc{
			Provider: a.Provider, Model: a.Model, Status: a.Status,
			Summary: a.Summary, Timeline: a.Timeline, Gaps: a.Gaps, Error: a.Error,
		}
	}
	for _, o := range nb.Observations {
		doc.Observations = append(doc.Observations, observationDoc{Body: o.Body, Cites: o.CiteRefs})
	}
	for _, h := range nb.Hypotheses {
		doc.Hypotheses = append(doc.Hypotheses, hypothesisDoc{
			Rank: h.Rank, Title: h.Title, Rationale: h.Rationale,
			Supporting: h.SupportingRefs, Contradicting: h.ContradictingRefs,
			Checks: h.Checks, Verdict: h.Verdict, VerdictNote: h.VerdictNote,
		})
	}
	for _, e := range nb.Evidence {
		doc.Evidence = append(doc.Evidence, evidenceDoc{
			Ref: e.Ref, Collector: e.Collector, Kind: e.Kind, Status: e.Status,
			Request: rawJSON(e.Request), Result: rawJSON(e.Result), Error: e.Error,
			Valid: e.IsValid, InvalidComment: e.InvalidComment,
		})
	}
	if r := nb.Resolution; r != nil {
		doc.Resolution = &resolutionDoc{
			RootCause: r.RootCause, ActionsTaken: r.ActionsTaken,
			UltimateSolution: r.UltimateSolution, Notes: r.Notes,
			ResolvedBy: r.ResolvedBy, ResolvedAt: r.ResolvedAt,
		}
	}
	for _, a := range nb.Alerts {
		doc.Alerts = append(doc.Alerts, alertDoc{
			Source: a.Source, Severity: a.Severity, Annotations: a.Annotations, ReceivedAt: a.ReceivedAt,
		})
	}
	return doc
}

// rawJSON embeds valid JSON verbatim; otherwise nil (dropped via omitempty).
func rawJSON(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" || !json.Valid([]byte(s)) {
		return nil
	}
	return json.RawMessage(s)
}
