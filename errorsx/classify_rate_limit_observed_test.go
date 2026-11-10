package errorsx

import (
	"net/http"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────
// 本文件是 2026-11-10「审计 F50」的落地门：**限流正文规则 ⇒ 归因到一个
// 「不惩罚节点」的独立 kind**（KindRateLimitObserved）。
//
// ── 为什么要有这个 kind ────────────────────────────────────────────────
//
// 事故本体（2026-11-02，llm.itestu.cn 生产）：上游回了 4xx，而错误体里
// **没有**任何凭据级 / 配额级 / 限流级裁决 ⇒ 落状态兜底 ⇒ KindTransient
// ⇒ 盲重试 + 把 (凭据,模型) 按 "http_5xx" 拉黑 5 分钟 + 3 连败后整凭据
// cooling ⇒ 一个客户端的错单让 /v1/embeddings 对所有租户 503。
//
// ★ 那一轮把「无法再归因的 4xx」收进了 KindRequestRejected（请求侧、
//   不罚节点）。但**限流形态的正文**此前仍落 request_rejected ⇒ 运维读
//   error_kind 只看到「上游拒收了请求」，**看不见**「上游其实在说限流」。
//
// ★ 更早的一条建议是「把限流正文映射到**既有的** KindRateLimit」——
//   **那条方向被否决**：KindRateLimit 驱动**节点健康阶梯**（http_429 ⇒
//   把 (凭据,模型) 拉黑 **且** 参与**连败累计** ⇒ 3 连败整凭据 cooling）
//   ⇒ 那正是本轮要关掉的放大器。
//
// ⇒ 折中（本 kind）：**归因更精确，罚则更轻** ——
//   · 归因：error_kind = "rate_limit_observed"（运维看得见「上游在说限流」）；
//   · 罚则：与 KindRequestRejected **完全同族**（不拉黑、不累计连败、
//     不扇出探针、不进熔断）—— 只换一个兄弟候选。
//
// ★ 线可见性：error_kind 的新字符串 "rate_limit_observed" 会出现在
//   candidate_failure_logs / URSM / 客户端错误信封上 ⇒ 任何按 kind 字符串
//   分档的地方都要认识它（未知取值一律落「既不重试也不惩罚」的兜底）。
// ─────────────────────────────────────────────────────────────────────────

// rateLimitObservedBody 是本轮的核心生产形态（限流措辞 + 请求侧状态码）。
const rateLimitObservedBody = `{"error":{"message":"rate limit exceeded"}}`

// TestRateLimitObserved_ProducedOnRequestSideStatus 是本门的**正向**判据：
// 请求侧状态族（400/404/413/414/422）+ 限流形态正文 ⇒ 新 kind。
//
// ★ 为什么 404 也在内：上游用 404 回「限流」时，它说的**不是**「这个
//   (凭据,模型) 不存在」⇒ 写绑定级冷却（model_unavailable）反而会摘掉
//   一对**还服务得好好的**凭据-模型 ⇒ 归因应当是「限流形态，不罚节点」。
func TestRateLimitObserved_ProducedOnRequestSideStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"400-rate-limit-exceeded", http.StatusBadRequest, rateLimitObservedBody},
		{"404-rate-limit-exceeded", http.StatusNotFound, rateLimitObservedBody},
		{"413-rate-limit-exceeded", http.StatusRequestEntityTooLarge, rateLimitObservedBody},
		{"414-rate-limit-exceeded, status is request-side", http.StatusRequestURITooLong, rateLimitObservedBody},
		{"422-rate-limit-exceeded", http.StatusUnprocessableEntity, rateLimitObservedBody},
		// 词表里的其它形态（同一语义、措辞不同）
		{"400-too-many-requests", http.StatusBadRequest, `{"error":{"message":"too many requests"}}`},
		{"400-rate-limit-underscore", http.StatusBadRequest, `{"error":{"message":"rate_limit_exceeded"}}`},
		{"400-ratelimit-camel", http.StatusBadRequest, `{"error":{"message":"RateLimit exceeded"}}`},
		{"400-throttled", http.StatusBadRequest, `{"error":{"message":"request was throttled"}}`},
		{"400-rpm-limit", http.StatusBadRequest, `{"error":{"message":"rpm limit exceeded"}}`},
		{"400-cjk", http.StatusBadRequest, `{"error":{"message":"请求过于频繁"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if got != KindRateLimitObserved {
				t.Fatalf("ClassifyErrorWithBody(%d, %q) = %q, want %q",
					tc.status, tc.body, got, KindRateLimitObserved)
			}
			// ★ 两个分类器**不得**在同一 (status, body) 上分叉（V20 / R30）。
			//   ClassifyResponseBody 是正文分类器（无状态兜底），本条规则
			//   必须**逐字同门、逐字同判**地出现在它身上。
			if rb := ClassifyResponseBody(tc.status, []byte(tc.body)); rb != KindRateLimitObserved {
				t.Fatalf("ClassifyResponseBody(%d, %q) = %q, want %q（两个分类器分叉了）",
					tc.status, tc.body, rb, KindRateLimitObserved)
			}
		})
	}
}

// TestRateLimitObserved_NotProducedOutsideRequestSideStatus 是本门的
// **阴性对照**（门必须有牙：规则被整个删掉时，本用例也要跟着变）。
//
//	429  ⇒ 仍是 KindRateLimit（限流的**规范**信号）
//	401  ⇒ 仍是 KindAuth（凭据级）
//	403  ⇒ 仍是 KindAuth（凭据级）
//	402  ⇒ 仍是 KindQuota/KindQuotaPermanent（凭据级）
//	5xx  ⇒ 仍是节点级（upstream_down / concurrent / upstream_overloaded）
func TestRateLimitObserved_NotProducedOutsideRequestSideStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		{"429-plain-rate-limit", 429, rateLimitObservedBody, KindRateLimit},
		{"429-too-many-requests", 429, `{"error":{"message":"too many requests"}}`, KindConcurrent},
		{"401-rate-limit-body", 401, rateLimitObservedBody, KindAuth},
		{"403-rate-limit-body", 403, rateLimitObservedBody, KindAuth},
		{"402-rate-limit-body", 402, rateLimitObservedBody, KindQuota},
		{"500-rate-limit-body", 500, rateLimitObservedBody, KindUpstreamDown},
		{"503-rate-limit-body", 503, rateLimitObservedBody, KindConcurrent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("ClassifyErrorWithBody(%d, %q) = %q, want %q（既有口径被抢走了）",
					tc.status, tc.body, got, tc.want)
			}
			if got == KindRateLimitObserved {
				t.Fatalf("status %d 上产出了 %q ⇒ 该状态族的既有裁决被覆盖",
					tc.status, got)
			}
		})
	}
}

// TestRateLimitObserved_UnrelatedBodyUnchanged 钉「规则必须**窄**」：
// 不相关的正文**不得**被吸进新 kind ⇒ 仍是 request_rejected（请求侧）。
//
// ★ 这一条是「低误报」的落点：词表只认**限流语义本体**，泛化措辞
//   （"slow down" / "try again later" / "backoff" / 并发措辞）**不**认。
func TestRateLimitObserved_UnrelatedBodyUnchanged(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"ollama-does-not-support-chat", `{"error":{"message":"\"qwen3-embedding:0.6b\" does not support chat","type":"invalid_request_error"}}`},
		{"plain-bad-request", `{"error":{"message":"bad request"}}`},
		{"slow-down", `{"error":{"message":"slow down"}}`},
		{"try-again-later", `{"error":{"message":"please try again later"}}`},
		{"concurrent-limit", `{"error":{"message":"concurrent limit exceeded for this account"}}`},
		{"cjk-overload", `{"error":{"message":"并发过大，达到上限"}}`},
		{"unrelated-limit-word", `{"error":{"message":"token limit reached"}}`},
		{"quota", `{"error":{"message":"quota exceeded"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(http.StatusBadRequest, []byte(tc.body))
			if got != KindRequestRejected {
				t.Fatalf("ClassifyErrorWithBody(400, %q) = %q, want %q（规则太宽 ⇒ 误报）",
					tc.body, got, KindRequestRejected)
			}
		})
	}
}

