package autoroute

// helpers_test.go — withRoleFailoverHead（R50 F14）单元测试。
// 契约：返回切片首元素 == prefs 在候选池内命中的首个元素（如有）；否则
// == tierPlan 自身首元素。prefs 按候选池过滤后提头；tierPlan 不再做
// 池过滤（由上游 tierFailoverPlan 等已筛过池成员）。跨 prefs/tierPlan
// 去重；空字符串视为不存在；tierPlan 为空时返回 nil。

import (
	"reflect"
	"testing"
)

// makeCands 构造指定 canonical 名的 ScoredCandidate 列表（不填其他字段，
// R50 F14 helper 只看 CanonicalName）。
func makeCands(names ...string) []ScoredCandidate {
	out := make([]ScoredCandidate, 0, len(names))
	for _, n := range names {
		out = append(out, ScoredCandidate{Candidate: Candidate{CanonicalName: n}})
	}
	return out
}

func TestWithRoleFailoverHead_HappyPath(t *testing.T) {
	// 候选池：a, b, c, d。prefs=[c, z] → c 在池首位命中，提头；tierPlan
	// 保序去重随后（c 已出现过；x 不在池但 tierPlan 不做池过滤——tierPlan
	// 由上游 tierFailoverPlan 等已过滤，prefs 才是动态输入需要按池校验）。
	cands := makeCands("a", "b", "c", "d")
	tierPlan := []string{"b", "d", "x", "c"} // x 在 tierPlan 中保留；c 已抽头
	prefs := []string{"c", "z"}
	got := withRoleFailoverHead(tierPlan, prefs, cands)
	want := []string{"c", "b", "d", "x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("happy: got %v, want %v", got, want)
	}
	// 契约：首元素即选中模型。
	if len(got) == 0 || got[0] != "c" {
		t.Fatalf("starts-with-selected contract violated: head=%q", got)
	}
}

func TestWithRoleFailoverHead_TierPlanNotFilteredByPool(t *testing.T) {
	// 显式锁定：tierPlan 不按候选池再过滤——避免后续维护者误以为
	// tierPlan 也需要 inPool 校验（prefs 才是按池过滤的输入；tierPlan
	// 由上游 tierFailoverPlan 等已筛过池成员）。这条契约如被打破，
	// 会导致 V1/V2 role promote 在 tier 配置外的模型被静默丢弃。
	cands := makeCands("a", "b") // 池只含 a, b
	tierPlan := []string{"a", "ghost-1", "b", "ghost-2"}
	prefs := []string{}
	got := withRoleFailoverHead(tierPlan, prefs, cands)
	want := []string{"a", "ghost-1", "b", "ghost-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tierPlan passthrough: got %v, want %v", got, want)
	}
}

func TestWithRoleFailoverHead_AllPrefsAbsent(t *testing.T) {
	// 候选池：a, b。prefs=[x, y] 全部不在池 → 仅返回 tierPlan 保序去重版。
	cands := makeCands("a", "b")
	tierPlan := []string{"a", "b"}
	got := withRoleFailoverHead(tierPlan, []string{"x", "y"}, cands)
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("all-absent: got %v, want %v", got, want)
	}
}

func TestWithRoleFailoverHead_NilTierPlan(t *testing.T) {
	// tierPlan 为空（nil 或 []string{}）一律返回 nil — 没有可恢复链，
	// 强造只会污染 TierFailoverModels 契约。
	if got := withRoleFailoverHead(nil, []string{"a"}, makeCands("a", "b")); got != nil {
		t.Fatalf("nil tierPlan: got %v, want nil", got)
	}
	if got := withRoleFailoverHead([]string{}, []string{"a"}, makeCands("a", "b")); got != nil {
		t.Fatalf("empty tierPlan: got %v, want nil", got)
	}
}

func TestWithRoleFailoverHead_NilPrefs(t *testing.T) {
	// prefs 为空 → 直接返回 tierPlan 保序去重版（视为候选池为空过滤态）。
	cands := makeCands("a", "b", "c")
	tierPlan := []string{"a", "b", "c"}
	if got := withRoleFailoverHead(tierPlan, nil, cands); !reflect.DeepEqual(got, tierPlan) {
		t.Fatalf("nil prefs: got %v, want %v", got, tierPlan)
	}
	if got := withRoleFailoverHead(tierPlan, []string{}, cands); !reflect.DeepEqual(got, tierPlan) {
		t.Fatalf("empty prefs: got %v, want %v", got, tierPlan)
	}
}

func TestWithRoleFailoverHead_DedupAcrossSources(t *testing.T) {
	// prefs 内重复 + prefs ∩ tierPlan 重叠 → 每个名字只出现一次。
	cands := makeCands("a", "b", "c")
	tierPlan := []string{"a", "b", "c"}
	prefs := []string{"a", "a", "b"} // a, a, b → a, b（去重）
	got := withRoleFailoverHead(tierPlan, prefs, cands)
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedup: got %v, want %v", got, want)
	}
}

func TestWithRoleFailoverHead_EmptyStringGuard(t *testing.T) {
	// 空字符串在 prefs 中视为不存在（被 add 闭包短路）。
	cands := makeCands("a", "b")
	got := withRoleFailoverHead([]string{"a", "b"}, []string{"", "a", ""}, cands)
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty guard: got %v, want %v", got, want)
	}
}

func TestWithRoleFailoverHead_RoleFallbackSurvivesAfterTier(t *testing.T) {
	// R50 F14 主场景：role 偏好被 tier 滤除后靠豁免复活，prefs 在
	// candidates 里但不在 tierPlan 里 → 应被拉到头。
	cands := makeCands("model-c", "model-a", "model-b") // 豁免复活进入候选池
	tierPlan := []string{"model-a", "model-b"}          // 但 tier 配置不含 model-c
	prefs := []string{"model-c"}                        // role 偏好
	got := withRoleFailoverHead(tierPlan, prefs, cands)
	want := []string{"model-c", "model-a", "model-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("role revival: got %v, want %v", got, want)
	}
}

func TestWithRoleFailoverHead_PinSingleElement(t *testing.T) {
	// P3 第二调用站点：单元素 prefs = pinned 胜者。复现 V2 line 348 用法。
	cands := makeCands("pinned-model", "tier-1", "tier-2")
	tierPlan := []string{"tier-1", "tier-2"}
	prefs := []string{cands[0].Candidate.CanonicalName} // "pinned-model"
	got := withRoleFailoverHead(tierPlan, prefs, cands)
	want := []string{"pinned-model", "tier-1", "tier-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pin promote: got %v, want %v", got, want)
	}
}

func TestWithRoleFailoverHead_DoesNotMutateInputs(t *testing.T) {
	// 调用方复用 tierPlan/prefs 切片时不能被污染（seen/out 是新建切片）。
	cands := makeCands("a", "b")
	tierPlan := []string{"a", "b"}
	prefs := []string{"a"}
	origTier := append([]string(nil), tierPlan...)
	origPrefs := append([]string(nil), prefs...)
	_ = withRoleFailoverHead(tierPlan, prefs, cands)
	if !reflect.DeepEqual(tierPlan, origTier) {
		t.Fatalf("tierPlan mutated: %v", tierPlan)
	}
	if !reflect.DeepEqual(prefs, origPrefs) {
		t.Fatalf("prefs mutated: %v", prefs)
	}
}
