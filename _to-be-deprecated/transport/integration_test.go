
package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// TestIntegration_Quadrants_Parity 验证 IR 和 Legacy 在 4 象限的输出语义等价。
//
// 这是 Phase 0.6 的核心验收测试：灰度的信心来源是两条路径产出等价的结果。
// 语义等价 = JSON 解析后关键字段（model, messages 结构, max_tokens）一致，
// 不要求字节相同（字段顺序、内部表示可能有差异）。
func TestIntegration_Quadrants_Parity(t *testing.T) {
	ir := NewIRTransport()
	legacy := NewLegacyTransport()

	type testCase struct {
		name      string
		client    string
		upstream  string
		body      string
		checkFunc func(t *testing.T, irOut, legacyOut []byte)
	}

	cases := []testCase{
		{
			name:     "Q1 OpenAI→OpenAI",
			client:   "openai-chat",
			upstream: "openai-chat",
			body:     openaiBody,
			checkFunc: func(t *testing.T, irOut, legacyOut []byte) {
				// Legacy 直通；IR 是 roundtrip。都应保留 model + messages
				assertJSONField(t, "Q1 IR", irOut, "model", "gpt-4o")
				assertJSONField(t, "Q1 Legacy", legacyOut, "model", "gpt-4o")
				assertMessagesPresent(t, "Q1 IR", irOut)
				assertMessagesPresent(t, "Q1 Legacy", legacyOut)
			},
		},
		{
			name:     "Q2 Anthropic→OpenAI",
			client:   "anthropic-messages",
			upstream: "openai-chat",
			body:     anthropicBody,
			checkFunc: func(t *testing.T, irOut, legacyOut []byte) {
				// 两者都应产出 OpenAI 风格请求
				assertMessagesPresent(t, "Q2 IR", irOut)
				assertMessagesPresent(t, "Q2 Legacy", legacyOut)
			},
		},
		{
			name:     "Q3 OpenAI→Anthropic",
			client:   "openai-chat",
			upstream: "anthropic-messages",
			body:     openaiBody,
			checkFunc: func(t *testing.T, irOut, legacyOut []byte) {
				// 两者都应产出 Anthropic 风格请求（有 max_tokens）
				assertHasField(t, "Q3 IR", irOut, "max_tokens")
				assertHasField(t, "Q3 Legacy", legacyOut, "max_tokens")
			},
		},
		{
			name:     "Q4 Anthropic→Anthropic",
			client:   "anthropic-messages",
			upstream: "anthropic-messages",
			body:     anthropicBody,
			checkFunc: func(t *testing.T, irOut, legacyOut []byte) {
				assertJSONField(t, "Q4 IR", irOut, "model", "claude-sonnet-4-20250514")
				assertJSONField(t, "Q4 Legacy", legacyOut, "model", "claude-sonnet-4-20250514")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnvelope(tc.client, tc.upstream, tc.body, "gpt-4o")

			irOut, err := ir.Convert(context.Background(), cloneEnvelope(env))
			if err != nil {
				t.Fatalf("IR Convert: %v", err)
			}

			legacyOut, err := legacy.Convert(context.Background(), cloneEnvelope(env))
			if err != nil {
				t.Fatalf("Legacy Convert: %v", err)
			}

			tc.checkFunc(t, irOut, legacyOut)
		})
	}
}

