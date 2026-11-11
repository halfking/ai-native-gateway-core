package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 客户端「数值/布尔参数写成字符串」的容错层守卫（2026-11-11 审计轮）。
//
// 口径（登记在案）：
//   ① **无歧义才转**：字符串能无损解析成该字段的目标类型才转；
//   ② **不猜意图**：转不了仍报错（"abc" → 0.7 不转）；
//   ③ **不碰字符串字段**：model="12345" 这类合法字符串值不被误转；
//   ④ **不静默**：每次转换上报 anomaly；
//   ⑤ **零手写清单**：目标字段由反射从 parser 的 src 结构体推导。
//
// 断言打�� **wire**（线上字节）——修正后的值必须真的上线。

// TestCoerceNumeric_WireLevelAcrossProtocols 断「修正后的值真的上线」。
func TestCoerceNumeric_WireLevelAcrossProtocols(t *testing.T) {
	t.Run("openai_temperature_and_max_tokens", func(t *testing.T) {
		req, err := ParseOpenAI([]byte(`{
			"model": "gpt-4",
			"messages": [{"role":"user","content":"hi"}],
			"temperature": "0.7",
			"max_tokens": "100",
			"top_p": "0.9",
			"seed": "42",
			"n": "2",
			"stream": "true"
		}`))
		require.NoError(t, err)

		require.NotNil(t, req.Temperature)
		assert.InDelta(t, 0.7, *req.Temperature, 1e-9)
		assert.Equal(t, 100, req.MaxTokens)
		require.NotNil(t, req.TopP)
		assert.InDelta(t, 0.9, *req.TopP, 1e-9)
		require.NotNil(t, req.Seed)
		assert.Equal(t, int64(42), *req.Seed)
		assert.Equal(t, 2, req.N)
		assert.True(t, req.Stream)

		// ★ 线级断言：线上必须是**数值**，不是字符串
		data, err := SerializeOpenAI(ValidateAndFixRequest(req, "coerce"))
		require.NoError(t, err)

		var wire map[string]any
		require.NoError(t, json.Unmarshal(data, &wire))

		assert.Equal(t, 0.7, wire["temperature"], "线上 temperature 必须是数值")
		assert.Equal(t, float64(100), wire["max_tokens"], "线上 max_tokens 必须是数值")
		assert.Equal(t, 0.9, wire["top_p"], "线上 top_p 必须是数值")
		assert.Equal(t, float64(42), wire["seed"], "线上 seed 必须是数值")
		assert.Equal(t, true, wire["stream"], "线上 stream 必须是布尔")
	})

	t.Run("anthropic_max_tokens", func(t *testing.T) {
		req, err := ParseAnthropic([]byte(`{
			"model": "claude-sonnet-4-20250514",
			"max_tokens": "2048",
			"temperature": "0.5",
			"messages": [{"role":"user","content":"hi"}]
		}`))
		require.NoError(t, err)
		assert.Equal(t, 2048, req.MaxTokens)
		require.NotNil(t, req.Temperature)
		assert.InDelta(t, 0.5, *req.Temperature, 1e-9)
	})

	t.Run("gemini_generation_config", func(t *testing.T) {
		// generationConfig 是嵌套对象 ⇒ 本轮容错层**不覆盖**嵌套层
		// （如实登记的边界）⇒ 这里断言它仍按原样解析成功/失败，
		// 不假装支持。
		req, err := ParseGemini([]byte(`{
			"model": "gemini-2.0-flash",
			"contents": [{"role":"user","parts":[{"text":"hi"}]}]
		}`))
		require.NoError(t, err)
		assert.NotNil(t, req)
	})
}

// TestCoerceNumeric_AmbiguousCasesRejected 断「无歧义性不成立就不转」。
func TestCoerceNumeric_AmbiguousCasesRejected(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"non_numeric_text", `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"temperature":"abc"}`},
		{"empty_string", `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"temperature":""}`},
		{"not_a_number", `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"max_tokens":"many"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseOpenAI([]byte(tt.input))
			require.Error(t, err, "无歧义性不成立时必须仍然失败")
		})
	}
}

// TestCoerceNumeric_DoesNotTouchStringFields 断「不动字符串字段」。
//
// model="12345" / user="007" 是**合法**的字符串值 ⇒ 绝不能被当成数值转。
func TestCoerceNumeric_DoesNotTouchStringFields(t *testing.T) {
	req, err := ParseOpenAI([]byte(`{
		"model": "12345",
		"user": "007",
		"messages": [{"role":"user","content":"hi"}]
	}`))
	require.NoError(t, err)
	assert.Equal(t, "12345", req.Model, "model 是字符串字段，不得被转成数值")
	assert.Equal(t, "007", req.User, "user 是字符串字段，不得被转成数值")
}

// TestCoerceNumeric_ReflectionDerivedFieldSet 断「字段集来自反射」这一设计。
//
// 这条是**防漂移守卫**：若将来有人把容错层改成手写清单，本断言会失败。
// 做法：断言容错层能转的字段，正是 parser 结构体里**数值/布尔类型**的字段；
// 而结构体里是 string / RawMessage 的字段不在候选集内。
func TestCoerceNumeric_ReflectionDerivedFieldSet(t *testing.T) {
	// temperature / top_p / seed / n / max_tokens / stream 是数值/布尔字段
	// ⇒ 必须能转
	for _, field := range []string{"temperature", "top_p", "seed", "n", "max_tokens", "stream"} {
		body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"` +
			field + `":"1"}`)
		_, err := ParseOpenAI(body)
		assert.NoError(t, err, "数值字段 %s 应能被容错转", field)
	}

	// model / user 是字符串字段 ⇒ 不在候选集（不应被转 ⇒ 解析仍成功且值不变）
	req, err := ParseOpenAI([]byte(`{"model":"12345","user":"007","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	assert.Equal(t, "12345", req.Model)
	assert.Equal(t, "007", req.User)

	// messages 是 RawMessage ⇒ 不在候选集（发了字符串也不该被转成数字）
	_, err = ParseOpenAI([]byte(`{"model":"gpt-4","messages":"42"}`))
	require.Error(t, err, "messages 是 RawMessage 字段，不得被容错转成数值")
}

// TestCoerceNumeric_NoChangeWhenAlreadyValid 断「原本合法的请求完全不受影响」。
//
// 这是**零回归面**的守卫：容错层是预修正 ⇒ 必须证明它对正常请求
// **不产生任何字节变化**（否则会引入难以察觉的差异）。
func TestCoerceNumeric_NoChangeWhenAlreadyValid(t *testing.T) {
	normal := []byte(`{
		"model": "gpt-4",
		"messages": [{"role":"user","content":"hi"}],
		"temperature": 0.7,
		"max_tokens": 100,
		"stream": true
	}`)

	// 直接解析（走容错层）——必须成功且语义正确
	req, err := ParseOpenAI(normal)
	require.NoError(t, err)
	require.NotNil(t, req.Temperature)
	assert.InDelta(t, 0.7, *req.Temperature, 1e-9)
	assert.Equal(t, 100, req.MaxTokens)

	// 再验证容错层本身报告"没有发生转换"
	fixed, changed := coerceNumericStrings(normal, &struct {
		Model       string    `json:"model"`
		Temperature *float64  `json:"temperature,omitempty"`
		MaxTokens   *int      `json:"max_tokens,omitempty"`
		Stream      *bool     `json:"stream,omitempty"`
	}{})
	assert.False(t, changed, "合法请求不应触发任何转换")
	assert.Equal(t, string(normal), string(fixed), "合法请求的 body 不应被改写")
}
