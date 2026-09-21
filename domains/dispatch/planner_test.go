package dispatch

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// V6-W1.6 T5/T6 (docs/架构优化v6/09-ir-class-journal-decoupling.md §R11/§R12):
// planner is the pure decision layer between the request state
// (QueuedRequest markers) and the queue-management pipeline. Same inputs must
// yield the same Decision — no goroutines, locks or I/O (invariant 5). The
// behavioral equivalence of the rewired pipeline is pinned by the full
// dispatch suite (hard gate), not here.

func planQR(id string) *QueuedRequest {
	qr := NewQueuedRequest(id, "t", "m", nil, nil)
	qr.ResolvedModel = "m"
	return qr
}

// C-chain step 1: same-credential retry while the budget lasts, unless the
// error is credential-fatal.
func TestPlanAfterFailureRetryLadder(t *testing.T) {
	cfg := Config{RetryPerCredential: 2}

	qr := planQR("pa1")
	if d := PlanAfterFailure(qr, ForwardOutcome{Err: errors.New("x")}, cfg); d.Action != NextActionRetrySameCred {
		t.Fatalf("budget left → %+v, want retry_same_cred", d)
	}
	qr.CredRetryCount = 1
	if d := PlanAfterFailure(qr, ForwardOutcome{Err: errors.New("x")}, cfg); d.Action != NextActionRetrySameCred {
		t.Fatalf("budget half spent → %+v, want retry_same_cred", d)
	}
	qr.CredRetryCount = 2
	if d := PlanAfterFailure(qr, ForwardOutcome{Err: errors.New("x")}, cfg); d.Action != "" {
		t.Fatalf("budget exhausted → %+v, want fall-through to credential switch", d)
	}

	fatal := planQR("pa2")
	if d := PlanAfterFailure(fatal, ForwardOutcome{FatalCredential: true}, cfg); d.Action != "" {
		t.Fatalf("credential-fatal → %+v, want fall-through (no same-cred retry)", d)
	}

	// Request-level override wins over config; negative falls back to config.
	override := planQR("pa3")
	override.RetryPerCredential = 0
	if d := PlanAfterFailure(override, ForwardOutcome{}, cfg); d.Action != "" {
		t.Fatalf("request budget 0 → %+v, want fall-through", d)
	}
	fallback := planQR("pa4")
	fallback.RetryPerCredential = -1
	fallback.CredRetryCount = 0
	if d := PlanAfterFailure(fallback, ForwardOutcome{}, cfg); d.Action != NextActionRetrySameCred {
		t.Fatalf("request budget -1 falls back to cfg → %+v, want retry_same_cred", d)
	}
}

// E5: the 100-attempt budget gates every continuation — 99 leaves budget,
// 100 refuses with failed(attempt_cap) (R12).
func TestPlanAfterFailureAttemptCap(t *testing.T) {
	cfg := Config{RetryPerCredential: 5}

	at99 := planQR("cap99")
	at99.AttemptCount = maxAttempts - 1
	if d := PlanAfterFailure(at99, ForwardOutcome{}, cfg); d.Action != NextActionRetrySameCred {
		t.Fatalf("at %d/%d → %+v, want continuation", maxAttempts-1, maxAttempts, d)
	}

	at100 := planQR("cap100")
	at100.AttemptCount = maxAttempts
	d := PlanAfterFailure(at100, ForwardOutcome{}, cfg)
	if d.Action != NextActionFailed || d.Reason != reasonAttemptCap {
		t.Fatalf("at cap → %+v, want failed(attempt_cap)", d)
	}

	// The cap is only consulted when the retry gate passes (credential-fatal
	// falls through to the switch ladder, which re-checks the budget).
	fatal := planQR("capf")
	fatal.AttemptCount = maxAttempts
	if d := PlanAfterFailure(fatal, ForwardOutcome{FatalCredential: true}, cfg); d.Action != "" {
		t.Fatalf("fatal at cap → %+v, want fall-through (switch ladder re-checks)", d)
	}
}

