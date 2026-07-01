package transformation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// Phase 1 Round-trip 测试：验证扩展属性在协议转换往返中无损保留。
//
// 测试矩阵来源：plans/渐进式IR迁移/04-测试矩阵.md §3

// extractField 从 body 提取顶层字段的原始 JSON 值。
func extractField(t *testing.T, body []byte, field string) json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, body)
	}
	return m[field]
}

// --- 场景 1: 自定义字段往返（OpenAI 顶层非标字段）---

func TestRoundtrip_CustomField_OpenAIToAnthropicResponse(t *testing.T) {
	tr := NewIRTransport()

	// 1. OpenAI 请求，含非标字段 custom_field + cache
	reqBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":1024,"custom_field":"value123","cache":true}`)

	env := newEnvelopeWithBody("openai-chat", "anthropic-messages", reqBody, "gpt-4o")

	// 2. Convert（请求方向）会提取 custom_field + cache 到 ExtensionsBag
	// 注意：round-trip 测试中 Convert 和 ConvertResponse 必须作用于同一 envelope
	// （Extensions 需要在两个阶段间传递），所以不能用 cloneEnvelope
	upstreamReq, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	// 3. 验证 custom_field 未泄漏到 Anthropic 请求（Anthropic 不认识这些字段）
	if strings.Contains(string(upstreamReq), "custom_field") {
		t.Errorf("Anthropic upstream should NOT contain custom_field: %s", upstreamReq)
	}

	// 4. ConvertResponse（响应方向）应把 custom_field + cache 还原到 OpenAI 响应
	upstreamResp := []byte(anthropicResponse)
	clientResp, err := tr.ConvertResponse(context.Background(), env, upstreamResp)
	if err != nil {
		t.Fatalf("ConvertResponse: %v", err)
	}

	// 5. 验证响应包含 custom_field + cache
	customField := extractField(t, clientResp, "custom_field")
	if string(customField) != `"value123"` {
		t.Errorf("custom_field = %s, want \"value123\"\nfull: %s", customField, clientResp)
	}
	cacheField := extractField(t, clientResp, "cache")
	if string(cacheField) != `true` {
		t.Errorf("cache = %s, want true\nfull: %s", cacheField, clientResp)
	}
}

// --- 场景 2: anthropic-beta header 往返 ---

func TestRoundtrip_AnthropicBetaHeader(t *testing.T) {
	tr := NewIRTransport()

	// 1. Anthropic 请求 + anthropic-beta header
	reqBody := []byte(anthropicBody)
	env := newEnvelopeWithBody("anthropic-messages", "openai-chat", reqBody, "gpt-4o")

	// 设置 header（模拟客户端请求带 anthropic-beta）
	env.Transport.R = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	env.Transport.R.Header.Set("anthropic-beta", "beta-2023-06-01")

	// 2. Convert 会提取 anthropic-beta header 到 ExtensionsBag.Headers
	_, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	// 3. 验证 ExtensionsBag.Headers 包含 anthropic-beta
	if env.Transport.Extensions.Headers["anthropic-beta"] != "beta-2023-06-01" {
		t.Errorf("anthropic-beta header not captured in ExtensionsBag: %v", env.Transport.Extensions.Headers)
	}
}

// --- 场景 3: 多个非标字段往返 ---

func TestRoundtrip_MultipleCustomFields(t *testing.T) {
	tr := NewIRTransport()

	reqBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"presence_penalty":0.5,"custom_a":"a1","custom_b":{"nested":true},"custom_c":[1,2,3]}`)

	env := newEnvelopeWithBody("openai-chat", "anthropic-messages", reqBody, "gpt-4o")

	_, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	// 验证非标字段被提取（presence_penalty 是标准字段，不应被提取）
	bag := env.Transport.Extensions
	if _, ok := bag.ClientRaw["custom_a"]; !ok {
		t.Error("custom_a should be extracted")
	}
	if _, ok := bag.ClientRaw["custom_b"]; !ok {
		t.Error("custom_b should be extracted")
	}
	if _, ok := bag.ClientRaw["custom_c"]; !ok {
		t.Error("custom_c should be extracted")
	}
	if _, ok := bag.ClientRaw["presence_penalty"]; ok {
		t.Error("presence_penalty is standard field, should NOT be extracted")
	}

	// ConvertResponse 还原
	clientResp, err := tr.ConvertResponse(context.Background(), env, []byte(anthropicResponse))
	if err != nil {
		t.Fatalf("ConvertResponse: %v", err)
	}

	if string(extractField(t, clientResp, "custom_a")) != `"a1"` {
		t.Errorf("custom_a not restored: %s", clientResp)
	}
	customB := extractField(t, clientResp, "custom_b")
	if !strings.Contains(string(customB), "nested") {
		t.Errorf("custom_b not restored correctly: %s", customB)
	}
}

// --- 场景 4: 无非标字段时 ExtensionsBag 为空（标准请求）---

func TestRoundtrip_NoExtensions_StandardRequest(t *testing.T) {
	tr := NewIRTransport()

	env := newEnvelopeWithBody("openai-chat", "anthropic-messages", []byte(openaiBody), "gpt-4o")

	_, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	// 标准请求：ClientRaw 应为空
	if len(env.Transport.Extensions.ClientRaw) != 0 {
		t.Errorf("standard request should have empty ClientRaw: %v", env.Transport.Extensions.ClientRaw)
	}
}

// --- 场景 5: ExtensionsBag 在 IR Convert 链中完整传递 ---

func TestRoundtrip_BagPreservedAcrossConvertChain(t *testing.T) {
	tr := NewIRTransport()

	reqBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":1024,"tag":"important"}`)
	env := newEnvelopeWithBody("openai-chat", "anthropic-messages", reqBody, "gpt-4o")

	// 第一次 Convert
	_, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert 1: %v", err)
	}
	tagAfterConvert1 := env.Transport.Extensions.ClientRaw["tag"]

	// 第二次 Convert（模拟响应阶段再次 Extract 前的状态）
	_, err = tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert 2: %v", err)
	}
	tagAfterConvert2 := env.Transport.Extensions.ClientRaw["tag"]

	// tag 应稳定保留
	if string(tagAfterConvert1) != `"important"` || string(tagAfterConvert2) != `"important"` {
		t.Errorf("tag not preserved: after1=%s after2=%s", tagAfterConvert1, tagAfterConvert2)
	}
}

