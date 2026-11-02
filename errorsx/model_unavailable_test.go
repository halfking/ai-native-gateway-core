package errorsx

import (
	"net/http"
	"testing"
)

// 本文件钉住 2026-11-02「4xx 归因族收口轮」的两件事：
//
//	① **绑定级**这一新档位（KindModelUnavailable）：裸 404 = 「上游确定性地
//	   不提供这个 (凭据,模型) 对」。它是第三类处置——既不是请求侧、也不是
//	   节点临时故障。
//	② **4xx 族不得因正文形态落进节点侧罚则**（并发/过载/限流形态的正文）。
//
// ── 为什么需要第 ③ 类 ────────────────────────────────────────────────
//
// 处置矩阵（2026-11-02 收口后）：
//
//	┌──────────────────┬───────────────┬──────────────┬──────────────┐
//	│ 处置             │ 证据指向       │ 写什么        │ 跨凭据转移   │
//	├──────────────────┼───────────────┼──────────────┼──────────────┤
//	│ 请求侧           │ 这次请求       │ 什么都不写    │ 保留         │
//	│ (request_rejected)│              │              │              │
//	├──────────────────┼───────────────┼──────────────┼──────────────┤
//	│ 绑定级           │ 这一对         │ 只写这一对    │ 保留         │
//	│ (model_unavailable)│              │ 的冷却        │              │
//	├──────────────────┼───────────────┼──────────────┼──────────────┤
//	│ 节点/凭据级      │ 节点/账号      │ 罚节点/账号   │ 视 kind      │
//	│ (transient/auth/…)│               │              │              │
//	└──────────────────┴───────────────┴──────────────┴──────────────┘
//
// ★ 「绑定级」这一档此前**不存在**：裸 404 只能落请求侧（什么都不写）或
//   节点侧（罚节点 ⇒ 3 连败整凭据 cooling ⇒ 全租户 503，2026-11-02 事故本体）。
//   它也**不能**借用 KindModelNotFound——那是终态语义（terminalActionKind）
//   ⇒ 放弃跨凭据故障转移 ⇒ 兄弟凭据本来能服务同一个模型时也被判死。
//
// ── 生产证据（211 candidate_failure_logs，45 天窗口，2026-11-02 复核）──
//
//	404 + error_kind='transient'：20 行 / 6 个 (凭据,模型) 对 / 7 次独立事件
//	  · attempt_index 1→2→3，同一对三次时间戳相隔 6s/11s ⇒ 同节点盲重试坐实
//	  · 三种正文形态全部是确定性、绑定级失败：
//	      · `404 page not found`                     （端点路径/路由错）
//	      · `Function id … is not found`             （账号里没有这个部署）
//	      · `openai_error / bad_response_status_code`（中转把上游错误包成 404）
//	  · 复核（2026-11-02）：6 对里 5 对至今 last_direct_ok=false /
//	    last_err_code='http_404' / consecutive_failures 9~21，仍被探针阶梯
//	    反复重探；第 6 对（nemotron-3-embed-1b）已被探针判活并恢复可路由。

// ── ① 分类 ────────────────────────────────────────────────────────────

