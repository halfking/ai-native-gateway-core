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

// R72 回归：解析期异常上报的 RequestID 恒为字面量 "unknown"（parser 只拿到
// body 字节），而 dedup 窗口是 1h —— 于是某个字段的**所有**出现被折叠成
// 每小时一条，100% 客户端都在发的字段与 0.1% 客户端偶发的字段在日志上
// 完全不可区分。原注释声称「key 含 RequestID，正常流量下同 key 天然不
// 重复」，该前提在唯一实际调用面上不成立。
//
// 注意这里守的是**窗口长度**的差异，不是「有没有抑制」：解析期异常天然会
// 重复（字段畸形对所有客户端都畸形），抑制本身仍需要，它是为了防止热循环
// 刷爆日志。所以测试缩短两个 TTL，让差异在不 sleep 的前提下可观测。
func TestAnomalyDedup_RequestlessEventsUseShorterWindow(t *testing.T) {
	prev := SetAnomalyReporter(func(AnomalyEvent) {})
	defer SetAnomalyReporter(prev)
	resetR72Dedup(t)

	prevTTL, prevNoID := anomalyDedupTTL, anomalyDedupTTLNoRequestID
	anomalyDedupTTL, anomalyDedupTTLNoRequestID = 400*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { anomalyDedupTTL, anomalyDedupTTLNoRequestID = prevTTL, prevNoID })

	var delivered int
	SetAnomalyReporter(func(AnomalyEvent) { delivered++ })

	// 无身份：等过短窗口后应能再次上报。
	ev := AnomalyEvent{AnomalyType: AnomalyUnknownField, FieldPath: "r72_ttl_probe", Reason: "probe"}
	ReportAnomaly(ev)
	delivered = 0
	time.Sleep(30 * time.Millisecond)
	ReportAnomaly(ev)
	if delivered != 1 {
		t.Fatalf("requestless event stayed suppressed past its short window: delivered=%d want=1", delivered)
	}

	// 同一时刻，带真实身份的重复仍应被 1h 窗口抑制（抑制本身没被削弱）。
	delivered = 0
	ident := AnomalyEvent{
		RequestID:   "r72-real-id",
		AnomalyType: AnomalyUnknownField,
		FieldPath:   "r72_ttl_probe",
		Reason:      "probe",
	}
	ReportAnomaly(ident)
	ReportAnomaly(ident)
	if delivered != 1 {
		t.Fatalf("identified duplicate should still be suppressed by the long window: delivered=%d want=1", delivered)
	}
}

// 生产取值本身就该是「短 < 长」，否则改动等于没做。
func TestAnomalyDedupNoRequestIDWindowIsShorter(t *testing.T) {
	if anomalyDedupTTLNoRequestID >= anomalyDedupTTL {
		t.Fatalf("requestless window (%v) must be shorter than the identified one (%v)",
			anomalyDedupTTLNoRequestID, anomalyDedupTTL)
	}
}

func TestHasRequestIdentity(t *testing.T) {
	for _, id := range []string{"req-1", " x ", "abc"} {
		if !hasRequestIdentity(id) {
			t.Errorf("hasRequestIdentity(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"", "   ", "unknown", " unknown "} {
		if hasRequestIdentity(id) {
			t.Errorf("hasRequestIdentity(%q) = true, want false", id)
		}
	}
}

func resetR72Dedup(t *testing.T) {
	t.Helper()
	reporterMu.Lock()
	reporterDed = map[string]time.Time{}
	reporterMu.Unlock()
}

// R72 回归：Gemini parts 过去是封闭匿名结构体且循环无 default 分支，
// `executableCode` / `codeExecutionResult`（Gemini 代码执行的线上真实 part）
// 落空即被丢弃——既无 IR 载体，也无异常上报，3 个 part 进、1 个出。
// parse_openai 同位置有 RawContent 兜底，属包内约定漂移。
func TestParseGemini_UnknownPartIsPreservedNotDropped(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[` +
		`{"text":"run this"},` +
		`{"executableCode":{"language":"PYTHON","code":"print(1)"}},` +
		`{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1"}}` +
		`]}]}`
	req, err := ParseGemini([]byte(body))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(req.Messages))
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 3 {
		t.Fatalf("content blocks = %d, want 3 (text + executableCode + codeExecutionResult); got %#v",
			len(blocks), blocks)
	}
	if blocks[0].Type != "text" || blocks[0].Text != "run this" {
		t.Errorf("block 0 changed: %#v", blocks[0])
	}
	for i, want := range []string{"executableCode", "codeExecutionResult"} {
		if blocks[i+1].Type != "raw" {
			t.Errorf("block %d type = %q, want raw", i+1, blocks[i+1].Type)
		}
		raw, _ := blocks[i+1].RawContent.(string)
		if !strings.Contains(raw, want) {
			t.Errorf("block %d lost %s: %q", i+1, want, raw)
		}
	}
}

// 未知 part 必须**上报**，否则「保留了但不可观测」与「丢弃」在排障时没区别。
func TestParseGemini_UnknownPartIsReported(t *testing.T) {
	var seen []AnomalyEvent
	prev := SetAnomalyReporter(func(e AnomalyEvent) { seen = append(seen, e) })
	defer SetAnomalyReporter(prev)
	resetR72Dedup(t)

	body := `{"contents":[{"role":"user","parts":[{"executableCode":{"language":"PYTHON","code":"x"}}]}]}`
	if _, err := ParseGemini([]byte(body)); err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	for _, e := range seen {
		if e.AnomalyType == AnomalyUnknownField {
			return
		}
	}
	t.Fatalf("unknown Gemini part produced no anomaly; got %+v", seen)
}

// 已建模的 part 不得被兜底重复收录（否则一次输入会变成两份内容）。
func TestParseGemini_KnownPartsAreNotDuplicated(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[` +
		`{"text":"hi"},` +
		`{"functionCall":{"name":"f","args":{}}}` +
		`]}]}`
	req, err := ParseGemini([]byte(body))
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want 2; got %#v", len(blocks), blocks)
	}
	for i, b := range blocks {
		if b.Type == "raw" {
			t.Errorf("block %d fell into the raw fallback: %#v", i, b)
		}
	}
}
