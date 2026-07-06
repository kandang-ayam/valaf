<p align="center">
  <img src="valaf_logo.svg" alt="valaf" width="96">
</p>

<h1 align="center">valaf</h1>
<p align="center"><b>AI-powered incident notebook</b> — open source, self-hosted, runs entirely inside your own infrastructure.</p>

---

Monitoring tells you *what is happening*. **valaf answers: what happened, what evidence supports it, what are the most likely causes, and what does the engineer need to decide next?**

When a serious alert fires, valaf investigates **before anyone touches anything** (acting first can destroy the evidence): it collects read-only evidence from your observability stack, has an AI analyze it, and hands the on-call engineer a structured **notebook** — timeline, cited observations, ranked root-cause hypotheses. The engineer reviews, corrects, and completes that notebook, and the notebook *is* the incident documentation. No separate "write it up later" step.

```
alert fires (high/critical only)
  → related alerts grouped into ONE incident
  → evidence collected automatically (read-only, best-effort)
  → AI analysis: timeline · observations · ranked hypotheses — every claim cites evidence
  → notebook published, engineers notified (with the analysis, not just the alert)
  → engineer reviews: confirm/reject hypotheses, flag bad evidence, write the resolution
  → resolved notebook joins the knowledge base
  → future similar incidents are analyzed WITH those verified outcomes
  → export to DOCX / Markdown / JSON on demand
```

## Key properties

- **Evidence is immutable.** Raw captures are never edited or deleted — engineers *flag* bad evidence with a comment. Enforced by database triggers.
- **Every AI claim cites evidence.** Citations pointing at evidence that doesn't exist are stripped before the engineer ever sees them.
- **No fake precision.** Hypotheses are *ranked*, with supporting/contradicting evidence — never "85% confidence".
- **The AI never executes anything.** There is no code path from model output to any command or API call. Evidence content is treated as untrusted data.
- **Honest degradation.** Dead Prometheus? AI down? Not configured? The notebook still publishes with whatever was captured, and every gap is recorded as a gap.
- **Learning is retrieval, not training.** New analyses receive the verified root causes — and the *ruled-out* hypotheses — of similar past incidents. Auditable, model-agnostic.
- **Pluggable AI.** Anthropic API, OpenAI API, or any OpenAI-compatible server (vLLM, Ollama, LiteLLM, internal gateways). Evidence never has to leave your network.
- **Low footprint.** One binary (or one container image in two roles) + PostgreSQL. No Redis, no message broker, no Node — the job queue lives in Postgres, the UI is server-rendered (HTMX), and all assets are embedded in the binary.

## Quick start (Docker Compose)

```bash
git clone https://github.com/valaf/valaf && cd valaf
cp .env.example .env          # edit at least POSTGRES_PASSWORD
docker compose up -d --build
```

Then bootstrap inside the running container:

```bash
# 1. create your admin user (interactive password prompt if omitted)
docker compose exec web valaf create-user admin admin

# 2. register an alert source and get its webhook token (printed ONCE)
docker compose exec web valaf intake-token prod-alertmanager alertmanager
```

Open **http://localhost:8080**, log in, and point Alertmanager at the webhook (below). That's it — the next high/critical alert produces a notebook.

