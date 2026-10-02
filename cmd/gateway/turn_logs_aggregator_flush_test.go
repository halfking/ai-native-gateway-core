package main

// turn_logs_aggregator_flush_test.go —
// 2026-09-27 critical audit (§17 F-9 / F-10) + R72 audit round. The defect
// history of the aggregate-and-flush path, each fixed by the shape pinned
// here:
//
//	F-9  `SET turn_logs_summary = $1::jsonb` replaced the whole column: a
//	     session with continuing traffic was re-polled with only its *new*
//	     rows and wiped every earlier turn's entry.
//	     → superseded by a COALESCE…||$1 SQL merge (same day), and then by
//	     the R72 design below.
//	R72  the SQL `||` merge only merges top-level keys, so it replaced the
//	     whole `turn_N` entry. A tick landing inside the writer's per-stage
//	     INSERT loop (session_writer_v2.go) aggregated the first half of a
//	     turn's rows; the next tick aggregated the rest; the second merge
//	     overwrote `turn_N` with only the later rows — early stages lost.
//	     Same hole, dual-instance form: a stale read set merging after a
//	     richer one regressed the turn to the subset. The flush now runs in
//	     one transaction that locks the sessions row (FOR UPDATE) and merges
//	     in Go with a per-turn stages-array union + dedup
//	     (mergeSummaries), tested below as behavior, not text.
//	F-10 The delete re-derived `… AND expires_at > NOW()` as a fresh
//	     statement, so rows written between the SELECT and the DELETE were
//	     deleted without ever being aggregated. → id-keyed delete, pinned
//	     as text because pgxmock does not execute SQL.
//
// The SQL text assertions are only defensible because they provably fail when
// the defect is reintroduced — verified by mutation (remove the lock / remove
// the id-scoped delete) in the R72 round.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func stageAt(stage string, at time.Time) StageLog {
	return StageLog{Stage: stage, Status: "success", LatencyMs: 1, StartedAt: at}
}

func TestMergeSummaries_CrossBatchTurnStagesUnion(t *testing.T) {
	// R72 case ①: tick 1 aggregated the first half of turn 5 (the writer's
	// per-stage INSERT loop had not finished); tick 2 must APPEND the rest,
	// not replace the turn entry.
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	existing := map[string]LogSummary{
		"turn_5": {TurnNo: 5, BuiltAt: t0, Stages: []StageLog{stageAt("retrieve", t0), stageAt("generate", t0.Add(time.Second))}},
	}
	raw, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(5 * time.Minute)
	merged := mergeSummaries(raw, map[int][]StageLog{
		5: {stageAt("grade", t1), stageAt("persist", t1.Add(time.Second))},
	}, t1)

	got := merged["turn_5"]
	if len(got.Stages) != 4 {
		t.Fatalf("turn_5 stages = %d, want 4 (union of both flush batches); got %+v", len(got.Stages), got.Stages)
	}
	names := []string{got.Stages[0].Stage, got.Stages[1].Stage, got.Stages[2].Stage, got.Stages[3].Stage}
	want := []string{"retrieve", "generate", "grade", "persist"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("stage order = %v, want %v (sorted by started_at)", names, want)
		}
	}
}

func TestMergeSummaries_StaleSubsetDoesNotRegressTurn(t *testing.T) {
	// R72 case ②: a second flush whose read set is a strict subset (rows the
	// richer flush already deleted) must not shrink the stored turn entry.
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	existing := map[string]LogSummary{
		"turn_3": {TurnNo: 3, BuiltAt: t0, Stages: []StageLog{
			stageAt("retrieve", t0), stageAt("generate", t0.Add(time.Second)), stageAt("grade", t0.Add(2 * time.Second)),
		}},
	}
	raw, _ := json.Marshal(existing)
	merged := mergeSummaries(raw, map[int][]StageLog{
		3: {stageAt("retrieve", t0), stageAt("generate", t0.Add(time.Second))},
	}, t0.Add(time.Minute))

	if got := merged["turn_3"]; len(got.Stages) != 3 {
		t.Fatalf("turn_3 stages = %d, want 3 — a stale subset flush must not drop stages the richer flush stored", len(got.Stages))
	}
}

func TestMergeSummaries_DedupsExactDuplicates(t *testing.T) {
	// Crash between UPDATE and DELETE re-reads the same rows next tick; the
	// re-merge must stay idempotent instead of duplicating stage entries.
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	one := []StageLog{stageAt("retrieve", t0)}
	existing := map[string]LogSummary{"turn_1": {TurnNo: 1, BuiltAt: t0, Stages: one}}
	raw, _ := json.Marshal(existing)
	merged := mergeSummaries(raw, map[int][]StageLog{1: one}, t0.Add(time.Minute))

	if got := merged["turn_1"]; len(got.Stages) != 1 {
		t.Fatalf("turn_1 stages = %d, want 1 — identical re-flushed rows must dedup", len(got.Stages))
	}
}