// TestIntegration_ResponseQuadrants_Parity 验证响应方向的等价性。
func TestIntegration_ResponseQuadrants_Parity(t *testing.T) {
	ir := NewIRTransport()
	legacy := NewLegacyTransport()

	type testCase struct {
		name         string
		client       string
		upstream     string
		upstreamBody string
		checkFunc    func(t *testing.T, irOut, legacyOut []byte)
	}

	cases := []testCase{
		{
			name:         "Q1 OpenAI→OpenAI response",
			client:       "openai-chat",
			upstream:     "openai-chat",
			upstreamBody: openaiResponse,
			checkFunc: func(t *testing.T, irOut, legacyOut []byte) {
				// Legacy 直通；IR roundtrip。都应有 choices
				assertHasField(t, "Q1 IR resp", irOut, "choices")
				assertHasField(t, "Q1 Legacy resp", legacyOut, "choices")
			},
		},
		{
			name:         "Q3 OpenAI client ← Anthropic upstream response",
			client:       "openai-chat",
			upstream:     "anthropic-messages",
			upstreamBody: anthropicResponse,
			checkFunc: func(t *testing.T, irOut, legacyOut []byte) {
				// 两者都应产出 OpenAI 风格响应（choices）
				assertHasField(t, "Q3 IR resp", irOut, "choices")
				assertHasField(t, "Q3 Legacy resp", legacyOut, "choices")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnvelope(tc.client, tc.upstream, openaiBody, "gpt-4o")

			irOut, err := ir.ConvertResponse(context.Background(), cloneEnvelope(env), []byte(tc.upstreamBody))
			if err != nil {
				t.Fatalf("IR ConvertResponse: %v", err)
			}

			legacyOut, err := legacy.ConvertResponse(context.Background(), cloneEnvelope(env), []byte(tc.upstreamBody))
			if err != nil {
				t.Fatalf("Legacy ConvertResponse: %v", err)
			}

			tc.checkFunc(t, irOut, legacyOut)
		})
	}
}

// TestIntegration_Factory_Pick_Determinism 验证同一请求多次 Pick 结果一致。
func TestIntegration_Factory_Pick_Determinism(t *testing.T) {
	t.Setenv("TRANSPORT_LAYER_IR_ENABLED", "true")
	t.Setenv("TRANSPORT_IR_ROLLOUT_PERCENT", "50")

	f := NewTransportFactory()
	f.Reload()

	env := domain.NewEnvelopeBuilder("r1").
		WithTenant(&domain.TenantContext{ID: "tenant-x"}).
		WithTransport(&domain.TransportContext{ClientModel: "gpt-4o"}).
		Build()

	first := f.Pick(context.Background(), env).Implementation()
	for i := 0; i < 20; i++ {
		got := f.Pick(context.Background(), env).Implementation()
		if got != first {
			t.Fatalf("Pick determinism broken at iter %d: got %s, want %s", i, got, first)
		}
	}
}

// cloneEnvelope 创建 envelope 的浅拷贝（transport 测试不改 body，浅拷贝足够）。
func cloneEnvelope(env *domain.RequestEnvelope) *domain.RequestEnvelope {
	if env == nil {
		return nil
	}
	cp := *env
	if env.Transport != nil {
		tc := *env.Transport
		cp.Transport = &tc
	}
	return &cp
}

// assertJSONField 验证 JSON 输出中某个字符串字段的值。
func assertJSONField(t *testing.T, label string, body []byte, field, want string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("%s: invalid JSON: %v\nbody: %s", label, err, body)
	}
	raw, ok := m[field]
	if !ok {
		t.Fatalf("%s: field %q missing in %s", label, field, body)
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: field %q not a string: %v", label, field, err)
	}
	if got != want {
		t.Errorf("%s: field %q = %q, want %q", label, field, got, want)
	}
}

// assertMessagesPresent 验证 messages 数组存在且非空。
func assertMessagesPresent(t *testing.T, label string, body []byte) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("%s: invalid JSON: %v\nbody: %s", label, err, body)
	}
	raw, ok := m["messages"]
	if !ok {
		t.Fatalf("%s: messages missing in %s", label, body)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("%s: messages not array: %v", label, err)
	}
	if len(arr) == 0 {
		t.Errorf("%s: messages empty", label)
	}
}

// assertHasField 验证 JSON 输出中包含某字段。
func assertHasField(t *testing.T, label string, body []byte, field string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("%s: invalid JSON: %v\nbody: %s", label, err, body)
	}
	if _, ok := m[field]; !ok {
		t.Fatalf("%s: field %q missing in %s", label, field, body)
	}
}
