// Package grafana is a read-only dashboard-snapshot collector. When an alert
// carries a Grafana panel reference (Grafana's __dashboardUid__/__panelId__ or a
// grafana_panel_url annotation), it renders that panel to a PNG via Grafana's
// image-renderer, stores it in the blob store, and records it as dashboard
// evidence with an attachment plus a deep "view in Grafana" link.
package grafana

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/valaf/valaf/internal/blob"
	"github.com/valaf/valaf/internal/core"
)

type Collector struct {
	baseURL string
	token   string
	blob    blob.Store
	http    *http.Client
}

// renderTimeout bounds one panel render. A cold image-renderer (Chromium
// startup + dashboard load) can take well over the default collector time-box.
const renderTimeout = 60 * time.Second

func New(baseURL, token string, store blob.Store) *Collector {
	return &Collector{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		blob:    store,
		http:    &http.Client{Timeout: renderTimeout},
	}
}

func (c *Collector) Kind() string { return "grafana" }

// TimeoutHint asks the collection service for a render-sized time-box.
func (c *Collector) TimeoutHint() time.Duration { return renderTimeout }

func (c *Collector) Collect(ctx context.Context, target core.CollectTarget) []core.EvidenceItem {
	ref, ok := resolvePanel(target.Annotations)
	if !ok {
		// No panel reference on the alert — an honest, self-describing gap.
		return []core.EvidenceItem{{
			Collector: "grafana", Kind: "dashboard", Status: core.EvidenceGap,
			Request: mustJSON(map[string]any{"collector": "grafana", "note": "looked for __dashboardUid__/__panelId__ or grafana_panel_url"}),
			Error:   "no Grafana panel reference found in alert labels/annotations",
		}}
	}
	return []core.EvidenceItem{c.renderPanel(ctx, ref, target)}
}

type panelRef struct {
	DashboardUID string
	PanelID      string
	OrgID        string
	Vars         url.Values // dashboard template variables (var-*), passed through
}

func (c *Collector) renderPanel(ctx context.Context, ref panelRef, target core.CollectTarget) core.EvidenceItem {
	fromMS := strconv.FormatInt(target.Window.Start.UnixMilli(), 10)
	toMS := strconv.FormatInt(target.Window.End.UnixMilli(), 10)

	q := url.Values{
		"panelId": {ref.PanelID},
		"from":    {fromMS},
		"to":      {toMS},
		"width":   {"1000"},
		"height":  {"500"},
		"tz":      {"UTC"},
	}
	if ref.OrgID != "" {
		q.Set("orgId", ref.OrgID)
	}
	// Dashboard template variables (var-node=ipdn, …) scope the panel to the
	// alerting entity; without them Grafana renders the dashboard's defaults.
	for k, vals := range ref.Vars {
		for _, v := range vals {
			q.Add(k, v)
		}
	}
	renderURL := fmt.Sprintf("%s/render/d-solo/%s/_?%s", c.baseURL, url.PathEscape(ref.DashboardUID), q.Encode())

	vq := url.Values{"viewPanel": {ref.PanelID}, "from": {fromMS}, "to": {toMS}}
	for k, vals := range ref.Vars {
		for _, v := range vals {
			vq.Add(k, v)
		}
	}
	viewURL := fmt.Sprintf("%s/d/%s?%s", c.baseURL, url.PathEscape(ref.DashboardUID), vq.Encode())

	request := mustJSON(map[string]any{
		"collector":     "grafana",
		"dashboard_uid": ref.DashboardUID,
		"panel_id":      ref.PanelID,
		"render_url":    renderURL,
		"from":          fromMS,
		"to":            toMS,
	})
	item := core.EvidenceItem{Collector: "grafana", Kind: "dashboard", Request: request}

	png, err := c.fetchPNG(ctx, renderURL)
	if err != nil {
		item.Status = core.EvidenceFailed
		item.Error = err.Error()
		return item
	}

	key := fmt.Sprintf("snapshots/%s/%s-%s.png", target.IncidentID, ref.DashboardUID, ref.PanelID)
	if err := c.blob.Put(ctx, key, "image/png", png); err != nil {
		item.Status = core.EvidenceFailed
		item.Error = "store snapshot: " + err.Error()
		return item
	}

	sum := sha256.Sum256(png)
	item.Status = core.EvidenceOK
	item.Result = mustJSON(map[string]any{
		"dashboard_uid": ref.DashboardUID,
		"panel_id":      ref.PanelID,
		"view_url":      viewURL,
		"image":         "attachment",
	})
	item.Attachment = &core.Attachment{
		StorageBackend: c.blob.Backend(),
		StorageKey:     key,
		MimeType:       "image/png",
		SizeBytes:      int64(len(png)),
		Checksum:       hex.EncodeToString(sum[:]),
	}
	return item
}

func (c *Collector) fetchPNG(ctx context.Context, renderURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, renderURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("grafana render http %d: %s", resp.StatusCode, truncate(body, 200))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		// image-renderer plugin missing usually returns HTML/JSON, not an image.
		return nil, fmt.Errorf("grafana render returned %q, not an image (is the image-renderer installed?)", ct)
	}
	return body, nil
}

// resolvePanel extracts a dashboard/panel reference from alert labels+annotations.
// It supports Grafana-managed alert labels and a plain grafana_panel_url annotation.
func resolvePanel(a map[string]string) (panelRef, bool) {
	uid := firstNonEmpty(a, "__dashboardUid__", "dashboard_uid", "grafana_dashboard_uid", "dashboardUID")
	pid := firstNonEmpty(a, "__panelId__", "panel_id", "grafana_panel_id", "panelId")
	org := firstNonEmpty(a, "__orgId__", "org_id")

	if uid != "" && pid != "" {
		return panelRef{DashboardUID: uid, PanelID: pid, OrgID: org}, true
	}
	// Fall back to a full panel URL, e.g. https://grafana/d/UID/slug?viewPanel=12
	if raw := firstNonEmpty(a, "grafana_panel_url", "dashboardURL", "__dashboard_url__"); raw != "" {
		if ref, ok := parsePanelURL(raw); ok {
			return ref, true
		}
	}
	return panelRef{}, false
}

func parsePanelURL(raw string) (panelRef, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return panelRef{}, false
	}
	// Path like /d/{uid}/{slug}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	var uid string
	for i, p := range parts {
		if p == "d" && i+1 < len(parts) {
			uid = parts[i+1]
			break
		}
	}
	pid := u.Query().Get("viewPanel")
	if pid == "" {
		pid = u.Query().Get("panelId")
	}
	// Grafana 11+ writes viewPanel=panel-152; the render API wants the bare id.
	pid = strings.TrimPrefix(pid, "panel-")
	if uid == "" || pid == "" {
		return panelRef{}, false
	}

	// Keep the dashboard template variables so the render targets the same
	// entity the alert is about (var-node=ipdn, var-job=…, …).
	vars := url.Values{}
	for k, vals := range u.Query() {
		if strings.HasPrefix(k, "var-") {
			for _, v := range vals {
				vars.Add(k, v)
			}
		}
	}
	return panelRef{DashboardUID: uid, PanelID: pid, OrgID: u.Query().Get("orgId"), Vars: vars}, true
}

func firstNonEmpty(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
