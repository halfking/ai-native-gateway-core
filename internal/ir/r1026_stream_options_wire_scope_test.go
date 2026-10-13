package ir

// r1026（2026-10-13）：stream_options 是 openai-chat 线格式专属参数。
//
// 事故形态（raw-logs 实测，vapeur 中转 grok-4.6）：
//
//	client  POST /v1/chat/completions
//	  {"model":"grok-4.6","stream":true,
//	   "stream_options":{"include_usage":true},   ← AI-SDK 流式恒发
//	   "messages":[...]}
//	gateway chat→responses 转换后出站体：
//	  {"input":[...],"model":"grok-4.6","stream":true,
//	   "stream_options":{"include_usage":true}}   ← Responses 线格式无此字段
//	upstream 400 "Unknown parameter: 'stream_options.include_usage'"
//	→ 单候选，整请求 503。
//
// 判据纪律：
//   - 同层驱动：真实 ParseOpenAI → SerializeXxx 全链，不手播种 Extensions；
//   - 前提在场：先断言 stream_options 确实进了 Extensions（否则是
//     「看不见」的假绿，判据纪律第 3 条）；
//   - 回放形状：请求体取自 raw-logs 捕获的真实失败请求；
//   - 过裁剪守卫：chat 线格式（含 unknown-relay 目标，即 vapeur 的
//     dst=DialectUnknown 生产条件）必须保留 stream_options。
import (
	"encoding/json"
	"strings"
	"testing"
)

// r1026CapturedChatBody 是 raw-logs（2026-10-13T17:04:11）捕获的真实失败
// 请求 client_request 体，原样回放。
const r1026CapturedChatBody = `{"model":"grok-4.6","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"你好，简短回复测试"}]}`

// r1026ParseForWire 用真实 parse 链构造 IR，并断言前提在场：
// stream_options 必须落在 Extensions 里（parse_openai 不消费它）。
func r1026ParseForWire(t *testing.T) *InternalRequest {
	t.Helper()
	req, err := ParseOpenAI([]byte(r1026CapturedChatBody))
	if err != nil {
		t.Fatalf("ParseOpenAI failed: %v", err)
	}
	if _, ok := req.Extensions["stream_options"]; !ok {
		t.Fatalf("premise missing: stream_options did not land in Extensions (got %v); the test would be a false green", req.Extensions)
	}
	if !req.Stream {
		t.Fatalf("premise missing: request should be streaming")
	}
	return req
}

// TestR1026StreamOptionsDroppedFromResponsesWire：chat 入向 →
// openai-responses 出站体不得含 stream_options（Responses 线格式无此字段，
// 严格中转 400）。目标条件复刻 vapeur：TargetProvider 非空且未登记 →
// dst=DialectUnknown，恰是生产里 KindPortable fail-open 透传的路径。
func TestR1026StreamOptionsDroppedFromResponsesWire(t *testing.T) {
	req := r1026ParseForWire(t)
	req.TargetProvider = "vapeur-unregistered-relay"

	body, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}
	if strings.Contains(string(body), "stream_options") {
		t.Fatalf("stream_options leaked into openai-responses wire body: %s", body)
	}
	// 正文主体仍在（裁的是参数不是内容）。
	if !strings.Contains(string(body), `"input"`) {
		t.Fatalf("responses wire body missing input: %s", body)
	}
}

// TestR1026StreamOptionsDroppedFromAnthropicAndGeminiWire：同守卫对
// anthropic-messages 与 gemini-generate 线格式同样生效。
func TestR1026StreamOptionsDroppedFromAnthropicAndGeminiWire(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		req := r1026ParseForWire(t)
		body, err := SerializeAnthropic(req)
		if err != nil {
			t.Fatalf("SerializeAnthropic failed: %v", err)
		}
		if strings.Contains(string(body), "stream_options") {
			t.Fatalf("stream_options leaked into anthropic wire body: %s", body)
		}
	})
	t.Run("gemini", func(t *testing.T) {
		req := r1026ParseForWire(t)
		body, err := SerializeGemini(req)
		if err != nil {
			t.Fatalf("SerializeGemini failed: %v", err)
		}
		if strings.Contains(string(body), "stream_options") {
			t.Fatalf("stream_options leaked into gemini wire body: %s", body)
		}
	})
}

// TestR1026StreamOptionsKeptOnChatWire：chat→chat（含 unknown-relay 目标，
// 即联界/智码/vapeur-chat 等未登记中转的生产条件）必须保留客户端
// stream_options——这是过裁剪守卫，防止有人把守卫扩到 chat 线格式上
// （会把 executor 再注入前的 include_usage=false 语义反转，重演
// fields.go:8-13 的 stream_options 静默丢失事故）。
func TestR1026StreamOptionsKeptOnChatWire(t *testing.T) {
	for _, target := range []string{"", "deepseek", "vapeur-unregistered-relay"} {
		req := r1026ParseForWire(t)
		req.TargetProvider = target
		body, err := SerializeOpenAI(req)
		if err != nil {
			t.Fatalf("SerializeOpenAI(target=%q) failed: %v", target, err)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		so, ok := out["stream_options"]
		if !ok {
			t.Fatalf("target=%q: stream_options was dropped from openai-chat wire body (over-trim)", target)
		}
		m, ok := so.(map[string]any)
		if !ok || m["include_usage"] != true {
			t.Fatalf("target=%q: stream_options = %v, want include_usage=true", target, so)
		}
	}
}
