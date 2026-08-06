package summarystore

import (
	"context"
	"testing"
	"time"
)

// TestUpsert_NilPoolIsError — nil pool must not panic; must return error.
// This is the cheap safety net that lets unit tests construct Store{} and
// verify other code paths without needing a real DB.
//
// 2026-08-06: signature changed to (UpsertResult, error). Nil-pool path
// must return a zero UpsertResult alongside the error so callers don't
// dereference nil fields.
func TestUpsert_NilPoolIsError(t *testing.T) {
	var s *Store
	res, err := s.Upsert(context.Background(), Summary{SessionKey: "gw_x"})
	if err == nil {
		t.Fatal("expected error from nil Store")
	}
	if res.Version != 0 || res.Updated {
		t.Fatalf("nil Store Upsert returned %+v, want zero UpsertResult", res)
	}
	s2 := &Store{}
	res2, err2 := s2.Upsert(context.Background(), Summary{SessionKey: "gw_x"})
	if err2 == nil {
		t.Fatal("expected error from Store with nil pool")
	}
	if res2.Version != 0 || res2.Updated {
		t.Fatalf("nil-pool Store Upsert returned %+v, want zero UpsertResult", res2)
	}
}

// TestUpsertResult_ZeroValueValid — UpsertResult{} is the documented
// "no result / error" sentinel. Generators should be able to declare
// one without pre-declaring every field.
func TestUpsertResult_ZeroValueValid(t *testing.T) {
	var r UpsertResult
	if r.Version != 0 || r.Updated {
		t.Fatalf("zero UpsertResult should be all-zero, got %+v", r)
	}
}

// TestLastSummarized_NilPoolIsError — same nil-safety contract for the
// reader path. NOTE: there are no DB integration tests for this package
// in-tree today (no tests/db_integration/ directory exists). The SQL is
// covered by manual deployment verification on 252.
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