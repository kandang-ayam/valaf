package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReviewRepo performs the engineer review actions. Each mutation and its audit
// record are written in one transaction, so the audit trail can never diverge
// from the change it describes.
type ReviewRepo struct {
	pool *pgxpool.Pool
}

func NewReviewRepo(pool *pgxpool.Pool) *ReviewRepo { return &ReviewRepo{pool: pool} }

// recordAudit appends an append-only audit entry within a transaction.
func recordAudit(ctx context.Context, tx pgx.Tx, actorID, action, entityType, entityID string, details map[string]any) error {
	d, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor_id, action, entity_type, entity_id, details) VALUES ($1, $2, $3, $4, $5)`,
		nullIfEmpty(actorID), action, entityType, entityID, d)
	return err
}

// SetHypothesisVerdict records an engineer's verdict on a hypothesis and returns
// the owning incident id (for re-rendering). verdict is confirmed|rejected|none.
func (r *ReviewRepo) SetHypothesisVerdict(ctx context.Context, hypothesisID, verdict, note, actorID string) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var incidentID string
	err = tx.QueryRow(ctx, `
		WITH upd AS (
			UPDATE hypotheses
			SET verdict = $2, verdict_note = $3, verdict_by = $4, verdict_at = now()
			WHERE id = $1
			RETURNING analysis_id
		)
		SELECT a.incident_id::text FROM analyses a JOIN upd ON a.id = upd.analysis_id`,
		hypothesisID, verdict, nullIfEmpty(note), actorID,
	).Scan(&incidentID)
	if err != nil {
		return "", fmt.Errorf("set hypothesis verdict: %w", err)
	}

	if err := recordAudit(ctx, tx, actorID, "hypothesis_verdict", "hypothesis", hypothesisID,
		map[string]any{"verdict": verdict, "note": note}); err != nil {
		return "", err
	}
	return incidentID, tx.Commit(ctx)
}

// FlagEvidence marks an evidence item invalid (raw data untouched — only the
// flag columns change, per the immutability trigger).
func (r *ReviewRepo) FlagEvidence(ctx context.Context, evidenceID, comment, actorID string) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var incidentID string
	err = tx.QueryRow(ctx, `
		UPDATE evidence_items
		SET is_valid = false, invalid_comment = $2, flagged_by = $3, flagged_at = now()
		WHERE id = $1
		RETURNING incident_id::text`,
		evidenceID, comment, actorID,
	).Scan(&incidentID)
	if err != nil {
		return "", fmt.Errorf("flag evidence: %w", err)
	}

	if err := recordAudit(ctx, tx, actorID, "evidence_flagged", "evidence_item", evidenceID,
		map[string]any{"comment": comment}); err != nil {
		return "", err
	}
	return incidentID, tx.Commit(ctx)
}

// ResolutionInput is the engineer's written resolution.
type ResolutionInput struct {
	RootCause        string
	ActionsTaken     string
	UltimateSolution string
	Notes            string
}

// SaveResolution writes (or updates) the resolution and marks the incident
// resolved — admitting it to the learning corpus.
func (r *ReviewRepo) SaveResolution(ctx context.Context, incidentID string, in ResolutionInput, actorID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO resolutions (incident_id, root_cause, actions_taken, ultimate_solution, notes, resolved_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (incident_id) DO UPDATE SET
			root_cause = EXCLUDED.root_cause,
			actions_taken = EXCLUDED.actions_taken,
			ultimate_solution = EXCLUDED.ultimate_solution,
			notes = EXCLUDED.notes,
			resolved_by = EXCLUDED.resolved_by,
			resolved_at = now()`,
		incidentID, in.RootCause, nullIfEmpty(in.ActionsTaken), nullIfEmpty(in.UltimateSolution), nullIfEmpty(in.Notes), actorID,
	); err != nil {
		return fmt.Errorf("save resolution: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE incidents SET status = 'resolved', resolved_at = now()
		WHERE id = $1 AND status NOT IN ('deleted')`, incidentID); err != nil {
		return fmt.Errorf("mark resolved: %w", err)
	}

	if err := recordAudit(ctx, tx, actorID, "resolution_saved", "incident", incidentID, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