// TestRateLimitObserved_DoesNotPunishNode 是本门**最核心**的一条：
// 新 kind ⇒ 不拉黑、不累计连败、不进熔断、不扇出探针。
//
// ★ 归责矩阵（PenaltyOf）是一切共享状态闸门的共同前置 ⇒ 这一条一旦漂，
//   下面所有闸门会同时失效 ⇒ 逐项钉死。
func TestRateLimitObserved_DoesNotPunishNode(t *testing.T) {
	kind := KindRateLimitObserved

	// ① 归责：不罚任何人（不是 binding、不是 credential、不是 provider）。
	p := PenaltyOf(kind)
	if p.Target != PenaltyNone {
		t.Fatalf("PenaltyOf(%s) = %+v, want Target=%q（不罚节点）", kind, p, PenaltyNone)
	}
	if p.Fuse || p.Permanent {
		t.Fatalf("PenaltyOf(%s) = %+v, want Fuse=false Permanent=false", kind, p)
	}
	if got := FailoverScopeOf(p); got != ScopeNone {
		t.Fatalf("FailoverScopeOf(%s) = %q, want %q", kind, got, ScopeNone)
	}

	// ② 判据入口：IsRequestSideError = true ⇒
	//    · nodehealth 的 nodeHealthErrorKind 落 ErrorKindRequest ⇒
	//      effectsFor 返回 nil ⇒ 不写绑定、不记熔断、**不累计连败**；
	//    · domains/credentialstate 的 UpdateOnFailure **早退** ⇒
	//      不累计 consecutive_fails、不扇出主动探针。
	if !IsRequestSideError(kind) {
		t.Fatalf("IsRequestSideError(%s) = false, want true（否则会进「节点不健康」阶梯）", kind)
	}
	// ③ 严重度镜像：请求侧（不是绑定级、不是凭据级）。
	if got := SeverityOf(kind); got != SeverityRequestSide {
		t.Fatalf("SeverityOf(%s) = %d, want %d（请求侧）", kind, got, SeverityRequestSide)
	}
	if IsNodeFaultPenalty(kind) {
		t.Fatalf("IsNodeFaultPenalty(%s) = true, want false（不是节点故障）", kind)
	}

	// ④ 处置动作：换兄弟候选，但**一个共享状态都不写**。
	d := DecideNextAction(DecisionContext{
		Kind:               kind,
		Phase:              PhaseUpstream,
		HasAlternateNode:   true,
		RemainingAttempts:  1,
		MaxSameNodeRetries: 2,
	})
	if d.Action != ActionSwitchNode {
		t.Fatalf("DecideNextAction = %q (reason %q), want %q", d.Action, d.ReasonCode, ActionSwitchNode)
	}
	if d.RecordProviderFailure {
		t.Error("RecordProviderFailure = true, want false（不得记 provider 失败）")
	}
	if d.RecordCredentialFailure {
		t.Error("RecordCredentialFailure = true, want false（不得记凭据失败）")
	}
	if d.RecordCredentialState {
		t.Error("RecordCredentialState = true, want false（不得把凭据推入 cooling）")
	}
	if d.PreserveClientConnection {
		t.Error("PreserveClientConnection = true, want false（不是等待型恢复 ⇒ 不得继承 KindRateLimit 的语义）")
	}

	// ⑤ 自动探针：不被探针捡走（KindRateLimit 是 3 分钟 + 模型粒度）。
	if IsAutomaticProbeEligible(kind) {
		t.Fatal("IsAutomaticProbeEligible = true, want false")
	}
	if p := AutomaticProbePolicyFor(kind); p.Enabled || p.Scope != AutomaticProbeNone {
		t.Fatalf("AutomaticProbePolicyFor(%s) = %+v, want disabled/none", kind, p)
	}

	// ⑥ 同节点盲重试：不开（同一 body 必然再败）。
	if IsRetryable(kind) {
		t.Fatal("IsRetryable = true, want false（同节点盲重试必须关掉）")
	}
	// ⑦ 凭据级致命：不是。
	if IsCredentialFatal(kind) {
		t.Fatal("IsCredentialFatal = true, want false")
	}
	// ⑧ 不是客户端自己的 bug（透传语义与归因语义分开，与 KindRequestRejected 同）。
	if IsClientBug(kind) {
		t.Fatal("IsClientBug = true, want false")
	}
}

