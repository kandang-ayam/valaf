-- Alertmanager repeats a firing group on its repeat_interval; the same alert
-- (same fingerprint) must not create duplicate rows within an incident. This
-- unique index backs the ON CONFLICT upsert in the intake repository.
CREATE UNIQUE INDEX uq_alerts_incident_fingerprint ON alerts (incident_id, fingerprint);
