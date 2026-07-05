// Command valaf is the single binary for the whole application. Runtime role is
// selected by subcommand:
//
//	valaf migrate                 apply database migrations, then exit
//	valaf serve                   run the web server (webhook intake, notebook UI, API)
//	valaf worker                  run the background worker (collection, analysis)
//	valaf intake-token <name> [source_type]
//	                              create/rotate a webhook shared token (prints it once)
//	valaf version                 print the build version
//
// The web and worker roles share one codebase and one database; there is no
// message broker (jobs live in Postgres).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/valaf/valaf/internal/adapters/ai"
	"github.com/valaf/valaf/internal/adapters/alertmanager"
	"github.com/valaf/valaf/internal/adapters/notify"
	"github.com/valaf/valaf/internal/adapters/prometheus"
	"github.com/valaf/valaf/internal/auth"
	"github.com/valaf/valaf/internal/config"
	"github.com/valaf/valaf/internal/core"
	"github.com/valaf/valaf/internal/migrate"
	"github.com/valaf/valaf/internal/store"
	"github.com/valaf/valaf/internal/web"
	"github.com/valaf/valaf/internal/worker"
)

// version is overridable at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	if cmd == "version" {
		fmt.Println("valaf", version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration error", "error", err)
		os.Exit(1)
	}

	// Signal-aware root context: Ctrl-C / SIGTERM cancels everything.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cmd, cfg, log); err != nil {
		log.Error("fatal", "command", cmd, "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd string, cfg config.Config, log *slog.Logger) error {
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch cmd {
	case "migrate":
		log.Info("applying migrations")
		if err := migrate.Run(ctx, pool); err != nil {
			return err
		}
		log.Info("migrations up to date")
		return nil

	case "serve":
		// Migrations are applied on startup so a fresh deploy is self-contained.
		if err := migrate.Run(ctx, pool); err != nil {
			return fmt.Errorf("startup migrations: %w", err)
		}
		return serve(ctx, cfg, pool, log)

	case "intake-token":
		return intakeToken(ctx, pool)

	case "create-user":
		return createUser(ctx, pool)

	case "worker":
		return runWorker(ctx, cfg, pool, log)

	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func serve(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) error {
	// Wire the intake slice: repositories → service → adapters → web.
	incidentRepo := store.NewIncidentRepo(pool)
	sourceRepo := store.NewSourceRepo(pool)
	readRepo := store.NewReadRepo(pool)
	userRepo := store.NewUserRepo(pool)
	sessionRepo := store.NewSessionRepo(pool)
	reviewRepo := store.NewReviewRepo(pool)
	intakeSvc := core.NewService(incidentRepo)
	adapters := map[string]core.IntakeAdapter{
		string(core.SourceAlertmanager): alertmanager.New(),
	}
	authCfg := web.AuthConfig{
		TrustedProxyHeader: cfg.TrustedProxyHeader,
		SessionSecure:      cfg.SessionSecure,
		SessionTTL:         7 * 24 * time.Hour,
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           web.New(pool, log, intakeSvc, sourceRepo, readRepo, userRepo, sessionRepo, reviewRepo, authCfg, adapters).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Serve in the background so we can watch for shutdown signals.
	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.HTTPAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down http server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func runWorker(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) error {
	collectors := buildCollectors(cfg, log)
	collection := core.NewCollectionService(store.NewEvidenceRepo(pool), collectors)

	provider := buildAIProvider(cfg, log)
	analysis := core.NewAnalysisService(provider, store.NewAnalysisRepo(pool))

	channels := buildChannels(cfg, log)
	notification := core.NewNotificationService(store.NewNotificationRepo(pool), channels, cfg.BaseURL)

	w := worker.New(store.NewJobRepo(pool), store.NewIncidentRepo(pool), collection, analysis, notification, log)
	log.Info("worker started", "collectors", len(collectors), "ai", provider != nil, "channels", len(channels))
	return w.Run(ctx)
}

// buildChannels registers a notification channel per configured destination.
func buildChannels(cfg config.Config, log *slog.Logger) []core.NotificationChannel {
	var chs []core.NotificationChannel
	if cfg.SlackWebhookURL != "" {
		chs = append(chs, notify.NewSlack(cfg.SlackWebhookURL))
		log.Info("notify channel enabled", "channel", "slack")
	}
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		chs = append(chs, notify.NewTelegram(cfg.TelegramBotToken, cfg.TelegramChatID))
		log.Info("notify channel enabled", "channel", "telegram")
	}
	if cfg.SMTPHost != "" && cfg.SMTPFrom != "" && len(cfg.SMTPTo) > 0 {
		chs = append(chs, notify.NewEmail(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPFrom, cfg.SMTPTo))
		log.Info("notify channel enabled", "channel", "email")
	}
	if cfg.WebhookURL != "" {
		chs = append(chs, notify.NewWebhook(cfg.WebhookURL, cfg.WebhookToken))
		log.Info("notify channel enabled", "channel", "webhook")
	}
	return chs
}

// buildAIProvider returns the configured provider, or nil when analysis is not
// configured (the notebook then publishes with evidence only).
func buildAIProvider(cfg config.Config, log *slog.Logger) core.AIProvider {
	switch cfg.AIProvider {
	case "openai_compat":
		log.Info("ai provider enabled", "provider", "openai_compat", "model", cfg.AIModel)
		return ai.NewOpenAICompat(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
	case "anthropic":
		log.Info("ai provider enabled", "provider", "anthropic", "model", cfg.AIModel)
		return ai.NewAnthropic(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
	case "":
		log.Info("ai provider disabled (notebooks publish with evidence only)")
		return nil
	default:
		log.Warn("unknown ai provider, analysis disabled", "provider", cfg.AIProvider)
		return nil
	}
}

// buildCollectors registers a collector per configured role. An unconfigured
// role is simply absent — its evidence becomes an honest gap, not an error.
func buildCollectors(cfg config.Config, log *slog.Logger) []core.Collector {
	var cs []core.Collector
	if cfg.PrometheusURL != "" {
		cs = append(cs, prometheus.New(cfg.PrometheusURL))
		log.Info("collector enabled", "kind", "prometheus", "url", cfg.PrometheusURL)
	}
	return cs
}

// intakeToken creates or rotates a shared-token intake source and prints the
// token once. Usage: valaf intake-token <name> [source_type].
func intakeToken(ctx context.Context, pool *pgxpool.Pool) error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: valaf intake-token <name> [source_type]")
	}
	name := os.Args[2]
	sourceType := string(core.SourceAlertmanager)
	if len(os.Args) >= 4 {
		sourceType = os.Args[3]
	}

	token, err := store.NewSourceRepo(pool).UpsertSharedToken(ctx, name, sourceType)
	if err != nil {
		return err
	}
	fmt.Printf("intake source %q (%s) ready.\n", name, sourceType)
	fmt.Printf("shared token (shown once): %s\n", token)
	fmt.Printf("send webhooks to: POST /webhook/%s  with header  Authorization: Bearer %s\n", name, token)
	return nil
}

// createUser creates a local account (admin bootstrap mechanism).
// Usage: valaf create-user <username> <viewer|engineer|admin> [password]
// If password is omitted, one is generated and printed once.
func createUser(ctx context.Context, pool *pgxpool.Pool) error {
	args := os.Args[2:]
	if len(args) < 2 {
		return fmt.Errorf("usage: valaf create-user <username> <viewer|engineer|admin> [password]")
	}
	username, role := args[0], args[1]
	switch role {
	case "viewer", "engineer", "admin":
	default:
		return fmt.Errorf("role must be viewer, engineer, or admin")
	}

	password, generated := "", false
	if len(args) >= 3 {
		password = args[2]
	} else {
		p, err := randomToken()
		if err != nil {
			return err
		}
		password, generated = p[:20], true
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	id, err := store.NewUserRepo(pool).Create(ctx, username, "", hash, role, "local")
	if err != nil {
		return err
	}
	fmt.Printf("created user %q (%s), id=%s\n", username, role, id)
	if generated {
		fmt.Printf("generated password (shown once): %s\n", password)
	}
	return nil
}

// randomToken returns a hex-encoded 32-byte random string.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: valaf <migrate|serve|worker|intake-token|create-user|version>")
}
