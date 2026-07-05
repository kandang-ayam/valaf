package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/valaf/valaf/internal/core"
)

// Webhook POSTs the notification as structured JSON to an arbitrary URL — for
// automation platforms like n8n, Zapier, or a custom endpoint. An optional
// bearer token authenticates the request.
type Webhook struct {
	url   string
	token string
	http  *http.Client
}

func NewWebhook(url, token string) *Webhook {
	return &Webhook{url: url, token: token, http: &http.Client{Timeout: 15 * time.Second}}
}

func (Webhook) Name() string { return "webhook" }

func (h *Webhook) Send(ctx context.Context, n core.Notification) error {
	// Forward the full structured notification plus a ready-to-use text field.
	body, _ := json.Marshal(struct {
		core.Notification
		Text string `json:"text"`
	}{Notification: n, Text: n.Text()})

	var headers map[string]string
	if h.token != "" {
		headers = map[string]string{"Authorization": "Bearer " + h.token}
	}
	return postJSON(ctx, h.http, h.url, body, headers)
}