Everything is configured through environment variables (see [.env.example](.env.example) and the [reference](#configuration-reference)). **Every integration is off until you configure it** — valaf treats a missing integration as an honest gap, not an error.

## Connecting your stack

### Alertmanager (alert source)

```yaml
# alertmanager.yml
receivers:
  - name: valaf
    webhook_configs:
      - url: http://valaf-host:8080/webhook/prod-alertmanager
        http_config:
          authorization:
            type: Bearer
            credentials: <token from `valaf intake-token`>

route:
  routes:
    - receiver: valaf
      matchers: [ 'severity =~ "high|critical"' ]
      continue: true          # valaf observes; your paging route still fires
```

The path segment (`prod-alertmanager`) is the source **name** you registered; rotate its token any time by re-running `valaf intake-token` with the same name. Alerts below high severity are dropped at the door (no notebook, no AI cost). Alertmanager's own grouping is respected: one webhook group = one incident, and repeats/storms attach to the existing incident instead of creating new ones.

### Prometheus (evidence)

```bash
VALAF_PROMETHEUS_URL=http://prometheus:9090
```

Works with anything that speaks the Prometheus HTTP query API: Prometheus, Thanos, Mimir, VictoriaMetrics. valaf runs *read-only* range queries around the alert window (CPU / memory for the affected host, target up-ness for the affected service) and records each query verbatim so any engineer can reproduce it by hand. A failed or empty query is stored as `failed` / `gap` evidence — visible in the notebook, never silently dropped.

### AI provider (analysis)

Pick **one**:

```bash
# Anthropic API
VALAF_AI_PROVIDER=anthropic
VALAF_AI_API_KEY=sk-ant-...
VALAF_AI_MODEL=claude-sonnet-5

# OpenAI API
VALAF_AI_PROVIDER=openai_compat
VALAF_AI_BASE_URL=https://api.openai.com/v1
VALAF_AI_API_KEY=sk-...
VALAF_AI_MODEL=gpt-4o-mini

# Fully self-hosted (Ollama example — evidence never leaves your network)
VALAF_AI_PROVIDER=openai_compat
VALAF_AI_BASE_URL=http://ollama:11434/v1
VALAF_AI_MODEL=llama3.1:8b
```

No provider configured? Notebooks still publish with all collected evidence and an explicit "analysis skipped" marker.

### Notifications

Configure any combination — valaf fans out to **all** configured channels when the AI triage verdict is *actionable*. Likely-noise incidents are recorded quietly (the notebook always exists; only the ping is filtered).

| Channel | Variables | Notes |
|---|---|---|
| **Slack** | `VALAF_SLACK_WEBHOOK_URL` | Incoming-webhook URL; message carries summary, top hypotheses, and the notebook link |
| **Telegram** | `VALAF_TELEGRAM_BOT_TOKEN`, `VALAF_TELEGRAM_CHAT_ID` | Create the bot with @BotFather; chat id may be a group id |
| **Email** | `VALAF_SMTP_HOST`, `VALAF_SMTP_PORT`, `VALAF_SMTP_USERNAME`, `VALAF_SMTP_PASSWORD`, `VALAF_SMTP_FROM`, `VALAF_SMTP_TO` | STARTTLS; `VALAF_SMTP_TO` is comma-separated |
| **Webhook** (n8n, Make, custom) | `VALAF_WEBHOOK_URL`, `VALAF_WEBHOOK_TOKEN` (optional) | Structured JSON POST; token sent as `Authorization: Bearer` |

Set `VALAF_BASE_URL` (e.g. `https://valaf.example.com`) so notifications carry clickable incident links.

The generic webhook payload — ready to drive an n8n workflow:

```json
{
  "event": "incident.published",
  "incident_id": "…uuid…",
  "url": "https://valaf.example.com/incidents/…",
  "title": "HighErrorRate",
  "severity": "critical",
  "triage_verdict": "actionable",
  "summary": "…AI summary…",
  "hypotheses": [ { "rank": 1, "title": "…" } ]
}
```

## Users, roles & SSO

Local accounts are always available:

```bash
valaf create-user alice engineer        # prompts for password
```

| Role | Can do |
|---|---|
| `viewer` | read everything |
| `engineer` | + confirm/reject hypotheses, flag evidence, write resolutions |
| `admin` | + manage users and settings |

Every engineer action is attributed and written to an **append-only audit log** in the same transaction as the change itself.

**Reverse-proxy SSO** — front valaf with your existing auth gateway (oauth2-proxy, Authelia, corporate SSO) and set:

```bash
VALAF_TRUSTED_PROXY_HEADER=X-Forwarded-User
```

valaf then trusts that header as the username (users are auto-provisioned as viewers; promote with `create-user`). **Only** set this when valaf is reachable exclusively through the proxy — anyone who can reach valaf directly could forge the header. Native OIDC is on the roadmap.

## Exports

From any incident page: **Markdown**, **JSON** (full machine-readable notebook), and **DOCX** (restyle in your corporate template). Exports are rendered on demand from the live notebook and never stored server-side — there is nothing to leak, retain, or go stale. PDF: use the browser's print-to-PDF for now (native PDF is on the roadmap).

## Configuration reference

| Variable | Default | Purpose |
|---|---|---|
| `VALAF_DATABASE_URL` | — **(required)** | PostgreSQL DSN (`DATABASE_URL` also accepted) |
| `VALAF_HTTP_ADDR` | `:8080` | Listen address (web role) |
| `VALAF_BASE_URL` | — | Public URL for links in notifications |
| `VALAF_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `VALAF_SESSION_SECURE` | `false` | Set `true` behind HTTPS (Secure cookies) |
| `VALAF_TRUSTED_PROXY_HEADER` | — | Header-based SSO (see above) |
| `VALAF_PROMETHEUS_URL` | — | Prometheus-compatible query API |
| `VALAF_AI_PROVIDER` | — | `anthropic` \| `openai_compat` \| empty = no AI |
| `VALAF_AI_BASE_URL` | provider default | API base URL |
| `VALAF_AI_API_KEY` | — | API key (omit for keyless internal servers) |
| `VALAF_AI_MODEL` | — | Model identifier |
| `VALAF_SLACK_WEBHOOK_URL` | — | Slack incoming webhook |
| `VALAF_TELEGRAM_BOT_TOKEN` / `VALAF_TELEGRAM_CHAT_ID` | — | Telegram bot |
| `VALAF_SMTP_HOST` / `PORT` / `USERNAME` / `PASSWORD` / `FROM` / `TO` | port `587` | Email (STARTTLS) |
| `VALAF_WEBHOOK_URL` / `VALAF_WEBHOOK_TOKEN` | — | Generic JSON webhook (n8n) |

Compose-only helpers: `POSTGRES_DB` / `POSTGRES_USER` / `POSTGRES_PASSWORD` (database container) and `VALAF_PORT` (published host port).

## Running without Docker

```bash
go build -o valaf ./cmd/valaf        # Go 1.26+; assets are embedded

export VALAF_DATABASE_URL='postgres://valaf:pw@localhost:5432/valaf?sslmode=disable'
./valaf migrate                      # or let `serve` do it on startup
./valaf serve   &                    # web: webhook intake + notebook UI
./valaf worker  &                    # collection, AI analysis, notifications
```

CLI: `valaf <migrate | serve | worker | intake-token | create-user | version>`

## Operations

- **Health:** `GET /healthz` (liveness), `GET /readyz` (DB-checked readiness).
- **Migrations** are embedded and applied automatically by `serve` (or explicitly via `migrate`). Safe to run repeatedly.
- **Scaling:** run multiple `worker` containers freely — the Postgres queue uses `FOR UPDATE SKIP LOCKED`; a crashed worker's job is reclaimed automatically.
- **Backups:** PostgreSQL is the single source of truth — `pg_dump` covers everything.
- **HTTPS:** terminate TLS at your reverse proxy and set `VALAF_SESSION_SECURE=true`.

## Security model (summary)

- Webhook intake is authenticated per source (bearer token or HMAC, hashed at rest, rotatable).
- All collector credentials are **read-only**; valaf never executes commands on your infrastructure and installs nothing on hosts.
- One documented outbound connection: the AI endpoint — which may be inside your network.
- Passwords: argon2id. Sessions: DB-backed, CSRF-protected. Server-rendered templates auto-escape all evidence content (which is treated as untrusted).
- Immutability (raw evidence, audit log) is enforced *in the database* by triggers, not just application code.

## Architecture (short version)

Modular monolith, hexagonal: a pure domain core behind ports (`IntakeAdapter`, `Collector`, `AIProvider`, `NotificationChannel`), with adapters selected by configuration. One image, two roles: `serve` (web) and `worker` (jobs). The full design of record lives in [PROJECT_BRIEF.md](PROJECT_BRIEF.md), [architecture.drawio](architecture.drawio), [flowchart.drawio](flowchart.drawio), and [erd.drawio](erd.drawio).

## Roadmap

Grafana panel-snapshot & Loki log collectors · Kubernetes state collector · native OIDC · PDF export · admin UI (users, intake sources, settings) · retention policies · Elasticsearch collector · cross-incident pattern mining.

## License

[MIT](LICENSE)
