package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/valaf/valaf/internal/core"
)

// AnalysisRepo loads analysis input and persists results. Implements
// core.AnalysisRepository.
type AnalysisRepo struct {
	pool *pgxpool.Pool
}

func NewAnalysisRepo(pool *pgxpool.Pool) *AnalysisRepo { return &AnalysisRepo{pool: pool} }

// LoadAnalysisRequest builds the model input: the incident plus its evidence,
// each assigned a stable handle E1, E2, … in capture order.
func (r *AnalysisRepo) LoadAnalysisRequest(ctx context.Context, incidentID string) (core.AnalysisRequest, error) {
	var (
		title      string
		severity   string
		entityJSON []byte
	)
	err := r.pool.QueryRow(ctx,
		`SELECT title, severity, entity_bag FROM incidents WHERE id = $1`, incidentID,
	).Scan(&title, &severity, &entityJSON)
	if err != nil {
		return core.AnalysisRequest{}, fmt.Errorf("load incident: %w", err)
	}

	entityBag := map[string]string{}
	if len(entityJSON) > 0 {
		_ = json.Unmarshal(entityJSON, &entityBag)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id::text, collector::text, kind::text, status::text, request, result
		FROM evidence_items
		WHERE incident_id = $1 AND is_valid
		ORDER BY captured_at, id`, incidentID)
	if err != nil {
		return core.AnalysisRequest{}, fmt.Errorf("load evidence: %w", err)
	}
	defer rows.Close()

	req := core.AnalysisRequest{IncidentTitle: title, Severity: severity, EntityBag: entityBag}
	i := 0
	for rows.Next() {
		var e core.EvidenceRef
		var result []byte
		if err := rows.Scan(&e.ID, &e.Collector, &e.Kind, &e.Status, &e.Request, &result); err != nil {
			return core.AnalysisRequest{}, err
		}
		i++
		e.Ref = "E" + strconv.Itoa(i)
		e.Result = result
		req.Evidence = append(req.Evidence, e)
	}
	if err := rows.Err(); err != nil {
		return core.AnalysisRequest{}, err
	}

	similar, err := r.loadSimilar(ctx, incidentID)
	if err != nil {
		return core.AnalysisRequest{}, err
	}
	req.Similar = similar
	return req, nil
}

// maxSimilar caps how many past incidents feed a new analysis.
const maxSimilar = 3

// loadSimilar finds past RESOLVED incidents sharing alertnames with this one
// and attaches their engineer-verified outcomes (rule 11).
func (r *AnalysisRepo) loadSimilar(ctx context.Context, incidentID string) ([]core.SimilarIncident, error) {
	rows, err := r.pool.Query(ctx, `
		WITH mine AS (
			SELECT DISTINCT labels->>'alertname' AS name
			FROM alerts WHERE incident_id = $1 AND labels->>'alertname' IS NOT NULL
		)
		SELECT i.id::text, i.title, res.root_cause, COALESCE(res.ultimate_solution, ''),
		       array_agg(DISTINCT a.labels->>'alertname') AS overlap
		FROM incidents i
		JOIN resolutions res ON res.incident_id = i.id
		JOIN alerts a ON a.incident_id = i.id
		WHERE i.status = 'resolved'
		  AND i.id <> $1
		  AND a.labels->>'alertname' IN (SELECT name FROM mine)
		GROUP BY i.id, i.title, res.root_cause, res.ultimate_solution, i.resolved_at
		ORDER BY count(DISTINCT a.labels->>'alertname') DESC, i.resolved_at DESC
		LIMIT `+strconv.Itoa(maxSimilar), incidentID)
	if err != nil {
		return nil, fmt.Errorf("find similar incidents: %w", err)
	}
	defer rows.Close()

	var out []core.SimilarIncident
	for rows.Next() {
		var s core.SimilarIncident
		if err := rows.Scan(&s.IncidentID, &s.Title, &s.RootCause, &s.Solution, &s.OverlapNames); err != nil {
			return nil, err
		}
		s.Score = float64(len(s.OverlapNames))
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Attach engineer verdicts from each similar incident's current analysis.
	for i := range out {
		vrows, err := r.pool.Query(ctx, `
			SELECT h.title, h.verdict::text
			FROM hypotheses h
			JOIN analyses an ON an.id = h.analysis_id AND an.is_current
			WHERE an.incident_id = $1 AND h.verdict IN ('confirmed', 'rejected')`,
			out[i].IncidentID)
		if err != nil {
			return nil, err
		}
		for vrows.Next() {
			var title, verdict string
			if err := vrows.Scan(&title, &verdict); err != nil {
				vrows.Close()
				return nil, err
			}
			if verdict == "confirmed" {
				out[i].Confirmed = append(out[i].Confirmed, title)
			} else {
				out[i].RuledOut = append(out[i].RuledOut, title)
			}
		}
		vrows.Close()
		if err := vrows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SaveAnalysis writes the analysis (and its observations/hypotheses/citations
// when ok) and flips the incident to published, in one transaction.
func (r *AnalysisRepo) SaveAnalysis(ctx context.Context, incidentID string, rec core.AnalysisRecord) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	// Only one current analysis per incident.
	if _, err := tx.Exec(ctx,
		`UPDATE analyses SET is_current = false WHERE incident_id = $1 AND is_current`, incidentID,
	); err != nil {
		return fmt.Errorf("clear current analysis: %w", err)
	}

	timeline, _ := json.Marshal(orEmptySlice(rec.Result.Timeline))
	gaps, _ := json.Marshal(orEmptyStrings(rec.Result.Gaps))

	var analysisID string
	err = tx.QueryRow(ctx, `
		INSERT INTO analyses (incident_id, provider, model, status, summary, timeline, gaps, triage_verdict, is_current, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, $9)
		RETURNING id`,
		incidentID,
		nullIfEmpty(rec.Provider),
		nullIfEmpty(rec.Model),
		rec.Status,
		nullIfEmpty(rec.Result.Summary),
		timeline,
		gaps,
		nullIfEmpty(rec.TriageVerdict),
		nullIfEmpty(rec.Error),
	).Scan(&analysisID)
	if err != nil {
		return fmt.Errorf("insert analysis: %w", err)
	}

	if rec.Status == "ok" {
		if err := insertObservations(ctx, tx, analysisID, rec); err != nil {
			return err
		}
		if err := insertHypotheses(ctx, tx, analysisID, rec); err != nil {
			return err
		}
		// Learning provenance: exactly which past incidents informed this run.
		for _, s := range rec.Similar {
			overlap, _ := json.Marshal(map[string]any{"alertnames": s.OverlapNames})
			if _, err := tx.Exec(ctx, `
				INSERT INTO analysis_similar_incidents (analysis_id, source_incident_id, overlap, score)
				VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
				analysisID, s.IncidentID, overlap, s.Score,
			); err != nil {
				return fmt.Errorf("record similar-incident provenance: %w", err)
			}
		}
	}

	// Publish the notebook (unless it was already resolved/deleted).
	if _, err := tx.Exec(ctx, `
		UPDATE incidents
		SET status = 'published', published_at = COALESCE(published_at, now()), triage_verdict = $2
		WHERE id = $1 AND status NOT IN ('resolved', 'false_positive', 'deleted')`,
		incidentID, nullIfEmpty(rec.TriageVerdict),
	); err != nil {
		return fmt.Errorf("publish incident: %w", err)
	}

	return tx.Commit(ctx)
}

