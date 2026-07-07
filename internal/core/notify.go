package core

import (
	"context"
	"fmt"
	"strings"
)

// Notification is the payload delivered to channels. json tags let the generic
// webhook channel forward it structured (e.g. into n8n).
type Notification struct {
	IncidentID    string `json:"incident_id"`
	IncidentURL   string `json:"incident_url,omitempty"`
	Title         string `json:"title"`
	Severity      string `json:"severity"`
	Verdict       string `json:"verdict"`
	Summary       string `json:"summary,omitempty"`
	TopHypothesis string `json:"top_hypothesis,omitempty"`
}

// Text renders a plain-text message used by chat/email channels.
func (n Notification) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s", strings.ToUpper(n.Severity), n.Title)
	if n.Verdict != "" {
		fmt.Fprintf(&b, " — %s", n.Verdict)
	}
	b.WriteByte('\n')
	if n.Summary != "" {
		b.WriteString(n.Summary)
		b.WriteByte('\n')
	}
	if n.TopHypothesis != "" {
		fmt.Fprintf(&b, "Most likely: %s\n", n.TopHypothesis)
	}
	if n.IncidentURL != "" {
		b.WriteString(n.IncidentURL)
	}
	return b.String()
}

// NotificationChannel delivers a notification to one destination. Best-effort:
// a failure is recorded, never retried (a retry would duplicate the ping).
type NotificationChannel interface {
	Name() string // "slack" | "email" | "telegram" | "webhook"
	Send(ctx context.Context, n Notification) error
}

// NotificationRepo loads the payload and records outcomes (implemented by store).
type NotificationRepo interface {
	LoadNotification(ctx context.Context, incidentID string) (Notification, error)
	Record(ctx context.Context, incidentID, channel, target, status, reason string) error
}

// NotifyResult summarizes a dispatch, for logging.
type NotifyResult struct {
	Sent   int
	Failed int
	Quiet  bool
}

// NotificationService implements the notify gate: actionable incidents fan out
// to every configured channel; non-actionable ones are recorded quiet and not
// sent (filter the notify, never the record).
type NotificationService struct {
	channels []NotificationChannel
	repo     NotificationRepo
	baseURL  string
}

func NewNotificationService(repo NotificationRepo, channels []NotificationChannel, baseURL string) *NotificationService {
	return &NotificationService{channels: channels, repo: repo, baseURL: strings.TrimRight(baseURL, "/")}
}

func (s *NotificationService) Notify(ctx context.Context, incidentID string) (NotifyResult, error) {
	note, err := s.repo.LoadNotification(ctx, incidentID)
	if err != nil {
		return NotifyResult{}, err
	}
	note.IncidentURL = s.incidentURL(incidentID)

	if !shouldNotify(note) {
		if err := s.repo.Record(ctx, incidentID, "gate", "", "quiet", "noise"); err != nil {
			return NotifyResult{}, err
		}
		return NotifyResult{Quiet: true}, nil
	}

	var res NotifyResult
	for _, ch := range s.channels {
		status := "sent"
		if err := ch.Send(ctx, note); err != nil {
			status = "failed"
			res.Failed++
		} else {
			res.Sent++
		}
		// Record outcome; a DB error here is real and should surface.
		if err := s.repo.Record(ctx, incidentID, ch.Name(), "", status, "actionable"); err != nil {
			return res, err
		}
	}
	return res, nil
}

// shouldNotify decides whether an incident warrants a ping. An AI "actionable"
// verdict always notifies; an explicit "likely_noise" stays quiet. When there is
// no AI verdict (AI disabled or failed → "unknown"/empty), we still notify,
// because the incident already cleared the severity threshold at intake.
func shouldNotify(n Notification) bool {
	switch n.Verdict {
	case "actionable":
		return true
	case "likely_noise":
		return false
	default: // "unknown" | "" — no AI opinion; the severity gate already vouched for it
		return true
	}
}

func (s *NotificationService) incidentURL(id string) string {
	if s.baseURL == "" {
		return "/incidents/" + id
	}
	return s.baseURL + "/incidents/" + id
}
