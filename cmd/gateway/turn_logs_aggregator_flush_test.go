package main

// turn_logs_aggregator_flush_test.go —
// 2026-09-27 critical audit (§17 F-9 / F-10). Two real defects in the
// aggregate-and-flush path, both silent:
//
//	F-9  `SET turn_logs_summary = $1::jsonb` replaced the whole column. Since
//	     the flush then deletes the rows it read, a session with continuing
//	     traffic is re-polled on the next tick with only its *new* rows and
//	     wipes every earlier turn's entry. The stored summary degenerated to
//	     "whatever the last 5-minute batch contained" instead of the session's
//	     turn history — and admin/session_detail_v2.go serves that column
//	     straight to API consumers.
//	F-10 The delete re-derived `… AND expires_at > NOW()` as a fresh
//	     statement, so rows written between the SELECT and the DELETE were
//	     deleted without ever being aggregated.
//
// The SQL is asserted as text because pgxmock does not execute SQL and cannot
// observe merge or row-identity semantics. That is only defensible if the
// assertions provably fail when the defect is reintroduced — verified by
// mutation (remove the merge / remove the id-scoped delete) in the commit
// message and handoff §17.

import (
	"strings"
	"testing"
)

func TestAggregateAndFlush_MergesSummaryInsteadOfOverwriting(t *testing.T) {
	upd := strings.ToUpper(flushMergeQuery)

	if !strings.Contains(upd, "TURN_LOGS_SUMMARY = COALESCE(TURN_LOGS_SUMMARY, '{}'::JSONB) || $1::JSONB") {
		t.Error("flush must MERGE into turn_logs_summary (COALESCE(...) || $1::jsonb) — a plain SET replaces the column and destroys earlier turns for any session with continuing traffic")
	}
	// The COALESCE is not decoration: in SQL `NULL || x` is NULL, so without
	// it a session's very first flush would store NULL.
	if !strings.Contains(upd, "COALESCE(") {
		t.Error("merge must COALESCE the existing column — `NULL || x` evaluates to NULL in SQL, so the first flush would store NULL")
	}
}

func TestAggregateAndFlush_DeletesExactlyWhatItAggregated(t *testing.T) {
	del := strings.ToUpper(flushDeleteQuery)

	// id-scoped delete: the read set, not a re-derived predicate.
	if !strings.Contains(del, "WHERE ID = ANY($1)") {
		t.Error("flush must delete by the captured primary keys (WHERE id = ANY($1)) — a re-derived predicate also matches rows written after the SELECT and silently drops unaggregated stages")
	}
	if strings.Contains(del, "EXPIRES_AT > NOW()") {
		t.Error("flush delete must not re-derive an expiry predicate — that was the F-10 race, where rows written between SELECT and DELETE were dropped unaggregated")
	}
}

func TestAggregateAndFlush_SelectCapturesPrimaryKey(t *testing.T) {
	sel := strings.ToUpper(flushSelectQuery)
	// The delete is only correct if the read actually captures id.
	if !strings.Contains(sel, "SELECT ID, TURN_NO, STAGE, STAGE_STATUS") {
		t.Error("flush SELECT must capture `id` — the id-scoped delete has nothing to key on otherwise")
	}
	// The read is still scoped to the tenant and to unexpired rows; only the
	// DELETE moved to id-keying.
	if !strings.Contains(sel, "WHERE TENANT_ID=$1 AND SESSION_ID=$2 AND EXPIRES_AT > NOW()") {
		t.Error("flush SELECT must stay scoped by tenant_id/session_id and to unexpired rows (RLS + TTL contract)")
	}
}
