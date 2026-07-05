package core

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrBadPayload marks a webhook body that could not be parsed. Callers should
// map it to a 4xx (retrying won't help), as opposed to storage errors (5xx).
var ErrBadPayload = errors.New("bad payload")

// IntakeAdapter normalizes a source-specific webhook body into an AlertBatch.
// Implemented per source (Alertmanager, generic, …) outside the core.
type IntakeAdapter interface {
	Source() SourceType
	Parse(body []byte) (AlertBatch, error)
}

// IncidentRepository is the persistence port the intake service needs. The
// store package implements it; the core never imports the database.
type IncidentRepository interface {
	// IngestBatch correlates the batch to an existing open incident with the
	// same grouping key opened after `since`, or creates a new one, then stores
	// (upserts) the alerts. severity is the resolved incidents.severity value.
	IngestBatch(ctx context.Context, batch AlertBatch, severity string, since time.Time) (IngestOutcome, error)
}

// IngestOutcome is what the repository did.
type IngestOutcome struct {
	IncidentID string
	Created    bool // true = new incident, false = attached to existing
	Alerts     int  // alerts inserted or updated
}

// Service runs the intake pipeline: normalize → pre-filter → correlate → store.
type Service struct {
	repo      IncidentRepository
	threshold Severity
	window    time.Duration
}

type Option func(*Service)

// WithThreshold overrides the minimum severity that triggers a notebook.
func WithThreshold(s Severity) Option { return func(svc *Service) { svc.threshold = s } }

// WithWindow overrides how far back an open incident may be to still absorb
// related alerts under the same grouping key.
func WithWindow(d time.Duration) Option { return func(svc *Service) { svc.window = d } }

func NewService(repo IncidentRepository, opts ...Option) *Service {
	s := &Service{repo: repo, threshold: SevHigh, window: 6 * time.Hour}
	for _, o := range opts {
		o(s)
	}
	return s
}

// IngestResult is the outcome reported back to the webhook caller.
type IngestResult struct {
	IncidentID string
	Created    bool
	Dropped    bool
	Reason     string
	Alerts     int
}

// Ingest normalizes the body, drops sub-threshold noise, and otherwise
// correlates and persists the batch.
func (s *Service) Ingest(ctx context.Context, adapter IntakeAdapter, body []byte) (IngestResult, error) {
	batch, err := adapter.Parse(body)
	if err != nil {
		return IngestResult{}, fmt.Errorf("%w: %v", ErrBadPayload, err)
	}

	level, ok := batch.MaxSeverity().IncidentLevel()
	if !ok || !batch.MaxSeverity().AtLeast(s.threshold) {
		return IngestResult{Dropped: true, Reason: "severity below threshold"}, nil
	}

	out, err := s.repo.IngestBatch(ctx, batch, level, time.Now().Add(-s.window))
	if err != nil {
		return IngestResult{}, err
	}
	return IngestResult{
		IncidentID: out.IncidentID,
		Created:    out.Created,
		Alerts:     out.Alerts,
	}, nil
}
