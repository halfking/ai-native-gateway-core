package fsstore

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// tempStore builds a Store rooted at a temp dir; caller is
// responsible for the store.Close().
func tempStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "fs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	s, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, root
}

func TestPutAndGetEntity(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()

	e := Entity{
		ID:   "openai",
		Kind: KindProvider,
		Payload: map[string]interface{}{
			"name":     "OpenAI",
			"endpoint": "https://api.openai.com",
		},
	}
	if err := s.PutEntity(e); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
	got, err := s.GetEntity(KindProvider, "openai")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.ID != "openai" {
		t.Errorf("id = %q, want openai", got.ID)
	}
	if got.Payload["name"] != "OpenAI" {
		t.Errorf("payload name = %v, want OpenAI", got.Payload["name"])
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt should be set by PutEntity")
	}
}

func TestGetEntityMissing(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()
	if _, err := s.GetEntity(KindProvider, "nope"); !os.IsNotExist(err) {
		t.Errorf("err = %v, want os.ErrNotExist", err)
	}
}

func TestListEntities(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()

	for _, id := range []string{"openai", "anthropic", "google"} {
		if err := s.PutEntity(Entity{ID: id, Kind: KindProvider, Payload: map[string]interface{}{}}); err != nil {
			t.Fatalf("PutEntity %s: %v", id, err)
		}
	}
	ids, err := s.ListEntities(KindProvider)
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	want := []string{"anthropic", "google", "openai"}
	if len(ids) != len(want) {
		t.Fatalf("len(ids) = %d, want %d", len(ids), len(want))
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
}

func TestConcurrentEntityWrites(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()

	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			e := Entity{
				ID:      "openai",
				Kind:    KindProvider,
				Payload: map[string]interface{}{"i": i},
			}
			if err := s.PutEntity(e); err != nil {
				t.Errorf("PutEntity %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	got, err := s.GetEntity(KindProvider, "openai")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.ID != "openai" {
		t.Errorf("id = %q, want openai", got.ID)
	}
}

func TestRequestRoundtrip(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()

	now := time.Now().UTC().Truncate(time.Second)
	rr := RequestRecord{
		ID:        "req-1",
		StartedAt: now,
		EndedAt:   now.Add(2 * time.Second),
		TenantID:  "acme",
		Status:    "success",
		CostUSD:   0.0123,
	}
	if err := s.PutRequest(rr); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}
	got, err := s.GetRequest("req-1")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.ID != "req-1" {
		t.Errorf("id = %q, want req-1", got.ID)
	}
	if got.TenantID != "acme" {
		t.Errorf("tenant = %q, want acme", got.TenantID)
	}
	if got.Status != "success" {
		t.Errorf("status = %q, want success", got.Status)
	}
	if got.CostUSD != 0.0123 {
		t.Errorf("cost = %v, want 0.0123", got.CostUSD)
	}

	day := now.Format("2006-01-02")
	ids, err := s.ListRequestsInDay(day)
	if err != nil {
		t.Fatalf("ListRequestsInDay: %v", err)
	}
	if len(ids) != 1 || ids[0] != "req-1" {
		t.Errorf("ListRequestsInDay = %v, want [req-1]", ids)
	}
}

func TestBodyPartAppendAndRead(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()

	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		line := []byte(`{"chunk":` + string(rune('0'+i)) + `}`)
		if err := s.PutBodyPart("req-1", now, BodyRequest, line); err != nil {
			t.Fatalf("PutBodyPart %d: %v", i, err)
		}
	}
	lines, err := s.ReadBodyPart("req-1", now, BodyRequest)
	if err != nil {
		t.Fatalf("ReadBodyPart: %v", err)
	}
	if len(lines) != 5 {
		t.Errorf("len(lines) = %d, want 5", len(lines))
	}
}

func TestRebuildIndexes(t *testing.T) {
	s, root := tempStore(t)
	defer s.Close()

	// Seed several entities + requests.
	for _, id := range []string{"openai", "anthropic"} {
		if err := s.PutEntity(Entity{ID: id, Kind: KindProvider}); err != nil {
			t.Fatalf("PutEntity %s: %v", id, err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.PutRequest(RequestRecord{ID: "req-a", StartedAt: now, TenantID: "acme"}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}

	// Close the store, blow away the on-disk indices, reopen, rebuild.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, p := range []string{
		filepath.Join(root, "index", "entities.bleve"),
		filepath.Join(root, "index", "requests.bleve"),
	} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatalf("rm %s: %v", p, err)
		}
	}

	s2, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if err := s2.RebuildIndexes(); err != nil {
		t.Fatalf("RebuildIndexes: %v", err)
	}

	// Entities should round-trip.
	e, err := s2.GetEntity(KindProvider, "openai")
	if err != nil {
		t.Fatalf("GetEntity after rebuild: %v", err)
	}
	if e.ID != "openai" {
		t.Errorf("id = %q, want openai", e.ID)
	}
	// Request should round-trip.
	rr, err := s2.GetRequest("req-a")
	if err != nil {
		t.Fatalf("GetRequest after rebuild: %v", err)
	}
	if rr.TenantID != "acme" {
		t.Errorf("tenant = %q, want acme", rr.TenantID)
	}
}

func TestInvalidKind(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()

	if err := s.PutEntity(Entity{ID: "x", Kind: "bogus"}); err == nil {
		t.Errorf("PutEntity with invalid kind should fail")
	}
	if _, err := s.GetEntity("bogus", "x"); err == nil {
		t.Errorf("GetEntity with invalid kind should fail")
	}
	if _, err := s.ListEntities("bogus"); err == nil {
		t.Errorf("ListEntities with invalid kind should fail")
	}
}
