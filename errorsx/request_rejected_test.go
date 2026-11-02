package errorsx

import (
	"net/http"
	"testing"
)

// 本文件是 2026-11-02「上游 4xx 被记成节点不健康」一轮的核心判据门。
//
// 事故本体（llm.itestu.cn，211 部署，生产日志 request_id 逐环可查）：
//
//	POST /v1/chat/completions {"model":"qwen3-embedding:0.6b"}
//	→ 上游 HTTP 400（Ollama：does not support chat）×3
//	→ executor failed "all 0 candidates failed"
//
// 那 3 次盲重试（5s/10s 退避）刚好凑满 node-health reducer 的 3 连败阈值
// ⇒ EffectSetCredentialUnavailable ⇒ **整个凭据**（其下全部 binding、
// 全部租户）被置 cooling ⇒ /v1/embeddings 对所有租户 503。
//
// 根因在分类层：那个 400 的正文**不匹配**仓内任何一条 body 形态正则 ⇒
// 落到 ClassifyResponseStatus 的 default ⇒ KindTransient ⇒ 归责矩阵给
// PenaltyBinding ⇒ 全部共享节点健康状态的闸门对它一律放行。
//
// 本轮把这类「上游 4xx 且无任何模型级/凭据级/配额级/限流级裁决」归入
// KindRequestRejected（请求侧错误族），并把「是否惩罚节点」的判据收敛到
// IsRequestSideError 一个入口。
//
// ★ 本文件只管**分类与归责**；各调用点的闸门由各自包里的测试钉
//   （executors / dispatch / streaming / credentialstate）。

const (
	// ollamaDoesNotSupportChatBody 是生产实采的上游 400 正文
	// （211 部署，本机 ollama provider 19，2026-11-02 实测）：
	//
	//	curl -sS -o /tmp/old.json -w "http=%{http_code}\n" \
	//	  http://192.168.31.30:11434/v1/chat/completions \
	//	  -H 'Content-Type: application/json' \
	//	  -d '{"model":"qwen3-embedding:0.6b","messages":[{"role":"user","content":"ping"}],"max_tokens":32,"stream":false}'
	//	⇒ http=400
	//	{"error":{"message":"\"qwen3-embedding:0.6b\" does not support chat","type":"invalid_request_error","param":null,"code":null}}
	//
	// 它不匹配 classify.go 里的任何一条 body 正则（逐条核对过：
	// modelNotFoundRe 要 "not found|does not exist"、unsupportedFeatureRe 的
	// 词表没有 "chat"、invalidRequestFormatRe 要 "invalid_request_format"…）
	// ⇒ 此前只能落状态兜底。
	ollamaDoesNotSupportChatBody = `{"error":{"message":"\"qwen3-embedding:0.6b\" does not support chat","type":"invalid_request_error","param":null,"code":null}}`

	// 事故当天从生产日志抄下的上游 400 正文（另一形态）。
	ollamaPlainBadRequestBody = `{"error":{"message":"bad request"}}`
)

// TestClassifyErrorWithBody_Unattributed4xxIsRequestRejected 钉住本轮的核心
// 分类口径：上游回了 4xx，而错误体里没有任何模型级/凭据级/配额级/限流级/
// 上下文级/审核级裁决 ⇒ 请求侧错误（KindRequestRejected）。
func TestClassifyErrorWithBody_Unattributed4xxIsRequestRejected(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		// 生产实采的 Ollama 正文（事故本体）。
		{"ollama-does-not-support-chat", http.StatusBadRequest, ollamaDoesNotSupportChatBody},
		{"ollama-plain-bad-request", http.StatusBadRequest, ollamaPlainBadRequestBody},
		// 空正文也要落到同一个 kind（正文没给出任何线索 ⇒ 同样无法归因）。
		{"400-empty-body", http.StatusBadRequest, ""},
		{"413-payload-too-large", http.StatusRequestEntityTooLarge, `{"error":"payload too large"}`},
		{"422-unprocessable", http.StatusUnprocessableEntity, `unprocessable entity`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if got != KindRequestRejected {
				t.Fatalf("ClassifyErrorWithBody(%d, %q) = %q, want %q",
					tc.status, tc.body, got, KindRequestRejected)
			}
		})
	}
}

