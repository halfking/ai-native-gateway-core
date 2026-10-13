package transformation

// r1026 pass2 判据：transport 层的第二遍扩展还原不得把 stream_options
// 写回非 openai-chat 线格式的出站体。
//
// 背景：internal/ir 的序列化器（pass1）已按线格式裁剪 stream_options，
// 但 transport 层的 restoreRequestExtensions（pass2）在 paramreg 启用路径
// 会再遍历一遍 req.Extensions——若两遍政策不一致，pass1 裁掉的字段会被
// pass2 写回去（r1021 thinking 事故的同构复发路径，ir_converter.go 内
// 2026-10-13 P0 注释有完整记录）。
//
// 判据纪律：inner mock 刻意返回**不含** stream_options 的 body（模拟 pass1
// 已裁剪的产物），Extensions 里带着 stream_options（前提在场）——
// pass2 若无守卫，会把它重新合并进 target。
import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// TestR1026Pass2StreamOptionsNotReAddedToAnthropicWire：anthropic 目标。
func TestR1026Pass2StreamOptionsNotReAddedToAnthropicWire(t *testing.T) {
	inner := &mockIRAdapter{
		serializeAnthropicFunc: func(req *ir.InternalRequest) ([]byte, error) {
			// pass1 已裁剪：body 不含 stream_options。
			return []byte(`{"model":"claude-sonnet-5","max_tokens":1024,"messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)

	req := &ir.InternalRequest{
		Model:          "claude-sonnet-5",
		SourceProtocol: ir.ProtocolOpenAIChat, // chat 入向 → anthropic 出向
		Extensions: map[string]json.RawMessage{
			"stream_options": json.RawMessage(`{"include_usage":true}`),
		},
	}
	out, err := conv.SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	if strings.Contains(string(out), "stream_options") {
		t.Fatalf("pass2 re-added stream_options into anthropic wire body: %s", out)
	}
}

// TestR1026Pass2StreamOptionsKeptOnChatWire：chat 目标必须保留（过裁剪守卫，
// 与 pass1 判据 TestR1026StreamOptionsKeptOnChatWire 配对）。
func TestR1026Pass2StreamOptionsKeptOnChatWire(t *testing.T) {
	inner := &mockIRAdapter{
		serializeOpenAIFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"grok-4.6","stream":true,"messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)

	req := &ir.InternalRequest{
		Model:          "grok-4.6",
		SourceProtocol: ir.ProtocolOpenAIChat,
		// 未登记中转（联界/智码/vapeur-chat 形态）的生产条件。
		TargetProvider: "vapeur-unregistered-relay",
		Extensions: map[string]json.RawMessage{
			"stream_options": json.RawMessage(`{"include_usage":true}`),
		},
	}
	out, err := conv.SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output invalid JSON: %v\n%s", err, out)
	}
	so, ok := m["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Fatalf("stream_options must survive pass2 on chat wire body: %s", out)
	}
}
