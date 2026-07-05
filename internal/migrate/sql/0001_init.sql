-- valaf — initial schema (v0.2)
-- The notebook is the `incidents` aggregate. Evidence and audit_log are
-- immutable (enforced by triggers). Binary snapshots live in a blob store; only
-- their key is stored. Exports are rendered on demand, never persisted — no table.
-- The migrator wraps this file in a single transaction.

CREATE EXTENSION IF NOT EXISTS pgcrypto;   -- gen_random_uuid()

-- ============================================================
-- Enums
-- ============================================================
CREATE TYPE auth_source        AS ENUM ('local', 'proxy', 'oidc');
CREATE TYPE user_role          AS ENUM ('viewer', 'engineer', 'admin');
CREATE TYPE incident_status    AS ENUM ('open', 'analyzing', 'published', 'resolved', 'false_positive', 'deleted');
CREATE TYPE severity_level     AS ENUM ('high', 'critical');
CREATE TYPE triage_verdict     AS ENUM ('actionable', 'likely_noise', 'unknown');
CREATE TYPE alert_source       AS ENUM ('alertmanager', 'grafana', 'datadog', 'newrelic', 'generic');
CREATE TYPE collector_type     AS ENUM ('prometheus', 'loki', 'elasticsearch', 'grafana', 'kubernetes');
CREATE TYPE evidence_kind      AS ENUM ('metric', 'log', 'dashboard', 'orch');
CREATE TYPE evidence_status    AS ENUM ('ok', 'gap', 'failed');
CREATE TYPE analysis_provider  AS ENUM ('anthropic', 'openai_compat');
CREATE TYPE analysis_status    AS ENUM ('ok', 'failed', 'skipped');
CREATE TYPE hypothesis_verdict AS ENUM ('none', 'confirmed', 'rejected');
CREATE TYPE evidence_relation  AS ENUM ('supporting', 'contradicting');
CREATE TYPE notification_status AS ENUM ('sent', 'quiet', 'digest', 'failed');
CREATE TYPE notification_reason AS ENUM ('actionable', 'noise');
CREATE TYPE storage_backend    AS ENUM ('local', 's3');   -- s3 = any S3-compatible (MinIO/Ceph/S3)
CREATE TYPE intake_auth_method AS ENUM ('shared_token', 'hmac');

-- ============================================================
-- Auth
-- ============================================================
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username      text NOT NULL UNIQUE,
    email         text,
    password_hash text,                       -- null for proxy/oidc identities
    auth_source   auth_source NOT NULL DEFAULT 'local',
    external_id   text,                        -- oidc subject / proxy identity
    role          user_role   NOT NULL DEFAULT 'viewer',
    is_active     boolean     NOT NULL DEFAULT true,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_users_external
    ON users(auth_source, external_id) WHERE external_id IS NOT NULL;

CREATE TABLE sessions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  text NOT NULL UNIQUE,
    csrf_secret text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL
);
CREATE INDEX idx_sessions_user    ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

-- Per-source webhook credentials (admin-managed, rotatable). Secrets are hashed.
CREATE TABLE intake_sources (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL UNIQUE,
    source_type  alert_source NOT NULL,
    auth_method  intake_auth_method NOT NULL DEFAULT 'shared_token',
    token_hash   text,                        -- for shared_token
    hmac_secret  text,                        -- for hmac-signed webhooks
    is_active    boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz,
    CONSTRAINT intake_secret_presence CHECK (
        (auth_method = 'shared_token' AND token_hash  IS NOT NULL) OR
        (auth_method = 'hmac'         AND hmac_secret IS NOT NULL)
    )
);