// TestClassifyErrorWithBody_RequestRejectedDoesNotStealVerdicts 是**负控**：
// 新 kind 只能接管「无裁决」的 4xx。凡是上游**明确给出了**模型级/凭据级/
// 配额级/限流级/上下文级/审核级裁决的形态，都必须仍然由各自的 kind 接管。
//
// 这一门的作用是钉住「新 kind 只补空白、不吃掉既有裁决」——否则一次改动
// 就可能把「模型不存在」重新说成「请求被拒」，从而丢掉绑定级冷却与
// 模型级故障转移。
func TestClassifyErrorWithBody_RequestRejectedDoesNotStealVerdicts(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		// 模型级：模型不存在 / 已下线
		// 注意：404 必须带**模型名**才判 model_not_found（modelNotFoundRe 的
		// 词形要求「model <名字> not found」）；裸 404 正文一直落 transient
		// （既有口径，见 classify.go 的 modelNotFoundRe 注释）。
		{"404-model-not-found", 404, `{"error":{"message":"model gpt-5 is not found"}}`, KindModelNotFound},
		{"400-model-not-found", 400, `{"error":{"message":"model gpt-5 is not found"}}`, KindModelNotFound},
		{"400-model-deprecated", 400, `{"error":{"message":"model gpt-4 has been deprecated"}}`, KindModelDeprecated},
		// 凭据级：鉴权 / 配额
		{"401-auth", 401, `{"error":"unauthorized"}`, KindAuth},
		{"403-auth", 403, `{"error":"forbidden"}`, KindAuth},
		// 「余额不足」命中 budgetExceededRe ⇒ quota_permanent（既有口径：
		// 永久余额耗尽罚账号级，且要立刻踢开这把钥匙）。
		{"402-quota", 402, `{"error":{"message":"insufficient balance"}}`, KindQuotaPermanent},
		// 限流：正文命中 concurrentOverloadRe ⇒ concurrent（既有口径；
		// 429 的纯限流走 ClassifyResponseStatus ⇒ rate_limit）。
		{"429-too-many-requests", 429, `{"error":{"message":"too many requests"}}`, KindConcurrent},
		{"429-plain-rate-limit", 429, `{"error":{"message":"rate limit exceeded"}}`, KindRateLimit},
		// 上下文长度 / 内容审核
		{"400-context-length", 400, `{"error":{"message":"This model's maximum context length is 8192 tokens."}}`, KindContextLength},
		{"400-content-filter", 400, `{"error":{"message":"content_filter: blocked"}}`, KindContentFilter},
		// 请求形态族（client_bug / unsupported_feature）——它们有各自的处置通道
		{"400-unsupported-parameter", 400, `{"error":{"message":"Unsupported parameter: 'temperature' is not supported"}}`, KindClientBug},
		{"400-unsupported-value", 400, `{"error":{"code":"unsupported_value","message":"Unsupported value: 'reasoning_effort'"}}`, KindClientBug},
		{"400-does-not-support-tools", 400, `{"error":{"message":"This model does not support tools"}}`, KindUnsupportedFeature},
		// 传输层与时序
		{"408-timeout", 408, `request timeout`, KindTimeout},
		{"500-upstream-down", 500, `internal server error`, KindUpstreamDown},
		{"503-concurrent", 503, `{"error":{"message":"overloaded"}}`, KindConcurrent},
		// 裸 410 的既有口径不变（2026-08-05 决议：走 transient 以便换兄弟凭据）
		{"410-gone", 410, `gone`, KindTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyErrorWithBody(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("ClassifyErrorWithBody(%d, %q) = %q, want %q（既有裁决被新 kind 抢走了）",
					tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestPenaltyOfRequestRejected 钉归责：请求侧错误不罚节点。
//
// PenaltyNone 是本轮所有共享状态闸门的共同前置（凭据冷却、熔断计数、
// 绑定翻红……），所以这一条一旦漂，下面所有闸门都会同时失效。
func TestPenaltyOfRequestRejected(t *testing.T) {
	p := PenaltyOf(KindRequestRejected)
	if p.Target != PenaltyNone {
		t.Fatalf("PenaltyOf(KindRequestRejected).Target = %q, want %q", p.Target, PenaltyNone)
	}
	if p.Fuse || p.Permanent {
		t.Fatalf("PenaltyOf(KindRequestRejected) = %+v, want Fuse=false Permanent=false", p)
	}
	if got := FailoverScopeOf(p); got != ScopeNone {
		t.Fatalf("FailoverScopeOf = %q, want %q", got, ScopeNone)
	}
}

// TestRequestRejectedIsRequestSideError 钉判据入口：
//
//   - IsRequestSideError（是否惩罚节点）—— 新 kind 必须在内；
//   - IsClientBug（是否是客户端自己的 bug / 是否原样透传上游正文）——
//     新 kind **不在**内：透传与否是客户端体验问题，与归因分开（见
//     errorsx/classify.go 里 IsRequestSideError 的注释）；
//   - IsRetryable（同节点盲重试）—— 新 kind **不在**内：同一 body 必然再败。
func TestRequestRejectedIsRequestSideError(t *testing.T) {
	if !IsRequestSideError(KindRequestRejected) {
		t.Fatal("IsRequestSideError(KindRequestRejected) = false, want true")
	}
	if IsClientBug(KindRequestRejected) {
		t.Fatal("IsClientBug(KindRequestRejected) = true, want false（透传语义要与归因语义分开）")
	}
	if IsRetryable(KindRequestRejected) {
		t.Fatal("IsRetryable(KindRequestRejected) = true, want false（同节点盲重试必须消失）")
	}
	// 既有成员不得因为新增判据而掉出去。
	for _, k := range []ErrorKind{KindClientBug, KindToolCallIdMismatch, KindUnsupportedFeature, KindCanceled} {
		if !IsRequestSideError(k) {
			t.Fatalf("IsRequestSideError(%q) = false, want true（既有成员被挤掉了）", k)
		}
	}
	// 节点侧故障族必须在判据之外。
	for _, k := range []ErrorKind{KindTransient, KindNetwork, KindTimeout, KindAuth, KindQuota, KindRateLimit, KindModelNotFound} {
		if IsRequestSideError(k) {
			t.Fatalf("IsRequestSideError(%q) = true, want false", k)
		}
	}
}

// TestDecideNextActionRequestRejected 钉动作：换兄弟候选，但一个共享状态
// 都不写。
//
// 本轮最容易复发的是「把新 kind 顺手塞进 retryableActionKind」——那会重新
// 打开盲重试 + 探针扇出。本门逐条钉住四个副作用开关。
func TestDecideNextActionRequestRejected(t *testing.T) {
	d := DecideNextAction(DecisionContext{
		Kind:               KindRequestRejected,
		Phase:              PhaseUpstream,
		HasAlternateNode:   true,
		RemainingAttempts:  1,
		MaxSameNodeRetries: 2,
	})
	if d.Action != ActionSwitchNode {
		t.Fatalf("action = %q, want %q（要换兄弟候选：约 44%% 的在册模型由 ≥2 个凭据服务）", d.Action, ActionSwitchNode)
	}
	if d.RecordProviderFailure {
		t.Error("RecordProviderFailure = true, want false（不得记 provider 失败）")
	}
	if d.RecordCredentialFailure {
		t.Error("RecordCredentialFailure = true, want false")
	}
	if d.RecordCredentialState {
		t.Error("RecordCredentialState = true, want false（不得把凭据推入 cooling）")
	}
	if d.PreserveClientConnection {
		t.Error("PreserveClientConnection = true, want false（不是等待型恢复）")
	}

	// 没有兄弟候选 ⇒ 终态失败（语义仍是「这次请求失败」）。
	noAlt := DecideNextAction(DecisionContext{
		Kind:               KindRequestRejected,
		Phase:              PhaseUpstream,
		HasAlternateNode:   false,
		RemainingAttempts:  1,
		MaxSameNodeRetries: 2,
	})
	if noAlt.Action != ActionFailTerminal {
		t.Fatalf("action without alternate node = %q, want %q", noAlt.Action, ActionFailTerminal)
	}

	// 没有尝试预算 ⇒ 终态失败。
	noBudget := DecideNextAction(DecisionContext{
		Kind:               KindRequestRejected,
		Phase:              PhaseUpstream,
		HasAlternateNode:   true,
		RemainingAttempts:  0,
		MaxSameNodeRetries: 2,
	})
	if noBudget.Action != ActionFailTerminal {
		t.Fatalf("action without attempt budget = %q, want %q", noBudget.Action, ActionFailTerminal)
	}
}

// TestAutomaticProbePolicyRequestRejected 钉「新 kind 不会被自动探针捡走」：
// AutomaticProbePolicyFor 的 default 分支就是 AutomaticProbeNone，本门
// 钉住这个既得行为不被改动。
func TestAutomaticProbePolicyRequestRejected(t *testing.T) {
	if IsAutomaticProbeEligible(KindRequestRejected) {
		t.Fatal("IsAutomaticProbeEligible(KindRequestRejected) = true, want false")
	}
	p := AutomaticProbePolicyFor(KindRequestRejected)
	if p.Enabled || p.Scope != AutomaticProbeNone {
		t.Fatalf("AutomaticProbePolicyFor(KindRequestRejected) = %+v, want disabled/none", p)
	}
}

// TestRequestRejectedRegisteredInClosedSet 钉闭集登记：AllErrorKinds 是
// 归责矩阵的登记表，漏登记 ⇒ TestPenaltyOfClosedSet 会红（这是仓内既有
// 的闭集门），本门额外把「kind 必须在册」写成显式断言，避免有人只改
// 表不改断言。
func TestRequestRejectedRegisteredInClosedSet(t *testing.T) {
	found := false
	for _, k := range AllErrorKinds() {
		if k == KindRequestRejected {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("KindRequestRejected 未登记进 AllErrorKinds（闭集登记表）")
	}
	if HTTPStatusForKind(KindRequestRejected) < 400 {
		t.Fatalf("HTTPStatusForKind(KindRequestRejected) = %d, want >= 400",
			HTTPStatusForKind(KindRequestRejected))
	}
}
