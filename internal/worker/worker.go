// Package worker drains the Postgres job queue. It runs as the `worker` role of
// the single binary, alongside (but separate from) the web role.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/valaf/valaf/internal/core"
	"github.com/valaf/valaf/internal/store"
)

// idlePoll is how long to wait before checking again when the queue is empty.
const idlePoll = 2 * time.Second

// retryBackoff delays a failed job's next attempt.
const retryBackoff = 30 * time.Second

// incidentLoader loads the collect target for an incident (implemented by the store).
type incidentLoader interface {
	LoadCollectTarget(ctx context.Context, incidentID string) (core.CollectTarget, error)
}

type Worker struct {
	jobs         *store.JobRepo
	incidents    incidentLoader
	collection   *core.CollectionService
	analysis     *core.AnalysisService
	notification *core.NotificationService
	log          *slog.Logger
}

func New(jobs *store.JobRepo, incidents incidentLoader, collection *core.CollectionService, analysis *core.AnalysisService, notification *core.NotificationService, log *slog.Logger) *Worker {
	return &Worker{jobs: jobs, incidents: incidents, collection: collection, analysis: analysis, notification: notification, log: log}
}

// Run drains jobs until ctx is cancelled. It processes back-to-back while work
// exists and sleeps briefly when the queue is empty.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		worked, err := w.processOne(ctx)
		if err != nil {
			w.log.ErrorContext(ctx, "worker loop error", "error", err)
		}
		if worked {
			continue
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(idlePoll):
		}
	}
}

// processOne claims and runs a single job. It returns true if a job was claimed
// (regardless of success), so the caller keeps draining.
func (w *Worker) processOne(ctx context.Context) (bool, error) {
	job, err := w.jobs.Dequeue(ctx)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}

	if err := w.handle(ctx, job); err != nil {
		w.log.ErrorContext(ctx, "job failed", "id", job.ID, "kind", job.Kind, "attempt", job.Attempts, "error", err)
		if job.Attempts >= job.MaxAttempts {
			return true, w.jobs.Fail(ctx, job.ID, err.Error())
		}
		return true, w.jobs.Retry(ctx, job.ID, retryBackoff, err.Error())
	}
	return true, w.jobs.Complete(ctx, job.ID)
}

func (w *Worker) handle(ctx context.Context, job *store.Job) error {
	switch job.Kind {
	case "collect":
		return w.handleCollect(ctx, job)
	case "analyze":
		return w.handleAnalyze(ctx, job)
	case "notify":
		return w.handleNotify(ctx, job)
	default:
		return fmt.Errorf("unknown job kind %q", job.Kind)
	}
}

func (w *Worker) handleCollect(ctx context.Context, job *store.Job) error {
	if job.IncidentID == "" {
		return fmt.Errorf("collect job %d has no incident_id", job.ID)
	}
	target, err := w.incidents.LoadCollectTarget(ctx, job.IncidentID)
	if err != nil {
		return err
	}
	n, err := w.collection.Collect(ctx, target)
	if err != nil {
		return err
	}
	w.log.InfoContext(ctx, "evidence collected", "incident", job.IncidentID, "items", n)

	// Evidence is stored; now hand off to analysis as a separate job so an AI
	// failure never rolls back the collection.
	return w.jobs.Enqueue(ctx, "analyze", job.IncidentID)
}

func (w *Worker) handleAnalyze(ctx context.Context, job *store.Job) error {
	if job.IncidentID == "" {
		return fmt.Errorf("analyze job %d has no incident_id", job.ID)
	}
	if err := w.analysis.Analyze(ctx, job.IncidentID); err != nil {
		return err
	}
	w.log.InfoContext(ctx, "incident analyzed & published", "incident", job.IncidentID)

	// Published; now run the notify gate as its own job.
	return w.jobs.Enqueue(ctx, "notify", job.IncidentID)
}

func (w *Worker) handleNotify(ctx context.Context, job *store.Job) error {
	if job.IncidentID == "" {
		return fmt.Errorf("notify job %d has no incident_id", job.ID)
	}
	res, err := w.notification.Notify(ctx, job.IncidentID)
	if err != nil {
		return err
	}
	w.log.InfoContext(ctx, "notification dispatched", "incident", job.IncidentID,
		"sent", res.Sent, "failed", res.Failed, "quiet", res.Quiet)
	return nil
}