// TestRateLimitObserved_DoesNotInheritRateLimitPunishment 是本门的
// **差异**判据：新 kind 与 KindRateLimit 的处置**必须不同** ——
// 否则「归因更精确」就等于「罚则更重」，那正是被否决的方向。
func TestRateLimitObserved_DoesNotInheritRateLimitPunishment(t *testing.T) {
	if PenaltyOf(KindRateLimitObserved) == PenaltyOf(KindRateLimit) {
		t.Fatal("新 kind 与 KindRateLimit 的归责相同 ⇒ 它继承了限流族的罚则")
	}
	if SeverityOf(KindRateLimitObserved) >= SeverityOf(KindRateLimit) {
		t.Fatalf("SeverityOf(new) = %d ≥ SeverityOf(rate_limit) = %d ⇒ 新 kind 不比限流族更轻",
			SeverityOf(KindRateLimitObserved), SeverityOf(KindRateLimit))
	}
	// 恢复投影：请求侧 ⇒ 客户端重试无用 ⇒ 显式登记的排除（不是遗漏）。
	if _, excluded := RecoveryProjectionExclusionReason(KindRateLimitObserved); !excluded {
		t.Fatal("新 kind 不在恢复投影的排除集里 ⇒ 客户端口径与处置口径矛盾")
	}
	if EffectiveRetryable(KindRateLimitObserved) {
		t.Fatal("EffectiveRetryable = true, want false（请求侧拒绝 ⇒ 客户端重试无用）")
	}
}

