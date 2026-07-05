package core

import (
	"context"
	"testing"
	"time"
)

type fakeRepo struct {
	called      bool
	gotSeverity string
	gotBatch    AlertBatch
}

func (f *fakeRepo) IngestBatch(_ context.Context, batch AlertBatch, severity string, _ time.Time) (IngestOutcome, error) {
	f.called = true
	f.gotSeverity = severity
	f.gotBatch = batch
	return IngestOutcome{IncidentID: "inc-1", Created: true, Alerts: len(batch.Alerts)}, nil
}

func batch(sevs ...string) AlertBatch {
	b := AlertBatch{Source: SourceAlertmanager, GroupingKey: "g", Title: "t"}
	for i, s := range sevs {
		b.Alerts = append(b.Alerts, Alert{Fingerprint: string(rune('a' + i)), Severity: s, Status: "firing"})
	}
	return b
}

func TestIngest_DropsBelowThreshold(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)

	res, err := svc.Ingest(context.Background(), stubAdapter{batch("warning", "info")}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Dropped {
		t.Fatalf("expected drop, got %+v", res)
	}
	if repo.called {
		t.Fatal("repository must not be called for dropped batches")
	}
}

func TestIngest_StoresAtOrAboveThreshold(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)

	res, err := svc.Ingest(context.Background(), stubAdapter{batch("warning", "critical")}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Dropped {
		t.Fatal("critical batch must not be dropped")
	}
	if !repo.called {
		t.Fatal("repository should be called")
	}
	if repo.gotSeverity != "critical" {
		t.Fatalf("severity = %q, want critical", repo.gotSeverity)
	}
	if res.IncidentID != "inc-1" || !res.Created {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestIngest_ResolvedAlertsDoNotTrigger(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)

	b := AlertBatch{GroupingKey: "g", Alerts: []Alert{{Fingerprint: "x", Severity: "critical", Status: "resolved"}}}
	res, err := svc.Ingest(context.Background(), stubAdapter{b}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Dropped {
		t.Fatal("a resolved-only batch should be dropped, not create a notebook")
	}
}

func TestIngest_HigherThresholdDropsHigh(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo, WithThreshold(SevCritical))

	res, _ := svc.Ingest(context.Background(), stubAdapter{batch("high")}, nil)
	if !res.Dropped {
		t.Fatal("high should be dropped when threshold is critical")
	}
}

type stubAdapter struct{ b AlertBatch }

func (s stubAdapter) Source() SourceType             { return SourceAlertmanager }
func (s stubAdapter) Parse([]byte) (AlertBatch, error) { return s.b, nil }
