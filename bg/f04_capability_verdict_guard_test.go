package bg

import (
	"sync"
	"testing"
)

// F04 (V3 持久化协议能力, 2026-09-30) — probe 侧「盖章 → 消费」归属守卫。
//
// 为什么这组测试单独立项：F04 的写侧（credentialfpslot 持久化）与读侧
// （executor_chat 短路）都已有覆盖，但**中间这一环**——探测盖章到
// probeResult.SupportsResponses 的传递——当时是零测试。
//
// 而这一环恰好是唯一一处「写错了也不会红」的地方：若 takeCapabilityVerdict
// 的 (credential, model) 归属判断失效，一个 credential 的协议能力结论会被
// 持久化到**另一个** credential/model 的节点上。后果是请求侧对错误节点做
// 短路——该节点明明支持 Responses，却被降级成 Chat，且不会有任何现有测试
// 变红（写侧只测「写进去能读出来」，读侧只测「有结论就短路」）。
//
// 归属判断必须以**实际携带的字段**为准，而不是键名或最近一次调用——这与
// 三十五轮 IR 那条「prompt 键名冲突」是同源的教训。

func boolPtr(b bool) *bool { return &b }

// TestF04_TakeCapabilityVerdictRequiresExactOwnership is the core guard test:
// a verdict stamped for one (credential, model) must only be consumable by
// exactly that pair.
func TestF04_TakeCapabilityVerdictRequiresExactOwnership(t *testing.T) {
	probe := &CredentialProbeV2{}
	ctx := "unused"

	// Exact match: the verdict is delivered.
	probe.setCapabilityVerdict(22, "gpt-5.6-terra", boolPtr(false))
	got := probe.takeCapabilityVerdict(22, "gpt-5.6-terra")
	if got == nil {
		t.Fatal("exact (credential, model) match must yield the verdict")
	}
	if *got != false {
		t.Fatalf("verdict = %v, want false", *got)
	}
	_ = ctx

	// Same credential, sibling model: must NOT be delivered.
	probe.setCapabilityVerdict(22, "gpt-5.6-terra", boolPtr(false))
	if v := probe.takeCapabilityVerdict(22, "sibling-model"); v != nil {
		t.Fatalf("sibling model must not receive another model's verdict; got %v", *v)
	}
	// ...and the real owner can still claim it (a rejected take must not
	// consume the stamp).
	if v := probe.takeCapabilityVerdict(22, "gpt-5.6-terra"); v == nil {
		t.Fatal("a rejected take must not consume the stamp; the owner lost its verdict")
	}

	// Same model, different credential: must NOT be delivered.
	probe.setCapabilityVerdict(22, "gpt-5.6-terra", boolPtr(true))
	if v := probe.takeCapabilityVerdict(99, "gpt-5.6-terra"); v != nil {
		t.Fatalf("other credential must not receive this credential's verdict; got %v", *v)
	}
	if v := probe.takeCapabilityVerdict(22, "gpt-5.6-terra"); v == nil {
		t.Fatal("the owning credential lost its verdict to a foreign take")
	}
}

// TestF04_CapabilityVerdictIsTakeOnce pins the clearing semantics: probeResult
// is consumed exactly once. Without the clear, a verdict stamped by probe N
// would be re-attached to probe N+1's writeHealth for the same node.
func TestF04_CapabilityVerdictIsTakeOnce(t *testing.T) {
	probe := &CredentialProbeV2{}
	probe.setCapabilityVerdict(7, "gpt-4", boolPtr(false))

	if v := probe.takeCapabilityVerdict(7, "gpt-4"); v == nil {
		t.Fatal("first take must deliver")
	}
	if v := probe.takeCapabilityVerdict(7, "gpt-4"); v != nil {
		t.Fatalf("second take must return nil (stale verdict would be re-persisted); got %v", *v)
	}
}

// TestF04_NoStaleVerdictCarriesAcrossProbes is the cross-probe hazard. Two
// credentials probed back to back: the second probe saw no capability
// evidence, so it must write no verdict. If the stale stamp survived, the
// second credential would inherit the first one's conclusion.
func TestF04_NoStaleVerdictCarriesAcrossProbes(t *testing.T) {
	probe := &CredentialProbeV2{}

	// Probe #1 observes "Responses unsupported" and the caller consumes it.
	probe.setCapabilityVerdict(100, "model-a", boolPtr(false))
	if v := probe.takeCapabilityVerdict(100, "model-a"); v == nil {
		t.Fatal("probe #1 must deliver its verdict")
	}

	// Probe #2 for a different credential: probeCredential re-stamps nil at
	// entry, and even if a caller skipped that, a consumed stamp stays nil.
	probe.setCapabilityVerdict(200, "model-b", nil)
	if v := probe.takeCapabilityVerdict(200, "model-b"); v != nil {
		t.Fatalf("probe #2 saw no capability evidence and must write none; got %v", *v)
	}
}

// TestF04_VerdictIsNotStampedBeforeAnyProbe covers the "never probed" shape: a
// zero-value receiver must report unknown, never a verdict.
func TestF04_VerdictIsNotStampedBeforeAnyProbe(t *testing.T) {
	probe := &CredentialProbeV2{}
	if v := probe.takeCapabilityVerdict(1, "any-model"); v != nil {
		t.Fatalf("a probe that never ran must yield no verdict; got %v", *v)
	}
}

// TestF04_CapabilityVerdictIsConcurrencySafe exercises the mutex under -race.
// writeHealth is called from cycleAll and ProbeNow; if both are active the
// stamp/take pair must not tear.
func TestF04_CapabilityVerdictIsConcurrencySafe(t *testing.T) {
	probe := &CredentialProbeV2{}
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			credID := 100 + i
			for j := 0; j < 200; j++ {
				probe.setCapabilityVerdict(credID, "gpt-4", boolPtr(i%2 == 0))
				_ = probe.takeCapabilityVerdict(credID, "gpt-4")
				// Foreign reads must never observe another goroutine's stamp.
				if v := probe.takeCapabilityVerdict(credID+1000, "gpt-4"); v != nil {
					t.Errorf("foreign credential received a verdict: %v", *v)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
