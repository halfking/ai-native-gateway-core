package streaming

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
)

// passive_timing_tracker_test.go — 被动时序指纹接线三件的契约门：
// 追踪器采样 / 组合器并存与中断裁决权 / 完成点特征合成。

// ① 追踪器：ObserveText 采样 ITT/TTFT；no-op 契约（永不中断）。
func TestPassiveTimingTrackerSamples(t *testing.T) {
	// ★ 2026-11-10：注入时钟（原实现直调 time.Now() ⇒ 断言只能靠 sleep 赌
	// 时钟粒度；粗粒度时钟上「首个增量与创建同刻」⇒ TTFT=0 ⇒ 本条必红）。
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	clock := base
	tr := NewPassiveTimingTracker(base)
	tr.SetClock(func() time.Time { return clock })

	// 首个增量在 +30ms 到达 ⇒ TTFT=30ms（确定性，不再是粒度赌注）。
	clock = clock.Add(30 * time.Millisecond)
	if tr.ObserveText("hello") {
		t.Fatal("tracker must never abort")
	}
	clock = clock.Add(10 * time.Millisecond)
	tr.ObserveText(" world")
	clock = clock.Add(20 * time.Millisecond)
	tr.ObserveText("!")

	ttft, itts, tp := tr.Snapshot(10)
	if ttft != 30*time.Millisecond {
		t.Errorf("ttft = %v, want 30ms", ttft)
	}
	if len(itts) != 2 {
		t.Errorf("itts = %d, want 2", len(itts))
	}
	wantItts := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	for i, d := range itts {
		if d != wantItts[i] {
			t.Errorf("itts[%d] = %v, want %v", i, d, wantItts[i])
		}
	}
	if tp <= 0 {
		t.Errorf("throughput = %v, want >0（10 tokens over 30ms）", tp)
	}

	// nil 接收者与空串安全。
	var nilTracker *PassiveTimingTracker
	if nilTracker.ObserveText("x") || nilTracker.ObserveText("") {
		t.Fatal("nil/empty must be no-op and never abort")
	}

	// no-op 契约面。
	if tr.BreachReason() != "" {
		t.Error("BreachReason must be empty")
	}
	if _, _, _, _, ok := tr.RepeatedContentHash(); ok {
		t.Error("RepeatedContentHash must report no breach")
	}

	// Reset 清空。
	tr.Reset()
	if _, itts2, _ := tr.Snapshot(10); len(itts2) != 0 {
		t.Errorf("after Reset itts = %d, want 0", len(itts2))
	}
}

// ② 组合器：两个观察者都收到文本；中断裁决权归 primary。
func TestComposedTextObserver(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	clock := base
	primary := NewPassiveTimingTracker(base)
	primary.SetClock(func() time.Time { return clock })
	passive := NewPassiveTimingTracker(base)
	passive.SetClock(func() time.Time { return clock })
	comp := &composedTextObserver{primary: primary, extra: []audit.StreamTextObserver{passive}}

	clock = clock.Add(25 * time.Millisecond)
	comp.ObserveText("abc")
	// 两边都采样到了：extra 的 TTFT 有值 ⇒ 组合器确实转发了文本。
	ttft2, _, _ := passive.Snapshot(0)
	if ttft2 != 25*time.Millisecond {
		t.Errorf("extra observer ttft = %v, want 25ms（extra 未收到文本）", ttft2)
	}
	if !passive.ObservedText() {
		t.Error("extra observer must report observed text")
	}

	// 组合器转发 primary 的 breach 面。
	if comp.BreachReason() != primary.BreachReason() {
		t.Error("BreachReason must delegate to primary")
	}

	// primary=nil 时安全。
	empty := &composedTextObserver{extra: []audit.StreamTextObserver{passive}}
	if empty.ObserveText("x") {
		t.Error("nil primary must never abort")
	}
	if empty.BreachReason() != "" {
		t.Error("nil primary BreachReason must be empty")
	}
}

