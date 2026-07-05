package web

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valaf/valaf/internal/core"
	"github.com/valaf/valaf/internal/store"
)

// TestDumpSnapshot writes rendered pages to $VALAF_DUMP_HTML for visual review.
// It is a dev aid, skipped unless that env var is set.
func TestDumpSnapshot(t *testing.T) {
	dir := os.Getenv("VALAF_DUMP_HTML")
	if dir == "" {
		t.Skip("set VALAF_DUMP_HTML=<dir> to dump rendered pages")
	}
	now := time.Now()
	list := listData{baseData: baseData{Title: "Incidents"}, Incidents: []store.IncidentSummary{
		{ID: "1", Title: "HighErrorRate on checkout", Status: "published", Severity: "critical", TriageVerdict: "actionable", AlertCount: 2, OpenedAt: now},
		{ID: "2", Title: "DiskCritical on postgres", Status: "resolved", Severity: "critical", TriageVerdict: "actionable", AlertCount: 1, OpenedAt: now.Add(-3 * time.Hour)},
		{ID: "3", Title: "TargetDown api-gateway", Status: "open", Severity: "high", TriageVerdict: "likely_noise", AlertCount: 4, OpenedAt: now.Add(-26 * time.Hour)},
	}}
	writeDump(t, dir, "index.html", "incidents_list", list)

	nb := &store.Notebook{
		Incident: store.IncidentDetail{
			ID: "1", Title: "HighErrorRate on checkout", Status: "published", Severity: "critical",
			TriageVerdict: "actionable", OpenedAt: now, PublishedAt: &now,
			EntityBag: map[string]string{"service": "checkout", "host": "10.0.0.5:9100", "env": "prod"},
		},
		Analysis: &store.AnalysisView{
			Provider: "openai_compat", Model: "gpt-4o-mini", Status: "ok",
			Summary:  "Error rate on checkout jumped above 5% shortly after the 09:00 deploy; the memory metrics collector was unreachable.",
			Timeline: []core.TimelineEntry{{At: "09:00", Text: "error rate began climbing"}, {At: "09:02", Text: "checkout p99 latency spiked"}},
			Gaps:     []string{"no logs collector configured"},
		},
		Observations: []store.ObservationView{
			{Body: "CPU busy is elevated on the host during the incident window.", CiteRefs: []string{"E1"}},
		},
		Hypotheses: []store.HypothesisView{
			{Rank: 1, Title: "Memory backend unreachable", Rationale: "The memory query failed to return.", SupportingRefs: []string{"E2"}, Checks: []string{"check node_exporter on the host"}},
			{Rank: 2, Title: "Bad deploy at 09:00", Rationale: "Timeline aligns with a deploy.", SupportingRefs: []string{"E1"}, ContradictingRefs: []string{"E3"}, Checks: []string{"roll back and observe"}},
		},
		Evidence: []store.EvidenceView{
			{Ref: "E1", Collector: "prometheus", Kind: "metric", Status: "ok", Request: "{\n  \"query\": \"cpu_busy_percent\"\n}", Result: "{\n  \"resultType\": \"matrix\"\n}", IsValid: true, CapturedAt: now},
			{Ref: "E2", Collector: "prometheus", Kind: "metric", Status: "failed", Error: "prometheus http 500: mem backend down", Request: "{\n  \"query\": \"mem_used_percent\"\n}", IsValid: true, CapturedAt: now},
			{Ref: "E3", Collector: "prometheus", Kind: "metric", Status: "gap", Error: "no data for query", Request: "{\n  \"query\": \"up\"\n}", IsValid: true, CapturedAt: now},
		},
		Alerts: []store.AlertView{
			{Source: "alertmanager", Severity: "critical", Annotations: map[string]string{"summary": "error rate above 5%"}, ReceivedAt: now},
		},
	}
	writeDump(t, dir, "detail.html", "incident_detail", detailData{baseData: baseData{Title: nb.Incident.Title}, Notebook: nb})
}

func writeDump(t *testing.T, dir, file, tmpl string, data any) {
	t.Helper()
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, tmpl, data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// These tests prove the templates actually execute — i.e. the server never ships
// raw {{ ... }} to the browser. If a template fails to run (bad name, parse
// error), the output would contain literal delimiters and these fail.

func renderTemplate(t *testing.T, name string, data any) string {
	t.Helper()
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	out := buf.String()
	if strings.Contains(out, "{{") || strings.Contains(out, "}}") {
		t.Fatalf("%s: raw template delimiters leaked to output — template not executed", name)
	}
	return out
}

func TestIncidentDetailRenders(t *testing.T) {
	now := time.Now()
	nb := &store.Notebook{
		Incident: store.IncidentDetail{
			ID: "abc", Title: "HighErrorRate", Status: "published", Severity: "critical",
			TriageVerdict: "actionable", OpenedAt: now,
			EntityBag: map[string]string{"service": "checkout"},
		},
		Analysis: &store.AnalysisView{
			Provider: "openai_compat", Model: "m", Status: "ok",
			Summary: "checkout is erroring", TriageVerdict: "actionable",
		},
		Observations: []store.ObservationView{{Body: "cpu high", CiteRefs: []string{"E1"}}},
		Hypotheses:   []store.HypothesisView{{Rank: 1, Title: "bad deploy", SupportingRefs: []string{"E1"}}},
		Evidence:     []store.EvidenceView{{Ref: "E1", Collector: "prometheus", Kind: "metric", Status: "ok", Request: "{}", IsValid: true, CapturedAt: now}},
	}

	out := renderTemplate(t, "incident_detail", detailData{baseData: baseData{Title: "HighErrorRate"}, Notebook: nb})
	for _, want := range []string{"HighErrorRate", "checkout is erroring", "bad deploy", "E1", "prometheus"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered detail missing %q", want)
		}
	}
}

func TestIncidentListRenders(t *testing.T) {
	data := listData{
		baseData: baseData{Title: "Incidents"},
		Incidents: []store.IncidentSummary{
			{ID: "abc", Title: "HighErrorRate", Status: "published", Severity: "critical", TriageVerdict: "actionable", AlertCount: 2, OpenedAt: time.Now()},
		},
	}
	out := renderTemplate(t, "incidents_list", data)
	for _, want := range []string{"HighErrorRate", "published", "critical"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered list missing %q", want)
		}
	}
}
