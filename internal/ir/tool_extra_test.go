package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// RISK-2（2026-10-29）：工具顶层非标准键的无损往返。
// 三个重建站点（streaming normalize / streaming sanitize / IR parse→serialize）
// 中 IR 层的保真由本文件钉住；streaming 侧在 tools_normalize_test.go。

// openai 同协议往返：工具顶层 vendor 键保真（包裹形 + 平铺形），canonical 键不重复。
func TestToolExtras_OpenAIRoundTrip(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[` +
		`{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object"}},"vendor_tool_flag":true,"vendor_meta":{"k":9007199254740993}},` +
		`{"name":"g","description":"d2","parameters":{"type":"object"},"vendor_tool_flag":true}` +
		`],"tool_choice":"auto"}`)

	req, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(req.Tools))
	}
	for i, td := range req.Tools {
		if len(td.Extra) == 0 {
			t.Fatalf("tool[%d]: Extra empty — top-level vendor keys dropped by parse", i)
		}
	}
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	var obj struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal out: %v\n%s", err, out)
	}
	if len(obj.Tools) != 2 {
		t.Fatalf("out tools = %d, want 2\n%s", len(obj.Tools), out)
	}
	for i, tool := range obj.Tools {
		if _, ok := tool["vendor_tool_flag"]; !ok {
			t.Fatalf("tool[%d]: vendor_tool_flag dropped by serialize (Extra not merged):\n%s", i, out)
		}
		fn, _ := tool["function"].(map[string]any)
		if fn == nil || fn["name"] == "" {
			t.Fatalf("tool[%d]: canonical shape broken:\n%s", i, out)
		}
		if _, hasFn := tool["function"]; hasFn {
			// canonical 键不重复：Extra 并回是「不覆盖已写入键」。
			if _, dup := tool["type"]; !dup {
				t.Fatalf("tool[%d]: type key lost\n%s", i, out)
			}
		}
	}
	// 字节保真：>2^53 的整数不得经 float64 漂移（preservedRawValue 同款纪律）。
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("big integer drifted (float64 round trip):\n%s", out)
	}
}

// anthropic 同协议往返：cache_control（Anthropic 工具级缓存断点的真实载体）保真。
func TestToolExtras_AnthropicRoundTrip(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5","max_tokens":1024,` +
		`"messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"name":"f","description":"d","input_schema":{"type":"object"},` +
		`"cache_control":{"type":"ephemeral"}}]}`)

	req, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(req.Tools) != 1 || len(req.Tools[0].Extra) == 0 {
		t.Fatalf("parse: cache_control not captured into Extra: %#v", req.Tools)
	}
	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if !strings.Contains(string(out), `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("cache_control lost on anthropic serialize:\n%s", out)
	}
	if !strings.Contains(string(out), `"name":"f"`) {
		t.Fatalf("canonical shape broken:\n%s", out)
	}
}

// 跨协议（openai 入 → anthropic 出）：extra 键并回 anthropic 工具顶层；
// 目标侧语义（sanitize 裁 / 原生直通）由执行器既有门接管 —— IR 层只管不丢。
func TestToolExtras_CrossProtocolToAnthropic(t *testing.T) {
	body := []byte(`{"model":"minimax-m2.5","max_tokens":1024,` +
		`"messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}},"vendor_tool_flag":true}]}`)

	req, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("serialize anthropic: %v", err)
	}
	if !strings.Contains(string(out), `"vendor_tool_flag":true`) {
		t.Fatalf("vendor_tool_flag lost on openai→anthropic serialize:\n%s", out)
	}
}

// 无 extra 的工具不得被塞入任何多余键；provider-specific Raw 工具原样直传（既有语义）。
func TestToolExtras_Negative_NoLeak(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[` +
		`{"type":"function","function":{"name":"f","parameters":{"type":"object"}}},` +
		`{"type":"web_search","search_domain_filter":["a.com"]}` +
		`]}`)
	req, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if req.Tools[0].Extra != nil {
		t.Fatalf("clean tool must have nil Extra, got %s", req.Tools[0].Extra)
	}
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if strings.Contains(string(out), "tool_extra") {
		t.Fatalf("internal carrier key must never leak to the wire:\n%s", out)
	}
	if !strings.Contains(string(out), `"search_domain_filter"`) {
		t.Fatalf("provider-specific Raw tool must pass through verbatim:\n%s", out)
	}
}

// responses（flat 形）往返：顶层非标准键保真（第 4 个重建站点对）。
func TestToolExtras_ResponsesRoundTrip(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","input":"hi","stream":false,` +
		`"tools":[{"type":"function","name":"f","description":"d","parameters":{"type":"object"},"vendor_tool_flag":true}]}`)
	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(req.Tools) != 1 || len(req.Tools[0].Extra) == 0 {
		t.Fatalf("parse: extra not captured: %#v", req.Tools)
	}
	out, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if !strings.Contains(string(out), `"vendor_tool_flag":true`) {
		t.Fatalf("vendor_tool_flag lost on responses serialize:\n%s", out)
	}
	if !strings.Contains(string(out), `"name":"f"`) {
		t.Fatalf("canonical shape broken:\n%s", out)
	}
}
