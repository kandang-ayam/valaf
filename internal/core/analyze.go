package core

import (
	"context"
	"encoding/json"
	"strings"
)

// EvidenceRef is one evidence item exposed to the AI with a stable handle (Ref,
// e.g. "E1"). The model cites handles; we validate them against this set before
// storing, so a claim can never reference evidence that does not exist.
type EvidenceRef struct {
	Ref       string // handle given to the model
	ID        string // evidence_items.id (uuid)
	Collector string
	Kind      string
	Status    string
	Request   json.RawMessage
	Result    json.RawMessage
}

// SimilarIncident is a past RESOLVED incident with overlapping alert types,
// carrying its engineer-verified outcome into a new analysis (rule 11:
// learning is retrieval, not training — auditable and model-agnostic).
type SimilarIncident struct {
	IncidentID   string   // provenance; recorded in analysis_similar_incidents
	Title        string
	RootCause    string
	Solution     string
	Confirmed    []string // engineer-confirmed hypothesis titles
	RuledOut     []string // engineer-rejected hypothesis titles
	OverlapNames []string // alertnames shared with the new incident
	Score        float64  // overlap count (simple, explainable)
}

// AnalysisRequest is the input handed to an AI provider.
type AnalysisRequest struct {
	IncidentTitle string
	Severity      string
	EntityBag     map[string]string
	Evidence      []EvidenceRef
	Similar       []SimilarIncident
}

type TimelineEntry struct {
	At   string `json:"at"`
	Text string `json:"text"`
}

// Observation is a cited statement of fact drawn from evidence.
type Observation struct {
	Body  string
	Cites []string // evidence handles
}

// Hypothesis is a ranked candidate cause with supporting/contradicting evidence
// and read-only verification steps. No confidence percentage (product rule 7).
type Hypothesis struct {
	Rank          int
	Title         string
	Rationale     string
	Supporting    []string // evidence handles
	Contradicting []string // evidence handles
	Checks        []string
}

// AnalysisResult is the structured output of an AI provider.
type AnalysisResult struct {
	Summary       string
	Timeline      []TimelineEntry
	Observations  []Observation
	Hypotheses    []Hypothesis
	Gaps          []string
	TriageVerdict string
}

// AIProvider is the pluggable analysis backend (Anthropic, OpenAI-compatible,
// internal gateway). Evidence never leaves the network beyond this call.
type AIProvider interface {
	Name() string  // provider enum value: "anthropic" | "openai_compat"
	Model() string // model identifier, for the audit record
	Analyze(ctx context.Context, req AnalysisRequest) (AnalysisResult, error)
}

// AnalysisRecord is what gets persisted for one analysis run.
type AnalysisRecord struct {
	Provider      string // "" when skipped
	Model         string // "" when skipped
	Status        string // "ok" | "failed" | "skipped"
	Error         string
	Result        AnalysisResult    // populated only when Status == ok
	RefToID       map[string]string // evidence handle -> uuid, for citations
	TriageVerdict string
	Similar       []SimilarIncident // provenance: which past incidents informed this run
}

// AnalysisRepository loads the analysis input and persists the outcome
// (implemented by the store; also flips the incident to published).
type AnalysisRepository interface {
	LoadAnalysisRequest(ctx context.Context, incidentID string) (AnalysisRequest, error)
	SaveAnalysis(ctx context.Context, incidentID string, rec AnalysisRecord) error
}

// AnalysisService runs analysis for an incident. It NEVER fails the notebook on
// an AI error: collection is already stored, so a provider failure just yields a
// published notebook with evidence and an analysis gap.
type AnalysisService struct {
	provider AIProvider // nil => no AI configured
	repo     AnalysisRepository
}

func NewAnalysisService(provider AIProvider, repo AnalysisRepository) *AnalysisService {
	return &AnalysisService{provider: provider, repo: repo}
}

func (s *AnalysisService) Analyze(ctx context.Context, incidentID string) error {
	req, err := s.repo.LoadAnalysisRequest(ctx, incidentID)
	if err != nil {
		return err
	}

	if s.provider == nil {
		return s.repo.SaveAnalysis(ctx, incidentID, AnalysisRecord{
			Status:        "skipped",
			TriageVerdict: "unknown",
		})
	}

	res, err := s.provider.Analyze(ctx, req)
	if err != nil {
		// Publish anyway — the evidence must not be lost to an AI error.
		return s.repo.SaveAnalysis(ctx, incidentID, AnalysisRecord{
			Provider:      s.provider.Name(),
			Model:         s.provider.Model(),
			Status:        "failed",
			Error:         err.Error(),
			TriageVerdict: "unknown",
		})
	}

	valid, refToID := refIndex(req.Evidence)
	res, _ = EnforceCitations(res, valid)

	return s.repo.SaveAnalysis(ctx, incidentID, AnalysisRecord{
		Provider:      s.provider.Name(),
		Model:         s.provider.Model(),
		Status:        "ok",
		Result:        res,
		RefToID:       refToID,
		TriageVerdict: NormalizeVerdict(res.TriageVerdict),
		Similar:       req.Similar,
	})
}

// CitationReport records what enforcement stripped (for logging/audit).
type CitationReport struct {
	DroppedObservations int
	DroppedHypotheses   int
	DroppedRefs         int
}

// EnforceCitations implements product rule 6: any citation to evidence that does
// not exist is stripped. An observation with no surviving citation is dropped
// entirely; a hypothesis with no surviving supporting/contradicting evidence is
// dropped. Surviving hypotheses are re-ranked to stay contiguous.
func EnforceCitations(res AnalysisResult, valid map[string]bool) (AnalysisResult, CitationReport) {
	var rep CitationReport

	keptObs := make([]Observation, 0, len(res.Observations))
	for _, o := range res.Observations {
		cites := filterRefs(o.Cites, valid, &rep.DroppedRefs)
		if len(cites) == 0 {
			rep.DroppedObservations++
			continue
		}
		o.Cites = cites
		keptObs = append(keptObs, o)
	}
	res.Observations = keptObs

	keptHyp := make([]Hypothesis, 0, len(res.Hypotheses))
	rank := 0
	for _, h := range res.Hypotheses {
		sup := filterRefs(h.Supporting, valid, &rep.DroppedRefs)
		con := filterRefs(h.Contradicting, valid, &rep.DroppedRefs)
		if len(sup) == 0 && len(con) == 0 {
			rep.DroppedHypotheses++
			continue
		}
		rank++
		h.Rank = rank
		h.Supporting = sup
		h.Contradicting = con
		keptHyp = append(keptHyp, h)
	}
	res.Hypotheses = keptHyp

	return res, rep
}

// NormalizeVerdict maps a free-form verdict onto the triage_verdict enum.
func NormalizeVerdict(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "actionable":
		return "actionable"
	case "likely_noise", "noise", "likely-noise", "likely noise":
		return "likely_noise"
	default:
		return "unknown"
	}
}

func filterRefs(refs []string, valid map[string]bool, dropped *int) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if valid[r] {
			out = append(out, r)
		} else {
			*dropped++
		}
	}
	return out
}

func refIndex(evidence []EvidenceRef) (valid map[string]bool, refToID map[string]string) {
	valid = make(map[string]bool, len(evidence))
	refToID = make(map[string]string, len(evidence))
	for _, e := range evidence {
		valid[e.Ref] = true
		refToID[e.Ref] = e.ID
	}
	return valid, refToID
}
