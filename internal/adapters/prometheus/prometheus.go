// Package prometheus is a read-only metrics collector. It picks range queries
// relevant to the alert (a disk alert pulls disk usage, not CPU), scopes them to
// the affected host, and stores a compact per-series SUMMARY (min/max/avg/last +
// a small trend) rather than the raw matrix — the exact query is kept in the
// request so any engineer can re-run it for full resolution. One collector covers
// the ecosystem: Prometheus, Thanos, Mimir, VictoriaMetrics speak the same API.
package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
	queries := buildQueries(target)
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

// buildQueries chooses PromQL relevant to the alert and scopes it to the host.
// The category is inferred from the alertname/title (disk / memory / cpu), so a
// NodeDiskUsageHigh alert gathers filesystem usage — not unrelated CPU/memory.
func buildQueries(t core.CollectTarget) []query {
	host := firstNonEmpty(t.EntityBag["host"], t.EntityBag["instance"], t.EntityBag["nodename"])
	if host == "" {
		// Nothing to scope to; a fleet-wide liveness snapshot is still context.
		return []query{{"targets_up", `up`}}
	}
	sel := fmt.Sprintf(`instance="%s"`, escapeLabel(host))

	var qs []query
	switch classify(append(append([]string{}, t.Alertnames...), t.Title)) {
	case "disk":
		qs = append(qs, query{"disk_used_percent", fmt.Sprintf(
			`100 - (node_filesystem_avail_bytes{%s,fstype!~"tmpfs|overlay|squashfs|ramfs"} / node_filesystem_size_bytes{%s,fstype!~"tmpfs|overlay|squashfs|ramfs"} * 100)`, sel, sel)})
	case "memory":
		qs = append(qs, query{"mem_used_percent", fmt.Sprintf(
			`100 * (1 - node_memory_MemAvailable_bytes{%s} / node_memory_MemTotal_bytes{%s})`, sel, sel)})
	case "cpu":
		qs = append(qs, query{"cpu_busy_percent", fmt.Sprintf(
			`100 - (avg(rate(node_cpu_seconds_total{mode="idle",%s}[5m])) * 100)`, sel)})
	default:
		// Unknown alert type: a general host-health snapshot.
		qs = append(qs,
			query{"cpu_busy_percent", fmt.Sprintf(`100 - (avg(rate(node_cpu_seconds_total{mode="idle",%s}[5m])) * 100)`, sel)},
			query{"mem_used_percent", fmt.Sprintf(`100 * (1 - node_memory_MemAvailable_bytes{%s} / node_memory_MemTotal_bytes{%s})`, sel, sel)},
		)
	}
	// Always confirm the target was actually up during the window. Scope by
	// instance, not job — job label conventions vary too much to hardcode.
	qs = append(qs, query{"target_up", fmt.Sprintf(`up{%s}`, sel)})
	return qs
}

// classify maps alert hints onto a metric category via keyword match.
func classify(hints []string) string {
	j := strings.ToLower(strings.Join(hints, " "))
	switch {
	case containsAny(j, "disk", "filesystem", "inode", "storage", "volume"):
		return "disk"
	case containsAny(j, "memory", "mem", "oom", "swap"):
		return "memory"
	case containsAny(j, "cpu", "load", "throttl"):
		return "cpu"
	default:
		return "general"
	}
}

// promResp / promData mirror the query_range response envelope.
type promResp struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  string          `json:"error"`
}

type promData struct {
	ResultType string      `json:"resultType"`
	Result     []rawSeries `json:"result"`
}

type rawSeries struct {
	Metric map[string]string   `json:"metric"`
	Values [][]json.RawMessage `json:"values"` // each: [ <ts number>, "<value string>" ]
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
	item.Result = summarize(q.name, data)
	return item
}

// ---- summarization ----

type seriesSummary struct {
	Labels map[string]string `json:"labels,omitempty"`
	Points int               `json:"points"`
	Min    float64           `json:"min"`
	Max    float64           `json:"max"`
	Avg    float64           `json:"avg"`
	First  float64           `json:"first"`
	Last   float64           `json:"last"`
	Trend  []float64         `json:"trend,omitempty"` // downsampled shape
}

type metricSummary struct {
	Metric string          `json:"metric"`
	Note   string          `json:"note"`
	Series []seriesSummary `json:"series"`
}

const (
	maxSeries   = 25
	trendPoints = 16
)

// summarize condenses a range matrix into per-series stats. The full raw points
// are intentionally not stored; the request's exact query reproduces them.
func summarize(name string, data promData) json.RawMessage {
	out := metricSummary{
		Metric: name,
		Note:   "summary of a range query; re-run request.query for the raw series",
	}
	for i, rs := range data.Result {
		if i >= maxSeries {
			break
		}
		vals := parseFloats(rs.Values)
		if len(vals) == 0 {
			continue
		}
		s := seriesSummary{Labels: trimLabels(rs.Metric), Points: len(vals),
			First: vals[0], Last: vals[len(vals)-1], Min: vals[0], Max: vals[0]}
		sum := 0.0
		for _, v := range vals {
			if v < s.Min {
				s.Min = v
			}
			if v > s.Max {
				s.Max = v
			}
			sum += v
		}
		s.Avg = round2(sum / float64(len(vals)))
		s.Min, s.Max, s.First, s.Last = round2(s.Min), round2(s.Max), round2(s.First), round2(s.Last)
		s.Trend = downsample(vals, trendPoints)
		out.Series = append(out.Series, s)
	}
	b, _ := json.Marshal(out)
	return b
}

func parseFloats(values [][]json.RawMessage) []float64 {
	out := make([]float64, 0, len(values))
	for _, pair := range values {
		if len(pair) != 2 {
			continue
		}
		var s string
		if err := json.Unmarshal(pair[1], &s); err != nil {
			continue
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) {
			out = append(out, f)
		}
	}
	return out
}

func downsample(vals []float64, n int) []float64 {
	if len(vals) <= n {
		out := make([]float64, len(vals))
		for i, v := range vals {
			out[i] = round2(v)
		}
		return out
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = round2(vals[i*(len(vals)-1)/(n-1)])
	}
	return out
}

// trimLabels drops noisy internal labels so the summary reads cleanly.
func trimLabels(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch k {
		case "__name__", "service_instance_id", "deployment_environment_name":
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func fail(item core.EvidenceItem, msg string) core.EvidenceItem {
	item.Status = core.EvidenceFailed
	item.Error = msg
	return item
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func formatTime(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

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
