-- Background job queue. Lives in Postgres (no external broker) and is drained by
-- the worker role via SELECT ... FOR UPDATE SKIP LOCKED. A new incident enqueues
-- a 'collect' job; the worker gathers evidence for it.
CREATE TYPE job_status AS ENUM ('pending', 'running', 'done', 'failed');

CREATE TABLE jobs (
    id           bigserial PRIMARY KEY,
    kind         text NOT NULL,             -- 'collect'
    incident_id  uuid REFERENCES incidents(id) ON DELETE CASCADE,
    payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
    status       job_status NOT NULL DEFAULT 'pending',
    attempts     integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 3,
    run_at       timestamptz NOT NULL DEFAULT now(),
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Partial index makes dequeue cheap: only pending jobs, ordered by run_at.
CREATE INDEX idx_jobs_dequeue ON jobs (run_at) WHERE status = 'pending';
