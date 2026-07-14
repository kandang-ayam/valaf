package grafana

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/valaf/valaf/internal/blob"
	"github.com/valaf/valaf/internal/core"
)

func TestResolvePanel(t *testing.T) {
	// Grafana-managed alert labels.
	if ref, ok := resolvePanel(map[string]string{"__dashboardUid__": "abc", "__panelId__": "12"}); !ok || ref.DashboardUID != "abc" || ref.PanelID != "12" {
		t.Fatalf("grafana labels: %+v ok=%v", ref, ok)
	}
	// Custom annotation keys.
	if ref, ok := resolvePanel(map[string]string{"dashboard_uid": "d1", "panel_id": "7"}); !ok || ref.DashboardUID != "d1" || ref.PanelID != "7" {
		t.Fatalf("custom keys: %+v ok=%v", ref, ok)
	}
	// Full panel URL.
	if ref, ok := resolvePanel(map[string]string{"grafana_panel_url": "https://g.example/d/uidX/disk?viewPanel=9&orgId=1"}); !ok || ref.DashboardUID != "uidX" || ref.PanelID != "9" {
		t.Fatalf("url parse: %+v ok=%v", ref, ok)
	}
	// Nothing usable.
	if _, ok := resolvePanel(map[string]string{"severity": "high"}); ok {
		t.Fatal("expected no panel ref")
	}
}

func target(ann map[string]string) core.CollectTarget {
	return core.CollectTarget{
		IncidentID:  "inc-1",
		Annotations: ann,
		Window:      core.TimeWindow{Start: time.Now().Add(-time.Hour), End: time.Now(), Step: 30 * time.Second},
	}
}

func TestCollect_NoRefGap(t *testing.T) {
	store, _ := blob.New(blob.Config{LocalDir: t.TempDir()})
	items := New("http://g", "tok", store).Collect(context.Background(), target(map[string]string{"severity": "high"}))
	if len(items) != 1 || items[0].Status != core.EvidenceGap {
		t.Fatalf("no ref should yield one gap item, got %+v", items)
	}
}

func TestCollect_RendersAndStores(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "PNGDATA")
	}))
	defer srv.Close()

	bs, _ := blob.New(blob.Config{LocalDir: t.TempDir()})
	items := New(srv.URL, "sekret", bs).Collect(context.Background(),
		target(map[string]string{"__dashboardUid__": "abc", "__panelId__": "12"}))

	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Status != core.EvidenceOK {
		t.Fatalf("status = %q (%s)", it.Status, it.Error)
	}
	if it.Kind != "dashboard" {
		t.Errorf("kind = %q", it.Kind)
	}
	if it.Attachment == nil || it.Attachment.MimeType != "image/png" || it.Attachment.SizeBytes != 7 {
		t.Fatalf("attachment wrong: %+v", it.Attachment)
	}
	if it.Attachment.Checksum == "" {
		t.Error("attachment missing checksum")
	}
	if gotAuth != "Bearer sekret" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if !strings.Contains(gotPath, "/render/d-solo/abc/") {
		t.Errorf("render path = %q", gotPath)
	}
	if !strings.Contains(string(it.Result), "view_url") {
		t.Errorf("result should carry a Grafana view_url: %s", it.Result)
	}
	// The PNG must actually be in the blob store.
	rc, err := bs.Open(context.Background(), it.Attachment.StorageKey)
	if err != nil {
		t.Fatalf("blob open: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "PNGDATA" {
		t.Errorf("stored bytes = %q", b)
	}
}

func TestCollect_RendererMissing(t *testing.T) {
	// image-renderer not installed → Grafana returns non-image → failed, not ok.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html>renderer unavailable</html>")
	}))
	defer srv.Close()

	bs, _ := blob.New(blob.Config{LocalDir: t.TempDir()})
	items := New(srv.URL, "t", bs).Collect(context.Background(),
		target(map[string]string{"__dashboardUid__": "abc", "__panelId__": "12"}))
	if items[0].Status != core.EvidenceFailed || !strings.Contains(items[0].Error, "image-renderer") {
		t.Fatalf("expected failed w/ renderer hint, got %+v", items[0])
	}
}
