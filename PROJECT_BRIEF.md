# valaf — Product Brief

> Technology-agnostic specification for building valaf properly. This
> document describes **what valaf is and the rules it must follow** — not how
> to implement it. Language, framework, and architecture are decided at the
> start of implementation. A working prototype exists in this repository and
> may be used as a reference or discarded.
> Last updated: 2026-07-05.

## 1. What valaf is

**valaf is an AI-powered incident notebook** — open source, self-hostable in
any company's own infrastructure.

Monitoring platforms answer *"what is happening?"*. valaf answers: *"what
happened, what evidence supports it, what are the most likely causes, and
what does the engineer need to decide next?"*

When a serious alert fires, valaf immediately performs the first stage of the
investigation: it collects evidence from the monitoring/observability layer
**before anyone takes action** (acting first can destroy the evidence), has
an AI analyze it, and hands the on-call engineer a structured **notebook**:
a timeline, cited observations, and ranked root-cause hypotheses. The
engineer reviews, corrects, and completes that notebook — and the notebook
*is* the incident documentation. There is no separate "write it up in
Confluence" step afterwards.

### The problem it solves

- Engineers spend most of incident response gathering information from many
  systems (dashboards, logs, hosts, deploy history) before they can decide.
- After resolution, the investigation knowledge evaporates — it lives in
  terminal history and people's heads, and leaves when they leave.
- Post-incident documentation is a manual chore done late or never, and each
  company wants it standardized to its own policy.

### The core loop

```
alert fires (high/critical only)
→ group related alerts into one incident
→ collect evidence automatically (read-only, before any action)
→ AI analysis: timeline, observations, ranked hypotheses — every claim cites evidence
→ notebook published; engineer notified (with the analysis, not just the alert)
→ engineer reviews: confirms/rejects hypotheses, flags bad evidence,
  documents root cause, actions taken, and the ultimate solution
→ resolved notebook joins the searchable knowledge base
→ future analyses of similar incidents automatically use past verified outcomes
→ notebook exportable to company-standard documents
```

## 2. Goals

1. **Cut time-to-decision.** The engineer opens one page that already
   contains what they would have spent 15–30 minutes gathering.
2. **Preserve evidence.** Captured before remediation, stored immutably.
3. **Documentation as a by-product.** The investigation record is created
   *during* the incident, not written afterwards.
4. **A growing knowledge base.** Every resolved incident is searchable:
   what happened, the evidence, the root cause, the fix, has it happened
   before. Knowledge survives engineer turnover.
5. **A system that improves.** New analyses are informed by previously
   confirmed root causes and previously ruled-out hypotheses.
6. **Deployable by any company.** Own infrastructure, own policies, own AI
   models. No dependency on any external service being reachable.

**Non-goals:** valaf does not remediate, does not replace the engineer's
judgment, and does not replace the monitoring stack — it consumes it.

## 3. Product rules (decided — do not silently relitigate)

These came out of extended design discussion; each has a reason.

1. **The notebook is the product.** Automated investigation exists elsewhere;
   the durable, evidence-linked, searchable, exportable incident record is
   what makes valaf valuable.
2. **Only high/critical alerts trigger investigations.** Low-severity noise
   must not burn AI cost or create notebook clutter (threshold configurable).
3. **One outage = one notebook.** Related alerts within a time window are
   grouped into a single incident; an alert storm must never produce fifty
   investigations.
4. **Evidence is immutable.** What the system captured is an audit trail. If
   a capture is wrong or misleading, the engineer *flags it invalid with a
   comment* — the raw data is never edited or deleted.
5. **Failed captures are recorded as evidence gaps,** never silently dropped.
   "The log system was unreachable" is itself diagnostic information.
6. **Every AI claim must cite specific evidence items.** Claims citing
   evidence that doesn't exist are stripped before the engineer sees them.
   One fabricated reference destroys trust permanently.
7. **No numeric confidence percentages.** AI cannot produce calibrated
   probabilities; "85%" is fake precision. Hypotheses are *ranked*, each with
   supporting evidence, contradicting evidence, and suggested read-only
   verification steps.
8. **Collection is agentless and strictly read-only.** Evidence comes from
   the observability layer the company already runs (metrics, logs,
   dashboards, orchestrator APIs). Nothing is installed on hosts, nothing
   executes commands on infrastructure, nothing can mutate state. (An
   SSH-probe approach was designed and explicitly rejected by the owner.)