func TestAttemptBudgetLeft(t *testing.T) {
	qr := planQR("bl")
	if got := AttemptBudgetLeft(qr); got != maxAttempts {
		t.Fatalf("fresh request budget = %d, want %d", got, maxAttempts)
	}
	qr.AttemptCount = maxAttempts - 1
	if got := AttemptBudgetLeft(qr); got != 1 {
		t.Fatalf("after %d attempts budget = %d, want 1", maxAttempts-1, got)
	}
	qr.AttemptCount = maxAttempts
	if got := AttemptBudgetLeft(qr); got != 0 {
		t.Fatalf("at cap budget = %d, want 0", got)
	}
}

func TestPlanSwitchCred(t *testing.T) {
	refs := []CredentialRef{
		{CredentialID: 1, ProviderID: 10, Vendor: "a"},
		{CredentialID: 2, ProviderID: 10, Vendor: "a"},
		{CredentialID: 3, ProviderID: 20, Vendor: "b"},
	}

	qr := planQR("sc1")
	next, scoped := PlanSwitchCred(qr, refs)
	if next == nil || next.CredentialID != 1 {
		t.Fatalf("next = %+v, want cred 1", next)
	}
	if len(scoped) != 0 {
		t.Fatalf("scoped = %v, want empty", scoped)
	}

	qr.markTriedCredential(1)
	next, _ = PlanSwitchCred(qr, refs)
	if next == nil || next.CredentialID != 2 {
		t.Fatalf("next after tried(1) = %+v, want cred 2", next)
	}

	qr.markTriedCredential(2)
	next, scoped = PlanSwitchCred(qr, refs)
	if next == nil || next.CredentialID != 3 {
		t.Fatalf("next after tried(1,2) = %+v, want cred 3", next)
	}
	if len(scoped) != 0 {
		t.Fatalf("provider change allowed by default, scoped = %v", scoped)
	}

	qr.markTriedCredential(3)
	if next, scoped := PlanSwitchCred(qr, refs); next != nil || scoped != nil {
		t.Fatalf("all tried → (%v, %v), want (nil, nil)", next, scoped)
	}
	if next, scoped := PlanSwitchCred(qr, nil); next != nil || scoped != nil {
		t.Fatalf("no candidates → (%v, %v), want (nil, nil)", next, scoped)
	}
}

// Provider scope: candidates outside the initial provider are reported in
// scoped (the executor marks them tried, preserving the legacy bookkeeping).
func TestPlanSwitchCredProviderScope(t *testing.T) {
	refs := []CredentialRef{
		{CredentialID: 1, ProviderID: 20}, // out of scope, comes first
		{CredentialID: 2, ProviderID: 10},
		{CredentialID: 3, ProviderID: 30}, // out of scope, after the pick
	}
	qr := planQR("sc2")
	qr.InitialProviderID = 10
	qr.AllowProviderChange = false

	next, scoped := PlanSwitchCred(qr, refs)
	if next == nil || next.CredentialID != 2 {
		t.Fatalf("next = %+v, want cred 2 (in scope)", next)
	}
	if len(scoped) != 1 || scoped[0] != 1 {
		t.Fatalf("scoped = %v, want [1]", scoped)
	}
}

func TestPlanNoRouteAndModelChange(t *testing.T) {
	qr := planQR("mc1")
	qr.AllowModelChange = true
	if d := PlanNoRoute(qr, false); d.Action != NextActionFailed {
		t.Fatalf("model-change disabled → %+v, want terminal", d)
	}
	if d := PlanNoRoute(qr, true); d.Action != "" {
		t.Fatalf("model-change enabled → %+v, want continuation", d)
	}
	noSwitch := planQR("mc2")
	noSwitch.AllowModelChange = false
	if d := PlanNoRoute(noSwitch, true); d.Action != NextActionFailed {
		t.Fatalf("request forbids model change → %+v, want terminal", d)
	}

	// Alternative picking: first untried wins; all tried → exhaustion.
	m := planQR("mc3")
	m.TriedModels = map[string]struct{}{"m": {}}
	if d := PlanModelChange(m, nil); d.Action != "" {
		t.Fatalf("no alternatives → %+v, want fall-through terminal", d)
	}
	if d := PlanModelChange(m, []string{"m", "m2"}); d.Action != NextActionSwitchModel || d.NextModel != "m2" {
		t.Fatalf("alts with tried head → %+v, want switch to m2", d)
	}
	m.TriedModels["m2"] = struct{}{}
	if d := PlanModelChange(m, []string{"m", "m2"}); d.Action != "" {
		t.Fatalf("all alts tried → %+v, want fall-through terminal", d)
	}
}

