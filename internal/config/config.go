// Package config loads valaf's runtime configuration.
//
// For this slice it reads from the environment only. Adapter bindings
// (collectors, AI providers, auth) will move to valaf.yaml via koanf when those
// modules land; database, HTTP, and log settings stay here as the base layer.
package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL string
	HTTPAddr    string
	LogLevel    string

	// SeverityThreshold is the minimum alert severity that opens a notebook:
	// "warning" | "high" | "critical". Default "high".
	SeverityThreshold string

	// PrometheusURL binds the metrics collector. Empty = collector disabled
	// (its evidence simply isn't gathered — an honest gap, not an error).
	PrometheusURL string

	// AI provider. AIProvider empty = no analysis (notebook publishes with
	// evidence only). "openai_compat" | "anthropic".
	AIProvider string
	AIBaseURL  string
	AIAPIKey   string
	AIModel    string

	// Auth
	TrustedProxyHeader string // e.g. "X-Forwarded-User"; empty disables proxy auth
	SessionSecure      bool   // set true when served over HTTPS

	// BaseURL is valaf's externally-reachable URL, used to build incident links
	// in notifications (e.g. https://valaf.example.com).
	BaseURL string

	// Notification channels. Each is enabled only when configured.
	SlackWebhookURL  string
	TelegramBotToken string
	TelegramChatID   string
	SMTPHost         string
	SMTPPort         int
	SMTPUsername     string
	SMTPPassword     string
	SMTPFrom         string
	SMTPTo           []string
	WebhookURL       string
	WebhookToken     string

	// Snapshot storage (blob store for dashboard PNGs). S3 set = s3 backend,
	// otherwise local volume at DataDir.
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3UseSSL    bool
	DataDir     string

	// Grafana snapshot collector. Empty = disabled (no dashboard captures).
	GrafanaURL   string
	GrafanaToken string
}

// Load reads configuration from the environment and validates required fields.
func Load() (Config, error) {
	c := Config{
		DatabaseURL:   firstNonEmpty(os.Getenv("VALAF_DATABASE_URL"), os.Getenv("DATABASE_URL")),
		HTTPAddr:          getenv("VALAF_HTTP_ADDR", ":8080"),
		LogLevel:          getenv("VALAF_LOG_LEVEL", "info"),
		SeverityThreshold: getenv("VALAF_SEVERITY_THRESHOLD", "high"),
		PrometheusURL: os.Getenv("VALAF_PROMETHEUS_URL"),
		AIProvider:    os.Getenv("VALAF_AI_PROVIDER"),
		AIBaseURL:     os.Getenv("VALAF_AI_BASE_URL"),
		AIAPIKey:      os.Getenv("VALAF_AI_API_KEY"),
		AIModel:       os.Getenv("VALAF_AI_MODEL"),

		TrustedProxyHeader: os.Getenv("VALAF_TRUSTED_PROXY_HEADER"),
		SessionSecure:      os.Getenv("VALAF_SESSION_SECURE") == "true",

		BaseURL:          strings.TrimRight(os.Getenv("VALAF_BASE_URL"), "/"),
		SlackWebhookURL:  os.Getenv("VALAF_SLACK_WEBHOOK_URL"),
		TelegramBotToken: os.Getenv("VALAF_TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("VALAF_TELEGRAM_CHAT_ID"),
		SMTPHost:         os.Getenv("VALAF_SMTP_HOST"),
		SMTPPort:         atoiOr(os.Getenv("VALAF_SMTP_PORT"), 587),
		SMTPUsername:     os.Getenv("VALAF_SMTP_USERNAME"),
		SMTPPassword:     os.Getenv("VALAF_SMTP_PASSWORD"),
		SMTPFrom:         os.Getenv("VALAF_SMTP_FROM"),
		SMTPTo:           splitList(os.Getenv("VALAF_SMTP_TO")),
		WebhookURL:       os.Getenv("VALAF_WEBHOOK_URL"),
		WebhookToken:     os.Getenv("VALAF_WEBHOOK_TOKEN"),

		S3Endpoint:  os.Getenv("VALAF_S3_ENDPOINT"),
		S3Bucket:    os.Getenv("VALAF_S3_BUCKET"),
		S3AccessKey: os.Getenv("VALAF_S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("VALAF_S3_SECRET_KEY"),
		S3UseSSL:    os.Getenv("VALAF_S3_USE_SSL") == "true",
		DataDir:     getenv("VALAF_DATA_DIR", "/var/lib/valaf"),

		GrafanaURL:   strings.TrimRight(os.Getenv("VALAF_GRAFANA_URL"), "/"),
		GrafanaToken: os.Getenv("VALAF_GRAFANA_TOKEN"),
	}
	if c.DatabaseURL == "" {
		return c, errors.New("VALAF_DATABASE_URL (or DATABASE_URL) is required")
	}
	return c, nil
}

func atoiOr(s string, fallback int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return fallback
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
