package autoroute

// decision_v2_routing_source_test.go — 锁定 DecideV2 路径填充 RoutingSource 的行为。
//
// 背景：V1 Decide 早已填充 RoutingSource（explicit_default/implicit_tag/override_pin/
// session_cache），但 V2 DecideV2（生产默认路径，channel-quality routing 开启时走它）
// 之前完全不填该字段，导致 V1/V2 的审计/日志不一致。本文件验证修复后 V2 在三种
// 来源下都正确标记。
//
// 注意：DecideV2 对 d.index 做 *Index 类型断言，stubIndex 会回退到 V1，所以这里
// 必须用真实的 *Index（pool=nil 走快照可用性，不依赖 DB）。

import (
	"context"
	"testing"
	"time"
)

// v2TestClassifier returns a fixed high-confidence classification so the
// heuristic path doesn't escalate to LLM fallback.
type v2TestClassifier struct{ task TaskType }

func (c *v2TestClassifier) Classify(_ context.Context, _ ClassificationSignals) (*Classification, error) {
	return &Classification{Primary: c.task, Confidence: 0.9, Classifier: "heuristic", Reason: "test"}, nil
}
func (c *v2TestClassifier) Name() string { return "heuristic" }

// newV2Index builds a real *Index with the given candidates and channel-quality
// routing enabled (production default).
func newV2Index(cands []Candidate) *Index {
	return &Index{entries: cands, lastRefresh: time.Now()}
}

// TestDecideV2_RoutingSource_ImplicitTag: 正常 channel-quality 评分选出 winner，
// 没有 override 干预 → RoutingSource=implicit_tag。
func TestDecideV2_RoutingSource_ImplicitTag(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
	defer SetGlobalFeatureFlagsForTest(old)

	idx := newV2Index([]Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "model-a", Tags: []string{"code"}, SuccessRate: 0.95},
	})
	d := NewDecider(&v2TestClassifier{task: TaskCode}, nil, idx, NewMemoryProfileStore())

	dec, err := d.DecideV2(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 err: %v", err)
	}
	if dec.RoutingSource != "implicit_tag" {
		t.Fatalf("RoutingSource: got %q, want implicit_tag", dec.RoutingSource)
	}
}

// TestDecideV2_RoutingSource_OverridePin: override pin 把非首选候选提到第一 →
// RoutingSource=override_pin。
func TestDecideV2_RoutingSource_OverridePin(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
	defer SetGlobalFeatureFlagsForTest(old)

	idx := newV2Index([]Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "natural-winner", Tags: []string{"code"}, SuccessRate: 0.99, UnitPriceInPer1M: 10},
		{CredentialID: 2, CanonicalID: 2, CanonicalName: "pinned-model", Tags: []string{"code"}, SuccessRate: 0.90, UnitPriceInPer1M: 200},
	})
	ors := NewOverrideStore(nil)
	ors.snapshot.Store(&overrideSnapshot{
		byTaskProfile: map[string][]Override{
			"code|smart": {{Mode: OverridePin, ModelChosen: "pinned-model"}},
		},
	})
	d := NewDecider(&v2TestClassifier{task: TaskCode}, nil, idx, NewMemoryProfileStore())
	d.SetOverrideStore(ors)

	dec, err := d.DecideV2(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 err: %v", err)
	}
	if dec.ChosenModel != "pinned-model" {
		t.Fatalf("pin should promote pinned-model, got %s", dec.ChosenModel)
	}
	if dec.RoutingSource != "override_pin" {
		t.Fatalf("RoutingSource: got %q, want override_pin", dec.RoutingSource)
	}
}

// TestDecideV2_RoutingSource_SessionCache: 会话缓存命中 → RoutingSource=session_cache。
//
// 注意：V2 的缓存命中分支受 UseCacheRevalidation 门控，且 ValidateCachedChoice 需要
// 真实 DB pool（pool==nil 时恒返回 false → 走重分类）。因此该分支的成功路径无法在
// 无 DB 的单元测试中触发，只能用集成测试（见 tests/ 或 make integrity-smoke）覆盖。
// 这里保留用例作为文档：一旦缓存命中，RoutingSource 必须是 session_cache。代码审查
// 已确认 decision_v2.go 的缓存分支直接赋字面量 "session_cache"。
func TestDecideV2_RoutingSource_SessionCache_DocumentationOnly(t *testing.T) {
	t.Skip("V2 session-cache 命中需要真实 DB pool（ValidateCachedChoice），留给集成测试")
}