func TestMergeSummaries_PreservesOtherTurns(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	existing := map[string]LogSummary{
		"turn_1": {TurnNo: 1, BuiltAt: t0, Stages: []StageLog{stageAt("retrieve", t0)}},
	}
	raw, _ := json.Marshal(existing)
	merged := mergeSummaries(raw, map[int][]StageLog{2: {stageAt("generate", t0.Add(time.Minute))}}, t0.Add(2 * time.Minute))

	if got := merged["turn_1"]; len(got.Stages) != 1 {
		t.Fatalf("turn_1 stages = %d, want 1 — turn keys absent from the payload must be carried over", len(got.Stages))
	}
	if got := merged["turn_2"]; len(got.Stages) != 1 || got.TurnNo != 2 {
		t.Fatalf("turn_2 = %+v, want the freshly aggregated entry", got)
	}
}

func TestMergeSummaries_NullAndGarbageExistingFallBackToPayload(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	fresh := map[int][]StageLog{1: {stageAt("retrieve", t0)}}

	// NULL column (session's very first flush): the pre-R72 COALESCE case.
	merged := mergeSummaries(nil, fresh, t0)
	if got := merged["turn_1"]; len(got.Stages) != 1 {
		t.Fatalf("turn_1 stages = %d, want 1 (first flush over NULL must store the payload)", len(got.Stages))
	}

	// Non-summary JSON in the column (a string value cannot unmarshal into a
	// LogSummary, so the whole parse fails): payload wins rather than failing
	// the flush forever.
	merged = mergeSummaries([]byte(`{"not":"a summary map"}`), fresh, t0)
	if got := merged["turn_1"]; len(got.Stages) != 1 {
		t.Fatalf("turn_1 stages = %d, want 1 (unparseable existing value must not wedge the flush)", len(got.Stages))
	}
}

func TestMergeSummaries_DedupIdentitySurvivesJSONRoundTrip(t *testing.T) {
	// The dedup identity must not rely on time.Time ==: a stage entry read
	// back from the stored summary has been through a JSON round-trip, and
	// location pointers do not survive it. Round-trip the existing value
	// through jsonb-shaped bytes and confirm an identical re-flush dedups.
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	one := []StageLog{stageAt("retrieve", t0)}
	existing := map[string]LogSummary{"turn_1": {TurnNo: 1, BuiltAt: t0, Stages: one}}
	raw, _ := json.Marshal(existing)
	// Simulate pgx returning jsonb bytes: identical content, fresh parse.
	var echo map[string]LogSummary
	if err := json.Unmarshal(raw, &echo); err != nil {
		t.Fatal(err)
	}
	raw2, _ := json.Marshal(echo)
	merged := mergeSummaries(raw2, map[int][]StageLog{1: one}, t0.Add(time.Minute))
	if got := merged["turn_1"]; len(got.Stages) != 1 {
		t.Fatalf("turn_1 stages = %d, want 1 — dedup identity must survive the JSON round-trip", len(got.Stages))
	}
}

func TestAggregateAndFlush_LocksSessionsRowAndMergesInGo(t *testing.T) {
	lock := strings.ToUpper(flushLockQuery)
	// The whole-column write is only safe under the sessions row lock: two
	// concurrent flushes doing read-modify-write without it would lose the
	// loser's payload (its rows are already deleted).
	if !strings.Contains(lock, "FOR UPDATE") {
		t.Error("flush must lock the sessions row (FOR UPDATE) — without it two flushes race their read-modify-write and the loser's stages are already deleted")
	}
	if !strings.Contains(lock, "TURN_LOGS_SUMMARY") {
		t.Error("flush lock statement must read the current turn_logs_summary — the Go merge builds on the locked row's value")
	}

	upd := strings.ToUpper(flushUpdateQuery)
	// Full replacement is the correct shape here BECAUSE the value written is
	// the Go-side merge of the locked row's current value with this flush's
	// rows; a blind replacement (pre-F-9) or an SQL per-key merge (its R72
	// hole) are both wrong.
	if !strings.Contains(upd, "SET TURN_LOGS_SUMMARY = $1::JSONB") {
		t.Error("flush update must write the merged value as a whole (SET turn_logs_summary = $1::jsonb)")
	}
	if strings.Contains(upd, "||") {
		t.Error("flush update must not re-introduce the SQL || merge — shallow per-key replacement is the R72 per-turn stage-loss defect")
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