// TestRateLimitObserved_RegisteredInClosedSet 钉闭集登记：AllErrorKinds 是
// 归责矩阵的登记表 ⇒ 漏登记 ⇒ 归责矩阵、恢复投影门、双分类器锁步门、
// nodehealth 镜像对拍门**全部看不见**这个 kind。
func TestRateLimitObserved_RegisteredInClosedSet(t *testing.T) {
	found := false
	for _, k := range AllErrorKinds() {
		if k == KindRateLimitObserved {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("KindRateLimitObserved 未登记进 AllErrorKinds（闭集登记表）")
	}
	if got := string(KindRateLimitObserved); got != "rate_limit_observed" {
		t.Fatalf("kind 字符串 = %q, want %q（这是**线可见**取值 ⇒ 必须稳定）",
			got, "rate_limit_observed")
	}
}

// TestRateLimitBodyRegex_HasNoBSlashB 钉**可移植性**：本仓已被 BSD libc 的
// regcomp 咬过一次（`\b` 在 BSD libc 里**不是**词边界 ⇒ 语义漂移）。
// ⇒ 本条正则必须用**显式边界**，且**不得**出现 `\b`。
//
// ★ 同时钉住「不得把 Go 专有的语法带进来」（POSIX ERE 没有 `(?:…)`），
//   否则同一段模式在 shell 侧（libc regcomp）上编译不过。
func TestRateLimitBodyRegex_HasNoBSlashB(t *testing.T) {
	pat := rateLimitBodyRe.String()
	if containsSubstring(pat, `\b`) {
		t.Fatalf("rateLimitBodyRe 含有 \\b ⇒ 在 BSD libc 的 regcomp 上**不是**词边界 ⇒ 语义漂移：%s", pat)
	}
	if containsSubstring(pat, `\B`) {
		t.Fatalf("rateLimitBodyRe 含有 \\B ⇒ 同上：%s", pat)
	}
	if containsSubstring(pat, `(?:`) {
		t.Fatalf("rateLimitBodyRe 含有 (?:…) ⇒ POSIX ERE 不支持非捕获组 ⇒ libc 侧编译失败：%s", pat)
	}
	// 显式边界必须真的在用（否则「没用 \b」也可能是「没有边界」）。
	if !containsSubstring(pat, `(^|[^A-Za-z0-9_])`) {
		t.Fatalf("rateLimitBodyRe 缺少左边界 (^|[^A-Za-z0-9_])：%s", pat)
	}
	if !containsSubstring(pat, `([^A-Za-z0-9_]|$)`) {
		t.Fatalf("rateLimitBodyRe 缺少右边界 ([^A-Za-z0-9_]|$)：%s", pat)
	}
}

// TestRateLimitBodyRegex_Strictness 钉正则的**行为**：边界必须真的生效
// （"ratelimiter" 这种**不是**限流词的拼接不得命中）。
func TestRateLimitBodyRegex_Strictness(t *testing.T) {
	mustMatch := []string{
		`{"error":{"message":"rate limit exceeded"}}`,
		`{"error":{"message":"Rate Limit Exceeded"}}`,
		`{"error":{"message":"rate_limit_exceeded"}}`,
		`{"error":{"message":"too many requests"}}`,
		`{"error":{"message":"request was throttled"}}`,
		`{"error":{"message":"请求过于频繁"}}`,
	}
	for _, body := range mustMatch {
		if !rateLimitBodyRe.MatchString(body) {
			t.Errorf("rateLimitBodyRe 应当命中 %q（漏检）", body)
		}
	}
	mustNotMatch := []string{
		`{"error":{"message":"ratelimiter misconfigured"}}`,
		`{"error":{"message":"prerate limit"}}`,
		`{"error":{"message":"token limit reached"}}`,
		`{"error":{"message":"bad request"}}`,
		`{"error":{"message":"please slow down"}}`,
	}
	for _, body := range mustNotMatch {
		if rateLimitBodyRe.MatchString(body) {
			t.Errorf("rateLimitBodyRe 不应命中 %q（误报）", body)
		}
	}
}

// containsSubstring 是本文件的小工具（避免为一个断言引入 strings 依赖面）。
func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
