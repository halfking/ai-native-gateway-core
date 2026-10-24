package executors

// routeorder_adapter_test.go — P1-2 的验收④：
// **现有策略迁移前后，候选集合与顺序一致**（对拍）。
//
// # 这道门守住哪块缺口
//
// 计划书 §6-P1-2 的验收④："现有策略迁移前后**候选集合与顺序一致**的对拍"。
//
// ★ 为什么这条最要紧：把既有排序"适配"成「只排序」契约，最大的风险是
//   **悄悄改了顺序**（少一个排序键、权重算错、身份匹配错位）。
//   ⇒ 对拍把"迁移不改变行为"变成**被断言**的事实。
//
// ★ 同时覆盖失败开放在**调用侧**的表现：策略炸了/超时了 ⇒ 原顺序，
//   且**候选一个不少**。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/routeorder"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func magpieP1Float(v float64) *float64 { return &v }

// magpieP1Candidates 造一组能拉开复合分差异的候选。
func magpieP1Candidates() []provider.Candidate {
	return []provider.Candidate{
		{
			CredentialID: 11, ProviderID: 1, RawModel: "m-cheap", Tier: 1, Weight: 5,
			PriceInPer1M: magpieP1Float(0.1), PriceOutPer1M: magpieP1Float(0.2),
			SuccessRate: 0.99, BillingMode: "paid",
		},
		{
			CredentialID: 12, ProviderID: 1, RawModel: "m-mid", Tier: 1, Weight: 9,
			PriceInPer1M: magpieP1Float(1.0), PriceOutPer1M: magpieP1Float(2.0),
			SuccessRate: 0.80, BillingMode: "paid",
		},
		{
			CredentialID: 13, ProviderID: 2, RawModel: "m-free", Tier: 0, Weight: 1,
			PriceInPer1M: magpieP1Float(0), PriceOutPer1M: magpieP1Float(0),
			SuccessRate: 0.50, BillingMode: "free",
		},
		{
			CredentialID: 14, ProviderID: 2, RawModel: "m-expensive", Tier: 2, Weight: 3,
			PriceInPer1M: magpieP1Float(9.0), PriceOutPer1M: magpieP1Float(9.0),
			SuccessRate: 0.95, BillingMode: "paid",
		},
	}
}

// magpieP1Identity 是候选的身份（对拍用）。
//
// ★ 身份必须**唯一**：用 (凭据ID, 模型, tier) 三元组。
func magpieP1Identity(c provider.Candidate) string {
	return fmt.Sprintf("%d/%s/%d", c.CredentialID, c.RawModel, c.Tier)
}

// TestCompositeScoreAdapterMatchesExistingSort 验收④：**对拍**。
//
// ★ 适配后的顺序必须与既有 `SortByCompositeScore` **逐位一致**。
func TestCompositeScoreAdapterMatchesExistingSort(t *testing.T) {
	candidates := magpieP1Candidates()
	weights := DefaultScoringWeights()

	// 既有路径（注意它**原地**排序 ⇒ 先复制）。
	existing := SortByCompositeScore(append([]provider.Candidate(nil), candidates...), weights)

	// 适配路径。
	adapted := ApplyRouteOrder(context.Background(), candidates, CompositeScoreOrder(candidates, weights), time.Second)

	if len(adapted) != len(existing) {
		t.Fatalf("候选数 = %d，want %d", len(adapted), len(existing))
	}
	for i := range existing {
		if magpieP1Identity(adapted[i]) != magpieP1Identity(existing[i]) {
			t.Fatalf("位置 %d 不一致：适配=%s（凭据 %d），既有=%s（凭据 %d）\n适配序=%v\n既有序=%v",
				i, adapted[i].RawModel, adapted[i].CredentialID,
				existing[i].RawModel, existing[i].CredentialID,
				magpieP1Identities(adapted), magpieP1Identities(existing))
		}
	}
}

