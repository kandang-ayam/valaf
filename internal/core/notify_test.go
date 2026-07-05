package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeChannel struct {
	name string
	sent []Notification
	err  error
}

func (f *fakeChannel) Name() string { return f.name }
func (f *fakeChannel) Send(_ context.Context, n Notification) error {
	f.sent = append(f.sent, n)
	return f.err
}

type fakeNotifyRepo struct {
	note    Notification
	records []string
}

func (f *fakeNotifyRepo) LoadNotification(context.Context, string) (Notification, error) {
	return f.note, nil
}
func (f *fakeNotifyRepo) Record(_ context.Context, _, channel, _, status, reason string) error {
	f.records = append(f.records, channel+":"+status+":"+reason)
	return nil
}

func TestNotify_ActionableSendsAndRecords(t *testing.T) {
	repo := &fakeNotifyRepo{note: Notification{Verdict: "actionable", Title: "x", Severity: "critical"}}
	ch := &fakeChannel{name: "slack"}
	res, err := NewNotificationService(repo, []NotificationChannel{ch}, "https://valaf.example").Notify(context.Background(), "inc-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 || res.Failed != 0 || res.Quiet {
		t.Fatalf("result = %+v", res)
	}
	if len(ch.sent) != 1 {
		t.Fatalf("channel should have received 1 send, got %d", len(ch.sent))
	}
	if ch.sent[0].IncidentURL != "https://valaf.example/incidents/inc-1" {
		t.Errorf("incident URL = %q", ch.sent[0].IncidentURL)
	}
	if len(repo.records) != 1 || repo.records[0] != "slack:sent:actionable" {
		t.Errorf("records = %v", repo.records)
	}
}

func TestNotify_NonActionableIsQuiet(t *testing.T) {
	repo := &fakeNotifyRepo{note: Notification{Verdict: "likely_noise"}}
	ch := &fakeChannel{name: "slack"}
	res, err := NewNotificationService(repo, []NotificationChannel{ch}, "").Notify(context.Background(), "inc-2")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Quiet || res.Sent != 0 {
		t.Fatalf("expected quiet, got %+v", res)
	}
	if len(ch.sent) != 0 {
		t.Fatal("quiet incidents must not send")
	}
	if len(repo.records) != 1 || repo.records[0] != "gate:quiet:noise" {
		t.Errorf("records = %v", repo.records)
	}
}

func TestNotify_SendFailureRecordedNotFatal(t *testing.T) {
	repo := &fakeNotifyRepo{note: Notification{Verdict: "actionable"}}
	ch := &fakeChannel{name: "telegram", err: errors.New("timeout")}
	res, err := NewNotificationService(repo, []NotificationChannel{ch}, "").Notify(context.Background(), "inc-3")
	if err != nil {
		t.Fatalf("send failure must not fail the job: %v", err)
	}
	if res.Failed != 1 {
		t.Fatalf("expected 1 failed, got %+v", res)
	}
	if repo.records[0] != "telegram:failed:actionable" {
		t.Errorf("records = %v", repo.records)
	}
}

func TestNotificationText(t *testing.T) {
	n := Notification{
		Title: "HighErrorRate", Severity: "critical", Verdict: "actionable",
		Summary: "checkout erroring", TopHypothesis: "bad deploy", IncidentURL: "http://x/incidents/1",
	}
	got := n.Text()
	for _, want := range []string{"[CRITICAL] HighErrorRate — actionable", "checkout erroring", "Most likely: bad deploy", "http://x/incidents/1"} {
		if !strings.Contains(got, want) {
			t.Errorf("Text() missing %q in:\n%s", want, got)
		}
	}
}
