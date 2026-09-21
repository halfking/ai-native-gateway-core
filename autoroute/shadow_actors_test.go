package autoroute

// shadow_actors_test.go — R37 (2026-09-17) pins for the R35-R2 灰度口径:
// gateway-synthetic rounds (goal-% shadow rounds, internal title/summary
// loopbacks) never enter the outcome registry or the feedback training
// signal, at either end (decision-time stash, terminal report).

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIsSyntheticActor(t *testing.T) {
	cases := []struct {
		actor string
		want  bool
	}{
		{"goal-audit", true},
		{"goal-continue", true},
		{"goal-model-switch", true},
		{"goal-whatever-future", true},
		{"auto-title-generator", true},
		{"auto-summary-generator", true},
		{"session-summary", true},
		{" auto-title-generator ", true}, // tolerate header whitespace
		{"", false},
		{"   ", false},
		{"user-mobile-app", false},
		{"goalkeeper-client", false}, // prefix is "goal-", not "goal"
		{"chrome-ext", false},
	}
	for _, c := range cases {
		if got := IsSyntheticActor(c.actor); got != c.want {
			t.Errorf("IsSyntheticActor(%q) = %v, want %v", c.actor, got, c.want)
		}
	}
}

func TestSQLExcludeSyntheticActors(t *testing.T) {
	bare := SQLExcludeSyntheticActors("")
	for _, want := range []string{
		"COALESCE(origin_actor, '') NOT LIKE 'goal-%'",
		"auto-title-generator", "auto-summary-generator", "session-summary",
		" AND COALESCE", // appendable inside a WHERE clause
	} {
		if !strings.Contains(bare, want) {
			t.Errorf("bare predicate missing %q: %s", want, bare)
		}
	}
	aliased := SQLExcludeSyntheticActors("r2")
	if !strings.Contains(aliased, "COALESCE(r2.origin_actor, '')") {
		t.Errorf("aliased predicate lost the alias: %s", aliased)
	}
}

// TestRecordFeedback_SyntheticRoundNeverStashes: a decision made on behalf
// of a goal-% shadow round writes neither the stash nor the legacy
// placeholder — otherwise the synthetic flow would surface as expired
// (TTL eviction) or matched, polluting the灰度口径.
func TestRecordFeedback_SyntheticRoundNeverStashes(t *testing.T) {
	resetOutcomeRegistry(t)
	d, ranker := newFeedbackTestDecider(t)

	before, _, _, _, _ := outcomeStatsSnapshot()
	ctx := WithOriginActor(WithRequestID(context.Background(), "req-shadow-1"), "goal-audit")
	if _, err := d.Decide(ctx, ClassificationSignals{}, 42, "", "", "sess-shadow"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	ranker.mu.Lock()
	n := len(ranker.feedbacks)
	ranker.mu.Unlock()
	if n != 0 {
		t.Fatalf("synthetic decision must not write feedback, got %d rows", n)
	}
	if stashed, _, _, _, _ := outcomeStatsSnapshot(); stashed != before {
		t.Fatalf("synthetic decision must not stash: stashed %d → %d", before, stashed)
	}

	// Legacy path (no correlation id) is gated by the same predicate.
	ctx2 := WithOriginActor(context.Background(), "auto-title-generator")
	if _, err := d.Decide(ctx2, ClassificationSignals{}, 42, "", "", "sess-title"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	ranker.mu.Lock()
	n = len(ranker.feedbacks)
	ranker.mu.Unlock()
	if n != 0 {
		t.Fatalf("synthetic legacy-path decision must not write feedback, got %d rows", n)
	}
}

// TestReportRoutingOutcome_SyntheticOutcomeIgnored: a terminal report from a
// synthetic round is dropped before any counter moves — a title/summary
// loopback terminal carries no stash (gated at decision time), so letting it
// through would inflate orphan; a goal-% terminal would inflate matched.
func TestReportRoutingOutcome_SyntheticOutcomeIgnored(t *testing.T) {
	resetOutcomeRegistry(t)
	d, ranker := newFeedbackTestDecider(t)

	ctx := WithRequestID(context.Background(), "req-real-1")
	if _, err := d.Decide(ctx, ClassificationSignals{}, 42, "", "", "sess-real"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	stashed, matchedBefore, orphanBefore, _, _ := outcomeStatsSnapshot()
	if stashed < 1 {
		t.Fatal("real decision should have been stashed before the terminal reports")
	}

	// Synthetic terminal for the SAME request id (attacker-forged or loopback
	// reuse): must be invisible.
	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-real-1", OriginActor: "auto-title-generator", Success: true})
	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-real-1", OriginActor: "goal-audit", Success: true})

	if _, matched, orphan, _, _ := outcomeStatsSnapshot(); matched != matchedBefore || orphan != orphanBefore {
		t.Fatalf("synthetic outcomes moved counters: matched %d→%d, orphan %d→%d",
			matchedBefore, matched, orphanBefore, orphan)
	}

	// The real outcome still matches the stash.
	_, matchedBefore2, orphanBefore2, _, _ := outcomeStatsSnapshot()
	ReportRoutingOutcome(RoutingOutcome{RequestID: "req-real-1", OriginActor: "user-app", Success: false})
	fbs := waitForFeedback(t, ranker, 1)
	if fbs[0].IsSuccess {
		t.Fatal("real outcome must carry the failed terminal, got IsSuccess=true")
	}
	if _, matched, orphan, _, _ := outcomeStatsSnapshot(); matched != matchedBefore2+1 || orphan != orphanBefore2 {
		t.Fatalf("real outcome should match exactly once, synthetics must stay invisible: matched %d→%d, orphan %d→%d",
			matchedBefore2, matched, orphanBefore2, orphan)
	}
}
