package errorsx

import "testing"

// 2026-10-13 gpt-5.6-sol 无响应事故判据（errorsx 层）。
//
// 事故链：上游 400 回 "Unsupported value: 'reasoning_effort' does not
// support 'max' ..."（code:"unsupported_value"）——此前不匹配任何专用正则，
// 落到 ClassifyResponseStatus 的 default → KindTransient → 同一 body 盲重试
// 3 次 + transientSuppressErrorCode 按 http_5xx 把 (credential, model) 拉黑
// 5 分钟，后续请求 no_candidate（客户端视角「无响应」）。
//
// 本文件锁两层前提：
//  1. 分类：该形态 400/422 → KindClientBug（两个分类器同判）；
//  2. 前提在场：判据不是「碰巧没拦」——正则必须真的命中这条 body。
//
// bodyVapeurEffortMax 是事故现场 upstream_response 的逐字节原文
// （raw-logs 286 字节，2026-10-13 06:01:34 request 4ba13e3b...）。
const bodyVapeurEffortMax = `{
  "error": {
    "message": "Unsupported value: 'reasoning_effort' does not support 'max' with this model. Supported values are: 'none', 'low', 'medium', 'high', and 'xhigh'.",
    "type": "invalid_request_error",
    "param": "reasoning_effort",
    "code": "unsupported_value"
  }
}`

func TestClassifyErrorWithBody_UnsupportedParamValueIsClientBug(t *testing.T) {
	for _, status := range []int{400, 422} {
		if got := ClassifyErrorWithBody(status, []byte(bodyVapeurEffortMax)); got != KindClientBug {
			t.Fatalf("status %d: got %s, want client_bug", status, got)
		}
	}
}

func TestClassifyResponseBody_UnsupportedParamValueIsClientBug(t *testing.T) {
	for _, status := range []int{400, 422} {
		if got := ClassifyResponseBody(status, []byte(bodyVapeurEffortMax)); got != KindClientBug {
			t.Fatalf("status %d: got %s, want client_bug", status, got)
		}
	}
}

// 同族变体：无 code 字段、仅 message 前缀 "Unsupported value: 'x'" 的中转写法。
func TestClassify_UnsupportedValueMessageFormIsClientBug(t *testing.T) {
	body := []byte(`{"error":{"message":"Unsupported value: 'thinking_budget' is not one of ['1024','4096']","type":"invalid_request_error"}}`)
	if got := ClassifyErrorWithBody(400, body); got != KindClientBug {
		t.Fatalf("message form: got %s, want client_bug", got)
	}
}

// 前提在场：正则必须命中事故 body（防「检测器看不见」的假绿——本仓库
// 判据纪律第 3 条）。
func TestUnsupportedParamValueRe_MatchesIncidentBody(t *testing.T) {
	if !unsupportedParamValueRe.Match([]byte(bodyVapeurEffortMax)) {
		t.Fatal("regex must match the incident body")
	}
	if unsupportedParamValueRe.MatchString("totally unrelated 500 oops") {
		t.Fatal("regex must not match generic error text")
	}
}

// 500 上的同类文案不得被改判：网关侧/上游侧故障仍走原有 5xx 语义
// （门是 400/422，5xx 不进该分支）。
func TestClassify_UnsupportedValueOn5xxStaysUpstreamDown(t *testing.T) {
	if got := ClassifyErrorWithBody(500, []byte(bodyVapeurEffortMax)); got != KindUpstreamDown {
		t.Fatalf("5xx: got %s, want upstream_down", got)
	}
}
