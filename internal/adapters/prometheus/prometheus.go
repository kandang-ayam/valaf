// Package prometheus is a read-only metrics collector. It runs a small set of
// range queries derived from the incident's entity bag against the Prometheus
// HTTP API and classifies each into ok (has data), gap (ran but empty), or
// failed (errored/unreachable). One collector covers the ecosystem — Thanos,
// Mimir, VictoriaMetrics and SigNoz all speak the same query API.
package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/valaf/valaf/internal/core"
)

type Collector struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Collector {
	return &Collector{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Collector) Kind() string { return "prometheus" }

func (c *Collector) Collect(ctx context.Context, target core.CollectTarget) []core.EvidenceItem {
	queries := buildQueries(target.EntityBag)
	items := make([]core.EvidenceItem, 0, len(queries))
	for _, q := range queries {
		items = append(items, c.runRange(ctx, q, target.Window))
	}
	return items
}

type query struct {
	name string
	expr string
}

// buildQueries derives PromQL from the entity bag using node-exporter / target
// conventions. Empty results become gaps, so over-asking is safe.
func buildQueries(bag map[string]string) []query {
	var qs []query
	if host := bag["host"]; host != "" {
		h := escapeLabel(host)
		qs = append(qs,
			query{"cpu_busy_percent", fmt.Sprintf(`100 - (avg(rate(node_cpu_seconds_total{mode="idle",instance="%s"}[5m])) * 100)`, h)},
			query{"mem_used_percent", fmt.Sprintf(`100 * (1 - node_memory_MemAvailable_bytes{instance="%s"} / node_memory_MemTotal_bytes{instance="%s"})`, h, h)},
		)
	}
	if svc := bag["service"]; svc != "" {
		qs = append(qs, query{"target_up", fmt.Sprintf(`up{job="%s"}`, escapeLabel(svc))})
	}
	if len(qs) == 0 {
		// Nothing to key on; a fleet-wide up snapshot is still useful context.
		qs = append(qs, query{"targets_up", `up`})
	}
	return qs
}

// promResp / promData mirror the query_range response envelope.
type promResp struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  string          `json:"error"`
}

type promData struct {
	ResultType string            `json:"resultType"`
	Result     []json.RawMessage `json:"result"`
}

func (c *Collector) runRange(ctx context.Context, q query, w core.TimeWindow) core.EvidenceItem {
	step := w.Step
	if step <= 0 {
		step = 30 * time.Second
	}
	params := url.Values{
		"query": {q.expr},
		"start": {formatTime(w.Start)},
		"end":   {formatTime(w.End)},
		"step":  {strconv.Itoa(int(step.Seconds())) + "s"},
	}

	// The request record is what makes the evidence reproducible by hand.
	request, _ := json.Marshal(map[string]any{
		"collector": "prometheus",
		"name":      q.name,
		"endpoint":  "/api/v1/query_range",
		"query":     q.expr,
		"start":     formatTime(w.Start),
		"end":       formatTime(w.End),
		"step":      params.Get("step"),
	})
	item := core.EvidenceItem{Collector: "prometheus", Kind: "metric", Request: request}

	endpoint := c.baseURL + "/api/v1/query_range?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fail(item, err.Error())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fail(item, err.Error())
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return fail(item, fmt.Sprintf("prometheus http %d: %s", resp.StatusCode, truncate(body, 200)))
	}

	var pr promResp
	if err := json.Unmarshal(body, &pr); err != nil {
		return fail(item, "decode response: "+err.Error())
	}
	if pr.Status != "success" {
		return fail(item, "prometheus error: "+pr.Error)
	}

	var data promData
	if err := json.Unmarshal(pr.Data, &data); err != nil {
		return fail(item, "decode data: "+err.Error())
	}
	if len(data.Result) == 0 {
		item.Status = core.EvidenceGap
		item.Error = "no data for query"
		return item
	}
	item.Status = core.EvidenceOK
	item.Result = pr.Data
	return item
}

func fail(item core.EvidenceItem, msg string) core.EvidenceItem {
	item.Status = core.EvidenceFailed
	item.Error = msg
	return item
}

func formatTime(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	return strings.ReplaceAll(v, `"`, `\"`)
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
