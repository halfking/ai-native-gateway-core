package dispatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// V6-W1.6 T3 验收（09 号 §4 / §3.3）：C 链（失败重试链）全走查的
// Seq/Counts/Attempt 恒等式。链形：gpt4(cred1,cred2) 全失败 → 换模型 m2(cred3)
// 失败 → 无备选 → failed 终态。
//
// 注意恒等式 2 的精确形态：AttemptCount == 实际 forward 次数（由
// forwardCalls 计数独立验证）；AttemptCount ≤ 1 + Σ发送型动作 ——
// 穷尽路径上的 switch_cred/switch_model 条目是"尝试穷尽标记"，
// 记录于 routeFunc 找候选之前（08 号 G-Ⅴ 语义），不对应后续发送。
func TestJournalChainFailoverLadder(t *testing.T) {
	var forwards atomic.Int32
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{
			"gpt4": {cred(1, ModeConcurrency, 5), cred(2, ModeConcurrency, 5)},
			"m2":   {cred(3, ModeConcurrency, 5)},
		},
		altsByModel: map[string][]string{"gpt4": {"m2"}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			forwards.Add(1)
			return ForwardOutcome{Err: errors.New("upstream 500"), ErrorKind: "upstream_error", HTTPStatus: 500}
		},
		forwardCalls: map[int]int{},
		allowChange:  true,
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("jc1", "t", "gpt4", context.Background(), "payload")
	qr.RetryPerCredential = 0 // ladder: switch immediately, no retry backoff
	qr.AllowModelChange = true
	if _, err := p.Submit(context.Background(), qr); err == nil {
		t.Fatalf("expected terminal failure, got success")
	}

	// The journal must end with a unique terminal entry.
	entries := qr.AttemptJournal
	if len(entries) < 3 {
		t.Fatalf("journal too short: %+v", entries)
	}
	tail := entries[len(entries)-1]
	if tail.Action != NextActionFailed {
		t.Fatalf("tail action = %q, want failed (entries: %+v)", tail.Action, entries)
	}
	if tail.ErrorKind == "" || tail.HTTPStatus != 500 {
		t.Fatalf("terminal entry lost failure context: %+v", tail)
	}

	// Invariant 1: Seq strictly increasing with no gaps; terminal Seq largest.
	for i, e := range entries {
		if e.Seq != i+1 {
			t.Fatalf("entry %d has Seq %d, want %d (gap/dup)", i, e.Seq, i+1)
		}
	}

	// Ladder shape: switch_cred(cred1) → switch_cred(cred2) → switch_model
	// → switch_cred(cred3) → failed.
	if qr.Counts != (ActionCounts{NodeSwitches: 3, ModelSwitches: 1}) {
		t.Fatalf("counts = %+v, want NodeSwitches=3 ModelSwitches=1", qr.Counts)
	}
	var sawModelSwitch bool
	for _, e := range entries {
		if e.Action == NextActionSwitchModel {
			sawModelSwitch = true
			if e.Model != "gpt4" {
				t.Fatalf("switch_model entry Model = %q, want from-model gpt4", e.Model)
			}
		}
	}
	if !sawModelSwitch {
		t.Fatalf("no switch_model entry in %+v", entries)
	}

	// Invariant 2 (precise form): AttemptCount == actual forwards;
	// AttemptCount ≤ 1 + sending actions (exhaustion markers over-count).
	if qr.AttemptCount != 3 {
		t.Fatalf("AttemptCount = %d, want 3", qr.AttemptCount)
	}
	if got := int(forwards.Load()); got != 3 {
		t.Fatalf("actual forwards = %d, want 3", got)
	}
	sending := qr.Counts.Retries + qr.Counts.NodeSwitches + qr.Counts.ModelSwitches
	if qr.AttemptCount > 1+sending {
		t.Fatalf("AttemptCount %d exceeds 1+sending %d", qr.AttemptCount, 1+sending)
	}
	if tail.Attempt != qr.AttemptCount {
		t.Fatalf("terminal Attempt = %d, want %d", tail.Attempt, qr.AttemptCount)
	}

	// Invariant 3: after the terminal entry no new journal entries appear —
	// complete() rides the completed CAS; assert the marker stays a requeue
	// projection (terminal did not overwrite it).
	if qr.LastFailover.NextAction == NextActionFailed {
		t.Fatalf("terminal entry leaked into LastFailover projection: %+v", qr.LastFailover)
	}
}

// TestJournalCompletedChain: the happy path — one failed credential, switch,
// success — journals the exact invariant-2 equality: AttemptCount == 1 + Σ发送.
func TestJournalCompletedChain(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"gpt4": {
			cred(1, ModeConcurrency, 5),
			cred(2, ModeConcurrency, 5),
		}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			if c.CredentialID == 1 {
				return ForwardOutcome{Err: errors.New("upstream 500"), ErrorKind: "upstream_error"}
			}
			return ForwardOutcome{Result: "ok"}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("jc2", "t", "gpt4", context.Background(), "payload")
	qr.RetryPerCredential = 0
	res, err := p.Submit(context.Background(), qr)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res != "ok:cred2:call1" {
		t.Fatalf("res = %v", res)
	}
	entries := qr.AttemptJournal
	if len(entries) != 2 {
		t.Fatalf("journal = %+v, want switch_cred + completed", entries)
	}
	if entries[0].Action != NextActionSwitchCred || entries[0].CredentialID != 1 {
		t.Fatalf("first entry = %+v, want switch_cred on cred 1", entries[0])
	}
	if entries[1].Action != NextActionCompleted {
		t.Fatalf("terminal entry = %+v, want completed", entries[1])
	}
	// Invariant 2 equality on the non-exhaustion chain.
	if qr.AttemptCount != 1+qr.Counts.NodeSwitches {
		t.Fatalf("AttemptCount %d != 1+NodeSwitches %d", qr.AttemptCount, 1+qr.Counts.NodeSwitches)
	}
	if entries[1].Seq <= entries[0].Seq {
		t.Fatalf("terminal Seq %d not largest", entries[1].Seq)
	}
}

// TestJournalScheduledParkFirstEntry: B 链 — the scheduled park journals a
// scheduled_wait entry carrying the due time; the entry exists even after the
// request completes (trace, not marker).
func TestJournalScheduledParkFirstEntry(t *testing.T) {
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{"gpt4": {cred(1, ModeConcurrency, 5)}},
		forwardFn: func(ctx context.Context, qr *QueuedRequest, c CredentialRef) ForwardOutcome {
			return ForwardOutcome{}
		},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("js1", "t", "gpt4", context.Background(), "payload")
	qr.DueAt = time.Now().Add(400 * time.Millisecond) // > minScheduleLead with slack for -race scheduling
	go func() { _, _ = p.Submit(context.Background(), qr) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(qr.AttemptJournal) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(qr.AttemptJournal) == 0 {
		t.Fatalf("scheduled park journaled nothing")
	}
	if e := qr.AttemptJournal[0]; e.Action != NextActionScheduledWait || qr.Counts.ScheduledWaits != 1 {
		t.Fatalf("first journal entry = %+v, counts %+v; want scheduled_wait", e, qr.Counts)
	}
	// Wait for terminal and assert the park entry survived in the trace.
	for time.Now().Before(deadline) && !qr.completed.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	if qr.AttemptJournal[0].Action != NextActionScheduledWait {
		t.Fatalf("park entry lost after completion: %+v", qr.AttemptJournal)
	}
	tail := qr.AttemptJournal[len(qr.AttemptJournal)-1]
	if tail.Action != NextActionCompleted {
		t.Fatalf("tail = %+v, want completed", tail)
	}
}