// TestModelUnavailableClassification 钉「裸 404 ⇒ 绑定级」。
func TestModelUnavailableClassification(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty-body", ""},
		{"generic-web-404", `404 page not found`},
		{"nimbus-function-not-found", `{"status":404,"title":"Not Found","detail":"Function id '9fa6fd04' version 'null': Specified function in account 'x' is not found"}`},
		{"relay-wrapped", `{"error":{"message":"openai_error","type":"bad_response_status_code","code":null}}`},
		{"unknown-provider", `{"error":{"message":"unknown provider for model grok-4.6","type":"invalid_request_error"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(404, []byte(tc.body))
			if got != KindModelUnavailable {
				t.Fatalf("ClassifyErrorWithBody(404, %q) = %q, want %q", tc.body, got, KindModelUnavailable)
			}
		})
	}
}

// TestModelUnavailableDoesNotStealVerdicts 钉「正文裁决优先」：
// 404 的**正文**若已经给出模型级 / 弃用级裁决，仍走既有 kind ——
// 本轮只补「正文无裁决」的那一格空白。
func TestModelUnavailableDoesNotStealVerdicts(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ErrorKind
	}{
		{"model-not-found", `{"error":{"message":"model gpt-5 is not found"}}`, KindModelNotFound},
		{"model-deprecated", `{"error":{"message":"model gpt-4 has been deprecated"}}`, KindModelDeprecated},
		{"model-not-exist", `{"error":{"message":"The model 'gpt-5-fake' does not exist","type":"invalid_request_error"}}`, KindModelNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyErrorWithBody(404, []byte(tc.body)); got != tc.want {
				t.Fatalf("ClassifyErrorWithBody(404, %q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestModelUnavailableRespBranchMatchesBodyAware 钉两个分类器不分叉：
// ClassifyError 的 resp 分支委托给 ClassifyErrorWithBody ⇒ 同一状态码
// 在两条路径上必须给出同一个 kind。
func TestModelUnavailableRespBranchMatchesBodyAware(t *testing.T) {
	got := ClassifyError(nil, &http.Response{StatusCode: 404})
	if got != KindModelUnavailable {
		t.Fatalf("ClassifyError(resp 404) = %q, want %q", got, KindModelUnavailable)
	}
}

// ── ② 归责与动作 ──────────────────────────────────────────────────────

// TestModelUnavailableAttribution 钉「绑定级」这一档的全部归责性质。
//
// 逐条对应事故放大链的每一环：
//
//	① IsRetryable = false        ⇒ 同节点盲重试不会发生
//	② IsRequestSideError = false ⇒ 它**要**写绑定级状态（请求侧不写）
//	③ PenaltyOf = PenaltyBinding ⇒ 只罚这一对，**不**罚账号
//	④ Fuse = false               ⇒ 不进账号级熔断
//	⑤ 非终态 + ActionSwitchNode  ⇒ 跨凭据故障转移保留
//	⑥ IsClientBug = false        ⇒ 透传语义与归因语义分开（既有口径不动）
func TestModelUnavailableAttribution(t *testing.T) {
	if IsRetryable(KindModelUnavailable) {
		t.Error("IsRetryable(KindModelUnavailable) = true ⇒ 同节点盲重试会被重新打开")
	}
	if IsRequestSideError(KindModelUnavailable) {
		t.Error("IsRequestSideError(KindModelUnavailable) = true ⇒ 绑定级处置被降级成请求侧，" +
			"该对不会被拉黑（§六.7 登记的缺口原样回来）")
	}
	if IsClientBug(KindModelUnavailable) {
		t.Error("IsClientBug(KindModelUnavailable) = true ⇒ 上游正文会被原样透传给客户端，" +
			"归因语义与客户端体验语义必须分开")
	}
	pen := PenaltyOf(KindModelUnavailable)
	if pen.Target != PenaltyBinding {
		t.Errorf("PenaltyOf(KindModelUnavailable).Target = %q, want %q（只罚这一对；"+
			"罚账号 ⇒ 兄弟模型连坐 ⇒ 全租户 503）", pen.Target, PenaltyBinding)
	}
	if pen.Fuse {
		t.Error("PenaltyOf(KindModelUnavailable).Fuse = true ⇒ 会进账号级熔断语义")
	}
	if pen.Permanent {
		t.Error("PenaltyOf(KindModelUnavailable).Permanent = true ⇒ 会被永久隔离，" +
			"而绑定级故障应当可由探针阶梯自愈")
	}
	if terminalActionKind(KindModelUnavailable) {
		t.Error("terminalActionKind(KindModelUnavailable) = true ⇒ 放弃跨凭据故障转移")
	}
}

// TestModelUnavailableKeepsCrossCredentialFailover 钉「换兄弟候选」。
func TestModelUnavailableKeepsCrossCredentialFailover(t *testing.T) {
	decision := DecideNextAction(DecisionContext{
		Kind:              KindModelUnavailable,
		HasAlternateNode:  true,
		RemainingAttempts: 3,
	})
	if decision.Action != ActionSwitchNode {
		t.Fatalf("DecideNextAction = %q (reason %q), want %q",
			decision.Action, decision.ReasonCode, ActionSwitchNode)
	}
	if decision.RecordCredentialFailure {
		t.Error("RecordCredentialFailure = true ⇒ 账号被连坐（绑定级证据不指向账号）")
	}
	if decision.RecordCredentialState {
		t.Error("RecordCredentialState = true ⇒ 账号状态被改写")
	}
}

// TestModelUnavailableWithoutAlternateNodeFailsTerminal 钉收尾：
// 没有兄弟候选时降级为终态（语义仍是「这次请求失败」）。
func TestModelUnavailableWithoutAlternateNodeFailsTerminal(t *testing.T) {
	decision := DecideNextAction(DecisionContext{
		Kind:              KindModelUnavailable,
		HasAlternateNode:  false,
		RemainingAttempts: 3,
	})
	if decision.Action != ActionFailTerminal {
		t.Fatalf("DecideNextAction = %q (reason %q), want %q",
			decision.Action, decision.ReasonCode, ActionFailTerminal)
	}
}

// ── ③ 4xx 族不得因正文形态落进节点侧罚则 ──────────────────────────────

// TestRequestRejectionStatusIgnoresOverloadBody 钉本轮关掉的那条放大链。
//
// ★ 事故链的第三段是「node-health reducer 累计连败 ⇒ 3 连败 ⇒
//
//	EffectSetCredentialUnavailable ⇒ 整凭据 cooling ⇒ 全租户 503」。
//	那条链的入口是 **KindConcurrent**（nodehealth 里映射成 ErrorKindRateLimit，
//	非永久、但连败计数照样累计）。此前并发/过载正文规则**没有状态门** ⇒
//	一个 4xx 正文只要带「too many requests / rate limit / 并发过大」就落
//	KindConcurrent ⇒ 换一副正文面孔，事故原样回来。
//
// ★ 口径：**状态码是上游对「这次请求」的裁决**。4xx = 上游拒收 ⇒ 按 2026-11-02
//
//	第三轮的裁决（§三 ①：上游 4xx 不得进入「节点不健康」阶梯）不得罚节点。
//	429 / 5xx 才是容量/节点级信号，规则照旧。
func TestRequestRejectionStatusIgnoresOverloadBody(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		// 请求侧族：4xx + 并发/限流形态正文 ⇒ 请求侧（不罚节点）
		{"400-too-many-requests", 400, `{"error":{"message":"too many requests"}}`, KindRequestRejected},
		{"400-rate-limit-exceeded", 400, `{"error":{"message":"rate limit exceeded"}}`, KindRequestRejected},
		{"400-slow-down", 400, `{"error":{"message":"slow down"}}`, KindRequestRejected},
		{"400-concurrent-limit", 400, `{"error":{"message":"concurrent limit exceeded for this account"}}`, KindRequestRejected},
		{"413-too-many-requests", 413, `{"error":{"message":"too many requests"}}`, KindRequestRejected},
		{"414-too-many-requests", 414, `{"error":{"message":"too many requests"}}`, KindRequestRejected},
		{"422-too-many-requests", 422, `{"error":{"message":"too many requests"}}`, KindRequestRejected},
		// 绑定级族：404 + 同款正文 ⇒ 绑定级（仍不罚节点）
		{"404-too-many-requests", 404, `{"error":{"message":"too many requests"}}`, KindModelUnavailable},
		{"404-cjk-overload", 404, `{"error":{"message":"并发过大，达到上限"}}`, KindModelUnavailable},
		{"400-cjk-overload", 400, `{"error":{"message":"并发过大，达到上限"}}`, KindRequestRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("ClassifyErrorWithBody(%d, %q) = %q, want %q",
					tc.status, tc.body, got, tc.want)
			}
			if got == KindConcurrent || got == KindRateLimit {
				t.Fatalf("4xx %d + 并发/限流形态正文落成了 %q ⇒ 节点侧罚则被打开（"+
					"3 连败 ⇒ 整凭据 cooling ⇒ 全租户 503）", tc.status, got)
			}
		})
	}
}

// TestNonRequestRejectionStatusKeepsOverloadBody 钉「只关 4xx，不动其余」：
// 429 / 5xx 的并发与过载语义必须**逐字不变**（限流族的全部处置都挂在 429 上；
// credentialhealth 的并发棘轮也靠 429/503 触发）。
func TestNonRequestRejectionStatusKeepsOverloadBody(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		{"429-too-many-requests", 429, `{"error":{"message":"too many requests"}}`, KindConcurrent},
		{"429-concurrent-limit", 429, `{"error":{"message":"concurrent limit exceeded for this account"}}`, KindConcurrent},
		{"429-plain-rate-limit", 429, `{"error":{"message":"rate limit exceeded"}}`, KindRateLimit},
		{"503-engine-busy", 503, `{"error":{"message":"engine busy, please retry"}}`, KindConcurrent},
		{"500-overloaded", 500, `{"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"upstream_error"}}`, KindUpstreamOverloaded},
		{"529-overloaded", 529, `{"error":{"message":"overloaded"}}`, KindConcurrent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyErrorWithBody(tc.status, []byte(tc.body)); got != tc.want {
				t.Fatalf("ClassifyErrorWithBody(%d, %q) = %q, want %q（既有口径不得变）",
					tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestAuthAndQuotaStatusKeepBodyRules 钉「没有把 401/403/402 的正文规则一起关掉」。
//
// ★ 这是本轮最容易误伤的一格：403 + 余额不足正文必须仍然是配额（凭据级），
//
//	403 + 鉴权正文必须仍然是鉴权（凭据级）——它们是**凭据级证据**，
//	与 4xx 请求侧族不是一回事。
func TestAuthAndQuotaStatusKeepBodyRules(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		{"403-insufficient-balance", 403, `{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`, KindQuotaPermanent},
		{"429-quota-exhausted", 429, `{"error":{"message":"quota exhausted"}}`, KindQuotaPermanent},
		{"403-forbidden", 403, `{"error":{"message":"forbidden"}}`, KindAuth},
		{"401-unauthorized", 401, `{"error":{"message":"unauthorized"}}`, KindAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyErrorWithBody(tc.status, []byte(tc.body)); got != tc.want {
				t.Fatalf("ClassifyErrorWithBody(%d, %q) = %q, want %q（凭据级口径不得变）",
					tc.status, tc.body, got, tc.want)
			}
		})
	}
}
