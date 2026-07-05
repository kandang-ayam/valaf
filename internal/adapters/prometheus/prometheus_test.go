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

func TestBuildQueries(t *testing.T) {
	if got := buildQueries(map[string]string{"host": "h:9100"}); len(got) != 2 {
		t.Errorf("host bag → %d queries, want 2", len(got))
	}
	if got := buildQueries(nil); len(got) != 1 || got[0].expr != "up" {
		t.Errorf("empty bag should fall back to a single up query, got %+v", got)
	}
}
