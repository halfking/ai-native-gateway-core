package executors

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// 2026-09-18 MiniMax thinking 事故的 executor 层接线回归测试。
//
// 背景：paramreg 对 thinking 的方言翻译（enabled→adaptive for MiniMax）与
// P5 的 ir.Thinking 方言渲染都发生在 internal/ir.SerializeOpenAI 内，且全部
// 以 irReq.TargetProvider 为方言键。internal/ir 层的契约测试
// (serialize_openai_thinking_dialect_test.go / serialize_openai_thinking_p5_test.go)
// 只能证明"TargetProvider 设置正确时序列化正确"，证明不了"生产路径真的
// 设置了 TargetProvider"——事故当天的 2135 正是栽在这里：翻译存在但从未
// 被调用，窗口内 200 的假阳性掩盖了这一点。
//
// 本文件把 finalizeOpenAIUpstreamBody 的三条出向分支逐条钉死：每条分支
// 都必须把 cand.CatalogCode 传到序列化层。
//
//	① legacy_with_ir（OpenAI 协议入向主路径，c6de4699d 接线）
//	② legacy 断路器兜底（同 commit 接线，走包级 ir 函数）
//	③ anthropic → openai IR 转换分支（P5 补线，ir.Thinking 唯一能被
//	   方言渲染的路径）
const minimaxWiredBody = `{
	"model": "minimax-m3",
	"max_tokens": 256,
	"messages": [{"role":"user","content":"hi"}],
	"thinking": {"type":"enabled","budget_tokens":1024}
}`

func decodeUpstreamBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("upstream body is not valid JSON: %v\nbody=%s", err, body)
	}
	return m
}

func assertUpstreamThinking(t *testing.T, body []byte, wantType string, minimal bool) {
	t.Helper()
	m := decodeUpstreamBody(t, body)
	th, ok := m["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("body 缺少 thinking 对象（方言翻译未生效，疑似 TargetProvider 未接线）: %s", body)
	}
	if th["type"] != wantType {
		t.Errorf("thinking.type=%v, want %q", th["type"], wantType)
	}
	if minimal && len(th) != 1 {
		t.Errorf("thinking 应为最小对象(仅 type): %v", th)
	}
}

func wiringExecParams(t *testing.T, clientProtocol string) *ExecParams {
	t.Helper()
	return &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/chat/completions", nil),
		ClientModel:    "minimax-m3",
		OutboundModel:  "minimax-m3",
		ClientProtocol: clientProtocol,
	}
}

// ① legacy_with_ir 分支：OpenAI 协议入向（zcode 的实际路径）。
func TestFinalizeOpenAIUpstreamBody_TargetProviderWiring_LegacyWithIR(t *testing.T) {
	executor := &Executor{IR: &irAdapterForTest{}}

	t.Run("minimax catalog translates enabled to adaptive", func(t *testing.T) {
		cand := provider.Candidate{
			ProviderID:   14,
			CredentialID: 42,
			Protocol:     "openai-completions",
			CatalogCode:  "minimax",
			RawModel:     "MiniMax-M3",
		}
		params := wiringExecParams(t, "")
		body, err := executor.finalizeOpenAIUpstreamBody(params, cand, []byte(minimaxWiredBody))
		if err != nil {
			t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
		}
		assertUpstreamThinking(t, body, "adaptive", true)
	})

	t.Run("empty catalog passes through verbatim", func(t *testing.T) {
		cand := provider.Candidate{
			ProviderID:   1,
			CredentialID: 1,
			Protocol:     "openai-completions",
			RawModel:     "some-openai-form-model",
		}
		params := wiringExecParams(t, "")
		body, err := executor.finalizeOpenAIUpstreamBody(params, cand, []byte(minimaxWiredBody))
		if err != nil {
			t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
		}
		assertUpstreamThinking(t, body, "enabled", false)
	})
}

// ② legacy 断路器兜底分支：e.IR.ParseOpenAI 返回 ErrConverterCircuitOpen 时
// 走包级 ir 函数，TargetProvider 接线必须同样生效（c6de4699d）。
func TestFinalizeOpenAIUpstreamBody_TargetProviderWiring_CircuitOpenFallback(t *testing.T) {
	executor := &Executor{IR: &circuitOpenIRAdapter{}}
	cand := provider.Candidate{
		ProviderID:   14,
		CredentialID: 21,
		Protocol:     "openai-completions",
		CatalogCode:  "minimax",
		RawModel:     "MiniMax-M3",
	}
	params := wiringExecParams(t, "")
	body, err := executor.finalizeOpenAIUpstreamBody(params, cand, []byte(minimaxWiredBody))
	if err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
	}
	assertUpstreamThinking(t, body, "adaptive", true)
}

// ③ anthropic → openai IR 分支：ir.Thinking 只在这条路径被消费，
// P5 方言渲染 + 本分支的 TargetProvider 接线共同决定推理意图是否送达。
func TestFinalizeOpenAIUpstreamBody_TargetProviderWiring_AnthropicBranch(t *testing.T) {
	anthropicBody := `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024,
		"messages": [{"role":"user","content":"hi"}],
		"thinking": {"type":"enabled","budget_tokens":4096}
	}`
	executor := &Executor{IR: &irAdapterForTest{}}

	t.Run("minimax catalog preserves reasoning intent", func(t *testing.T) {
		cand := provider.Candidate{
			ProviderID:   14,
			CredentialID: 42,
			Protocol:     "openai-completions",
			CatalogCode:  "minimax",
			RawModel:     "MiniMax-M3",
		}
		params := wiringExecParams(t, "anthropic-messages")
		body, err := executor.finalizeOpenAIUpstreamBody(params, cand, []byte(anthropicBody))
		if err != nil {
			t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
		}
		assertUpstreamThinking(t, body, "adaptive", true)
	})

	t.Run("unknown catalog still drops with loss report", func(t *testing.T) {
		cand := provider.Candidate{
			ProviderID:   1,
			CredentialID: 1,
			Protocol:     "openai-completions",
			RawModel:     "some-openai-form-model",
		}
		params := wiringExecParams(t, "anthropic-messages")
		body, err := executor.finalizeOpenAIUpstreamBody(params, cand, []byte(anthropicBody))
		if err != nil {
			t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
		}
		m := decodeUpstreamBody(t, body)
		if _, exists := m["thinking"]; exists {
			t.Errorf("未知目标方言不得猜测输出 thinking: %s", body)
		}
	})
}
