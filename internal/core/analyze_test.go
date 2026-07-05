package core

import (
	"context"
	"errors"
	"testing"
)

func TestEnforceCitations_StripsFabricatedRefs(t *testing.T) {
	valid := map[string]bool{"E1": true, "E2": true}
	res := AnalysisResult{
		Observations: []Observation{
			{Body: "real", Cites: []string{"E1", "E9"}}, // E9 fabricated → dropped ref, kept obs
			{Body: "fabricated", Cites: []string{"E9"}},  // only fabricated → whole obs dropped
			{Body: "uncited", Cites: nil},                // no cites → dropped
		},
		Hypotheses: []Hypothesis{
			{Rank: 1, Title: "grounded", Supporting: []string{"E2", "E9"}},
			{Rank: 2, Title: "ungrounded", Supporting: []string{"E9"}, Contradicting: []string{"E8"}},
		},
	}

	out, rep := EnforceCitations(res, valid)

	if len(out.Observations) != 1 || out.Observations[0].Body != "real" {
		t.Fatalf("observations = %+v, want only 'real'", out.Observations)
	}
	if len(out.Observations[0].Cites) != 1 || out.Observations[0].Cites[0] != "E1" {
		t.Fatalf("cites = %v, want [E1]", out.Observations[0].Cites)
	}
	if len(out.Hypotheses) != 1 || out.Hypotheses[0].Title != "grounded" {
		t.Fatalf("hypotheses = %+v, want only 'grounded'", out.Hypotheses)
	}
	if out.Hypotheses[0].Rank != 1 {
		t.Errorf("surviving hypothesis should be re-ranked to 1, got %d", out.Hypotheses[0].Rank)
	}
	if rep.DroppedObservations != 2 || rep.DroppedHypotheses != 1 {
		t.Errorf("report = %+v, want 2 obs / 1 hyp dropped", rep)
	}
}

func TestNormalizeVerdict(t *testing.T) {
	cases := map[string]string{
		"actionable": "actionable", "ACTIONABLE": "actionable",
		"likely_noise": "likely_noise", "noise": "likely_noise",
		"weird": "unknown", "": "unknown",
	}
	for in, want := range cases {
		if got := NormalizeVerdict(in); got != want {
			t.Errorf("NormalizeVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeAnalysisRepo struct {
	req   AnalysisRequest
	saved AnalysisRecord
}

func (f *fakeAnalysisRepo) LoadAnalysisRequest(context.Context, string) (AnalysisRequest, error) {
	return f.req, nil
}
func (f *fakeAnalysisRepo) SaveAnalysis(_ context.Context, _ string, rec AnalysisRecord) error {
	f.saved = rec
	return nil
}

type fakeProvider struct {
	res AnalysisResult
	err error
}

func (fakeProvider) Name() string  { return "openai_compat" }
func (fakeProvider) Model() string { return "test-model" }
func (f fakeProvider) Analyze(context.Context, AnalysisRequest) (AnalysisResult, error) {
	return f.res, f.err
}

func TestAnalyze_NoProviderSkips(t *testing.T) {
	repo := &fakeAnalysisRepo{}
	if err := NewAnalysisService(nil, repo).Analyze(context.Background(), "inc"); err != nil {
		t.Fatal(err)
	}
	if repo.saved.Status != "skipped" {
		t.Fatalf("status = %q, want skipped", repo.saved.Status)
	}
}

func TestAnalyze_ProviderErrorStillPublishes(t *testing.T) {
	repo := &fakeAnalysisRepo{}
	prov := fakeProvider{err: errors.New("model down")}
	if err := NewAnalysisService(prov, repo).Analyze(context.Background(), "inc"); err != nil {
		t.Fatal(err)
	}
	if repo.saved.Status != "failed" {
		t.Fatalf("status = %q, want failed", repo.saved.Status)
	}
	if repo.saved.Error == "" {
		t.Fatal("failed analysis must record the error")
	}
}

func TestAnalyze_OKEnforcesCitations(t *testing.T) {
	repo := &fakeAnalysisRepo{
		req: AnalysisRequest{Evidence: []EvidenceRef{{Ref: "E1", ID: "uuid-1"}}},
	}
	prov := fakeProvider{res: AnalysisResult{
		Summary:       "s",
		TriageVerdict: "actionable",
		Observations: []Observation{
			{Body: "grounded", Cites: []string{"E1"}},
			{Body: "hallucinated", Cites: []string{"E7"}},
		},
	}}
	if err := NewAnalysisService(prov, repo).Analyze(context.Background(), "inc"); err != nil {
		t.Fatal(err)
	}
	if repo.saved.Status != "ok" {
		t.Fatalf("status = %q, want ok", repo.saved.Status)
	}
	if len(repo.saved.Result.Observations) != 1 {
		t.Fatalf("hallucinated observation should be stripped, got %d", len(repo.saved.Result.Observations))
	}
	if repo.saved.TriageVerdict != "actionable" {
		t.Errorf("verdict = %q", repo.saved.TriageVerdict)
	}
	if repo.saved.RefToID["E1"] != "uuid-1" {
		t.Errorf("ref map not passed for citation persistence")
	}
}
