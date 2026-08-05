package summarystore

import (
	"context"
	"testing"
	"time"
)

// TestUpsert_NilPoolIsError — nil pool must not panic; must return error.
// This is the cheap safety net that lets unit tests construct Store{} and
// verify other code paths without needing a real DB.
func TestUpsert_NilPoolIsError(t *testing.T) {
	var s *Store
	if err := s.Upsert(context.Background(), Summary{SessionKey: "gw_x"}); err == nil {
		t.Fatal("expected error from nil Store")
	}
	s2 := &Store{}
	if err := s2.Upsert(context.Background(), Summary{SessionKey: "gw_x"}); err == nil {
		t.Fatal("expected error from Store with nil pool")
	}
}

// TestLastSummarized_NilPoolIsError — same nil-safety contract for the
// reader path. Real DB integration tests live under tests/db_integration/
// and require a running pg-252-pg17 container.
func TestLastSummarized_NilPoolIsError(t *testing.T) {
	s := &Store{}
	if _, err := s.LastSummarized(context.Background(), "gw_x"); err == nil {
		t.Fatal("expected error from nil-pool LastSummarized")
	}
}

// TestCountNewTurns_NilPoolIsError — same nil-safety for the rolling gate.
func TestCountNewTurns_NilPoolIsError(t *testing.T) {
	s := &Store{}
	if _, err := s.CountNewTurns(context.Background(), "gw_x", time.Now()); err == nil {
		t.Fatal("expected error from nil-pool CountNewTurns")
	}
}

// TestSummary_ZeroValueValid — Summary{} should be a usable zero value so
// generators can build one incrementally without pre-declaring every field.
func TestSummary_ZeroValueValid(t *testing.T) {
	var s Summary
	if s.SessionKey != "" || s.Title != "" || s.LastSummarized != (time.Time{}) {
		t.Fatalf("zero Summary should be all-empty, got %+v", s)
	}
}