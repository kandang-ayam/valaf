package export

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/valaf/valaf/internal/core"
	"github.com/valaf/valaf/internal/store"
)

func sampleNotebook() *store.Notebook {
	now := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	return &store.Notebook{
		Incident: store.IncidentDetail{
			ID: "abc12345-6789", Title: "HighErrorRate on checkout", Status: "resolved",
			Severity: "critical", TriageVerdict: "actionable", OpenedAt: now, PublishedAt: &now,
			EntityBag: map[string]string{"service": "checkout"},
		},
		Analysis: &store.AnalysisView{
			Provider: "openai_compat", Model: "m", Status: "ok",
			Summary:  "checkout is erroring",
			Timeline: []core.TimelineEntry{{At: "09:00", Text: "errors began"}},
			Gaps:     []string{"no logs collector"},
		},
		Observations: []store.ObservationView{{Body: "cpu high", CiteRefs: []string{"E1"}}},
		Hypotheses: []store.HypothesisView{
			{ID: "h1", Rank: 1, Title: "memory backend unreachable", SupportingRefs: []string{"E2"}, Verdict: "confirmed", VerdictNote: "matches"},
		},
		Evidence: []store.EvidenceView{
			{Ref: "E1", Collector: "prometheus", Kind: "metric", Status: "ok", Request: `{"query":"cpu"}`, Result: `{"resultType":"matrix"}`, IsValid: true, CapturedAt: now},
			{Ref: "E2", Collector: "prometheus", Kind: "metric", Status: "failed", Error: "http 500", Request: `{"query":"mem"}`, IsValid: false, InvalidComment: "stale", CapturedAt: now},
		},
		Resolution: &store.ResolutionView{RootCause: "node_exporter down", ResolvedBy: "bob", ResolvedAt: now},
	}
}

func TestJSONExport(t *testing.T) {
	var buf bytes.Buffer
	if err := (jsonExporter{}).Render(&buf, sampleNotebook()); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
	// request should be embedded raw JSON, not a re-encoded string
	if !strings.Contains(buf.String(), `"query": "cpu"`) && !strings.Contains(buf.String(), `"query":"cpu"`) {
		t.Errorf("evidence request not embedded as raw JSON:\n%s", buf.String())
	}
	if doc["resolution"] == nil {
		t.Error("resolution missing from export")
	}
}

func TestMarkdownExport(t *testing.T) {
	var buf bytes.Buffer
	if err := (markdownExporter{}).Render(&buf, sampleNotebook()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"# HighErrorRate on checkout", "## AI analysis", "## Resolution", "node_exporter down", "FLAGGED INVALID: stale", "confirmed"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}

func TestDocxExport_IsValidZipWithContent(t *testing.T) {
	var buf bytes.Buffer
	if err := (docxExporter{}).Render(&buf, sampleNotebook()); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("docx is not a valid zip: %v", err)
	}
	names := map[string]bool{}
	var doc string
	for _, f := range zr.File {
		names[f.Name] = true
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			doc = string(b)
		}
	}
	for _, req := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		if !names[req] {
			t.Errorf("docx missing required part %q", req)
		}
	}
	if !strings.Contains(doc, "HighErrorRate on checkout") {
		t.Error("document.xml missing incident title")
	}
	if strings.Contains(doc, "{{") {
		t.Error("unexpected template delimiters in docx")
	}
}

func TestFilename(t *testing.T) {
	got := Filename(store.IncidentDetail{ID: "abcdef1234", Title: "High Error Rate!!"}, "docx")
	if got != "valaf-high-error-rate-abcdef12.docx" {
		t.Errorf("Filename = %q", got)
	}
}

func TestForUnknownFormat(t *testing.T) {
	if _, ok := For("xlsx"); ok {
		t.Error("xlsx should not be supported")
	}
}
