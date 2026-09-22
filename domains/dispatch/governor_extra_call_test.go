package dispatch

// governor_extra_call_test.go — R51-F15 钉桩：reqprobe 免费重试 / ctxLen
// 恢复这类"准入内额外上游调用"通过 ExtraCallRecorder 能力补记进速率桶。
//
//	R1 rpm/tpm RecordExtraCall 使后续 Acquire 实际等待（债务生效）。
//	R2 债务有下限（-limit），重复补记不会把桶打成无界负值。
//	R3 concurrency/noop governor 无该能力（槽位全程持有，无需补记）。
//	R4 extraCallMeterFor 仅对有能力者返回非 nil meter。

import (
	"context"
	"testing"
	"time"
)

func TestRPMGovernorRecordExtraCallChargesBucket(t *testing.T) {
	g := newRPMGovernor(60) // 1 token/s
	ctx := context.Background()
	if err := g.Acquire(ctx, nil, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// 桶满 60，Acquire 消耗 1 → 59；补记 1 次显性扣减 → 58。
	g.RecordExtraCall(nil)
	if got := g.tokens; got != 58 {
		t.Fatalf("tokens after one extra call = %v, want 58", got)
	}
	// 打到债务下限：58 - 121 次 = -63 → 钳在 -60。
	for i := 0; i < 121; i++ {
		g.RecordExtraCall(nil)
	}
	if got := g.tokens; got != -60 {
		t.Fatalf("tokens after 201 extra calls = %v, want floor -60", got)
	}
	// 债务生效：下一个 Acquire 必须等满 60 个 token 的恢复时长。
	start := time.Now()
	if err := g.Acquire(ctx, nil, time.Now().Add(30*time.Millisecond)); err == nil {
		t.Fatal("acquire under debt should pace-timeout within 30ms")
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("acquire returned too fast (%v): debt not honored", elapsed)
	}
}

func TestTPMGovernorRecordExtraCallChargesEstimatedTokens(t *testing.T) {
	g := newTPMGovernor(1000)
	qr := &QueuedRequest{EstimatedTokens: 400}
	g.RecordExtraCall(qr)
	if got := g.tokens; got != 600 {
		t.Fatalf("tokens after extra call = %v, want 600", got)
	}
	// EstimatedTokens 未知 → 保守固定成本 defaultTokenEstimate。
	g.RecordExtraCall(&QueuedRequest{})
	if got := g.tokens; got != -200 {
		t.Fatalf("tokens after unknown-size extra call = %v, want -200", got)
	}
	// 下限 -tpm。
	for i := 0; i < 10; i++ {
		g.RecordExtraCall(qr)
	}
	if got := g.tokens; got != -1000 {
		t.Fatalf("tokens floor = %v, want -1000", got)
	}
}

func TestConcurrencyAndNoopGovernorsHaveNoExtraCallCapability(t *testing.T) {
	if _, ok := Governor(newConcurrencyGovernor(2)).(ExtraCallRecorder); ok {
		t.Fatal("concurrency governor must not implement ExtraCallRecorder (slot held for whole admission)")
	}
	if _, ok := Governor(newNoopGovernor()).(ExtraCallRecorder); ok {
		t.Fatal("noop governor must not implement ExtraCallRecorder")
	}
}

func TestExtraCallMeterForCapabilityDiscovery(t *testing.T) {
	qr := &QueuedRequest{}
	if m := extraCallMeterFor(newConcurrencyGovernor(2), qr); m != nil {
		t.Fatal("meter must be nil for governors without the capability")
	}
	m := extraCallMeterFor(newTPMGovernor(500), qr)
	if m == nil {
		t.Fatal("meter must be non-nil for tpm governor")
	}
	m() // 每次调用计一次；这里只验证不 panic（水位断言在 governor 测试内）。
}
