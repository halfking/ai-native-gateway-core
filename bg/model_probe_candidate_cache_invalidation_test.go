package bg

// model_probe_candidate_cache_invalidation_test.go —— legacy 探针栈的
// 「判定变了 ⇒ 候选缓存作废」守卫（2026-11-11 缓存与路由状态即时同步轮）。
//
// ★ 为什么要给**legacy 栈**单独钉一条：
//   新探针栈（bg/node_probe.go）早就有「探针翻判定 ⇒ 清候选缓存」的注入点，
//   而 legacy 栈（bg/model_probe.go）**一次都没有** ⇒ ⇒ 同一个缺陷只修了一半。
//   ⇒ 而 legacy 栈在新模式下**也是活的**：consensus 循环被跳过，但
//   **TriggerManual（管理端「立即探测」）走的是同一条 applyResult** ⇒ ⇒
//   运维点一次探测、上游确实挂了 ⇒ 绑定被标成不可用 ⇒ ⇒ 而候选缓存没被清
//   ⇒ ⇒ 已被判死的桶还能再被选中最多 30s（candidateCacheTTL）
//   ⇒ ⇒ 表现就是「路由总是命中不可用或失败的供应商桶」。
//
// ★ 本守卫分两层（★ 缺一层就是缺覆盖）：
//   ① **行为层**：注入点的语义（装得上、调得动、nil 安全、0 不调）。
//   ② **调用点层**：applyResult 的**两个状态翻转臂**都必须触发失效 ——
//      ★ 这一层用**源码断言**（★ 仓内既有惯例：bg/node_probe_test.go 的
//      TestRunOneSuccessInvokesInvalidateAndNotify 同款）⇒ ★ 因为真实写库
//      路径需要 PG ⇒ ⇒ 单测里跑不了 ⇒ ⇒ 只能用源码面把「调用点接对了」钉住。

import (
	"os"
	"strings"
	"testing"
)

// TestModelProbeInvalidateCandidateCacheHookSemantics 钉「注入点」的语义。
func TestModelProbeInvalidateCandidateCacheHookSemantics(t *testing.T) {
	r := NewModelProbeRunner(nil, nil)

	// nil 注入 ⇒ 不得 panic（单测 / 非网关进程照旧工作）。
	r.invalidateCandidateCacheForCredential(42)

	got := 0
	r.SetInvalidateCandidateCache(func(credentialID int) { got = credentialID })
	r.invalidateCandidateCacheForCredential(42)
	if got != 42 {
		t.Fatalf("invalidate callback got credential %d, want 42", got)
	}

	// credentialID == 0 ⇒ 不该调用（0 不是合法凭据 ID）。
	got = 0
	r.invalidateCandidateCacheForCredential(0)
	if got != 0 {
		t.Fatalf("invalidate callback was called for credential 0 (got %d)", got)
	}
}

// TestModelProbeApplyResultSkipsInvalidationWhenStateUnchanged 钉
// 「状态没变 ⇒ 不许清缓存」：
// applied=false 的早退分支**不得**触发失效 ⇒ 否则会变成「每次探测都清缓存」
// 的抖动源（缓存失效 → 回源 DB → 压力上移）。
func TestModelProbeApplyResultSkipsInvalidationWhenStateUnchanged(t *testing.T) {
	r := NewModelProbeRunner(nil, nil)

	var invalidated []int
	r.SetInvalidateCandidateCache(func(credentialID int) {
		invalidated = append(invalidated, credentialID)
	})

	// applied=false ⇒ applyResult 早退（不写库 ⇒ 不需要 DB）⇒ 不该触发失效。
	r.applyResult(t.Context(), probeTarget{CredentialID: 7, RawModel: "glm-5.3-flash"},
		"error", nil, "http_429", "too many requests", 10,
		"unchanged", false, "test", 0, 1, "broken_confirmed")
	if len(invalidated) != 0 {
		t.Fatalf("applied=false triggered candidate cache invalidation: %v", invalidated)
	}
}

// TestModelProbeApplyResultInvalidatesCandidateCacheOnBindingFlip 钉**调用点**：
// applyResult 的两个绑定可用性翻转臂（broken_confirmed / healthy_confirmed）
// 都必须触发候选缓存失效。
//
// ★ 为什么用源码断言（★ 仓内既有惯例，见 bg/node_probe_test.go 同款）：
//   真实写库路径要 PG ⇒ 单测跑不了 ⇒ ⇒ 只测「注入点存在」会漏掉
//   「臂里没接上」这一种最常见的漏接 ⇒ ⇒ 必须把**臂里的调用**钉住。
func TestModelProbeApplyResultInvalidatesCandidateCacheOnBindingFlip(t *testing.T) {
	src, err := os.ReadFile("model_probe.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)

	// (1) 两个臂都必须调。
	if n := strings.Count(body, "r.invalidateCandidateCacheForCredential(t.CredentialID)"); n != 2 {
		t.Fatalf("model_probe.go must call r.invalidateCandidateCacheForCredential(t.CredentialID) "+
			"in BOTH binding-availability arms (broken_confirmed + healthy_confirmed); found %d call(s)", n)
	}

	// (2) 调用必须在写库之后（★ 否则「写库失败但缓存已清」⇒ 白清一次缓存，
	//     且下一次回源又拿到同样的旧计划 ⇒ ⇒ 无谓的抖动）。
	brokenIdx := strings.Index(body, `unavailable_reason = 'model_probe_broken'`)
	healthyIdx := strings.Index(body, `unavailable_reason = NULL`)
	if brokenIdx < 0 || healthyIdx < 0 {
		t.Fatalf("model_probe.go binding-availability UPDATE blocks not found "+
			"(broken_confirmed / healthy_confirmed arms) — update bg/model_probe.go:applyResult")
	}
	firstInvalidate := strings.Index(body, "r.invalidateCandidateCacheForCredential(t.CredentialID)")
	if firstInvalidate < 0 {
		t.Fatalf("model_probe.go must call r.invalidateCandidateCacheForCredential(t.CredentialID)")
	}
	if firstInvalidate < brokenIdx {
		t.Fatalf("candidate cache invalidation must run AFTER the binding UPDATE "+
			"(found invalidation at %d, broken_confirmed UPDATE at %d)", firstInvalidate, brokenIdx)
	}
	secondInvalidate := strings.LastIndex(body, "r.invalidateCandidateCacheForCredential(t.CredentialID)")
	if secondInvalidate < healthyIdx {
		t.Fatalf("candidate cache invalidation must run AFTER the binding UPDATE "+
			"(found invalidation at %d, healthy_confirmed UPDATE at %d)", secondInvalidate, healthyIdx)
	}

	// (3) 注入点必须被**守卫**（nil 安全）⇒ ⇒ 摘掉 setter 时 runner 照旧工作。
	if !strings.Contains(body, "if r == nil || r.invalidateCandidateCache == nil || credentialID == 0 {") {
		t.Fatalf("invalidateCandidateCacheForCredential must be nil-guarded "+
			"(r == nil || r.invalidateCandidateCache == nil || credentialID == 0)")
	}
}
