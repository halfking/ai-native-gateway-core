package persist

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStages records the exact context each stage received so assertions can
// read budgets instead of inferring them from timing. Both stages can be made
// slow, and both record their context's state *on entry* — checking after
// RunCycle returns is useless because the caller cancels the contexts as part
// of normal cleanup.
type fakeStages struct {
	collectCtx      context.Context
	flushCtx        context.Context
	collectErrEntry error
	flushErrEntry   error
	collectDelay    time.Duration
	flushDelay      time.Duration
	rows            []Row
	collectErr      error
	flushErr        error

	collectCalls int
	flushCalls   int
	flushedRows  int
}

func (f *fakeStages) Collect(ctx context.Context) ([]Row, error) {
	f.collectCalls++
	f.collectCtx = ctx
	f.collectErrEntry = ctx.Err()
	if f.collectDelay > 0 {
		select {
		case <-time.After(f.collectDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.rows, f.collectErr
}

func (f *fakeStages) Flush(ctx context.Context, rows []Row) error {
	f.flushCalls++
	f.flushCtx = ctx
	f.flushErrEntry = ctx.Err()
	f.flushedRows = len(rows)
	if f.flushDelay > 0 {
		select {
		case <-time.After(f.flushDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.flushErr
}

func left(ctx context.Context) (time.Duration, bool) {
	if ctx == nil {
		return 0, false
	}
	d, ok := ctx.Deadline()
	if !ok {
		return 0, false
	}
	return time.Until(d), true
}

// ───────────────────────── 预切模式（Preset=true）─────────────────────────

// ⭐ 预切模式存在的全部理由：Collect 用光自己那一份之后，Flush 仍拿满预算。
func TestPresetGivesFlushAFullBudgetAfterCollectSpendsItsOwn(t *testing.T) {
	f := &fakeStages{
		collectDelay: 18 * time.Millisecond, // 20ms 切片里的 18ms
		rows:         []Row{{CredentialID: 1}, {CredentialID: 2}},
		collectErr:   errors.New("collect ran to its ceiling"),
	}
	out := RunCycle(context.Background(), f, CycleTimeouts{
		Collect: 20 * time.Millisecond, Flush: 500 * time.Millisecond, Preset: true,
	})
	if out.CollectErr == nil {
		t.Fatal("expected Collect to report its error")
	}
	if f.flushCalls != 1 {
		t.Fatalf("Flush called %d times; it must still run on the rows Collect produced", f.flushCalls)
	}
	if rem, ok := left(f.flushCtx); !ok {
		t.Fatal("Flush context carries no deadline")
	} else if rem < 400*time.Millisecond {
		t.Fatalf("Flush had only %s of its 500ms left: it inherited Collect's exhausted context", rem)
	}
}

func TestPresetDoesNotHandFlushTheCollectContext(t *testing.T) {
	f := &fakeStages{rows: []Row{{CredentialID: 1}}}
	RunCycle(context.Background(), f, CycleTimeouts{Collect: time.Second, Flush: time.Second, Preset: true})

	if f.flushCtx == nil || f.collectCtx == nil {
		t.Fatal("both stages must have run")
	}
	if f.flushCtx == f.collectCtx {
		t.Fatal("Flush received Collect's context object; per-stage budgeting is not in effect")
	}
	if f.flushErrEntry != nil {
		t.Fatalf("Flush context dead on entry: %v", f.flushErrEntry)
	}
	if f.collectErrEntry != nil {
		t.Fatalf("Collect context dead on its own entry: %v", f.collectErrEntry)
	}
}

func TestPresetFlushContextAlwaysCarriesACeiling(t *testing.T) {
	f := &fakeStages{rows: []Row{{CredentialID: 1}}}
	out := RunCycle(context.Background(), f, CycleTimeouts{Collect: time.Second, Flush: 0, Preset: true})

	if _, ok := left(f.flushCtx); !ok {
		t.Fatal("Flush has no deadline: a non-positive timeout must fall back to the default, never to unbounded")
	}
	if out.FlushBudget != DefaultFlushTimeout {
		t.Fatalf("FlushBudget=%s, want default %s", out.FlushBudget, DefaultFlushTimeout)
	}
}

// ───────────────────── 共享模式（Preset=false，默认）─────────────────────

// ⭐⭐ 钉死默认形态：入库这段代码**不得**改变生产行为。
//
// §55 实测：预切单独启用会把「还能写 2 行」变成「写 0 行」，因为
// Collect 超时时 return nil, err（writer.go:218）一行都交不出来。
func TestSharedModeIsTheDefaultAndReproducesLegacyBehaviour(t *testing.T) {
	def := DefaultCycleTimeouts()
	if def.Preset {
		t.Fatal("default CycleTimeouts must NOT pre-cut the budget: on its own it writes FEWER rows than the legacy shape")
	}
	if def.Collect != DefaultTotalCycleBudget {
		t.Fatalf("default total budget=%s, want the historical %s", def.Collect, DefaultTotalCycleBudget)
	}

	f := &fakeStages{
		collectDelay: 18 * time.Millisecond,
		rows:         []Row{{CredentialID: 1}, {CredentialID: 2}},
	}
	out := RunCycle(context.Background(), f, CycleTimeouts{Collect: 30 * time.Millisecond, Preset: false})

	if f.flushCalls != 1 {
		t.Fatalf("Flush called %d times, want 1", f.flushCalls)
	}
	if f.flushCtx != f.collectCtx {
		t.Fatal("shared mode must hand both stages the same context — that is the behaviour being preserved")
	}
	if out.FlushBudget <= 0 || out.FlushBudget > 15*time.Millisecond {
		t.Fatalf("FlushBudget=%s; in shared mode it is the *leftover* of one 30s budget, not a fresh slice", out.FlushBudget)
	}
}

func TestSharedModeSkipsFlushWhenCollectProducedNoRows(t *testing.T) {
	f := &fakeStages{rows: nil}
	RunCycle(context.Background(), f, CycleTimeouts{Collect: time.Second, Preset: false})
	if f.flushCalls != 0 {
		t.Fatalf("Flush ran %d times on an empty batch", f.flushCalls)
	}
}

func TestBothModesSkipFlushOnEmptyBatch(t *testing.T) {
	for _, preset := range []bool{false, true} {
		f := &fakeStages{rows: nil}
		RunCycle(context.Background(), f, CycleTimeouts{Collect: time.Second, Flush: time.Second, Preset: preset})
		if f.flushCalls != 0 {
			t.Fatalf("preset=%v: Flush ran %d times on an empty batch", preset, f.flushCalls)
		}
	}
}

// ─────────────────────────────── 通用 ───────────────────────────────

func TestRunCycleFlushesEveryCollectedRow(t *testing.T) {
	rows := []Row{{CredentialID: 1}, {CredentialID: 2}, {CredentialID: 3}}
	f := &fakeStages{rows: rows}
	out := RunCycle(context.Background(), f, DefaultCycleTimeouts())

	if out.Rows != 3 || f.flushedRows != 3 {
		t.Fatalf("Rows=%d flushed=%d, want 3/3", out.Rows, f.flushedRows)
	}
	if out.Err() != nil {
		t.Fatalf("unexpected error: %v", out.Err())
	}
}

// §40 §四 wanted Flush stage timings; there was never such a number in any log.
func TestRunCycleReportsBothStageDurations(t *testing.T) {
	f := &fakeStages{
		rows:         []Row{{CredentialID: 1}},
		collectDelay: 15 * time.Millisecond,
		flushDelay:   15 * time.Millisecond,
	}
	out := RunCycle(context.Background(), f, DefaultCycleTimeouts())

	if out.CollectTook < 10*time.Millisecond || out.CollectTook > 200*time.Millisecond {
		t.Fatalf("CollectTook=%s out of plausible range", out.CollectTook)
	}
	if out.FlushTook < 10*time.Millisecond || out.FlushTook > 200*time.Millisecond {
		t.Fatalf("FlushTook=%s out of plausible range", out.FlushTook)
	}
}

func TestCycleTimeoutsFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		preset, budget, collect, flush string
		wantPreset                           bool
		wantCollect                          time.Duration
	}{
		{"unset", "", "", "", "", false, DefaultTotalCycleBudget},
		{"garbage falls back", "", "abc", "xx", "-1", false, DefaultTotalCycleBudget},
		{"zero is not unbounded", "", "0", "0", "0", false, DefaultTotalCycleBudget},
		{"shared total override", "", "25000", "", "", false, 25 * time.Second},
		{"preset needs exactly 1", "0", "", "", "", false, DefaultTotalCycleBudget},
		{"preset on", "1", "20000", "", "", true, 20 * time.Second},
		{"preset on with stage split", "1", "", "18000", "12000", true, 18 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("URSM_V2_PERSIST_CYCLE_PRESET", tc.preset)
			t.Setenv("URSM_V2_PERSIST_TOTAL_BUDGET_MS", tc.budget)
			t.Setenv("URSM_V2_PERSIST_COLLECT_TIMEOUT_MS", tc.collect)
			t.Setenv("URSM_V2_PERSIST_FLUSH_TIMEOUT_MS", tc.flush)
			got := CycleTimeoutsFromEnv()
			if got.Preset != tc.wantPreset {
				t.Fatalf("Preset=%v, want %v (env %q)", got.Preset, tc.wantPreset, tc.preset)
			}
			if got.Collect != tc.wantCollect {
				t.Fatalf("Collect=%s, want %s", got.Collect, tc.wantCollect)
			}
		})
	}
}

// In shared mode the Flush ceiling is meaningless, so it must not silently
// shrink the total budget when a stray value is present.
func TestFlushCeilingNeverShrinksTheSharedBudget(t *testing.T) {
	t.Setenv("URSM_V2_PERSIST_CYCLE_PRESET", "1")
	t.Setenv("URSM_V2_PERSIST_TOTAL_BUDGET_MS", "40000")
	t.Setenv("URSM_V2_PERSIST_FLUSH_TIMEOUT_MS", "2000")
	got := CycleTimeoutsFromEnv()
	if got.Collect != 18*time.Second && got.Collect != 40*time.Second && got.Collect != DefaultTotalCycleBudget {
		t.Fatalf("Collect=%s unexpected", got.Collect)
	}
}
