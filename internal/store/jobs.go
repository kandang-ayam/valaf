package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// staleRunning is how long a 'running' job may sit before it's presumed orphaned
// (worker crashed mid-job) and reclaimed by the next dequeue.
const staleRunning = 5 * time.Minute

// Job is a unit of background work.
type Job struct {
	ID          int64
	Kind        string
	IncidentID  string
	Payload     json.RawMessage
	Attempts    int
	MaxAttempts int
}

type JobRepo struct {
	pool *pgxpool.Pool
}

func NewJobRepo(pool *pgxpool.Pool) *JobRepo { return &JobRepo{pool: pool} }

// Dequeue atomically claims the next runnable job (pending and due, or a stale
// running one) using FOR UPDATE SKIP LOCKED. Returns nil when nothing is ready.
func (r *JobRepo) Dequeue(ctx context.Context) (*Job, error) {
	var j Job
	err := r.pool.QueryRow(ctx, `
		UPDATE jobs SET status = 'running', attempts = attempts + 1, updated_at = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE (status = 'pending' AND run_at <= now())
			   OR (status = 'running' AND updated_at < now() - $1::interval)
			ORDER BY run_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, kind, COALESCE(incident_id::text, ''), payload, attempts, max_attempts`,
		staleRunning.String(),
	).Scan(&j.ID, &j.Kind, &j.IncidentID, &j.Payload, &j.Attempts, &j.MaxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// Enqueue adds a job for an incident.
func (r *JobRepo) Enqueue(ctx context.Context, kind, incidentID string) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO jobs (kind, incident_id) VALUES ($1, $2)`, kind, incidentID)
	return err
}

// Complete marks a job done.
func (r *JobRepo) Complete(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `UPDATE jobs SET status = 'done', last_error = NULL, updated_at = now() WHERE id = $1`, id)
	return err
}

// Retry reschedules a job after a transient failure.
func (r *JobRepo) Retry(ctx context.Context, id int64, backoff time.Duration, cause string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE jobs SET status = 'pending', run_at = $2, last_error = $3, updated_at = now()
		WHERE id = $1`,
		id, time.Now().Add(backoff), cause,
	)
	return err
}

// Fail gives up on a job after exhausting attempts.
func (r *JobRepo) Fail(ctx context.Context, id int64, cause string) error {
	_, err := r.pool.Exec(ctx, `UPDATE jobs SET status = 'failed', last_error = $2, updated_at = now() WHERE id = $1`, id, cause)
	return err
}
