package ir

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gemini 专有语义参数的跨协议丢失披露（2026-11-11 审计轮）。
//
// 缺口：parse_gemini 把 safetySettings / cachedContent 消费进 IR 的结构化
// 字段 ⇒ 它们不在 Extensions 里 ⇒ 非 Gemini 的序列化器看不见 ⇒ 跨协议时
// 被**静默丢弃且无 loss 事件**。
//
// 修法：reportGeminiOnlyLosses（internal/ir/gemini_only_loss.go）—— 四个
// 非 Gemini 序列化器共用，诚实披露丢失。
//
// 断言打��� **loss 事件**，而不是"值还在不在 IR 里"——
// 上一轮的第一版守卫正是打在 IR 值上，测不出"静默蒸发"这个真实缺陷。

// TestGeminiOnlyLoss_DisclosedOnNonGeminiTargets 断「有 loss 事件」。
func TestGeminiOnlyLoss_DisclosedOnNonGeminiTargets(t *testing.T) {
	geminiBody := []byte(`{
		"model": "gemini-2.0-flash",
		"contents": [{"role":"user","parts":[{"text":"hi"}]}],
		"safetySettings": [
			{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE"}
		],
		"cachedContent": "cachedContents/abc123"
	}`)

	targets := []struct {
		name      string
		serialize func(*InternalRequest) ([]byte, error)
		protocol  string
	}{
		{"openai-chat", SerializeOpenAI, ProtocolOpenAIChat},
		{"anthropic-messages", SerializeAnthropic, ProtocolAnthropicMessages},
		{"openai-responses", SerializeResponsesRequest, ProtocolOpenAIResponses},
		{"ollama-chat", SerializeOllama, ProtocolOllamaChat},
	}

	for _, tgt := range targets {
		t.Run(tgt.name, func(t *testing.T) {
			// 用 scoped reporter 捕获本次序列化产生的事件（隔离全局去重表）
			scope := NewIRScopedReporter(nil)
			defer scope.Close()
			scope.SetAsCurrent()
			defer scope.Unset()

			req, err := ParseGemini(geminiBody)
			require.NoError(t, err)
			require.Equal(t, ProtocolGeminiGenerate, req.SourceProtocol)
			require.NotEmpty(t, req.SafetySettings)
			require.Equal(t, "cachedContents/abc123", req.CachedContent)

			// Gemini 的 model 走 URL 路径（不在 body 里）⇒ parse_gemini 不设
			// Model；执行器按路由填。这里模拟该行为（Ollama 序列化器硬要求
			// model 非空）。
			req.Model = "gemini-2.0-flash"

			_, err = tgt.serialize(ValidateAndFixRequest(req, "gemini-only-loss"))
			require.NoError(t, err)

			events := scope.Snapshot()

			// 两条丢失都必须被披露：safety_settings + cached_content
			var sawSafety, sawCached bool
			for _, ev := range events {
				if ev.AnomalyType != AnomalyProtocolLoss {
					continue
				}
				if ev.TargetProtocol != tgt.protocol {
					continue
				}
				switch ev.FieldPath {
				case "safety_settings":
					sawSafety = true
					assert.Equal(t, ProtocolGeminiGenerate, ev.SourceProtocol,
						"source 协议必须是 Gemini")
					assert.Equal(t, "loss", ev.Reason)
				case "cached_content":
					sawCached = true
					assert.Equal(t, ProtocolGeminiGenerate, ev.SourceProtocol)
					assert.Equal(t, "loss", ev.Reason)
				}
			}

			assert.True(t, sawSafety,
				"safety_settings 跨协议到 %s 必须上报丢失（静默丢弃＝合规风险）", tgt.name)
			assert.True(t, sawCached,
				"cached_content 跨协议到 %s 必须上报丢失", tgt.name)
		})
	}
}

// TestGeminiOnlyLoss_SameProtocolNoLoss 断「同协议不报」——
// Gemini → Gemini 时 serialize_gemini 自己输出这两个字段 ⇒ 不是丢失。
// 这条防「把正常同协议往返也报成丢失」的假阳性。
func TestGeminiOnlyLoss_SameProtocolNoLoss(t *testing.T) {
	scope := NewIRScopedReporter(nil)
	defer scope.Close()
	scope.SetAsCurrent()
	defer scope.Unset()

	body := []byte(`{
		"model": "gemini-2.0-flash",
		"contents": [{"role":"user","parts":[{"text":"hi"}]}],
		"safetySettings": [
			{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE"}
		],
		"cachedContent": "cachedContents/abc123"
	}`)

	req, err := ParseGemini(body)
	require.NoError(t, err)

	data, err := SerializeGemini(ValidateAndFixRequest(req, "gemini-same-proto"))
	require.NoError(t, err)

	// 同协议：两个字段必须都在线上
	wire := guardWire(t, data)
	assert.Contains(t, wire, "safetySettings", "Gemini 同协议必须输出 safetySettings")
	assert.Contains(t, wire, "cachedContent", "Gemini 同协议必须输出 cachedContent")

	// 且不得上报丢失
	for _, ev := range scope.Snapshot() {
		if ev.AnomalyType == AnomalyProtocolLoss {
			assert.NotEqual(t, "safety_settings", ev.FieldPath,
				"Gemini 同协议往返不得上报 safety_settings 丢失")
			assert.NotEqual(t, "cached_content", ev.FieldPath,
				"Gemini 同协议往返不得上报 cached_content 丢失")
		}
	}
}

// TestGeminiOnlyLoss_NonGeminiSourceNoReport 断「来源不可证不报」——
// 程序化构造的 IR（SourceProtocol 为空）不得产生 Gemini 丢失事件
// （否则是假阳性，会污染运维的异常面板）。
func TestGeminiOnlyLoss_NonGeminiSourceNoReport(t *testing.T) {
	scope := NewIRScopedReporter(nil)
	defer scope.Close()
	scope.SetAsCurrent()
	defer scope.Unset()

	// SourceProtocol 为空的程序化 IR
	req := &InternalRequest{
		Model:          "gpt-4",
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		SafetySettings: []SafetySetting{{Category: "HARM_CATEGORY_HATE_SPEECH", Threshold: "BLOCK_NONE"}},
		CachedContent:  "cachedContents/xyz",
	}

	_, err := SerializeOpenAI(ValidateAndFixRequest(req, "no-src"))
	require.NoError(t, err)

	for _, ev := range scope.Snapshot() {
		if ev.AnomalyType == AnomalyProtocolLoss {
			assert.NotEqual(t, "safety_settings", ev.FieldPath,
				"来源不可证时不得报 safety_settings 丢失（假阳性）")
			assert.NotEqual(t, "cached_content", ev.FieldPath,
				"来源不可证时不得报 cached_content 丢失（假阳性）")
		}
	}
}
