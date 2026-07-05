package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/valaf/valaf/internal/core"
)

// Read models for the notebook UI. These are view shapes, deliberately flat and
// template-friendly.

type IncidentSummary struct {
	ID            string
	Title         string
	Status        string
	Severity      string
	TriageVerdict string
	AlertCount    int
	OpenedAt      time.Time
}

type IncidentDetail struct {
	ID            string
	Title         string
	Status        string
	Severity      string
	TriageVerdict string
	EntityBag     map[string]string
	OpenedAt      time.Time
	PublishedAt   *time.Time
	ResolvedAt    *time.Time
}

type AlertView struct {
	Source      string
	Fingerprint string
	Severity    string
	Annotations map[string]string
	StartsAt    *time.Time
	ReceivedAt  time.Time
}

type AnalysisView struct {
	Provider      string
	Model         string
	Status        string
	Summary       string
	Timeline      []core.TimelineEntry
	Gaps          []string
	TriageVerdict string
	Error         string
	CreatedAt     time.Time
}

type ObservationView struct {
	Body     string
	CiteRefs []string
}

type HypothesisView struct {
	ID                string
	Rank              int
	Title             string
	Rationale         string
	Verdict           string
	VerdictNote       string
	Checks            []string
	SupportingRefs    []string
	ContradictingRefs []string
}

type EvidenceView struct {
	Ref            string
	ID             string
	Collector      string
	Kind           string
	Status         string
	Request        string // pretty JSON
	Result         string // pretty JSON
	Error          string
	IsValid        bool
	InvalidComment string
	CapturedAt     time.Time
}

type ResolutionView struct {
	RootCause        string
	ActionsTaken     string
	UltimateSolution string
	Notes            string
	ResolvedBy       string
	ResolvedAt       time.Time
}

// SimilarUsedView is a past incident whose verified outcome informed the
// current analysis (learning provenance, rendered on the detail page).
type SimilarUsedView struct {
	IncidentID string
	Title      string
	Overlap    []string
}

// Notebook is the full incident detail assembled for the detail page.
type Notebook struct {
	Incident     IncidentDetail
	Alerts       []AlertView
	Analysis     *AnalysisView
	Observations []ObservationView
	Hypotheses   []HypothesisView
	Evidence     []EvidenceView
	Resolution   *ResolutionView
	SimilarUsed  []SimilarUsedView
}

type ReadRepo struct {
	pool *pgxpool.Pool
}

func NewReadRepo(pool *pgxpool.Pool) *ReadRepo { return &ReadRepo{pool: pool} }

