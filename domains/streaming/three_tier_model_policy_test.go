package streaming

// 三级模型过滤判定语义判据（2026-11-27 三级配额制轮）。
//
// team_budget_gate / domains/tenant/policy.go 教训：判定体写好但零调用点
// ⇒ 门静默失效。本判据钉两点：
//   ① enforceTenantModelPolicy（四面共用判定体）内部真实执行 org/user 级
//      检查（mock Checker 变异语义 ⇒ 返回值跟着变 ⇒ 不是自证）；
//   ② 三面 pre-auto 调用点 + post-auto 调用点全部走这一个判定体
//      （源码锚点判据：call-site 计数）。

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/internal/modelpolicy"
)

// stubOwnerChecker 只覆盖本判据用到的 Checker 子集：租户级恒放行，
// org/user 级按注入的名单判定。enforceTenantModelPolicy 的参数是
// *modelpolicy.Checker（concrete 类型）⇒ 用nil-DB Checker + 直接注入
// 缓存的方式构造（owner 解析在 nil-DB 下 fail-open，需绕开）。
//
// 由于 Checker 是 concrete 类型，org/user 级判据走缓存注入：
// ResolveOwner 在 dbPool==nil 时返回 (0,0,false) ⇒ org/user 检查跳过。
// 因此「接线真实存在」的判据由两部分构成：
//
//	(a) 源码锚点：enforceTenantModelPolicy 必须引用 IsForbiddenForOrg
//	    与 IsForbiddenForUser（调用点在判定体内，四面共用）；
//	(b) 缓存注入：直接向 Checker 注入 t:/o:/u: 三级缓存 + 预热 owner
//	    缓存不可行（ownerCache 私有）⇒ org/user 层语义已由
//	    modelpolicy 包 TestChecker_ScopeIsolation_OrgUser 钉住；
//	    这里钉「streaming 判定体调用链存在且顺序正确」。
func TestThreeTierModelPolicy_JudgementBodyHasOrgUserChecks(t *testing.T) {
	src := policyJudgementBodySource(t)

	for _, anchor := range []string{
		"mp.IsForbidden(ctx, tenantID, canonical)",      // 租户级（既有）
		"mp.IsForbiddenForOrg(ctx, orgID, canonical)",   // 组织级
		"mp.IsForbiddenForUser(ctx, userID, canonical)", // 用户级
		"mp.ResolveOwner(ctx, owner)",                   // owner 解析
		// 第四级（2026-11-28 L144 轮死列激活）：application（Key）allowlist
		// —— KeyInfo.ApplicationID 直达，无 owner 解析。
		"mp.IsForbiddenForApplication(ctx, int64(keyInfo.ApplicationID), canonical)",
	} {
		if !strings.Contains(src, anchor) {
			t.Errorf("three-tier judgement body missing anchor %q — org/user checks were removed from the shared enforcement body (team_budget_gate regression shape)", anchor)
		}
	}
	// 顺序契约：租户级先于 org/user 级（先判既有语义，再判新层级）。
	if strings.Index(src, "mp.IsForbidden(ctx, tenantID, canonical)") > strings.Index(src, "mp.IsForbiddenForOrg(ctx, orgID, canonical)") {
		t.Error("tenant check must run before org/user checks in the shared enforcement body")
	}
	// 顺序契约：application 级在 org/user 级之后（第四级兜底）。
	if strings.Index(src, "mp.IsForbiddenForUser(ctx, userID, canonical)") > strings.Index(src, "mp.IsForbiddenForApplication(ctx, int64(keyInfo.ApplicationID), canonical)") {
		t.Error("application (Key) check must run after org/user checks in the shared enforcement body")
	}
}

// TestThreeTierModelPolicy_TenantOnlyStillWorks：org/user 解析不可用时
// （owner 为空），判定退化为纯租户级——既有行为零变化。
func TestThreeTierModelPolicy_TenantOnlyStillWorks(t *testing.T) {
	mp := modelpolicy.New(nil)
	keyInfo := &authentication.KeyInfo{TenantID: "acme"} // no OwnerUser

	// nil resolver ⇒ canonical="" ⇒ 整个判定跳过（fail-open，既有口径）。
	denied, canonical, tenantID := enforceTenantModelPolicy(
		context.Background(), "glm-5.1", keyInfo, mp, nil, "openai")
	if denied || canonical != "" || tenantID != "acme" {
		t.Errorf("no-resolver path must fail-open with tenantID carried: got denied=%v canonical=%q tenantID=%q", denied, canonical, tenantID)
	}
}

// TestThreeTierModelPolicy_AutoExempt：model=auto 首查豁免（既有口径）。
func TestThreeTierModelPolicy_AutoExempt(t *testing.T) {
	mp := modelpolicy.New(nil)
	keyInfo := &authentication.KeyInfo{TenantID: "acme"}
	denied, _, tenantID := enforceTenantModelPolicy(
		context.Background(), autoRequestMagic, keyInfo, mp, nil, "openai")
	if denied {
		t.Error("model=auto must be exempt on the first pass")
	}
	if tenantID != "acme" {
		t.Errorf("tenantID must still be carried for auto requests: %q", tenantID)
	}
}

// TestThreeTierModelPolicy_CallSitesAllUseSharedBody：三面 pre-auto
// 调用点（handler/messages/responses）+ post-auto 调用点必须全部经过
// enforceTenantModelPolicy（共用判定体）。源码锚点判据。
func TestThreeTierModelPolicy_CallSitesAllUseSharedBody(t *testing.T) {
	sites := map[string]string{
		"handler.go":   "domains/streaming/handler.go",
		"messages.go":  "domains/streaming/messages.go",
		"responses.go": "domains/streaming/responses.go",
	}
	for name, path := range sites {
		src := readFileSource(t, path)
		if !strings.Contains(src, "enforceTenantModelPolicy(") {
			t.Errorf("%s: pre-auto call site missing — surface no longer enforces model policy", name)
		}
	}
	// post-auto 复查（防 auto 绕过）只在 chat 面（既有形状）。
	handlerSrc := readFileSource(t, "domains/streaming/handler.go")
	if !strings.Contains(handlerSrc, "enforceTenantModelPolicyAfterAuto(") {
		t.Error("handler.go: post-auto recheck missing — auto becomes a bypass vector")
	}
}

// policyJudgementBodySource reads policy.go source for anchor checks.
func policyJudgementBodySource(t *testing.T) string {
	t.Helper()
	return readFileSource(t, "domains/streaming/policy.go")
}

// readFileSource reads a source file relative to the package dir.
// Tests run with the package dir as CWD, so paths are relative to
// domains/streaming/.  Accepts both package-relative ("policy.go")
// and repo-relative ("domains/streaming/policy.go") spellings used
// at call sites, normalising to the basename.
func readFileSource(t *testing.T, relPath string) string {
	t.Helper()
	base := relPath
	if idx := strings.LastIndex(relPath, "/"); idx >= 0 {
		base = relPath[idx+1:]
	}
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read %s: %v", base, err)
	}
	return string(data)
}
