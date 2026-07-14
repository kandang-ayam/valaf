package prometheus

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/valaf/valaf/internal/core"
)

func mockProm(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case strings.Contains(q, "fail"):
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "boom")
		case strings.Contains(q, "empty"):
			io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
		default:
			io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"x"},"values":[[1,"1"]]}]}}`)
		}
	}))
}

func window() core.TimeWindow {
	return core.TimeWindow{Start: time.Now().Add(-time.Hour), End: time.Now(), Step: 30 * time.Second}
}

func TestRunRange_OK(t *testing.T) {
	srv := mockProm(t)
	defer srv.Close()

	it := New(srv.URL).runRange(context.Background(), query{"m", "node_cpu"}, window())
	if it.Status != core.EvidenceOK {
		t.Fatalf("status = %q, want ok", it.Status)
	}
	if len(it.Result) == 0 {
		t.Fatal("ok item must carry a result")
	}
	if len(it.Request) == 0 {
		t.Fatal("item must record the reproducible request")
	}
}

func TestRunRange_Gap(t *testing.T) {
	srv := mockProm(t)
	defer srv.Close()

	it := New(srv.URL).runRange(context.Background(), query{"m", "empty_query"}, window())
	if it.Status != core.EvidenceGap {
		t.Fatalf("status = %q, want gap", it.Status)
	}
	if len(it.Result) != 0 {
		t.Fatal("gap item must not carry a result (DB CHECK)")
	}
	if it.Error == "" {
		t.Fatal("gap should be self-describing")
	}
}

func TestRunRange_Failed(t *testing.T) {
	srv := mockProm(t)
	defer srv.Close()

	it := New(srv.URL).runRange(context.Background(), query{"m", "fail_query"}, window())
	if it.Status != core.EvidenceFailed {
		t.Fatalf("status = %q, want failed", it.Status)
	}
	if it.Error == "" {
		t.Fatal("failed capture must record the error")
	}
}

func TestRunRange_Unreachable(t *testing.T) {
	// Nothing listening → connection error → failed, not a panic or stuck call.
	it := New("http://127.0.0.1:1").runRange(context.Background(), query{"m", "node_cpu"}, window())
	if it.Status != core.EvidenceFailed {
		t.Fatalf("status = %q, want failed", it.Status)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"NodeDiskUsageHigh": "disk",
		"FilesystemFull":    "disk",
		"HighMemoryUsage":   "memory",
		"OOMKilled":         "memory",
		"CPUThrottlingHigh": "cpu",
		"TargetDown":        "general",
	}
	for name, want := range cases {
		if got := classify([]string{name}); got != want {
			t.Errorf("classify(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBuildQueries_AlertAware(t *testing.T) {
	// A disk alert must query the filesystem, scoped to the host — not CPU/memory.
	qs := buildQueries(core.CollectTarget{
		Title:      "NodeDiskUsageHigh",
		Alertnames: []string{"NodeDiskUsageHigh"},
		EntityBag:  map[string]string{"host": "ipdn"},
	})
	var joined string
	for _, q := range qs {
		joined += q.expr + "\n"
	}
	if !strings.Contains(joined, "node_filesystem_avail_bytes") {
		t.Errorf("disk alert should query filesystem, got:\n%s", joined)
	}
	if strings.Contains(joined, "node_cpu_seconds_total") || strings.Contains(joined, "MemAvailable") {
		t.Errorf("disk alert should NOT query cpu/memory, got:\n%s", joined)
	}
	if !strings.Contains(joined, `instance="ipdn"`) {
		t.Errorf("queries should be scoped to the host, got:\n%s", joined)
	}
	if !strings.Contains(joined, "up{") {
		t.Errorf("should always include a liveness (up) query, got:\n%s", joined)
	}
}

func TestBuildQueries_NoHostFallback(t *testing.T) {
	qs := buildQueries(core.CollectTarget{})
	if len(qs) != 1 || qs[0].expr != "up" {
		t.Errorf("no host → single fleet up query, got %+v", qs)
	}
}

func TestSummarize(t *testing.T) {
	// 5 points: 10,20,30,40,50 with a mountpoint label.
	raw := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"__name__":"x","mountpoint":"/"},"values":[[1,"10"],[2,"20"],[3,"30"],[4,"40"],[5,"50"]]}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, raw)
	}))
	defer srv.Close()

	it := New(srv.URL).runRange(context.Background(), query{"disk_used_percent", "q"}, window())
	if it.Status != core.EvidenceOK {
		t.Fatalf("status = %q", it.Status)
	}
	got := string(it.Result)
	// Compact summary, not a raw 5-point matrix dump.
	for _, want := range []string{`"min":10`, `"max":50`, `"avg":30`, `"first":10`, `"last":50`, `"points":5`, `"mountpoint":"/"`} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q in: %s", want, got)
		}
	}
	if strings.Contains(got, "__name__") {
		t.Errorf("summary should drop internal labels, got: %s", got)
	}
}
