package core

import (
	"context"
	"errors"
	"testing"
)

type fakeCollector struct {
	items []EvidenceItem
}

func (f fakeCollector) Kind() string { return "fake" }
func (f fakeCollector) Collect(context.Context, CollectTarget) []EvidenceItem {
	return f.items
}

type fakeEvidence struct {
	incident string
	saved    []EvidenceItem
	err      error
}

func (f *fakeEvidence) SaveEvidence(_ context.Context, incidentID string, items []EvidenceItem) error {
	f.incident = incidentID
	f.saved = items
	return f.err
}

func TestCollectionService_PersistsAllItems(t *testing.T) {
	ev := &fakeEvidence{}
	svc := NewCollectionService(ev, []Collector{
		fakeCollector{items: []EvidenceItem{{Collector: "prometheus", Status: EvidenceOK, Result: []byte(`{}`)}}},
		fakeCollector{items: []EvidenceItem{{Collector: "prometheus", Status: EvidenceGap, Error: "no data"}}},
	})

	n, err := svc.Collect(context.Background(), CollectTarget{IncidentID: "inc-9"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Fatalf("collected %d, want 2", n)
	}
	if ev.incident != "inc-9" || len(ev.saved) != 2 {
		t.Fatalf("evidence not persisted correctly: %+v", ev)
	}
}

func TestCollectionService_NoCollectorsSavesNothing(t *testing.T) {
	ev := &fakeEvidence{}
	svc := NewCollectionService(ev, nil)

	n, err := svc.Collect(context.Background(), CollectTarget{IncidentID: "inc-1"})
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, want 0/nil", n, err)
	}
	if ev.saved != nil {
		t.Fatal("SaveEvidence must not be called with no items")
	}
}

func TestCollectionService_PropagatesSaveError(t *testing.T) {
	ev := &fakeEvidence{err: errors.New("db down")}
	svc := NewCollectionService(ev, []Collector{
		fakeCollector{items: []EvidenceItem{{Collector: "prometheus", Status: EvidenceOK, Result: []byte(`{}`)}}},
	})

	if _, err := svc.Collect(context.Background(), CollectTarget{IncidentID: "x"}); err == nil {
		t.Fatal("expected save error to propagate")
	}
}
