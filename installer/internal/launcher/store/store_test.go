package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlanSaveLoad(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	now := time.Now().UTC().Truncate(time.Second)
	p := &Plan{
		ID:        "plan-001",
		CreatedAt: now,
		State:     StatePrepared,
		Current:   Release{Version: "v1.4.2"},
		Target:    Release{Version: "v1.5.0", DownloadURL: "http://x/v1.5.0", SHA256: "abc"},
		BlueAddr:  "127.0.0.1:8782",
		GreenAddr: "127.0.0.1:8783",
		History:   []StateEvent{{State: StateNotified, At: now}},
	}
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	// File on disk
	if _, err := os.Stat(filepath.Join(dir, "plans", "plan-001.json")); err != nil {
		t.Fatalf("plan file not written: %v", err)
	}

	got, err := s.Load("plan-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StatePrepared || got.Target.Version != "v1.5.0" {
		t.Fatalf("loaded plan mismatch: %+v", got)
	}
}

func TestPlanList(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	for _, id := range []string{"a", "b", "c"} {
		_ = s.Save(&Plan{ID: id, State: StateDone})
	}
	ids, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 plans, got %d", len(ids))
	}
}

func TestActivePointer(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	// Initially empty
	ap, err := s.LoadActive()
	if err != nil {
		t.Fatal(err)
	}
	if ap != nil {
		t.Fatalf("expected nil active, got %+v", ap)
	}
	// Save
	if err := s.SaveActive(&ActivePointer{Addr: "127.0.0.1:8783", Version: "v1.5.0"}); err != nil {
		t.Fatal(err)
	}
	ap2, err := s.LoadActive()
	if err != nil {
		t.Fatal(err)
	}
	if ap2 == nil || ap2.Addr != "127.0.0.1:8783" || ap2.Version != "v1.5.0" {
		t.Fatalf("active mismatch: %+v", ap2)
	}
}
