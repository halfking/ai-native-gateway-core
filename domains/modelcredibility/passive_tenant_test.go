package modelcredibility

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// 2026-11-10 审计 P1-C 的回归门。
//
// ★ 缺陷形态：读路径写死 `tenant_id = 'default'`（store_passive.go），
// 而写路径用真实租户 ⇒ 非 default 租户的行写进 (acme, cred, model)，
// 下一次 flush 却读 (default, cred, model) ⇒
//   ① 混进 default 租户的统计（跨租户污染）；
//   ② acme 自己积累的历史被 upsert 覆盖丢弃。
//
// ★ 为什么必须让替身按 tenant 建键：原替身只按 cred|model 建键 ⇒
// 天然**无法**表达「两个租户各有自己的行」⇒ 这个缺陷在单测里不可见。

func tenantFeatures(tenant, model string, cred int64, n int) []*PassiveFeatures {
	out := make([]*PassiveFeatures, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, &PassiveFeatures{
			TenantID:         tenant,
			ModelName:        model,
			CredentialID:     cred,
			TTFT:             100 * time.Millisecond,
			ITTs:             []time.Duration{40 * time.Millisecond},
			Throughput:       22.0,
			PromptTokens:     100,
			CompletionTokens: 80,
			MeasuredAt:       time.Now(),
		})
	}
	return out
}

// TestPassiveCollector_TenantIsolation 钉住「租户之间互不污染」。
//
// ★ 场景必须构造「default 租户**已有**历史基线」：
// 生产里 default 租户往往已经积累了数天的统计，而 acme 租户是新的。
// 若读路径写死 'default'，acme 的第一次 flush 就会**把 default 的历史
// 并进自己的行**（sample_count = default 的 + acme 的）⇒ 跨租户污染。
// （两个租户在同一批 flush 里首次落库时看不出来——那时两边都还没有行，
//
//	所以必须预置 default 的既有基线。）
func TestPassiveCollector_TenantIsolation(t *testing.T) {
	sink := &fakePassiveSink{baselines: map[string]*PassiveFingerprint{}}
	// 预置 default 租户的既有基线：100 个样本、TTFT 9999ms（与 acme 的
	// 100ms 明显不同 ⇒ 一旦被合并进来，数值立刻可见）。
	defaultTTFT := time.Duration(9999) * time.Millisecond
	sink.baselines[fakeKey("default", 77, "shared-model")] = &PassiveFingerprint{
		TenantID:     "default",
		ModelID:      "shared-model",
		CredentialID: 77,
		SampleCount:  100,
		TTFTMean:     defaultTTFT,
		CollectedTo:  time.Now(),
	}

	c := NewPassiveCollector()
	// 只有 acme 租户本轮有数据（default 本轮没有 ⇒ 不参与本轮 flush）。
	for _, pf := range tenantFeatures("acme", "shared-model", 77, 10) {
		c.Add(pf)
	}
	if got := c.Pending(); got != 1 {
		t.Fatalf("Pending = %d, want 1", got)
	}
	if _, err := c.Flush(context.Background(), sink); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	acme, err := sink.LoadPassiveBaseline(context.Background(), "acme", 77, "shared-model")
	if err != nil || acme == nil {
		t.Fatalf("load acme baseline: %v %+v", err, acme)
	}
	// ★ 核心判据：acme 只能拿到**自己**的 10 个样本。
	// 读路径写死 'default' 时，这里会变成 100（default 的）+ 10（acme 的）= 110。
	if acme.SampleCount != 10 {
		t.Fatalf("acme SampleCount = %d, want 10 — the read path mixed another tenant's baseline in", acme.SampleCount)
	}
	// ★ 数值判据：acme 的 TTFT 均值必须是自己窗口的 ~100ms，
	// 而不是 default 的 9999ms（被合并后会落在两者之间）。
	if got := durationToMS(acme.TTFTMean); got < 50 || got > 500 {
		t.Fatalf("acme TTFTMean = %v ms, want ~100 (another tenant's baseline was merged in)", got)
	}
	if acme.TenantID != "acme" {
		t.Fatalf("acme row tenant = %q, want \"acme\"", acme.TenantID)
	}

	// default 租户的既有基线必须**原封不动**。
	def, err := sink.LoadPassiveBaseline(context.Background(), "default", 77, "shared-model")
	if err != nil || def == nil {
		t.Fatalf("load default baseline: %v %+v", err, def)
	}
	if def.SampleCount != 100 {
		t.Fatalf("default SampleCount = %d, want 100 (the other tenant's baseline was mutated)", def.SampleCount)
	}
}

// TestPassiveCollector_LoadErrorDoesNotDestroyBaseline 钉住「读失败不是
// 「没有基线」」。
//
// ★ 原实现 `if err == nil && existing != nil && …` 把读错误与「无行 /
// 陈旧」混成同一条路 ⇒ 一次瞬时读错误就让随后的 upsert **覆盖**掉积累
// 多天的基线（静默数据摧毁，无日志）。
//
// ⇒ 期望：读错误 ⇒ 本窗口**不落库** ⇒ 既有基线完好（sample_count 不变）。
func TestPassiveCollector_LoadErrorDoesNotDestroyBaseline(t *testing.T) {
	sink := &fakePassiveSink{baselines: map[string]*PassiveFingerprint{}}
	c := NewPassiveCollector()

	// 第一轮：正常落库（积累基线）。
	for _, pf := range tenantFeatures("acme", "m", 5, 10) {
		c.Add(pf)
	}
	if _, err := c.Flush(context.Background(), sink); err != nil {
		t.Fatalf("first Flush: %v", err)
	}
	before, err := sink.LoadPassiveBaseline(context.Background(), "acme", 5, "m")
	if err != nil || before == nil {
		t.Fatalf("load baseline: %v %+v", err, before)
	}
	baselineBefore := before.SampleCount
	if baselineBefore == 0 {
		t.Fatalf("baseline sample count = 0, want > 0")
	}

	// 第二轮：读失败 ⇒ 必须**不落库**，且既有基线不被覆盖。
	sink.loadErr = fmt.Errorf("simulated transient DB read failure")
	c2 := NewPassiveCollector()
	for _, pf := range tenantFeatures("acme", "m", 5, 10) {
		c2.Add(pf)
	}
	flushed, err := c2.Flush(context.Background(), sink)
	if err == nil {
		t.Fatalf("Flush must surface the load error, got nil (flushed=%d)", flushed)
	}
	if flushed != 0 {
		t.Fatalf("flushed = %d, want 0 — a failed load must not write a rebuilt baseline over the existing one", flushed)
	}

	// 读错误恢复后：既有基线必须**原封不动**（sample_count 未被覆盖成 10）。
	sink.loadErr = nil
	after, err := sink.LoadPassiveBaseline(context.Background(), "acme", 5, "m")
	if err != nil || after == nil {
		t.Fatalf("load baseline after: %v %+v", err, after)
	}
	if after.SampleCount != baselineBefore {
		t.Fatalf("baseline SampleCount = %d, want %d — the failed read destroyed the accumulated baseline",
			after.SampleCount, baselineBefore)
	}
}