9. **The AI never chooses or executes actions.** There is no code path from
   model output to any command, query mutation, or infrastructure call. Log
   content is attacker-influenceable and is treated as untrusted data in
   prompts.
10. **The AI backend is pluggable.** Companies with strict policies must be
    able to use their own internally-hosted models; using an external AI API
    is a configuration choice, never a requirement.
11. **Learning is retrieval, not training.** When a new incident resembles
    past resolved ones (overlapping alert types), their engineer-verified
    root causes, solutions, and *ruled-out* hypotheses are provided to the
    analysis. Auditable and model-agnostic.
12. **The engineer is the decision-maker.** valaf accelerates; it never
    concludes on the record. Only an engineer can mark a root cause.
13. **PostgreSQL is the production database** (owner decision). Notebook
    documents, search, and the knowledge base live there. Large binary
    evidence (dashboard snapshots) is stored as files, referenced from the
    database.
14. **Exports follow company policy, not ours.** Word document export is the
    primary path (engineers restyle in the corporate template), plus PDF for
    read-only distribution, plus raw machine-readable formats for custom
    pipelines.
15. **License: MIT**, published as open source.

## 4. Functional requirements

### Intake & correlation
- Receive alerts via webhook from Alertmanager (first-class); design so other
  alert sources (cloud alerting, ELK watchers) can be added.
- Filter: investigate only high/critical severities (configurable).
- Group related alerts (shared grouping key, time window) into one incident.
- Webhook must be authenticatable (shared token) — alert sources can't do SSO.

### Evidence collection
- Pluggable **collector** concept: given an incident, return evidence items.
  Each item records *what* was captured, *the exact query/request that
  produced it* (reproducible by hand), *when*, and the raw result — or the
  error if capture failed.
- Initial collectors (matching the first deployment): Prometheus metrics
  around the alert window (host metrics via node-exporter, containers via
  cAdvisor), Loki log queries, Grafana dashboard-panel image snapshots,
  Kubernetes state (for clusters). Roadmap: Elasticsearch/ELK, cloud
  providers.
- Collection is time-boxed and best-effort; a slow or dead source produces an
  evidence gap, not a stuck investigation.

### AI analysis
- Input: the incident's evidence (+ similar past resolved incidents).
  Output, structured: a summary, a timeline, observations, ranked hypotheses
  (per rule 7), and explicit evidence gaps.
- Enforce citation validity (rule 6). Treat evidence content as untrusted
  data (rule 9).
- Provider abstraction (rule 10): at minimum Anthropic-compatible and
  OpenAI-compatible APIs, so self-hosted model servers work out of the box.
- If analysis fails, the notebook still publishes with evidence — collection
  must never be lost to an AI error.

### The notebook (web application)
- Incident list with status, search across all notebook content, and filters.
- Incident detail: alerts, AI summary/timeline/observations, hypotheses with
  their evidence links, full evidence index with raw data viewable inline,
  dashboard snapshots displayed as images.
- Engineer actions (all recorded with who/when — see auth):
  - confirm / reject each hypothesis, with a note;
  - flag evidence invalid, with a comment (raw data untouched);
  - write the resolution: root cause, step-by-step actions taken, ultimate
    solution/prevention, notes. Saving a root cause marks the incident
    resolved and admits it to the learning corpus.
- Exports per incident: Word (DOCX), PDF, Markdown, raw JSON — all generated
  from the same underlying document, dashboard snapshots embedded.
- Notifications: on investigation completion, push a summary + link to chat
  (Slack first; design for other channels).

### Authentication & authorization (required for v1)
- **Local accounts** (secure password hashing, sessions, CSRF protection),
  with an admin bootstrap mechanism.
- **Reverse-proxy header authentication** (trusted-proxy mode) so companies
  can front valaf with their existing SSO gateway (oauth2-proxy, Authelia,
  corporate gateways) with zero IdP integration in valaf itself.
- **OIDC** built-in (Keycloak, Entra ID, Okta, Google) as the native SSO
  path — may follow shortly after v1.
- **Not PAM** — PAM authenticates host logins, not web applications; the
  need behind it is covered by SSO/LDAP. LDAP/AD only if a deployment
  demands it.
