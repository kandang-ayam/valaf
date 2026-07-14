package blob

import (
	"context"
	"io"
	"testing"
)

func TestLocalStore_RoundTrip(t *testing.T) {
	s, err := New(Config{LocalDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != "local" {
		t.Fatalf("backend = %q, want local", s.Backend())
	}

	ctx := context.Background()
	want := []byte("PNG-BYTES")
	if err := s.Put(ctx, "snapshots/abc/panel.png", "image/png", want); err != nil {
		t.Fatal(err)
	}

	rc, err := s.Open(ctx, "snapshots/abc/panel.png")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != string(want) {
		t.Fatalf("read %q, want %q", got, want)
	}

	if err := s.Delete(ctx, "snapshots/abc/panel.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "snapshots/abc/panel.png"); err == nil {
		t.Fatal("expected error opening deleted object")
	}
	// Delete is idempotent.
	if err := s.Delete(ctx, "snapshots/abc/panel.png"); err != nil {
		t.Fatalf("delete of missing object should be nil, got %v", err)
	}
}

func TestLocalStore_RejectsTraversal(t *testing.T) {
	s, _ := New(Config{LocalDir: t.TempDir()})
	if err := s.Put(context.Background(), "../../etc/evil", "text/plain", []byte("x")); err == nil {
		t.Fatal("path traversal should be rejected")
	}
}

func TestNew_DefaultsToLocal(t *testing.T) {
	s, err := New(Config{LocalDir: t.TempDir()})
	if err != nil || s.Backend() != "local" {
		t.Fatalf("expected local backend, got %v / %v", s, err)
	}
}
