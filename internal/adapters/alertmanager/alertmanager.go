// Package alertmanager normalizes Prometheus Alertmanager webhook payloads
// (schema version 4) into a core.AlertBatch. Alertmanager already groups related
// alerts, so one webhook == one incident: its groupKey is our grouping key.
package alertmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/valaf/valaf/internal/core"
)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (Adapter) Source() core.SourceType { return core.SourceAlertmanager }

// webhook mirrors the Alertmanager v4 webhook envelope.
type webhook struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	Alerts            []amAlert         `json:"alerts"`
}

type amAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

func (a Adapter) Parse(body []byte) (core.AlertBatch, error) {
	var wh webhook
	if err := json.Unmarshal(body, &wh); err != nil {
		return core.AlertBatch{}, fmt.Errorf("decode alertmanager webhook: %w", err)
	}
	if len(wh.Alerts) == 0 {
		return core.AlertBatch{}, errors.New("alertmanager webhook has no alerts")
	}

	groupKey := wh.GroupKey
	if groupKey == "" {
		groupKey = deriveKey(wh.GroupLabels)
	}

	batch := core.AlertBatch{
		Source:      core.SourceAlertmanager,
		GroupingKey: groupKey,
		Title:       title(wh),
		EntityBag:   entities(merge(wh.CommonLabels, wh.GroupLabels)),
		Alerts:      make([]core.Alert, 0, len(wh.Alerts)),
	}

	for _, al := range wh.Alerts {
		raw, _ := json.Marshal(al)
		fp := al.Fingerprint
		if fp == "" {
			fp = deriveKey(al.Labels) // Alertmanager always sends one; be safe
		}
		batch.Alerts = append(batch.Alerts, core.Alert{
			Fingerprint: fp,
			Severity:    al.Labels["severity"],
			Status:      al.Status,
			Labels:      al.Labels,
			Annotations: al.Annotations,
			StartsAt:    nonZero(al.StartsAt),
			EndsAt:      nonZero(al.EndsAt),
			Raw:         raw,
		})
	}
	return batch, nil
}

// title prefers the alert name, then a common summary, then the group key.
func title(wh webhook) string {
	if v := wh.GroupLabels["alertname"]; v != "" {
		return v
	}
	if v := wh.CommonLabels["alertname"]; v != "" {
		return v
	}
	if v := wh.CommonAnnotations["summary"]; v != "" {
		return v
	}
	if wh.GroupKey != "" {
		return wh.GroupKey
	}
	return "incident"
}

// entities extracts the thin entity bag from labels, trying common conventions.
func entities(labels map[string]string) map[string]string {
	pick := map[string][]string{
		"service": {"service", "service_name", "job", "app"},
		"host":    {"instance", "host", "hostname", "nodename", "node"},
		"pod":     {"pod", "pod_name"},
		"env":     {"env", "environment", "deployment_environment"},
	}
	bag := map[string]string{}
	for key, candidates := range pick {
		for _, c := range candidates {
			if v := labels[c]; v != "" {
				bag[key] = v
				break
			}
		}
	}
	return bag
}

func merge(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b { // group labels win over common labels
		out[k] = v
	}
	return out
}

// deriveKey builds a stable key from sorted label pairs (fallback only).
func deriveKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte(',')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func nonZero(t time.Time) *time.Time {
	if t.IsZero() || t.Year() <= 1 {
		return nil
	}
	return &t
}
