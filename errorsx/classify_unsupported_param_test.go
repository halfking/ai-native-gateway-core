package errorsx

import "testing"

// 2026-10-13 Vapeur gpt-5.6-sol temperature 事故判据（errorsx 层）。
//
// 事故链：chat 客户端 temperature=0.2 → 网关转 /v1/responses 原样带
// temperature → Vapeur（gpt-5.6-sol 推理家族）400 回
// "Unsupported parameter: 'temperature' is not supported with this model."
// （code:"invalid_request"，参数本身被拒，区别于 unsupported_value）——
// 此前不匹配任何专用正则，落到 ClassifyResponseStatus 的 default →
// KindTransient → 同一 body 盲重试三次全 400 → 503。
//
// 与 classify_unsupported_value_test.go 的分界：那边是「值不被支持」
// （code:"unsupported_value" / "Unsupported value: 'x'"），这边是
// 「参数本身不被支持」（"Unsupported parameter: 'x'" / "Unknown
// parameter: 'x'" / "Unrecognized request argument"）。两者同判
// KindClientBug：请求侧问题，盲重试必再败，节点健康不该冷却；
// reqprobe 的参数剔除重试（executor 里先于可重试判定执行）兜底。
//
// bodyVapeurTempRejected 是事故现场 upstream_response 逐字节原文
// （gateway-8782.log 2026-10-13 07:19:17 request dc59af64...）。
const bodyVapeurTempRejected = `{"error":{"message":"Unsupported parameter: 'temperature' is not supported with this model.","type":"invalid_request_error","code":"invalid_request"}}`

func TestClassifyErrorWithBody_UnsupportedParamIsClientBug(t *testing.T) {
	for _, status := range []int{400, 422} {
		if got := ClassifyErrorWithBody(status, []byte(bodyVapeurTempRejected)); got != KindClientBug {
			t.Fatalf("status %d: got %s, want client_bug", status, got)
		}
	}
}

func TestClassifyResponseBody_UnsupportedParamIsClientBug(t *testing.T) {
	for _, status := range []int{400, 422} {
		if got := ClassifyResponseBody(status, []byte(bodyVapeurTempRejected)); got != KindClientBug {
			t.Fatalf("status %d: got %s, want client_bug", status, got)
		}
	}
}

// 同族变体：探测路径实测过的 Vapeur "Unknown parameter: 'thinking'."
// （credentials#63 state_reason_detail 原文）与 OpenAI 经典
// "Unrecognized request argument supplied: reasoning_effort"、kimi 聚合
// 中转的括号复数形态（2026-10-13 缺陷③现场）。
func TestClassify_UnsupportedParamFamilyVariantsAreClientBug(t *testing.T) {
	variants := []string{
		`{"error":{"message":"Unknown parameter: 'thinking'.","type":"invalid_request_error","code":"invalid_request"}}`,
		`{"error":{"message":"Unrecognized request argument supplied: reasoning_effort","type":"invalid_request_error"}}`,
		`{"error":{"message":"Validation: Unsupported parameter(s): 'web_search'","type":"invalid_request_error"}}`,
	}
	for i, v := range variants {
		if got := ClassifyErrorWithBody(400, []byte(v)); got != KindClientBug {
			t.Fatalf("variant %d: got %s, want client_bug", i, got)
		}
	}
}

// 前提在场：正则必须命中事故 body（判据纪律第 3 条，防假绿）。
func TestUnsupportedParamRe_MatchesIncidentBody(t *testing.T) {
	if !unsupportedParamRe.Match([]byte(bodyVapeurTempRejected)) {
		t.Fatal("regex must match the incident body")
	}
	for _, neg := range []string{
		"totally unrelated 500 oops",
		// 「值」形态归 unsupportedParamValueRe 管，本正则不得越界自证
		// （两条正则在分类器里是兄弟分支，各自命中即可，但正则单元层
		// 保持互斥能防止将来改文案时双规则打架）。
		`{"error":{"message":"Unsupported value: 'reasoning_effort' does not support 'max'","code":"unsupported_value"}}`,
	} {
		if unsupportedParamRe.MatchString(neg) {
			t.Fatalf("regex must not match: %s", neg)
		}
	}
}

// 5xx 上的同类文案不得被改判（门是 400/422）。
func TestClassify_UnsupportedParamOn5xxStaysUpstreamDown(t *testing.T) {
	if got := ClassifyErrorWithBody(500, []byte(bodyVapeurTempRejected)); got != KindUpstreamDown {
		t.Fatalf("5xx: got %s, want upstream_down", got)
	}
}