func insertObservations(ctx context.Context, tx pgx.Tx, analysisID string, rec core.AnalysisRecord) error {
	for i, o := range rec.Result.Observations {
		var obsID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO observations (analysis_id, body, ordinal) VALUES ($1, $2, $3) RETURNING id`,
			analysisID, o.Body, i,
		).Scan(&obsID); err != nil {
			return fmt.Errorf("insert observation: %w", err)
		}
		for _, ref := range o.Cites {
			id, ok := rec.RefToID[ref]
			if !ok {
				continue
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO observation_citations (observation_id, evidence_item_id) VALUES ($1, $2)
				 ON CONFLICT DO NOTHING`, obsID, id,
			); err != nil {
				return fmt.Errorf("insert citation: %w", err)
			}
		}
	}
	return nil
}

func insertHypotheses(ctx context.Context, tx pgx.Tx, analysisID string, rec core.AnalysisRecord) error {
	for _, h := range rec.Result.Hypotheses {
		checks, _ := json.Marshal(orEmptyStrings(h.Checks))
		var hypID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO hypotheses (analysis_id, rank, title, rationale, suggested_checks)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			analysisID, h.Rank, h.Title, nullIfEmpty(h.Rationale), checks,
		).Scan(&hypID); err != nil {
			return fmt.Errorf("insert hypothesis: %w", err)
		}
		if err := linkEvidence(ctx, tx, hypID, h.Supporting, "supporting", rec.RefToID); err != nil {
			return err
		}
		if err := linkEvidence(ctx, tx, hypID, h.Contradicting, "contradicting", rec.RefToID); err != nil {
			return err
		}
	}
	return nil
}

func linkEvidence(ctx context.Context, tx pgx.Tx, hypID string, refs []string, relation string, refToID map[string]string) error {
	for _, ref := range refs {
		id, ok := refToID[ref]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO hypothesis_evidence (hypothesis_id, evidence_item_id, relation)
			 VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, hypID, id, relation,
		); err != nil {
			return fmt.Errorf("link hypothesis evidence: %w", err)
		}
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orEmptySlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
