package sanitizerules

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUserPublishCannotSkipBaseline(t *testing.T) {
	m, _ := NewManager(NewMemoryStore(), testConfig(), Options{})
	if _, err := m.Publish(context.Background(), 0, testConfig(), "admin", "skip"); !errors.Is(err, ErrConflict) {
		t.Fatalf("baseline bypass: %v", err)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Current().Version != 1 {
		t.Fatal("baseline missing")
	}
}
func TestSampleChecksAllEffectiveCaptures(t *testing.T) {
	cfg := testConfig()
	cfg.Rules[0].Pattern = `skip|password=(\w+)`
	cfg.Rules[0].Samples = []Sample{{Text: "skip password=secret", Match: true}}
	if err := Validate(cfg); err != nil {
		t.Fatalf("later capture ignored: %v", err)
	}
}
func TestEventStructuralSummaryAndAggregation(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	e := Event{TenantID: "a", RuleID: "pair", Version: 1, Phase: "input", Action: "mask", Type: "secret", Field: "credential_pair"}
	if err := s.RecordEvent(ctx, e, 100); err != nil {
		t.Fatal(err)
	}
	e.Field = "password"
	_ = s.RecordEvent(ctx, e, 100)
	rows, _ := s.ListEvents(ctx, EventFilter{})
	if len(rows) != 2 {
		t.Fatal("different structures collapsed")
	}
	if !strings.Contains(rows[0].SampleMasked, "secret") {
		t.Fatal("no structural evidence")
	}
	e.Type = "raw-password"
	e.Field = "my-raw-secret"
	_ = s.RecordEvent(ctx, e, 100)
	rows, _ = s.ListEvents(ctx, EventFilter{})
	for _, r := range rows {
		if strings.Contains(r.SampleMasked, "raw-") {
			t.Fatal("untrusted field leaked")
		}
	}
}
