package ir

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// R72 回归：OpenAI 的 max_tokens 是可选的，Anthropic 的 max_tokens 是必填
// 且必须 > 0。IR 路径过去把可选字段直接透传，于是「客户端不带
// max_tokens」这一完全正常的请求，在转发到 Anthropic 时变成
// {"max_tokens": 0} → 上游必 400。
//
// 修复取的是旧非 IR 转换路径已在生产使用的默认值（4096），不是新发明的。
// domains/transformation/anthropic/chat_to_anthropic_parity_test.go 的
// max_tokens 裁决与三条 golden 已同步收口。
func TestSerializeAnthropic_MaxTokensNeverZero(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{
			name: "client omitted max_tokens entirely",
			in:   `{"model":"claude-x","messages":[{"role":"user","content":"hi"}]}`,
			want: DefaultAnthropicMaxTokens,
		},
		{
			name: "explicit max_tokens is preserved",
			in:   `{"model":"claude-x","max_tokens":321,"messages":[{"role":"user","content":"hi"}]}`,
			want: 321,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := ParseOpenAI([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseOpenAI: %v", err)
			}
			out, err := SerializeAnthropic(req)
			if err != nil {
				t.Fatalf("SerializeAnthropic: %v", err)
			}
			var body map[string]any
			if err := json.Unmarshal(out, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			mt, ok := body["max_tokens"].(float64)
			if !ok {
				t.Fatalf("max_tokens missing or not numeric: %s", out)
			}
			if int(mt) != tc.want {
				t.Fatalf("max_tokens = %d, want %d (body=%s)", int(mt), tc.want, out)
			}
			if int(mt) <= 0 {
				t.Fatalf("Anthropic rejects max_tokens <= 0; got %d", int(mt))
			}
		})
	}
}

// R72 回归：SerializeOllama 丢弃无命名空间扩展时必须留痕。其它四个
// 序列化器都调 ReportProtocolLoss，只有 Ollama 静默丢——客户端的厂商私有
// 参数消失时没有任何可观测记录。
//
// 注意这里刻意「不恢复」：线上字节不能变。
// TestSerializeOllama_PrivateOptionsDontLeakAsTopLevel 钉死了「无 ollama.
// 命名空间的键不得出现在顶层」这条契约，本测试守的是它的另一半——可观测性。
func TestSerializeOllama_UnnamespacedExtensionsAreDroppedButReported(t *testing.T) {
	req := &InternalRequest{
		Model:          "llama3.1",
		SourceProtocol: ProtocolOpenAIChat,
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Extensions: map[string]json.RawMessage{
			"ollama.options.num_ctx": json.RawMessage(`2048`),
			"my_vendor_thing":        json.RawMessage(`42`),
		},
	}
	body, err := SerializeOllama(req)
	if err != nil {
		t.Fatalf("SerializeOllama: %v", err)
	}
	// 线上字节契约：命名的进 options，未命名的不得出现在顶层。
	if strings.Contains(string(body), "my_vendor_thing") {
		t.Fatalf("unnamespaced extension must not reach the wire: %s", body)
	}
	if !strings.Contains(string(body), `"num_ctx"`) {
		t.Fatalf("ollama.options.* must still be lifted: %s", body)
	}
}

// 确认 loss 上报确实发生：装一个计数用的 reporter sink，直接数事件，
// 而不是靠看日志。
func TestSerializeOllama_EmitsProtocolLossForDroppedExtension(t *testing.T) {
	var seen []AnomalyEvent
	prev := SetAnomalyReporter(func(e AnomalyEvent) { seen = append(seen, e) })
	defer SetAnomalyReporter(prev)

	// dedup key 含 field path + 时间戳，用一次性名字避免被前一次运行抑制。
	probe := fmt.Sprintf("r72_drop_probe_%d", time.Now().UnixNano())
	req := &InternalRequest{
		Model:          "llama3.1",
		SourceProtocol: ProtocolOpenAIChat,
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Extensions:     map[string]json.RawMessage{probe: json.RawMessage(`1`)},
	}
	if _, err := SerializeOllama(req); err != nil {
		t.Fatalf("SerializeOllama: %v", err)
	}

	for _, e := range seen {
		if e.FieldPath == probe && e.AnomalyType == AnomalyProtocolLoss {
			return
		}
	}
	t.Fatalf("dropped extension %q produced no protocol-loss anomaly; got %+v", probe, seen)
}
