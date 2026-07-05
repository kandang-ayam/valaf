package alertmanager

import "testing"

const sample = `{
  "version":"4",
  "groupKey":"{}:{alertname=\"HighErrorRate\"}",
  "status":"firing",
  "groupLabels":{"alertname":"HighErrorRate"},
  "commonLabels":{"alertname":"HighErrorRate","severity":"critical","service":"checkout","instance":"10.0.0.5:9100","env":"prod"},
  "commonAnnotations":{"summary":"error rate above 5%"},
  "alerts":[
    {"status":"firing","labels":{"severity":"critical"},"annotations":{},"startsAt":"2026-07-05T09:00:00Z","endsAt":"0001-01-01T00:00:00Z","fingerprint":"abc123"},
    {"status":"firing","labels":{"severity":"warning"},"annotations":{},"startsAt":"2026-07-05T09:01:00Z","endsAt":"0001-01-01T00:00:00Z","fingerprint":"def456"}
  ]
}`

func TestParse(t *testing.T) {
	b, err := New().Parse([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if b.GroupingKey != `{}:{alertname="HighErrorRate"}` {
		t.Errorf("grouping key = %q", b.GroupingKey)
	}
	if b.Title != "HighErrorRate" {
		t.Errorf("title = %q", b.Title)
	}
	if len(b.Alerts) != 2 {
		t.Fatalf("alerts = %d, want 2", len(b.Alerts))
	}
	if got := b.EntityBag["service"]; got != "checkout" {
		t.Errorf("entity service = %q", got)
	}
	if got := b.EntityBag["host"]; got != "10.0.0.5:9100" {
		t.Errorf("entity host = %q", got)
	}
	if got := b.EntityBag["env"]; got != "prod" {
		t.Errorf("entity env = %q", got)
	}
	// endsAt of a firing alert is the zero time → nil.
	if b.Alerts[0].EndsAt != nil {
		t.Errorf("firing alert EndsAt should be nil, got %v", b.Alerts[0].EndsAt)
	}
	if b.Alerts[0].StartsAt == nil {
		t.Errorf("firing alert StartsAt should be set")
	}
}

func TestParse_NoAlerts(t *testing.T) {
	if _, err := New().Parse([]byte(`{"version":"4","alerts":[]}`)); err == nil {
		t.Fatal("expected error for zero alerts")
	}
}

func TestParse_BadJSON(t *testing.T) {
	if _, err := New().Parse([]byte(`{not json`)); err == nil {
		t.Fatal("expected error for bad json")
	}
}