- **Roles:** `viewer` (read), `engineer` (verdicts, flags, resolutions),
  `admin` (users, settings). The acting user is recorded on every engineer
  action instead of free-text name fields.
- **Audit log:** append-only record of every verdict, flag, and resolution
  change (who, what, when) — consistent with the audit-trail philosophy and
  the first thing a security review asks for.

## 5. Non-functional requirements

- **Self-hosted anywhere:** a company must be able to run valaf entirely
  inside its network — including the AI model. Simple deployment (containers
  + PostgreSQL); no external SaaS dependencies.
- **Infrastructure-agnostic:** works whether workloads run on Kubernetes,
  Docker, VMs, bare metal, or hybrid — because it consumes the observability
  layer, not the infrastructure.
- **Security review-friendly:** read-only credentials only; one clearly
  documented outbound connection (the AI endpoint, which may be internal);
  no command execution anywhere; notebooks render evidence, never execute it.
- **Low operational footprint:** an SRE team should be able to run it
  without adopting new infrastructure beyond PostgreSQL.
- **Honest degradation:** any missing integration (no Grafana renderer, no
  Loki, AI endpoint down) reduces the notebook's richness but never breaks
  the flow.

## 6. Branding

- Logo: owner-supplied green "V" vector (`src/valaf/web/static/logo.svg` in
  this repo — keep this asset; it was hand-corrected and must not be
  regenerated).
- UI theme matches the logo: olive/lime green accent (#55801a family), dark
  green header, light warm background. Status colors stay semantic
  (green resolved, red failed, orange in-progress).

## 7. First deployment environment (acceptance target)

The owner's own infrastructure is the v1 acceptance environment:

- Multiple VPS (2+), applications both in Docker and directly on hosts.
  No Kubernetes.
- Existing observability stack (Docker on an "obs" VPS): Prometheus v3,
  Loki 3, Grafana 11, Alloy (log shipping), node-exporter, cAdvisor, Tempo,
  OpenTelemetry collector.
- Alertmanager is **not yet enabled** — setting it up (with starter alert
  rules for instance-down / disk / memory / container-flapping) is part of
  the rollout. A setup guide exists in `docs/DEPLOY_VPS.md`.
- Grafana snapshot capture requires the Grafana image-renderer plugin and a
  viewer-role service token.
- **Acceptance:** a real alert on this stack produces, with no human input,
  a notebook containing Prometheus evidence, Loki log excerpts, Grafana
  panel images, and a cited AI analysis; the engineer can complete the
  review workflow and export a DOCX; a later similar alert's analysis
  visibly uses the first incident's verified outcome.

## 8. Roadmap after v1

1. OIDC native SSO; audit log UI.
2. Elasticsearch/ELK collector (validates the collector contract against a
   second logging ecosystem).
3. Retention policies and admin deletion (DB + snapshot files).
4. Full-text search upgrade (database-native) as the corpus grows.
5. Vision-capable analysis: feed dashboard snapshots to multimodal models.
6. Cross-incident pattern mining ("third time this node", "always after
   deploys of X") — emerges from the knowledge base once it has volume.

## 9. Status of this repository

A working prototype (Python) lives here: it demonstrated the full loop —
webhook → correlation → collectors (Kubernetes, Prometheus, Loki, Grafana
snapshots) → AI analysis with enforced citations → web notebook with the
complete engineer review workflow → DOCX/PDF export → learning loop — with a
green test suite at its best point. It is currently **mid-refactor and not
runnable** (a database migration and collector removal were half-applied),
and **nothing is committed to git**. Treat it as a reference implementation
of the requirements above, not as the foundation: the next conversation
should propose the proper stack and architecture first, then decide what to
reuse.

## 10. Kickoff prompt for the next conversation

> Read `docs/PROJECT_BRIEF.md` end-to-end. You are building valaf properly,
> from this specification. First propose the technology stack and
> architecture (with your reasoning, honoring §3 product rules and §5
> non-functional requirements — PostgreSQL is already decided), and confirm
> the v1 scope against §4 including authentication. After agreement,
> implement incrementally with tests, verifying against a running instance
> as you go. The existing Python code is a reference prototype only — reuse
> ideas or code from it deliberately, not by default. The logo asset must be
> kept as-is. Acceptance is §7.
