package errorsx

import "testing"

// 2026-10-13 MiniMax 2013 工具调用配对事故判据（errorsx 层）。
//
// 事故链：/v1/responses 客户端 input 里带悬空 function_call（没有配对的
// function_call_output）→ 网关转 chat 形态原样透传 → MiniMax 400 回
// "invalid params, tool call and result not match (2013)"（姊妹措辞
// "tool call result does not follow tool call (2013)" 出自更早的并行调用
// 拆分事故）。此前不匹配任何专用正则 → KindTransient → 同一 body 盲重试
// 三次全 400 → 凭据熔断把健康节点停掉（殃及真实流量）→ 客户端拿到误导性
// 的 provider_unavailable/503。
//
// 同判 KindClientBug：请求侧结构问题，盲重试必再败，节点健康不该冷却。
// 与 cbdc92a26（sanitize 合并被拆分的并行 tool call）的分界：那边修的是
// 「网关自己删错了合法结果」，这边修的是「客户端请求本身残缺时不再盲重试」。
//
// bodyMiniMax2013Pairing 是事故现场 upstream_response 逐字节原文
// （raw-logs 2026-10-13 09:52:44 request 8ba8096e18f3）。
const bodyMiniMax2013Pairing = `{"type":"error","error":{"type":"bad_request_error","message":"invalid params, tool call and result not match (2013)","http_code":"400"},"request_id":"071cc1ec9f22152a4d297498c03576ce"}`

func TestClassifyErrorWithBody_ToolCallPairingIsClientBug(t *testing.T) {
	for _, status := range []int{400, 422} {
		if got := ClassifyErrorWithBody(status, []byte(bodyMiniMax2013Pairing)); got != KindClientBug {
			t.Fatalf("status %d: got %s, want client_bug", status, got)
		}
	}
}

func TestClassifyResponseBody_ToolCallPairingIsClientBug(t *testing.T) {
	for _, status := range []int{400, 422} {
		if got := ClassifyResponseBody(status, []byte(bodyMiniMax2013Pairing)); got != KindClientBug {
			t.Fatalf("status %d: got %s, want client_bug", status, got)
		}
	}
}

// 姊妹措辞：并行调用拆分事故期间实测的 "tool call result does not follow
// tool call (2013)"（raw-logs request d717f89a8c79 多轮原文）。
func TestClassify_ToolCallPairingFamilyVariantsAreClientBug(t *testing.T) {
	variants := []string{
		`{"type":"error","error":{"type":"bad_request_error","message":"invalid params, tool call result does not follow tool call (2013)","http_code":"400"},"request_id":"x"}`,
		// 双嵌套（Vapeur 型 502 包 400 内层 body 同样可能漂进分类器）
		`{"code":4006,"message":"{\"message\":\"invalid params, tool call and result not match (2013)\"}"}`,
	}
	for i, v := range variants {
		if got := ClassifyErrorWithBody(400, []byte(v)); got != KindClientBug {
			t.Fatalf("variant %d: got %s, want client_bug", i, got)
		}
	}
}

// 前提在场：正则必须命中事故 body（判据纪律第 3 条，防「看不见」假绿）。
func TestToolCallPairingRe_MatchesIncidentBody(t *testing.T) {
	if !toolCallPairingRe.Match([]byte(bodyMiniMax2013Pairing)) {
		t.Fatal("toolCallPairingRe 不命中事故 body——判据测了个寂寞")
	}
}

// 反向：模型散文里正常谈论工具调用/结果不得被误判（该分类器只跑在
// 4xx/422 错误 body 上，但散文防御仍要钉住，防未来有人放宽调用面）。
func TestToolCallPairingRe_DoesNotMatchProse(t *testing.T) {
	prose := []string{
		`{"choices":[{"message":{"content":"the tool call result was appended to the transcript"}}]}`,
		`{"usage":{"total_tokens":100},"object":"chat.completion"}`,
	}
	for i, p := range prose {
		if toolCallPairingRe.Match([]byte(p)) {
			t.Fatalf("prose %d 误命中 toolCallPairingRe", i)
		}
		if got := ClassifyErrorWithBody(400, []byte(p)); got == KindClientBug {
			t.Fatalf("prose %d 被误判 client_bug", i)
		}
	}
}
