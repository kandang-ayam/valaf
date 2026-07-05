package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/valaf/valaf/internal/core"
)

// Telegram sends via the Bot API sendMessage method.
type Telegram struct {
	token   string
	chatID  string
	apiBase string // overridable for tests; defaults to the public API
	http    *http.Client
}

func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{
		token:   token,
		chatID:  chatID,
		apiBase: "https://api.telegram.org",
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (Telegram) Name() string { return "telegram" }

func (t *Telegram) Send(ctx context.Context, n core.Notification) error {
	body, _ := json.Marshal(map[string]string{
		"chat_id":                  t.chatID,
		"text":                     n.Text(),
		"disable_web_page_preview": "true",
	})
	url := t.apiBase + "/bot" + t.token + "/sendMessage"
	return postJSON(ctx, t.http, url, body, nil)
}
