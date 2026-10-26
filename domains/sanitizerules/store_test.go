package sanitizerules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryStorePublishCASAndHistory(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	v, err := s.Current(ctx)
	if err != nil || v.ID != 0 {
		t.Fatal(v, err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, err := s.Publish(ctx, 0, testConfig(), "admin", "initial", 0)
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("unexpected: %v", err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("CAS winners", wins.Load())
	}
	v, _ = s.Current(ctx)
	v.Config.Rules[0].Name = "mutated"
	stored, _ := s.GetVersion(ctx, 1)
	if stored.Config.Rules[0].Name == "mutated" {
		t.Fatal("history mutated")
	}
	all, err := s.ListVersions(ctx, 50)
	if err != nil || len(all) != 1 {
		t.Fatal(all, err)
	}
}
func TestEventsAggregateTenantReviewAndNeverPersistCallerSample(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	secret := "raw-password-secret"
	e := Event{TenantID: "tenant-a", RuleID: "secret", Version: 1, Phase: "input", Action: "mask", Type: "secret", Field: "password", SampleMasked: secret}
	if err := s.RecordEvent(ctx, e, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEvent(ctx, e, 2); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListEvents(ctx, EventFilter{TenantID: "tenant-a", Limit: 20})
	if err != nil || len(rows) != 1 || rows[0].Count != 2 {
		t.Fatal(rows, err)
	}
	serialized, _ := json.Marshal(rows)
	if strings.Contains(string(serialized), secret) {
		t.Fatal("caller sample persisted")
	}
	if rows[0].SampleMasked == "" {
		t.Fatal("missing generated summary")
	}
	if _, err = s.ReviewEvent(ctx, "tenant-b", rows[0].ID, "false_positive", "reviewer", "checked"); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-tenant review", err)
	}
	reviewed, err := s.ReviewEvent(ctx, "tenant-a", rows[0].ID, "false_positive", "reviewer", "checked")
	if err != nil || reviewed.Reviewer != "reviewer" || reviewed.ReviewedAt == nil {
		t.Fatal(reviewed, err)
	}
	if _, err = s.ReviewEvent(ctx, "tenant-a", rows[0].ID, "allow_forever", "reviewer", ""); err == nil {
		t.Fatal("unknown review accepted")
	}
	e.TenantID = "tenant-b"
	_ = s.RecordEvent(ctx, e, 2)
	e.RuleID = "another"
	_ = s.RecordEvent(ctx, e, 2)
	all, _ := s.ListEvents(ctx, EventFilter{Limit: 20})
	if len(all) != 2 {
		t.Fatal("event bound not enforced", len(all))
	}
	// Reviewing an aggregate is sticky when later identical hits arrive.
	e.TenantID = "tenant-b"
	e.RuleID = "secret"
	_ = s.RecordEvent(ctx, e, 2)
	rows, _ = s.ListEvents(ctx, EventFilter{TenantID: "tenant-b", Limit: 20})
	if len(rows) == 0 {
		t.Fatal("missing tenant event")
	}
}
func TestInstanceAndInvalidEvent(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()
	i := Instance{InstanceID: "instance-a", Version: 3, LastSeen: now, LastRefresh: now}
	if err := s.Heartbeat(ctx, i); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListInstances(ctx)
	if len(all) != 1 || all[0].Version != 3 {
		t.Fatal(all)
	}
	if err := s.RecordEvent(ctx, Event{RuleID: "password=secret", Phase: "input", Action: "mask"}, 100); err == nil {
		t.Fatal("unsafe identifier accepted")
	}
}

func TestReviewedEventsAreLinkedInPublicationTransaction(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.RecordEvent(ctx, Event{TenantID: "a", RuleID: "secret", Version: 1, Phase: "input", Action: "mask"}, 100)
	rows, _ := s.ListEvents(ctx, EventFilter{TenantID: "a"})
	id := rows[0].ID
	if _, err := s.Publish(ctx, 0, testConfig(), "admin", "pending", 0, ReviewLink{ID: id, TenantID: "a"}); err == nil {
		t.Fatal("pending event linked")
	}
	head, _ := s.Current(ctx)
	if head.ID != 0 {
		t.Fatal("failed publication advanced head")
	}
	_, _ = s.ReviewEvent(ctx, "a", id, "false_positive", "reviewer", "scoped")
	if _, err := s.Publish(ctx, 0, testConfig(), "admin", "wrong tenant", 0, ReviewLink{ID: id, TenantID: "b"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross tenant link", err)
	}
	v, err := s.Publish(ctx, 0, testConfig(), "admin", "fix", 0, ReviewLink{ID: id, TenantID: "a"})
	if err != nil || len(v.ReviewLinks) != 1 {
		t.Fatal(v, err)
	}
	rows, _ = s.ListEvents(ctx, EventFilter{TenantID: "a"})
	if rows[0].ResolvedVersion != v.ID {
		t.Fatal("missing resolution", rows)
	}
	_ = s.RecordEvent(ctx, Event{TenantID: "a", RuleID: "secret", Version: 2, Phase: "input", Action: "mask"}, 100)
	rows, _ = s.ListEvents(ctx, EventFilter{TenantID: "a"})
	if len(rows) != 2 || rows[0].Status != "pending" {
		t.Fatal("new version inherited review", rows)
	}
}

func TestEventAggregateReviewRemainsSticky(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	e := Event{TenantID: "a", RuleID: "secret", Version: 1, Phase: "input", Action: "mask"}
	_ = s.RecordEvent(ctx, e, 10)
	rows, _ := s.ListEvents(ctx, EventFilter{TenantID: "a"})
	_, _ = s.ReviewEvent(ctx, "a", rows[0].ID, "ignored", "admin", "irrelevant")
	_ = s.RecordEvent(ctx, e, 10)
	rows, _ = s.ListEvents(ctx, EventFilter{TenantID: "a"})
	if rows[0].Count != 2 || rows[0].Status != "ignored" {
		t.Fatal(rows)
	}
}
func TestEventRetentionKeepsRecentlyAggregatedEntry(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	e := Event{RuleID: "one", Phase: "input", Action: "mask"}
	_ = s.RecordEvent(ctx, e, 2)
	e.RuleID = "two"
	_ = s.RecordEvent(ctx, e, 2)
	e.RuleID = "one"
	_ = s.RecordEvent(ctx, e, 2)
	e.RuleID = "three"
	_ = s.RecordEvent(ctx, e, 2)
	rows, _ := s.ListEvents(ctx, EventFilter{})
	found := false
	for _, row := range rows {
		if row.RuleID == "one" {
			found = true
		}
	}
	if !found {
		t.Fatal("recent aggregate pruned by creation order")
	}
}
