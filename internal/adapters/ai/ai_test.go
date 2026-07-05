package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/valaf/valaf/internal/core"
)

func TestParseResult_ToleratesFences(t *testing.T) {
	content := "```json\n{\"summary\":\"s\",\"triage_verdict\":\"actionable\"," +
		"\"observations\":[{\"body\":\"b\",\"cites\":[\"E1\"]}]}\n```"
	res, err := parseResult(content)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "s" || res.TriageVerdict != "actionable" {
		t.Fatalf("bad parse: %+v", res)
	}
	if len(res.Observations) != 1 || res.Observations[0].Cites[0] != "E1" {
		t.Fatalf("observations not parsed: %+v", res.Observations)
	}
}

func TestBuildUserPrompt_IncludesVerifiedOutcomes(t *testing.T) {
	req := core.AnalysisRequest{
		IncidentTitle: "HighErrorRate",
		Severity:      "critical",
		Evidence:      []core.EvidenceRef{{Ref: "E1", Collector: "prometheus", Kind: "metric", Status: "ok"}},
		Similar: []core.SimilarIncident{{
			IncidentID: "old-1", Title: "HighErrorRate on checkout",
			RootCause: "node_exporter down", Solution: "add watchdog",
			Confirmed: []string{"memory backend unreachable"},
			RuledOut:  []string{"bad deploy"},
			OverlapNames: []string{"HighErrorRate"},
		}},
	}
	got := buildUserPrompt(req)
	for _, want := range []string{
		"PAST RESOLVED SIMILAR INCIDENTS",
		"verified root cause: node_exporter down",
		"solution: add watchdog",
		"confirmed hypotheses: memory backend unreachable",
		"RULED OUT (do not re-propose without new evidence): bad deploy",
		"shared alerts: HighErrorRate",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBuildUserPrompt_NoSimilarSection(t *testing.T) {
	got := buildUserPrompt(core.AnalysisRequest{IncidentTitle: "x", Severity: "high"})
	if strings.Contains(got, "PAST RESOLVED") {
		t.Error("similar section should be absent when there are no similar incidents")
	}
}

func TestParseResult_NoJSON(t *testing.T) {
	if _, err := parseResult("I refuse to answer."); err == nil {
		t.Fatal("expected error when there is no JSON object")
	}
}

func TestOpenAICompat_Analyze(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		// Echo back a valid analysis as the assistant message content.
		content := `{"summary":"checkout is erroring","triage_verdict":"actionable",` +
			`"observations":[{"body":"error rate spiked","cites":["E1"]}],` +
			`"hypotheses":[{"rank":1,"title":"bad deploy","supporting":["E1"],"checks":["roll back"]}],` +
			`"gaps":["no logs collector"]}`
		resp := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewOpenAICompat(srv.URL, "secret-key", "test-model")
	res, err := p.Analyze(context.Background(), core.AnalysisRequest{
		IncidentTitle: "HighErrorRate",
		Severity:      "critical",
		Evidence:      []core.EvidenceRef{{Ref: "E1", Collector: "prometheus", Kind: "metric", Status: "ok"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "checkout is erroring" {
		t.Errorf("summary = %q", res.Summary)
	}
	if len(res.Hypotheses) != 1 || res.Hypotheses[0].Title != "bad deploy" {
		t.Errorf("hypotheses = %+v", res.Hypotheses)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if !strings.Contains(gotBody, "E1") {
		t.Errorf("evidence handle not included in prompt body")
	}
}