// TestApplyRouteOrderKeepsCandidateSet 断言**候选集合**不变（只重排）。
func TestApplyRouteOrderKeepsCandidateSet(t *testing.T) {
	candidates := magpieP1Candidates()
	weights := DefaultScoringWeights()
	adapted := ApplyRouteOrder(context.Background(), candidates, CompositeScoreOrder(candidates, weights), time.Second)

	before := map[string]int{}
	for _, c := range candidates {
		before[magpieP1Identity(c)]++
	}
	after := map[string]int{}
	for _, c := range adapted {
		after[magpieP1Identity(c)]++
	}
	if len(before) != len(after) {
		t.Fatalf("候选集合大小变了：before=%d after=%d", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("候选 %s 的出现次数 before=%d after=%d（集合必须不变）", k, v, after[k])
		}
	}
}

// TestApplyRouteOrderFailureOpenOnPanic 断言策略 panic ⇒ **原顺序**、候选一个不少。
func TestApplyRouteOrderFailureOpenOnPanic(t *testing.T) {
	candidates := magpieP1Candidates()
	before := magpieP1Identities(candidates)

	panicky := routeorder.OrderFunc{StrategyName: "panicky", Fn: func(string, []routeorder.Seat) []int {
		panic("boom")
	}}
	got := ApplyRouteOrder(context.Background(), candidates, panicky, time.Second)

	if len(got) != len(candidates) {
		t.Fatalf("候选数 = %d，want %d（失败开放也要原样返回全部候选）", len(got), len(candidates))
	}
	for i := range before {
		if magpieP1Identity(got[i]) != before[i] {
			t.Fatalf("panic 后顺序 = %v，want 原顺序 %v", magpieP1Identities(got), before)
		}
	}
}

// TestApplyRouteOrderFailureOpenOnTimeout 断言超时 ⇒ 原顺序。
func TestApplyRouteOrderFailureOpenOnTimeout(t *testing.T) {
	candidates := magpieP1Candidates()
	before := magpieP1Identities(candidates)

	release := make(chan struct{})
	defer close(release)
	slow := routeorder.OrderFunc{StrategyName: "slow", Fn: func(string, []routeorder.Seat) []int {
		<-release
		return nil
	}}
	got := ApplyRouteOrder(context.Background(), candidates, slow, 20*time.Millisecond)

	for i := range before {
		if magpieP1Identity(got[i]) != before[i] {
			t.Fatalf("超时后顺序 = %v，want 原顺序 %v", magpieP1Identities(got), before)
		}
	}
}

// TestApplyRouteOrderDoesNotMutateInput 断言**不改入参**（调用方可安全丢弃结果）。
func TestApplyRouteOrderDoesNotMutateInput(t *testing.T) {
	candidates := magpieP1Candidates()
	before := magpieP1Identities(candidates)
	_ = ApplyRouteOrder(context.Background(), candidates, CompositeScoreOrder(candidates, DefaultScoringWeights()), time.Second)
	after := magpieP1Identities(candidates)
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("入参被改动了：before=%v after=%v", before, after)
		}
	}
}

// TestCompositeScoreStrategyIsSeatOnly 断言适配器只按**座位信息**排序，
// 且座位信息里没有价格口径（价格是既有复合分的事，不进 allow-list）。
func TestCompositeScoreStrategyIsSeatOnly(t *testing.T) {
	seats := routeorder.Seat{
		Index: 0, CredentialID: 1, ProviderID: 1, Model: "m",
		Tier: 1, Weight: 5, Known: true, SuccessRate: 0.9,
	}
	s := CompositeScoreStrategy(DefaultScoringWeights())
	idx := s.Order("pool", []routeorder.Seat{seats, {Index: 1, CredentialID: 2, Resting: true}})
	if len(idx) != 2 {
		t.Fatalf("排列长度 = %d，want 2", len(idx))
	}
	if !routeorder.IsPermutation(idx, 2) {
		t.Fatalf("排列 %v 不是排列", idx)
	}
	// 不可用的座位应当排后面（但**仍在**排列里）。
	if idx[0] != 0 {
		t.Fatalf("排列 = %v，want 不可用的排后面（[0 1]）", idx)
	}
}

func magpieP1Identities(cs []provider.Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = magpieP1Identity(c)
	}
	return out
}