-- Admin-editable runtime config (severity threshold, correlation window, flapping
-- limits, notification routing). Infra/role bindings stay in valaf.yaml.
CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- ============================================================
-- Core: the notebook aggregate
-- ============================================================
CREATE TABLE incidents (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title          text NOT NULL,
    status         incident_status NOT NULL DEFAULT 'open',
    severity       severity_level  NOT NULL,
    grouping_key   text NOT NULL,             -- correlation key: one outage = one row
    entity_bag     jsonb NOT NULL DEFAULT '{}'::jsonb,  -- service/host/pod/env
    triage_verdict triage_verdict,
    assigned_to    uuid REFERENCES users(id) ON DELETE SET NULL,
    notified       boolean NOT NULL DEFAULT false,
    opened_at      timestamptz NOT NULL DEFAULT now(),
    published_at   timestamptz,
    resolved_at    timestamptz,
    deleted_at     timestamptz,               -- soft-delete; admin purge hard-deletes
    search_vector  tsvector GENERATED ALWAYS AS (to_tsvector('english', title)) STORED
);
CREATE INDEX idx_incidents_status     ON incidents(status);
CREATE INDEX idx_incidents_severity   ON incidents(severity);
CREATE INDEX idx_incidents_grouping   ON incidents(grouping_key);
CREATE INDEX idx_incidents_opened     ON incidents(opened_at DESC);
CREATE INDEX idx_incidents_assigned   ON incidents(assigned_to);
CREATE INDEX idx_incidents_entity_bag ON incidents USING gin (entity_bag);
CREATE INDEX idx_incidents_search     ON incidents USING gin (search_vector);

CREATE TABLE alerts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id  uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    source       alert_source NOT NULL,
    fingerprint  text NOT NULL,
    severity     text NOT NULL,
    labels       jsonb NOT NULL DEFAULT '{}'::jsonb,
    annotations  jsonb NOT NULL DEFAULT '{}'::jsonb,
    raw_payload  jsonb NOT NULL,
    starts_at    timestamptz,
    ends_at      timestamptz,
    received_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_alerts_incident    ON alerts(incident_id);
CREATE INDEX idx_alerts_fingerprint ON alerts(fingerprint);

-- Immutable audit trail. Only the flag columns may change (see trigger below).
CREATE TABLE evidence_items (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id     uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    collector       collector_type NOT NULL,
    kind            evidence_kind  NOT NULL,
    request         jsonb NOT NULL,           -- exact, reproducible query/request
    result          jsonb,                    -- null when status <> 'ok'
    status          evidence_status NOT NULL,
    error           text,                     -- populated on 'failed'/'gap'
    is_valid        boolean NOT NULL DEFAULT true,
    invalid_comment text,
    flagged_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    flagged_at      timestamptz,
    captured_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT evidence_result_presence
        CHECK ((status = 'ok') = (result IS NOT NULL)),
    CONSTRAINT evidence_flag_consistency
        CHECK (is_valid OR invalid_comment IS NOT NULL)
);
CREATE INDEX idx_evidence_incident ON evidence_items(incident_id);
CREATE INDEX idx_evidence_status   ON evidence_items(status);

