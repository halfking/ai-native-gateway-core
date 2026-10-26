// classify_quota_code_characterization_test.go —— R149 轮（2026-10-27）
//
// ★★★ 本文件是【刻画测试】(characterization test)，不是「期望行为」测试。
// 它把**生产实测到的错误归责链路**钉死，使下一个人不必重新推导；
// 若属主批准改判（见审计档 §八 D1），本文件与 classify.go 必须**同批翻转**。
//
// ── 实测来源（本地 8782 网关，provider 2453 / credential 1 / deepseek-v4-flash）
// ────────────────────────────────────────────────────────────────────────────
// 网关日志原文（2026-10-25 04:09:59，request_id=57e2c2c087b097c5729cf8bc6aee9fe6）：
//
//	"all 3 candidates failed: [concurrent] upstream 429:
//	 {"error":{"message":"inference exceeds tpm/rpm limit",
//	           "type":"rate_limit_error","code":"insufficient_quota"}}"
//
// ★ 三个上游实测事实：
//   (1) HTTP 429；
//   (2) body 的 code 字段是 OpenAI 标准的 **insufficient_quota**（配额/计费耗尽语义）；
//   (3) message 里含 "tpm/rpm limit" ⇒ 命中 concurrentOverloadRe 的
//       `(rpm|tpm).{0,20}(limit|exceed|reach|over)` 分支。
//
// ⇒ 分类器当前把它判成 **KindConcurrent**（实测，见下），于是：
//   · 走「模型粒度冷却」路径（domains/credential/writer.go:373 那一组），
//     KindConcurrent 的冷却时长是 **5 分钟**（KindRateLimit 是 3 分钟）；
//   · 探针抑制的错误码记成 "http_5xx" 而不是 "http_429"
//     （domains/streaming/executors/executor.go:transientSuppressErrorCode）；
//   · 客户端拿到的是 **503 / code=model_not_found**（"全候选失败"分支，
//     domains/streaming/handler.go:5583 写死 StatusServiceUnavailable）。
//
// ★ 为什么仓内「同码的另一形态」判的是 rate_limit：
//
//	{"error":{"code":"insufficient_quota","message":"insufficient_quota",
//	          "type":"insufficient_quota"}}
//
//   这个 body 同样没有命中 budgetExceededRe（该正则的配额码清单只有
//   "code":"INSUFFICIENT_BALANCE" / "insufficient_balance"，
//   **没有** OpenAI 标准的 "insufficient_quota"）⇒ 落到 429 的兜底 ⇒ KindRateLimit。
//   ⇒ 所以「显式配额码 ⇒ rate_limit」本就是**仓内既有口径**，
//     只是本例被 message 里的 "tpm/rpm limit" 字面抢走了。
//
// ★ 改判的**客户端可见差异**（审计档 §八 D1 的核心，属「客户端响应形状」⇒ 需属主放行）：
//
//	改判前：503 + {"code":"model_not_found","kind":"concurrent"}
//	改判后：429 + {"code":"rate_limit","type":"rate_limit_error"} + Retry-After 头
//	         （domains/streaming/handler.go:5511 的 LastKind==KindRateLimit 分支）
//
// ⇒ 客户端「形状」不变、**语义变真**（429+Retry-After 正是 SDK 退避逻辑期待的），
//   但**状态码与 error.code 会变** ⇒ 属契约面变更 ⇒ 本轮只刻画、不改判。

package errorsx

import "testing"

// TestCharacterization_RealUpstreamInsufficientQuotaBody pins the CURRENT
// classification of the exact upstream body observed in production.
//
// ★ 阳性对照：同一个 429、只改 message，若不含 "tpm/rpm limit" 就判 rate_limit
//   ⇒ 证明**决定性因素是 message 命中了并发过载正则**，不是 429 本身。
func TestCharacterization_RealUpstreamInsufficientQuotaBody(t *testing.T) {
	const realBody = `{"error":{"message":"inference exceeds tpm/rpm limit","type":"rate_limit_error","code":"insufficient_quota"}}`

	if got := ClassifyErrorWithBody(429, []byte(realBody)); got != KindConcurrent {
		t.Fatalf("刻画值已变：真实上游报文当前判为 %q，若属主已批准 D1 改判 ⇒ 本测试与 classify.go 必须同批翻转（got=%q）", got, got)
	}
}

// TestCharacterization_QuotaCodeAloneYieldsRateLimit is the positive control:
// the SAME explicit quota code, without the "tpm/rpm limit" wording, already
// classifies as KindRateLimit today. It is the in-repo precedent that makes
// "explicit quota code ⇒ rate_limit" the existing house convention.
func TestCharacterization_QuotaCodeAloneYieldsRateLimit(t *testing.T) {
	const body = `{"error":{"code":"insufficient_quota","message":"insufficient_quota","type":"insufficient_quota"}}`

	if got := ClassifyErrorWithBody(429, []byte(body)); got != KindRateLimit {
		t.Fatalf("同码另一形态当前判为 %q，仓内既有口径应为 rate_limit", got)
	}
}

// TestCharacterization_GenuineConcurrencyStillConcurrent guards the other
// direction: a real "concurrent limit exceeded" body must NOT be re-routed to
// rate_limit by any future quota-code change (it carries no quota code).
func TestCharacterization_GenuineConcurrencyStillConcurrent(t *testing.T) {
	const body = `{"error":{"message":"concurrent limit exceeded for this account","type":"rate_limit_error"}}`

	if got := ClassifyErrorWithBody(429, []byte(body)); got != KindConcurrent {
		t.Fatalf("真并发过载必须仍判 concurrent，实际 %q", got)
	}
}
