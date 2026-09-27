package main

// turn_logs_aggregator_poll_test.go —
// 2026-09-27 critical audit: the aggregator's "which sessions to flush" query
// used to be an inline literal in main.go with no ORDER BY, so it was neither
// deterministic nor testable. These tests pin the two properties that make the
// cut-off safe rather than a silent data-loss path:
//
//	ORDER BY MIN(started_at) — oldest-unflushed-first, i.e. FIFO, so a quiet
//	                          session cannot be crowded out by busier ones.
//	WHERE expires_at > NOW() — only unexpired rows are candidates; anything
//	                          that expires is deleted by the TTL sweep before it
//	                          can ever be aggregated.
//
// The query is asserted as text because pgxmock does not execute SQL and
// cannot observe row ordering. That is only acceptable if the assertion can be
// made to fail, so TestPendingSessions_QueryOrdersOldestFirst is verified by
// mutation in the commit message and in handoff §17.

import (
	"strings"
	"testing"
)

// pendingSessionsQuerySource is the single place the poll query lives. Keeping
// it as a package-level const (rather than inline in the method) is what makes
// it assertable without a database.
func pendingSessionsQuerySource() string {
	return pendingSessionsQuery
}

func TestPendingSessions_QueryOrdersOldestFirst(t *testing.T) {
	q := strings.ToUpper(pendingSessionsQuerySource())

	if !strings.Contains(q, "ORDER BY MIN(STARTED_AT) ASC") {
		t.Error("pending-sessions query must ORDER BY MIN(started_at) ASC — without a total order PostgreSQL returns an arbitrary 100 and quiet sessions get silently crowded out")
	}
	// Tie-breakers: MIN(started_at) alone is not a total order, so sessions
	// sharing a timestamp could still shuffle between ticks.
	if !strings.Contains(q, "TENANT_ID ASC") || !strings.Contains(q, "SESSION_ID ASC") {
		t.Error("pending-sessions query must break ties deterministically (ORDER BY … tenant_id ASC, session_id ASC)")
	}
}

func TestPendingSessions_QueryOnlyUnexpiredRows(t *testing.T) {
	q := strings.ToUpper(pendingSessionsQuerySource())
	if !strings.Contains(q, "WHERE EXPIRES_AT > NOW()") {
		t.Error("pending-sessions query must filter expires_at > NOW() — expired rows are deleted by the TTL sweep before they can be aggregated")
	}
	if !strings.Contains(q, "GROUP BY TENANT_ID, SESSION_ID") {
		t.Error("pending-sessions query must GROUP BY tenant_id, session_id (one flush per session)")
	}
	if !strings.Contains(q, "LIMIT $1") {
		t.Error("pending-sessions query must keep a bounded LIMIT bound to the caller-supplied $1")
	}
}

// TestPendingSessions_LimitIsBounded guards the footgun in the bound
// parameter: a 0 or negative limit is a SQL error at runtime, so it is
// normalised instead of passed through.
func TestPendingSessions_LimitIsBounded(t *testing.T) {
	for _, bad := range []int{0, -1, -100} {
		if got := normalizePendingSessionLimit(bad); got < 1 {
			t.Errorf("normalizePendingSessionLimit(%d) = %d, want >= 1", bad, got)
		}
	}
	if got := normalizePendingSessionLimit(100); got != 100 {
		t.Errorf("normalizePendingSessionLimit(100) = %d, want 100", got)
	}
}
