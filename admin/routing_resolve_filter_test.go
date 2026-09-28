package admin

// 2026-09-29 stream 上游污染治理回归测试。
//
// 不变式：resolve("X") 返回的 candidates 集合中，所有 candidate.canonical_id
// 要么等于 resolve_input("X") 解析出的 canonical_id，要么为 NULL（legacy
// 绑定未挂 canonical）。
//
// 这里聚焦纯函数 filterResolveCandidatesByCid 的边界 + 模型名侧
// NormalizeRouteKeyAliasesNoStrip 的语义（resolveInputCanonicalID 的输入侧）。

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

func TestFilterResolveCandidatesByCid_DropsForeignCanonical(t *testing.T) {
	// 复现 glm-5.3-flash 在修复前的污染：candidates 同时携带 2716170 (flash)
	// 与 2422803 (base) 的 canonical_id。
	makeC := func(cid *int64, model string) resolveCandidate {
		return resolveCandidate{CanonicalID: cid, ModelName: model}
	}
	cands := []resolveCandidate{
		makeC(int64Ptr(2716170), "glm-5.3-flash"),
		makeC(int64Ptr(2716170), "z-ai/glm-5.3-flash"),
		makeC(int64Ptr(2422803), "glm-5.3"),   // 污染：base 模型凭据
		makeC(int64Ptr(2422803), "z-ai/glm-5.3"), // 污染
		makeC(nil, "legacy/raw-uncategorized"), // NULL = legacy 保留
	}
	filtered := filterResolveCandidatesByCid(cands, 2716170)
	if got, want := len(filtered), 3; got != want {
		t.Fatalf("len(filtered) = %d, want %d", got, want)
	}
	for _, c := range filtered {
		if c.CanonicalID != nil && *c.CanonicalID != 2716170 {
			t.Errorf("foreign canonical leaked through: %+v", c)
		}
	}
}

func TestFilterResolveCandidatesByCid_NoOpWhenUnknownCid(t *testing.T) {
	cands := []resolveCandidate{
		{CanonicalID: int64Ptr(1), ModelName: "a"},
		{CanonicalID: int64Ptr(2), ModelName: "b"},
		{CanonicalID: nil, ModelName: "c"},
	}
	// expectedCid == 0 → fail-open：原样返回，不动 operators 的诊断页
	got := filterResolveCandidatesByCid(cands, 0)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (no-op when cid unknown)", len(got))
	}
}

func TestFilterResolveCandidatesByCid_NullKept(t *testing.T) {
	cands := []resolveCandidate{
		{CanonicalID: int64Ptr(42), ModelName: "match"},
		{CanonicalID: nil, ModelName: "legacy"},
	}
	got := filterResolveCandidatesByCid(cands, 42)
	if len(got) != 2 {
		t.Fatalf("NULL canonical_id should be kept; got len=%d", len(got))
	}
}

func TestNormalizeRouteKeyAliasesNoStrip_KeepsWrapperTokens(t *testing.T) {
	// resolve_input('glm-5.3-flash') 的 *strict* 矩阵不应包含 'glm-5.3'
	// ——否则又回到了原来污染同款的语义。
	got := modelname.NormalizeRouteKeyAliasesNoStrip("glm-5.3-flash")
	for _, v := range got {
		if v == "glm-5.3" {
			t.Fatalf("NormalizeRouteKeyAliasesNoStrip must NOT strip wrapper tokens; leaked: %v", got)
		}
	}
	// 但 dot↔dash 仍应在（点号版本、连字符版本）
	wantSome := []string{"glm-5.3-flash", "glm-5-3-flash"}
	for _, w := range wantSome {
		found := false
		for _, v := range got {
			if v == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected variant %q in output %v", w, got)
		}
	}
}

func TestNormalizeRouteKeyAliasesNoStrip_BaseUnchanged(t *testing.T) {
	// 反向：base 模型名不带包装词，应与 NormalizeRouteKeyAliases 等价。
	noStrip := modelname.NormalizeRouteKeyAliasesNoStrip("glm-5.3")
	withStrip := modelname.NormalizeRouteKeyAliases("glm-5.3")
	if len(noStrip) != len(withStrip) {
		t.Fatalf("base model should produce identical variants; noStrip=%v withStrip=%v",
			noStrip, withStrip)
	}
	for i := range noStrip {
		if noStrip[i] != withStrip[i] {
			t.Errorf("variant[%d] mismatch: noStrip=%q withStrip=%q", i, noStrip[i], withStrip[i])
		}
	}
}