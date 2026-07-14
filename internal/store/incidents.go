package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/valaf/valaf/internal/core"
)

// IncidentRepo persists incidents and alerts. It implements core.IncidentRepository.
type IncidentRepo struct {
	pool *pgxpool.Pool
}

func NewIncidentRepo(pool *pgxpool.Pool) *IncidentRepo { return &IncidentRepo{pool: pool} }

// statuses an incident can be in and still absorb related alerts. Terminal
// states (resolved/false_positive/deleted) start a fresh notebook next time.
const activeStatuses = `('open','analyzing','published')`

func (r *IncidentRepo) IngestBatch(ctx context.Context, batch core.AlertBatch, severity string, since time.Time) (core.IngestOutcome, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return core.IngestOutcome{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	incidentID, created, err := findOrCreate(ctx, tx, batch, severity, since)
	if err != nil {
		return core.IngestOutcome{}, err
	}

	for _, a := range batch.Alerts {
		if err := upsertAlert(ctx, tx, incidentID, batch.Source, a); err != nil {
			return core.IngestOutcome{}, err
		}
	}

	// A brand-new incident triggers evidence collection. Enqueued in the same
	// transaction so the job can never be lost after the incident is committed.
	if created {
		if _, err := tx.Exec(ctx, `INSERT INTO jobs (kind, incident_id) VALUES ('collect', $1)`, incidentID); err != nil {
			return core.IngestOutcome{}, fmt.Errorf("enqueue collect job: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return core.IngestOutcome{}, err
	}
	return core.IngestOutcome{IncidentID: incidentID, Created: created, Alerts: len(batch.Alerts)}, nil
}

// LoadCollectTarget assembles what a collector needs for an incident: its entity
// bag and a time window anchored on the earliest alert (falling back to when the
// incident opened).
func (r *IncidentRepo) LoadCollectTarget(ctx context.Context, incidentID string) (core.CollectTarget, error) {
	var (
		title      string
		entityJSON []byte
		openedAt   time.Time
		anchor     *time.Time
		alertnames []string
	)
	err := r.pool.QueryRow(ctx, `
		SELECT i.title, i.entity_bag, i.opened_at,
		       (SELECT min(starts_at) FROM alerts a WHERE a.incident_id = i.id),
		       (SELECT COALESCE(array_agg(DISTINCT a.labels->>'alertname')
		               FILTER (WHERE a.labels->>'alertname' IS NOT NULL), '{}')
		        FROM alerts a WHERE a.incident_id = i.id)
		FROM incidents i
		WHERE i.id = $1`,
		incidentID,
	).Scan(&title, &entityJSON, &openedAt, &anchor, &alertnames)
	if err != nil {
		return core.CollectTarget{}, fmt.Errorf("load collect target: %w", err)
	}

	entityBag := map[string]string{}
	if len(entityJSON) > 0 {
		if err := json.Unmarshal(entityJSON, &entityBag); err != nil {
			return core.CollectTarget{}, fmt.Errorf("decode entity_bag: %w", err)
		}
	}

	// Merge labels + annotations across the incident's alerts, so collectors can
	// find references like Grafana's __dashboardUid__ / grafana_panel_url.
	annotations := map[string]string{}
	arows, err := r.pool.Query(ctx, `SELECT labels, annotations FROM alerts WHERE incident_id = $1`, incidentID)
	if err != nil {
		return core.CollectTarget{}, fmt.Errorf("load alert annotations: %w", err)
	}
	defer arows.Close()
	for arows.Next() {
		var labelsJSON, annJSON []byte
		if err := arows.Scan(&labelsJSON, &annJSON); err != nil {
			return core.CollectTarget{}, err
		}
		for k, v := range decodeStringMap(labelsJSON) {
			annotations[k] = v
		}
		for k, v := range decodeStringMap(annJSON) {
			annotations[k] = v
		}
	}
	if err := arows.Err(); err != nil {
		return core.CollectTarget{}, err
	}

	at := openedAt
	if anchor != nil {
		at = *anchor
	}
	end := at.Add(15 * time.Minute)
	if now := time.Now(); end.After(now) {
		end = now
	}
	return core.CollectTarget{
		IncidentID:  incidentID,
		Title:       title,
		Alertnames:  alertnames,
		EntityBag:   entityBag,
		Annotations: annotations,
		Window: core.TimeWindow{
			Start: at.Add(-15 * time.Minute),
			End:   end,
			Step:  30 * time.Second,
		},
	}, nil
}

// findOrCreate locks and returns an existing active incident with the same
// grouping key opened after `since`, or inserts a new one.
func findOrCreate(ctx context.Context, tx pgx.Tx, batch core.AlertBatch, severity string, since time.Time) (id string, created bool, err error) {
	err = tx.QueryRow(ctx, `
		SELECT id FROM incidents
		WHERE grouping_key = $1
		  AND status IN `+activeStatuses+`
		  AND opened_at > $2
		ORDER BY opened_at DESC
		LIMIT 1
		FOR UPDATE`,
		batch.GroupingKey, since,
	).Scan(&id)

	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("lookup incident: %w", err)
	}

	entityBag, err := json.Marshal(batch.EntityBag)
	if err != nil {
		return "", false, err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO incidents (title, severity, grouping_key, entity_bag)
		VALUES ($1, $2, $3, $4)
		RETURNING id`,
		batch.Title, severity, batch.GroupingKey, entityBag,
	).Scan(&id)
	if err != nil {
		return "", false, fmt.Errorf("create incident: %w", err)
	}
	return id, true, nil
}

func upsertAlert(ctx context.Context, tx pgx.Tx, incidentID string, source core.SourceType, a core.Alert) error {
	labels, err := json.Marshal(orEmpty(a.Labels))
	if err != nil {
		return err
	}
	annotations, err := json.Marshal(orEmpty(a.Annotations))
	if err != nil {
		return err
	}
	raw := a.Raw
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO alerts (incident_id, source, fingerprint, severity, labels, annotations, raw_payload, starts_at, ends_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (incident_id, fingerprint) DO UPDATE SET
			severity    = EXCLUDED.severity,
			labels      = EXCLUDED.labels,
			annotations = EXCLUDED.annotations,
			raw_payload = EXCLUDED.raw_payload,
			ends_at     = EXCLUDED.ends_at,
			received_at = now()`,
		incidentID, string(source), a.Fingerprint, a.Severity,
		labels, annotations, []byte(raw), a.StartsAt, a.EndsAt,
	)
	if err != nil {
		return fmt.Errorf("upsert alert %s: %w", a.Fingerprint, err)
	}
	return nil
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
