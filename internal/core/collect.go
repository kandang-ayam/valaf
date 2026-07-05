package core

import (
	"context"
	"encoding/json"
	"time"
)

// Evidence status values match the evidence_status enum.
const (
	EvidenceOK     = "ok"     // captured, has data
	EvidenceGap    = "gap"    // query ran but returned nothing / source unreachable
	EvidenceFailed = "failed" // capture errored
)

// TimeWindow is the interval a collector queries, around the incident.
type TimeWindow struct {
	Start time.Time
	End   time.Time
	Step  time.Duration
}

// CollectTarget is everything a collector needs to gather evidence for an incident.
type CollectTarget struct {
	IncidentID string
	EntityBag  map[string]string
	Window     TimeWindow
}

// EvidenceItem is one captured (or failed) piece of evidence. Result is set only
// when Status == EvidenceOK, matching the evidence_result_presence DB constraint.
type EvidenceItem struct {
	Collector string          // collector_type enum value
	Kind      string          // evidence_kind enum value
	Request   json.RawMessage // the exact, reproducible query/request
	Result    json.RawMessage // raw result; present only when Status == ok
	Status    string
	Error     string
}

// Collector gathers evidence for a target. It is best-effort and MUST NOT return
// an error for a dead/slow source: it returns EvidenceItems with status gap or
// failed instead, so a broken source becomes a recorded gap, never a stuck job.
type Collector interface {
	Kind() string // collector_type enum value
	Collect(ctx context.Context, target CollectTarget) []EvidenceItem
}

// EvidenceRepository persists captured evidence (implemented by the store).
type EvidenceRepository interface {
	SaveEvidence(ctx context.Context, incidentID string, items []EvidenceItem) error
}

// CollectionService runs every configured collector for a target, time-boxing
// each one, then persists all resulting evidence in one shot.
type CollectionService struct {
	collectors []Collector
	evidence   EvidenceRepository
	perTimeout time.Duration
}

type CollectionOption func(*CollectionService)

// WithPerCollectorTimeout caps how long any single collector may run.
func WithPerCollectorTimeout(d time.Duration) CollectionOption {
	return func(s *CollectionService) { s.perTimeout = d }
}

func NewCollectionService(evidence EvidenceRepository, collectors []Collector, opts ...CollectionOption) *CollectionService {
	s := &CollectionService{collectors: collectors, evidence: evidence, perTimeout: 20 * time.Second}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Collect gathers and stores evidence for target, returning how many items were
// recorded (including gaps and failures).
func (s *CollectionService) Collect(ctx context.Context, target CollectTarget) (int, error) {
	var items []EvidenceItem
	for _, c := range s.collectors {
		cctx, cancel := context.WithTimeout(ctx, s.perTimeout)
		items = append(items, c.Collect(cctx, target)...)
		cancel()
	}
	if len(items) == 0 {
		return 0, nil
	}
	if err := s.evidence.SaveEvidence(ctx, target.IncidentID, items); err != nil {
		return 0, err
	}
	return len(items), nil
}