// D path: capacity waits pace at capacityRetryDelay until maxCapacityRetries
// rounds, then escalate (fall-through) to the model-change ladder.
func TestPlanCapacityWait(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	qr := planQR("cw")
	for round := 1; round <= maxCapacityRetries; round++ {
		d := PlanCapacityWait(qr, now)
		if d.Action != NextActionCapacityWait {
			t.Fatalf("round %d → %+v, want capacity_wait", round, d)
		}
		if !d.RetryAt.Equal(now.Add(capacityRetryDelay)) {
			t.Fatalf("round %d RetryAt = %v, want %v", round, d.RetryAt, now.Add(capacityRetryDelay))
		}
		qr.CapacityRetryCount++
	}
	if d := PlanCapacityWait(qr, now); d.Action != "" {
		t.Fatalf("round %d → %+v, want escalation fall-through", maxCapacityRetries+1, d)
	}
}

// Invariant 5: the planner is a pure function — repeated calls with the same
// inputs return deeply equal Decisions.
func TestPlannerPurity(t *testing.T) {
	cfg := Config{RetryPerCredential: 2}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	refs := []CredentialRef{{CredentialID: 1, ProviderID: 10}}

	build := func() *QueuedRequest {
		qr := planQR("pure")
		qr.AttemptCount = 3
		qr.CredRetryCount = 1
		qr.CapacityRetryCount = 2
		qr.InitialProviderID = 10
		qr.TriedModels = map[string]struct{}{"m": {}}
		return qr
	}
	pairs := [][2]any{
		{PlanAfterFailure(build(), ForwardOutcome{ErrorKind: "timeout"}, cfg), PlanAfterFailure(build(), ForwardOutcome{ErrorKind: "timeout"}, cfg)},
		{PlanSwitchCredPair(build(), refs), PlanSwitchCredPair(build(), refs)},
		{PlanNoRoute(build(), true), PlanNoRoute(build(), true)},
		{PlanModelChange(build(), []string{"m", "m2"}), PlanModelChange(build(), []string{"m", "m2"})},
		{PlanCapacityWait(build(), now), PlanCapacityWait(build(), now)},
	}
	for i, pair := range pairs {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Fatalf("planner call #%d not pure: %+v != %+v", i, pair[0], pair[1])
		}
	}
}

// PlanSwitchCredPair adapts the two-result PlanSwitchCred for the purity
// table above.
func PlanSwitchCredPair(qr *QueuedRequest, refs []CredentialRef) any {
	next, scoped := PlanSwitchCred(qr, refs)
	return [2]any{next, scoped}
}

// R12 end-to-end: a request that burns the 100-attempt budget terminates via
// terminateOnAttemptCap, its journal tail is failed(attempt_cap), and the Seq
// chain stays contiguous with the terminal entry on top (invariants 1/3).
func TestAttemptCapJournalTerminal(t *testing.T) {
	creds := make([]CredentialRef, 0, 120)
	for i := 1; i <= 120; i++ {
		creds = append(creds, cred(i, ModeConcurrency, 1))
	}
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"m": creds},
		forwardFn: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			return ForwardOutcome{Err: errors.New("fail")}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("cap-j", "t", "m", context.Background(), nil)
	_, err := p.Submit(context.Background(), qr)
	if err == nil {
		t.Fatal("expected terminal failure")
	}
	var exhausted *ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("budget termination should reuse the aggregate envelope, got %T", err)
	}
	if qr.AttemptCount > maxAttempts {
		t.Fatalf("attempt cap violated: %d forwards", qr.AttemptCount)
	}
	tail := qr.AttemptJournal[len(qr.AttemptJournal)-1]
	if tail.Action != NextActionFailed || tail.ErrorKind != reasonAttemptCap {
		t.Fatalf("journal tail = %+v, want failed(attempt_cap)", tail)
	}
	prev := 0
	for _, e := range qr.AttemptJournal {
		if e.Seq != prev+1 {
			t.Fatalf("journal seq not contiguous: prev %d, entry seq %d", prev, e.Seq)
		}
		prev = e.Seq
	}
	if tail.Seq != prev {
		t.Fatalf("terminal entry seq %d is not the max %d", tail.Seq, prev)
	}
}