// ③ 完成点合成：usage=0 ⇒ 吞吐缺省；空流 ⇒ nil（缺失不计 0）。
func TestExtractPassiveFromTracker(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	clock := base
	tr := NewPassiveTimingTracker(base)
	tr.SetClock(func() time.Time { return clock })
	clock = clock.Add(30 * time.Millisecond)
	tr.ObserveText("hello")

	meta := modelcredibility.Metadata{
		TenantID:     "default",
		ModelName:    "claude-opus-5-5",
		CredentialID: 31,
		RequestID:    "req-1",
	}
	feat := ExtractPassiveFromTracker(tr, 5, meta)
	if feat == nil {
		t.Fatal("expected features for non-empty stream")
	}
	if feat.ModelName != "claude-opus-5-5" || feat.CredentialID != 31 || feat.TenantID != "default" {
		t.Errorf("metadata lost: %+v", feat)
	}
	// 吞吐需要 ≥2 个增量（lastAt > firstAt 才有时长）；单增量 ⇒ 吞吐缺省。
	featSingle := ExtractPassiveFromTracker(tr, 5, meta)
	if featSingle == nil || featSingle.Throughput != 0 {
		t.Errorf("single-chunk throughput = %+v, want 0 (no duration)", featSingle)
	}

	clock = clock.Add(12 * time.Millisecond)
	tr.ObserveText(" more")
	feat = ExtractPassiveFromTracker(tr, 5, meta)
	if feat == nil {
		t.Fatal("expected features for non-empty stream")
	}
	if feat.TTFT <= 0 {
		t.Error("ttft missing")
	}
	if feat.Throughput <= 0 {
		t.Error("throughput missing with usage>0 and ≥2 chunks")
	}

	// usage=0 ⇒ 吞吐 0（不是估算值）。
	feat2 := ExtractPassiveFromTracker(tr, 0, meta)
	if feat2 == nil || feat2.Throughput != 0 {
		t.Errorf("usage=0 throughput = %+v, want 0", feat2)
	}

	// 空流 ⇒ nil。
	empty := NewPassiveTimingTracker(time.Now())
	if ExtractPassiveFromTracker(empty, 5, meta) != nil {
		t.Error("empty stream must yield nil features")
	}
	// nil tracker / 空模型名 ⇒ nil。
	if ExtractPassiveFromTracker(nil, 5, meta) != nil {
		t.Error("nil tracker must yield nil")
	}
	noModel := meta
	noModel.ModelName = ""
	if ExtractPassiveFromTracker(tr, 5, noModel) != nil {
		t.Error("empty model name must yield nil")
	}
}

// ④ PassiveTrackerFromCapture：直挂与组合两种形态都能取出。
func TestPassiveTrackerFromCapture(t *testing.T) {
	tr := NewPassiveTimingTracker(time.Now())

	// 直挂形态（integrity 未接线时）。
	cap := audit.NewStreamCapture()
	cap.SetTextObserver(tr)
	if got := PassiveTrackerFromCapture(cap); got != tr {
		t.Fatal("direct-attach tracker not retrieved")
	}

	// 组合形态（integrity 观察者占位时）。
	cap2 := audit.NewStreamCapture()
	cap2.SetTextObserver(&composedTextObserver{primary: NewPassiveTimingTracker(time.Now()), extra: []audit.StreamTextObserver{tr}})
	if got := PassiveTrackerFromCapture(cap2); got != tr {
		t.Fatal("composed tracker not retrieved")
	}

	// 空槽 / nil。
	if PassiveTrackerFromCapture(audit.NewStreamCapture()) != nil {
		t.Error("empty capture must yield nil")
	}
	if PassiveTrackerFromCapture(nil) != nil {
		t.Error("nil capture must yield nil")
	}
}

// ★ 2026-11-10 回归门（真实缺陷）：首 token 与追踪器创建**同刻**时 TTFT 合法
// 为 0（粗粒度时钟上更是常见）——旧实现用 `ttft==0 && len(itts)==0` 代理
// 「空流」⇒ 有文本的单块流被误判为空流、特征被静默丢弃（采样面盲区）。
func TestExtractPassiveKeepsSingleChunkStreamWithZeroTTFT(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	clock := base
	tr := NewPassiveTimingTracker(base)
	tr.SetClock(func() time.Time { return clock })
	// 首个增量与创建同刻 ⇒ TTFT=0（不是「没观察到文本」）。
	tr.ObserveText("hello")

	meta := modelcredibility.Metadata{
		TenantID: "default", ModelName: "claude-opus-5-5",
		CredentialID: 31, RequestID: "req-zero-ttft",
	}
	feat := ExtractPassiveFromTracker(tr, 5, meta)
	if feat == nil {
		t.Fatal("single-chunk stream must still yield features（观察过文本 ≠ TTFT>0）")
	}
	if feat.TTFT != 0 {
		t.Errorf("ttft = %v, want 0（同刻到达）", feat.TTFT)
	}
	if feat.Throughput != 0 {
		t.Errorf("throughput = %v, want 0（单增量无时长）", feat.Throughput)
	}
}

// 对照：真的没有文本增量 ⇒ 不产出特征（空流语义不变）。
func TestExtractPassiveNilForEmptyStream(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tr := NewPassiveTimingTracker(base)
	tr.SetClock(func() time.Time { return base })
	meta := modelcredibility.Metadata{
		TenantID: "default", ModelName: "claude-opus-5-5",
		CredentialID: 31, RequestID: "req-empty",
	}
	if feat := ExtractPassiveFromTracker(tr, 5, meta); feat != nil {
		t.Fatalf("empty stream must yield nil, got %+v", feat)
	}
}
