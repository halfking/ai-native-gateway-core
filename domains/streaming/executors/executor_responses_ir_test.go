package executors

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/transformation"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// /v1/responses 入站迁移 IR 主路径的 executor 级钉测（2026-11-12 审计第二轮批 5）。
//
// 背景：此前 responses 客户端的 body 在 handler 经手写 convertResponsesToChatBody
// 预转成 chat 形态再按 openai-chat 解析 —— Responses 专有顶层参数在手写转换里
// 被丢弃/走样，responses wire 语义双实现。迁移后 BodyBytes = responses 原文，
// executor 按 ClientProtocol 分派 ir.ParseResponses。
//
// 字节级主钉：TestUpstreamBodiesUnchanged/responses_to_*（走真实
// NewResponsesHandler 全链，含本文件钉住的两条分派腿）。

// TestResponsesClient_IRMainPath_OpenAIUpstream：responses wire 的
// Responses 专有语义经 IR 主路径落到 openai-chat 出站。
func TestResponsesClient_IRMainPath_OpenAIUpstream(t *testing.T) {
	executor := &Executor{IR: &irAdapterForTest{}}
	cand := provider.Candidate{ProviderID: 1, CredentialID: 1, Protocol: "openai-completions", CatalogCode: "openai", RawModel: "gpt-5.6"}

	// responses wire 原文（批 5 后 BodyBytes 的形态）：instructions /
	// store / text.format 是 Responses 线专有；response_format 是混发的
	// chat 形参数（消费集对齐后同样进 IR）。
	body := `{"model":"gpt-5.6","stream":false,
		"instructions":"You are terse.",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"max_output_tokens":256,
		"store":true,
		"text":{"format":{"type":"text"}}}`

	params := &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/responses", nil),
		BodyBytes:      []byte(body),
		ClientProtocol: "openai-responses",
		ClientModel:    "gpt-5.6",
	}
	out, err := executor.finalizeOpenAIUpstreamBody(params, cand, []byte(body))
	if err != nil {
		t.Fatalf("finalizeOpenAIUpstreamBody: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("instructions 应提升为 system 消息 + user 消息，got %d 条: %s", len(msgs), out)
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "You are terse." {
		t.Fatalf("instructions 应成为首条 system 消息，got %v", first)
	}
	if m["max_tokens"] != float64(256) {
		t.Fatalf("max_output_tokens 应映射 max_tokens，got %v", m["max_tokens"])
	}
	if _, ok := m["input"]; ok {
		t.Fatalf("responses 线的 input 键不得透传 chat wire: %s", out)
	}
	if _, ok := m["instructions"]; ok {
		t.Fatalf("instructions 不得透传 chat wire（已提升 system）: %s", out)
	}
}

// TestResponsesClient_IRMainPath_AnthropicUpstream：responses 客户端 →
// anthropic 上游的 ParseResponses 分派（批 5 前该腿吃 chat 形态）。
// 混发的 chat 形 response_format 必须进 IR 结构化字段并被 anthropic
// 序列化器**披露丢弃**，而不是经 Extensions 裸透传进严格 body
// （迁移过程中 upstream_bytes golden 实测抓到的 400 回归）。
func TestResponsesClient_IRMainPath_AnthropicUpstream(t *testing.T) {
	executor := &Executor{IR: &irAdapterForTest{}}
	cand := provider.Candidate{ProviderID: 2, CredentialID: 2, Protocol: "anthropic-messages", CatalogCode: "anthropic", RawModel: "claude-sonnet-5"}

	body := `{"model":"claude-sonnet-5","stream":false,
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"max_output_tokens":256,
		"response_format":{"type":"text"}}`

	params := &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/responses", nil),
		BodyBytes:      []byte(body),
		ClientProtocol: "openai-responses",
		ClientModel:    "claude-sonnet-5",
	}
	out, err := executor.prepareAnthropicRequestBody(params, cand, []byte(body))
	if err != nil {
		t.Fatalf("finalizeAnthropicBody: %v", err)
	}
	if strings.Contains(string(out), "response_format") {
		t.Fatalf("混发 response_format 不得进入 anthropic wire（应披露丢弃），got: %s", out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("anthropic wire 应有 1 条 user 消息，got %d: %s", len(msgs), out)
	}
}

// TestResponsesClient_CircuitOpen_FallsBackToLegacyChat：responses 分支的
// IR 断路器应急回退（镜像 2026-08-08 P0 anthropic 回退的语义）。
func TestResponsesClient_CircuitOpen_FallsBackToLegacyChat(t *testing.T) {
	executor := &Executor{IR: &circuitOpenResponsesAdapter{}}
	executor.ResponsesToChat = func(body []byte) ([]byte, error) {
		// 手写转换的应急形态：instructions → system 消息。
		return []byte(`{"model":"gpt-5.6","messages":[{"role":"system","content":"legacy"},{"role":"user","content":"hi"}]}`), nil
	}
	cand := provider.Candidate{ProviderID: 3, CredentialID: 3, Protocol: "openai-completions", CatalogCode: "openai", RawModel: "gpt-5.6"}

	body := `{"model":"gpt-5.6","stream":false,"instructions":"ignored-in-legacy","input":"hi"}`
	params := &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/responses", nil),
		BodyBytes:      []byte(body),
		ClientProtocol: "openai-responses",
		ClientModel:    "gpt-5.6",
	}
	out, err := executor.finalizeOpenAIUpstreamBody(params, cand, []byte(body))
	if err != nil {
		t.Fatalf("circuit-open fallback failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应急回退应使用 ResponsesToChat 的产物，got %d 条: %s", len(msgs), out)
	}
	if first := msgs[0].(map[string]any); first["content"] != "legacy" {
		t.Fatalf("回退产物应来自 ResponsesToChat 回调，got %v", first)
	}
}

// circuitOpenResponsesAdapter 只在 ParseResponses 上触发断路器（responses
// 客户端的主路径分派点）。
type circuitOpenResponsesAdapter struct {
	irAdapterForTest
}

func (a *circuitOpenResponsesAdapter) ParseResponses(body []byte) (*ir.InternalRequest, error) {
	return nil, transformation.ErrConverterCircuitOpen
}

// 编译期守卫：responses 分支盖章链必须覆盖 unknown-relay 哨兵语义
// （与 anthropic 分支同款；变量引用防漂移）。
var _ = paramreg.CatalogCodeUnknownRelay
