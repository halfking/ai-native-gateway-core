package admin

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

func matchedTestConfig(id, name, typ, pattern string) sanitizerules.Config {
	return sanitizerules.Config{Rules: []sanitizerules.Rule{{
		ID: id, Name: name, Type: typ, Kind: "pattern",
		Pattern: pattern, Group: 1, Scopes: []string{"input"},
		Action: "mask", Enabled: true, Priority: 10,
		Samples: []sanitizerules.Sample{
			{Text: "password=alpha", Match: true},
			{Text: "ordinary text", Match: false},
		},
	}}}
}

func TestEnrichSanitizeRuleEventsAttachesMatchedRule(t *testing.T) {
	store := sanitizerules.NewMemoryStore()
	ctx := context.Background()
	cfg := matchedTestConfig("secret", "Secret", "secret", `password=(\w+)`)
	if _, err := store.Publish(ctx, 0, cfg, "admin", "seed", 0); err != nil {
		t.Fatalf("publish: %v", err)
	}
	ver, err := store.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ev := sanitizerules.Event{
		TenantID: "t1", RuleID: "secret", Version: ver.ID,
		Phase: "input", Action: "mask", Type: "secret", Field: "pattern",
	}
	if err := store.RecordEvent(ctx, ev, 100); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListEvents(ctx, sanitizerules.EventFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %v n=%d", err, len(rows))
	}
	views := enrichSanitizeRuleEvents(ctx, store, rows)
	if len(views) != 1 || views[0].MatchedRule == nil {
		t.Fatalf("expected matched_rule: %+v", views)
	}
	mr := views[0].MatchedRule
	if mr.Missing || mr.Pattern != `password=(\w+)` || mr.Name != "Secret" {
		t.Fatalf("matched_rule wrong: %+v", mr)
	}
	if len(mr.Samples) < 1 {
		t.Fatalf("samples missing: %+v", mr.Samples)
	}
	if views[0].Type != "secret" || views[0].Field != "pattern" {
		t.Fatalf("type/field not on view: %+v", views[0].Event)
	}
}

func TestEnrichSanitizeRuleEventsMarksMissingRule(t *testing.T) {
	store := sanitizerules.NewMemoryStore()
	ctx := context.Background()
	cfg := matchedTestConfig("keep", "Keep", "secret", `password=(\w+)`)
	if _, err := store.Publish(ctx, 0, cfg, "admin", "seed", 0); err != nil {
		t.Fatalf("publish: %v", err)
	}
	ver, _ := store.Current(ctx)
	_ = store.RecordEvent(ctx, sanitizerules.Event{
		TenantID: "t1", RuleID: "gone", Version: ver.ID,
		Phase: "output", Action: "log", Type: "custom", Field: "text",
	}, 100)
	rows, _ := store.ListEvents(ctx, sanitizerules.EventFilter{})
	views := enrichSanitizeRuleEvents(ctx, store, rows)
	if len(views) != 1 || views[0].MatchedRule == nil || !views[0].MatchedRule.Missing {
		t.Fatalf("want missing matched_rule, got %+v", views)
	}
}
