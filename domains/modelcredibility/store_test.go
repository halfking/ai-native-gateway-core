package modelcredibility

import "testing"

// TestFilterRollup 钉住「汇总只写本轮扫到的行」。
//
// ★ 场景：历史判定表里有 3 个 (provider, model)，但本轮扫描面
// （featured）只覆盖 2 个 ⇒ provider_quality_profiles 不得被第 3 行
// （已下线/已移出特色清单的模型）覆盖。
func TestFilterRollup(t *testing.T) {
	all := []ProviderModelScore{
		{ProviderID: 1, RawModelName: "featured-a", Credibility: 90, SampleCount: 1, Verdict: "verified"},
		{ProviderID: 1, RawModelName: "Featured-B", Credibility: 40, SampleCount: 2, Verdict: "diluted"},
		{ProviderID: 2, RawModelName: "retired-model", Credibility: 10, SampleCount: 1, Verdict: "diluted"},
	}
	want := map[rollupKey]struct{}{
		{1, "featured-a"}: {},
		{1, "featured-b"}: {}, // 大小写归一（targets 里与表内大小写不同）
		{3, "new-model"}:  {}, // want 里有但 all 里没有 ⇒ 不产出
	}
	got := filterRollup(all, want)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (retired 行不得混入)", len(got))
	}
	for _, r := range got {
		if r.RawModelName == "retired-model" {
			t.Fatal("retired-model 必须被过滤掉")
		}
	}
	// 大小写不敏感命中：Featured-B 命中 want 里的 featured-b。
	found := false
	for _, r := range got {
		if r.RawModelName == "Featured-B" {
			found = true
		}
	}
	if !found {
		t.Error("大小写不同的同模型必须命中（SQL 侧 lower 对齐）")
	}
}
