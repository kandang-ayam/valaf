// Package notify implements core.NotificationChannel over common transports:
// Slack, Telegram, email (SMTP), and a generic JSON webhook (e.g. for n8n).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/valaf/valaf/internal/core"
)

// Slack posts to a Slack Incoming Webhook URL.
type Slack struct {
	webhookURL string
	http       *http.Client
}

func NewSlack(webhookURL string) *Slack {
	return &Slack{webhookURL: webhookURL, http: &http.Client{Timeout: 15 * time.Second}}
}

func (Slack) Name() string { return "slack" }

func (s *Slack) Send(ctx context.Context, n core.Notification) error {
	body, _ := json.Marshal(map[string]string{"text": n.Text()})
	return postJSON(ctx, s.http, s.webhookURL, body, nil)
}

// postJSON POSTs a JSON body and treats any non-2xx as an error.
func postJSON(ctx context.Context, client *http.Client, url string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("http %d: %s", resp.StatusCode, snippet)
	}
	return nil
}
