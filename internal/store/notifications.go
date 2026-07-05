package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/valaf/valaf/internal/core"
)

// NotificationRepo builds notification payloads and records outcomes.
// Implements core.NotificationRepo.
type NotificationRepo struct {
	pool *pgxpool.Pool
}

func NewNotificationRepo(pool *pgxpool.Pool) *NotificationRepo { return &NotificationRepo{pool: pool} }

// LoadNotification assembles the payload for an incident: its identity/verdict
// plus the current analysis summary and top-ranked hypothesis, if any.
func (r *NotificationRepo) LoadNotification(ctx context.Context, incidentID string) (core.Notification, error) {
	var n core.Notification
	err := r.pool.QueryRow(ctx, `
		SELECT i.id::text, i.title, i.severity::text, COALESCE(i.triage_verdict::text, 'unknown'),
		       COALESCE(a.summary, ''),
		       COALESCE((SELECT h.title FROM hypotheses h WHERE h.analysis_id = a.id ORDER BY h.rank LIMIT 1), '')
		FROM incidents i
		LEFT JOIN analyses a ON a.incident_id = i.id AND a.is_current
		WHERE i.id = $1`, incidentID,
	).Scan(&n.IncidentID, &n.Title, &n.Severity, &n.Verdict, &n.Summary, &n.TopHypothesis)
	if err != nil {
		return core.Notification{}, fmt.Errorf("load notification: %w", err)
	}
	return n, nil
}

// Record writes a notifications row for one channel outcome.
func (r *NotificationRepo) Record(ctx context.Context, incidentID, channel, target, status, reason string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notifications (incident_id, channel, target, status, reason)
		VALUES ($1, $2, $3, $4, $5)`,
		incidentID, channel, nullIfEmpty(target), status, reason)
	return err
}
