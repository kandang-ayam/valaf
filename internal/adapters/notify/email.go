package notify

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/valaf/valaf/internal/core"
)

// Email sends via SMTP with STARTTLS (the common 587/25 path). Auth is used when
// a username is set. Implicit-TLS (465) is not handled here.
type Email struct {
	host     string
	port     int
	username string
	password string
	from     string
	to       []string
}

func NewEmail(host string, port int, username, password, from string, to []string) *Email {
	return &Email{host: host, port: port, username: username, password: password, from: from, to: to}
}

func (Email) Name() string { return "email" }

func (e *Email) Send(_ context.Context, n core.Notification) error {
	addr := fmt.Sprintf("%s:%d", e.host, e.port)
	var auth smtp.Auth
	if e.username != "" {
		auth = smtp.PlainAuth("", e.username, e.password, e.host)
	}
	return smtp.SendMail(addr, auth, e.from, e.to, buildMessage(e.from, e.to, n))
}

// buildMessage assembles RFC 5322 headers + body.
func buildMessage(from string, to []string, n core.Notification) []byte {
	subject := fmt.Sprintf("[valaf] %s — %s", strings.ToUpper(n.Severity), n.Title)
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(n.Text())
	b.WriteString("\r\n")
	return []byte(b.String())
}
