package core

import (
	"encoding/json"
	"time"
)

// SourceType identifies where an alert came from; values match the alert_source
// enum in the database.
type SourceType string

const (
	SourceAlertmanager SourceType = "alertmanager"
	SourceGrafana      SourceType = "grafana"
	SourceDatadog      SourceType = "datadog"
	SourceNewRelic     SourceType = "newrelic"
	SourceGeneric      SourceType = "generic"
)

// Alert is one normalized alert, source-agnostic.
type Alert struct {
	Fingerprint string
	Severity    string // raw severity label, as sent by the source
	Status      string // "firing" | "resolved"
	Labels      map[string]string
	Annotations map[string]string
	StartsAt    *time.Time
	EndsAt      *time.Time
	Raw         json.RawMessage // the original per-alert payload, stored verbatim
}

// AlertBatch is one correlated group of alerts — the unit that maps to a single
// incident (one outage = one notebook). Adapters produce it; the intake service
// consumes it.
type AlertBatch struct {
	Source      SourceType
	GroupingKey string            // stable key for correlation/dedup
	Title       string            // human title for the incident
	EntityBag   map[string]string // service/host/pod/env, best-effort
	Alerts      []Alert
}

// MaxSeverity returns the highest severity among firing alerts. Resolved alerts
// do not trigger a notebook (resolution is the engineer's call, not the source's).
func (b AlertBatch) MaxSeverity() Severity {
	max := SevUnknown
	for _, a := range b.Alerts {
		if a.Status == "resolved" {
			continue
		}
		if s := ParseSeverity(a.Severity); s > max {
			max = s
		}
	}
	return max
}