// ListIncidents returns recent incidents, optionally filtered by a full-text
// query over the title.
func (r *ReadRepo) ListIncidents(ctx context.Context, query string) ([]IncidentSummary, error) {
	sql := `
		SELECT i.id::text, i.title, i.status::text, i.severity::text,
		       COALESCE(i.triage_verdict::text, ''), i.opened_at,
		       (SELECT count(*) FROM alerts a WHERE a.incident_id = i.id)
		FROM incidents i
		WHERE i.deleted_at IS NULL`
	args := []any{}
	if query != "" {
		sql += ` AND (i.search_vector @@ plainto_tsquery('english', $1) OR i.title ILIKE '%' || $1 || '%')`
		args = append(args, query)
	}
	sql += ` ORDER BY i.opened_at DESC LIMIT 200`

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []IncidentSummary
	for rows.Next() {
		var s IncidentSummary
		if err := rows.Scan(&s.ID, &s.Title, &s.Status, &s.Severity, &s.TriageVerdict, &s.OpenedAt, &s.AlertCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ErrNotebookNotFound is returned when an incident id does not exist.
var ErrNotebookNotFound = fmt.Errorf("notebook not found")

// LoadNotebook assembles the full incident detail.
func (r *ReadRepo) LoadNotebook(ctx context.Context, id string) (*Notebook, error) {
	nb := &Notebook{}
	var entityJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id::text, title, status::text, severity::text,
		       COALESCE(triage_verdict::text, ''), entity_bag, opened_at, published_at, resolved_at
		FROM incidents WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(&nb.Incident.ID, &nb.Incident.Title, &nb.Incident.Status, &nb.Incident.Severity,
		&nb.Incident.TriageVerdict, &entityJSON, &nb.Incident.OpenedAt, &nb.Incident.PublishedAt, &nb.Incident.ResolvedAt)
	if err != nil {
		return nil, ErrNotebookNotFound
	}
	nb.Incident.EntityBag = decodeStringMap(entityJSON)

	if err := r.loadAlerts(ctx, nb); err != nil {
		return nil, err
	}

	// Evidence first, so we can map ids to display refs (E1, E2, …).
	refByID, err := r.loadEvidence(ctx, nb)
	if err != nil {
		return nil, err
	}

	analysisID, err := r.loadAnalysis(ctx, nb)
	if err != nil {
		return nil, err
	}
	if analysisID != "" {
		if err := r.loadObservations(ctx, nb, analysisID, refByID); err != nil {
			return nil, err
		}
		if err := r.loadHypotheses(ctx, nb, analysisID, refByID); err != nil {
			return nil, err
		}
	}
	if err := r.loadResolution(ctx, nb); err != nil {
		return nil, err
	}
	if analysisID != "" {
		if err := r.loadSimilarUsed(ctx, nb, analysisID); err != nil {
			return nil, err
		}
	}
	return nb, nil
}

// loadSimilarUsed lists the past incidents recorded as informing this analysis.
func (r *ReadRepo) loadSimilarUsed(ctx context.Context, nb *Notebook, analysisID string) error {
	rows, err := r.pool.Query(ctx, `
		SELECT s.source_incident_id::text, i.title,
		       COALESCE(ARRAY(SELECT jsonb_array_elements_text(s.overlap->'alertnames')), '{}')
		FROM analysis_similar_incidents s
		JOIN incidents i ON i.id = s.source_incident_id
		WHERE s.analysis_id = $1
		ORDER BY s.score DESC`, analysisID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v SimilarUsedView
		if err := rows.Scan(&v.IncidentID, &v.Title, &v.Overlap); err != nil {
			return err
		}
		nb.SimilarUsed = append(nb.SimilarUsed, v)
	}
	return rows.Err()
}

func (r *ReadRepo) loadResolution(ctx context.Context, nb *Notebook) error {
	var res ResolutionView
	err := r.pool.QueryRow(ctx, `
		SELECT r.root_cause, COALESCE(r.actions_taken, ''), COALESCE(r.ultimate_solution, ''),
		       COALESCE(r.notes, ''), COALESCE(u.username, ''), r.resolved_at
		FROM resolutions r
		LEFT JOIN users u ON u.id = r.resolved_by
		WHERE r.incident_id = $1`, nb.Incident.ID,
	).Scan(&res.RootCause, &res.ActionsTaken, &res.UltimateSolution, &res.Notes, &res.ResolvedBy, &res.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // no resolution yet
	}
	if err != nil {
		return err
	}
	nb.Resolution = &res
	return nil
}

func (r *ReadRepo) loadAlerts(ctx context.Context, nb *Notebook) error {
	rows, err := r.pool.Query(ctx, `
		SELECT source::text, fingerprint, severity, annotations, starts_at, received_at
		FROM alerts WHERE incident_id = $1 ORDER BY received_at`, nb.Incident.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a AlertView
		var annJSON []byte
		if err := rows.Scan(&a.Source, &a.Fingerprint, &a.Severity, &annJSON, &a.StartsAt, &a.ReceivedAt); err != nil {
			return err
		}
		a.Annotations = decodeStringMap(annJSON)
		nb.Alerts = append(nb.Alerts, a)
	}
	return rows.Err()
}

func (r *ReadRepo) loadEvidence(ctx context.Context, nb *Notebook) (map[string]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, collector::text, kind::text, status::text, request, result,
		       COALESCE(error, ''), is_valid, COALESCE(invalid_comment, ''), captured_at
		FROM evidence_items WHERE incident_id = $1 ORDER BY captured_at, id`, nb.Incident.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	refByID := map[string]string{}
	i := 0
	for rows.Next() {
		var e EvidenceView
		var request, result []byte
		if err := rows.Scan(&e.ID, &e.Collector, &e.Kind, &e.Status, &request, &result,
			&e.Error, &e.IsValid, &e.InvalidComment, &e.CapturedAt); err != nil {
			return nil, err
		}
		i++
		e.Ref = "E" + strconv.Itoa(i)
		e.Request = prettyJSON(request)
		e.Result = prettyJSON(result)
		refByID[e.ID] = e.Ref
		nb.Evidence = append(nb.Evidence, e)
	}
	return refByID, rows.Err()
}

func (r *ReadRepo) loadAnalysis(ctx context.Context, nb *Notebook) (string, error) {
	var (
		a          AnalysisView
		id         string
		provider   *string
		model      *string
		summary    *string
		verdict    *string
		errText    *string
		timeline   []byte
		gaps       []byte
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id::text, provider::text, model, status::text, summary, timeline, gaps,
		       triage_verdict::text, error, created_at
		FROM analyses WHERE incident_id = $1 AND is_current
		LIMIT 1`, nb.Incident.ID,
	).Scan(&id, &provider, &model, &a.Status, &summary, &timeline, &gaps, &verdict, &errText, &a.CreatedAt)
	if err != nil {
		return "", nil // no analysis yet is fine
	}
	a.Provider = deref(provider)
	a.Model = deref(model)
	a.Summary = deref(summary)
	a.TriageVerdict = deref(verdict)
	a.Error = deref(errText)
	_ = json.Unmarshal(timeline, &a.Timeline)
	_ = json.Unmarshal(gaps, &a.Gaps)
	nb.Analysis = &a
	return id, nil
}

func (r *ReadRepo) loadObservations(ctx context.Context, nb *Notebook, analysisID string, refByID map[string]string) error {
	rows, err := r.pool.Query(ctx, `
		SELECT o.body,
		       COALESCE(array_agg(oc.evidence_item_id::text) FILTER (WHERE oc.evidence_item_id IS NOT NULL), '{}')
		FROM observations o
		LEFT JOIN observation_citations oc ON oc.observation_id = o.id
		WHERE o.analysis_id = $1
		GROUP BY o.id, o.body, o.ordinal
		ORDER BY o.ordinal`, analysisID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var o ObservationView
		var ids []string
		if err := rows.Scan(&o.Body, &ids); err != nil {
			return err
		}
		o.CiteRefs = mapRefs(ids, refByID)
		nb.Observations = append(nb.Observations, o)
	}
	return rows.Err()
}

func (r *ReadRepo) loadHypotheses(ctx context.Context, nb *Notebook, analysisID string, refByID map[string]string) error {
	rows, err := r.pool.Query(ctx, `
		SELECT h.id::text, h.rank, h.title, COALESCE(h.rationale, ''), h.suggested_checks,
		       h.verdict::text, COALESCE(h.verdict_note, ''),
		       COALESCE(array_agg(he.evidence_item_id::text) FILTER (WHERE he.relation = 'supporting'), '{}'),
		       COALESCE(array_agg(he.evidence_item_id::text) FILTER (WHERE he.relation = 'contradicting'), '{}')
		FROM hypotheses h
		LEFT JOIN hypothesis_evidence he ON he.hypothesis_id = h.id
		WHERE h.analysis_id = $1
		GROUP BY h.id, h.rank, h.title, h.rationale, h.suggested_checks, h.verdict, h.verdict_note
		ORDER BY h.rank`, analysisID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h HypothesisView
		var checks []byte
		var sup, con []string
		if err := rows.Scan(&h.ID, &h.Rank, &h.Title, &h.Rationale, &checks, &h.Verdict, &h.VerdictNote, &sup, &con); err != nil {
			return err
		}
		_ = json.Unmarshal(checks, &h.Checks)
		h.SupportingRefs = mapRefs(sup, refByID)
		h.ContradictingRefs = mapRefs(con, refByID)
		nb.Hypotheses = append(nb.Hypotheses, h)
	}
	return rows.Err()
}

// helpers

func decodeStringMap(b []byte) map[string]string {
	m := map[string]string{}
	if len(b) > 0 {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func prettyJSON(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return string(b)
	}
	return buf.String()
}

func mapRefs(ids []string, refByID map[string]string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if ref, ok := refByID[id]; ok {
			out = append(out, ref)
		}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