-- Bytes live in a blob store (local disk or S3-compatible), never in Postgres.
-- The row is a pointer: backend + key + integrity metadata.
CREATE TABLE attachments (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    evidence_item_id uuid NOT NULL REFERENCES evidence_items(id) ON DELETE CASCADE,
    storage_backend  storage_backend NOT NULL DEFAULT 'local',
    storage_key      text NOT NULL,           -- local: relative path · s3: object key
    mime_type        text NOT NULL,
    size_bytes       bigint,
    checksum         text,                    -- sha-256; integrity + dedup
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_attachments_evidence ON attachments(evidence_item_id);

-- ============================================================
-- Analysis (may be re-run; is_current flags the live one)
-- ============================================================
CREATE TABLE analyses (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id    uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    provider       analysis_provider NOT NULL,
    model          text NOT NULL,
    status         analysis_status NOT NULL,
    summary        text,
    timeline       jsonb NOT NULL DEFAULT '[]'::jsonb,
    gaps           jsonb NOT NULL DEFAULT '[]'::jsonb,   -- AI-declared "wanted but missing"
    triage_verdict triage_verdict,
    is_current     boolean NOT NULL DEFAULT true,
    error          text,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_analyses_incident ON analyses(incident_id);
CREATE UNIQUE INDEX uq_analyses_current
    ON analyses(incident_id) WHERE is_current;

CREATE TABLE observations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_id uuid NOT NULL REFERENCES analyses(id) ON DELETE CASCADE,
    body        text NOT NULL,
    ordinal     integer NOT NULL DEFAULT 0
);
CREATE INDEX idx_observations_analysis ON observations(analysis_id);

CREATE TABLE hypotheses (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_id      uuid NOT NULL REFERENCES analyses(id) ON DELETE CASCADE,
    rank             integer NOT NULL,        -- ranked, never a confidence %
    title            text NOT NULL,
    rationale        text,
    suggested_checks jsonb NOT NULL DEFAULT '[]'::jsonb,  -- read-only verification steps
    verdict          hypothesis_verdict NOT NULL DEFAULT 'none',
    verdict_note     text,
    verdict_by       uuid REFERENCES users(id) ON DELETE SET NULL,
    verdict_at       timestamptz
);
CREATE INDEX idx_hypotheses_analysis ON hypotheses(analysis_id);

-- Citations: every claim links to real evidence (fabricated refs cannot exist).
CREATE TABLE observation_citations (
    observation_id   uuid NOT NULL REFERENCES observations(id)   ON DELETE CASCADE,
    evidence_item_id uuid NOT NULL REFERENCES evidence_items(id) ON DELETE CASCADE,
    PRIMARY KEY (observation_id, evidence_item_id)
);

CREATE TABLE hypothesis_evidence (
    hypothesis_id    uuid NOT NULL REFERENCES hypotheses(id)     ON DELETE CASCADE,
    evidence_item_id uuid NOT NULL REFERENCES evidence_items(id) ON DELETE CASCADE,
    relation         evidence_relation NOT NULL,
    PRIMARY KEY (hypothesis_id, evidence_item_id, relation)
);

-- Learning provenance: which past resolved incidents fed this analysis (auditable).
CREATE TABLE analysis_similar_incidents (
    analysis_id        uuid NOT NULL REFERENCES analyses(id)  ON DELETE CASCADE,
    source_incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    overlap            jsonb NOT NULL DEFAULT '{}'::jsonb,
    score              double precision,
    PRIMARY KEY (analysis_id, source_incident_id)
);

-- ============================================================
-- Output
-- ============================================================
CREATE TABLE resolutions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id       uuid NOT NULL UNIQUE REFERENCES incidents(id) ON DELETE CASCADE,
    root_cause        text NOT NULL,
    actions_taken     text,
    ultimate_solution text,
    notes             text,
    resolved_by       uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    resolved_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notifications (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    channel     text NOT NULL,
    target      text,
    status      notification_status NOT NULL,
    reason      notification_reason NOT NULL,
    sent_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_notifications_incident ON notifications(incident_id);

-- ============================================================
-- Audit log — append-only, survives a purged notebook
-- ============================================================
CREATE TABLE audit_log (
    id          bigserial PRIMARY KEY,
    actor_id    uuid REFERENCES users(id) ON DELETE SET NULL,  -- null for system
    action      text NOT NULL,
    entity_type text NOT NULL,               -- polymorphic ref (no FK by design)
    entity_id   text NOT NULL,
    details     jsonb NOT NULL DEFAULT '{}'::jsonb,
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_entity ON audit_log(entity_type, entity_id);
CREATE INDEX idx_audit_actor  ON audit_log(actor_id);
CREATE INDEX idx_audit_at     ON audit_log(at DESC);

-- ============================================================
-- Immutability guards
-- ============================================================

-- evidence_items: only the flag columns may be updated.
CREATE FUNCTION guard_evidence_immutable() RETURNS trigger AS $$
BEGIN
    IF (NEW.incident_id, NEW.collector, NEW.kind, NEW.request,
        NEW.result, NEW.status, NEW.error, NEW.captured_at)
       IS DISTINCT FROM
       (OLD.incident_id, OLD.collector, OLD.kind, OLD.request,
        OLD.result, OLD.status, OLD.error, OLD.captured_at)
    THEN
        RAISE EXCEPTION 'evidence_items is immutable; only is_valid/invalid_comment/flagged_by/flagged_at may change';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_evidence_immutable
    BEFORE UPDATE ON evidence_items
    FOR EACH ROW EXECUTE FUNCTION guard_evidence_immutable();

-- audit_log: append-only (no UPDATE, no DELETE).
CREATE FUNCTION guard_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_audit_append_only
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION guard_append_only();