// --- 场景 6: Extractor + Restorer 直接组合测试 ---

func TestRoundtrip_ExtractorRestorer_Direct(t *testing.T) {
	ext := &IRExtensionExtractor{}
	res := &IRExtensionRestorer{}

	original := []byte(`{"model":"gpt-4o","custom_x":"x_val"}`)

	// 1. Extract
	bag, err := ext.Extract(original, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	// 2. 模拟 IR 处理后只剩标准字段（model 被处理，custom_x 丢失）
	processed := []byte(`{"model":"gpt-4o","choices":[]}`)

	// 3. Restore 应把 custom_x 加回来
	restored, err := res.Restore(processed, bag)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if string(extractField(t, restored, "custom_x")) != `"x_val"` {
		t.Errorf("custom_x not restored: %s", restored)
	}
	// model 不应被覆盖
	if string(extractField(t, restored, "model")) != `"gpt-4o"` {
		t.Errorf("model should remain: %s", restored)
	}
}

// --- 场景 7-8: 两端都是 Anthropic 时的往返 ---

func TestRoundtrip_AnthropicToAnthropic_CustomField(t *testing.T) {
	tr := NewIRTransport()

	reqBody := []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}],"max_tokens":1024,"thinking_budget":5000}`)
	env := newEnvelopeWithBody("anthropic-messages", "anthropic-messages", reqBody, "claude-sonnet-4-20250514")

	_, err := tr.Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	if _, ok := env.Transport.Extensions.ClientRaw["thinking_budget"]; !ok {
		t.Errorf("thinking_budget should be extracted: %v", env.Transport.Extensions.ClientRaw)
	}
}

// --- 场景 9-10: isStandardField 单元测试 ---

func TestIsStandardField(t *testing.T) {
	standard := []string{"model", "messages", "max_tokens", "temperature", "tools", "system", "thinking"}
	nonStandard := []string{"custom_field", "cache", "presence_penalty_v2", "my_extension", "extra_body"}

	for _, f := range standard {
		if !isStandardField(f) {
			t.Errorf("isStandardField(%q) = false, want true", f)
		}
	}
	for _, f := range nonStandard {
		if isStandardField(f) {
			t.Errorf("isStandardField(%q) = true, want false", f)
		}
	}
}

// --- 场景 11-12: 空 body / nil headers 容错 ---

func TestRoundtrip_EmptyBody(t *testing.T) {
	ext := &IRExtensionExtractor{}
	bag, err := ext.Extract(nil, nil)
	if err != nil {
		t.Fatalf("Extract(nil,nil): %v", err)
	}
	if bag == nil {
		t.Fatal("bag should not be nil")
	}
	if len(bag.ClientRaw) != 0 {
		t.Errorf("ClientRaw should be empty for nil body: %v", bag.ClientRaw)
	}
}

func TestRoundtrip_InvalidJSONBody(t *testing.T) {
	ext := &IRExtensionExtractor{}
	// 无效 JSON 不应报错，只是提取不到字段
	bag, err := ext.Extract([]byte(`{invalid`), nil)
	if err != nil {
		t.Fatalf("Extract invalid JSON should not error: %v", err)
	}
	if len(bag.ClientRaw) != 0 {
		t.Errorf("ClientRaw should be empty for invalid JSON: %v", bag.ClientRaw)
	}
}

// newEnvelopeWithBody 用给定 body 创建 envelope。
func newEnvelopeWithBody(client, upstream string, body []byte, model string) *domain.RequestEnvelope {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return domain.NewEnvelopeBuilder("roundtrip-req").
		WithTransport(&domain.TransportContext{
			R:                req,
			BodyBytes:        body,
			ClientProtocol:   client,
			UpstreamProtocol: upstream,
			ClientModel:      model,
		}).
		Build()
}
