package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/valaf/valaf/internal/core"
)

var sample = core.Notification{
	IncidentID: "inc-1", Title: "HighErrorRate", Severity: "critical",
	Verdict: "actionable", Summary: "checkout erroring", IncidentURL: "http://x/incidents/inc-1",
}

func capturing(t *testing.T, status int) (*httptest.Server, *string, *string) {
	t.Helper()
	var body, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		auth = r.Header.Get("Authorization")
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &auth
}

func TestSlack_Send(t *testing.T) {
	srv, body, _ := capturing(t, 200)
	if err := NewSlack(srv.URL).Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*body, "HighErrorRate") || !strings.Contains(*body, `"text"`) {
		t.Errorf("slack body = %s", *body)
	}
}

func TestSlack_Non2xxErrors(t *testing.T) {
	srv, _, _ := capturing(t, 500)
	if err := NewSlack(srv.URL).Send(context.Background(), sample); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestTelegram_Send(t *testing.T) {
	srv, body, _ := capturing(t, 200)
	tg := NewTelegram("bottoken", "12345")
	tg.apiBase = srv.URL
	if err := tg.Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*body, `"chat_id":"12345"`) || !strings.Contains(*body, "HighErrorRate") {
		t.Errorf("telegram body = %s", *body)
	}
}

func TestWebhook_SendStructuredWithAuth(t *testing.T) {
	srv, body, auth := capturing(t, 200)
	if err := NewWebhook(srv.URL, "sekret").Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	// structured payload: has incident_id AND a ready-made text field
	if !strings.Contains(*body, `"incident_id":"inc-1"`) || !strings.Contains(*body, `"text"`) {
		t.Errorf("webhook body = %s", *body)
	}
	if *auth != "Bearer sekret" {
		t.Errorf("auth header = %q", *auth)
	}
}

func TestEmail_BuildMessage(t *testing.T) {
	msg := string(buildMessage("valaf@x", []string{"oncall@x", "sre@x"}, sample))
	for _, want := range []string{"From: valaf@x", "To: oncall@x, sre@x", "Subject: [valaf] CRITICAL — HighErrorRate", "checkout erroring"} {
		if !strings.Contains(msg, want) {
			t.Errorf("email missing %q in:\n%s", want, msg)
		}
	}
}
