package transformation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/irconv"
)

func TestTransportIRConverter_SameProviderRestoresExtensions(t *testing.T) {
	inner := &mockIRAdapter{
		serializeOpenAIFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"gpt-4o","messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)
	ctx := &domain.TransportContext{
		ClientCatalogCode:   "openai",
		UpstreamCatalogCode: "openai",
	}
	conv.SetContext(ctx)

	req := &ir.InternalRequest{
		Model:          "gpt-4o",
		SourceProtocol: ir.ProtocolOpenAIChat,
		Extensions: map[string]json.RawMessage{
			"openai_specific": json.RawMessage(`true`),
		},
	}

	out, err := conv.SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	if !strings.Contains(string(out), "openai_specific") {
		t.Fatalf("same-provider extension not restored: %s", out)
	}
}

// TestTransportIRConverter_CrossProviderExtensionsPassthrough 验证跨 provider 时
// 未登记字段的透传行为（2026-08-11 P2 修复后的正确语义）。
//
// 修复前：ClientCatalogCode != UpstreamCatalogCode 时整包 Extensions 被丢弃。
// 修复后：未登记字段按"未知即透传"原则无条件还原；仅方言私有的已登记字段才会被裁剪。
//
// "deepseek_only" 是测试名字，但它在注册表里未登记，所以是未知字段，会透传。
// 测试改为验证这一新语义。如需阻止某个字段，应在 strip_request_fields 里配置。
func TestTransportIRConverter_CrossProviderExtensionsPassthrough(t *testing.T) {
	inner := &mockIRAdapter{
		serializeOpenAIFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"gpt-4o","messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)
	ctx := &domain.TransportContext{
		ClientCatalogCode:   "deepseek",
		UpstreamCatalogCode: "openai",
	}
	conv.SetContext(ctx)

	req := &ir.InternalRequest{
		Model:          "gpt-4o",
		SourceProtocol: ir.ProtocolOpenAIChat,
		Extensions: map[string]json.RawMessage{
			"some_unknown_field": json.RawMessage(`true`),
		},
	}

	out, err := conv.SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	// 修复后：未知字段无条件透传，即使跨 provider
	if !strings.Contains(string(out), "some_unknown_field") {
		t.Errorf("unknown extension should pass through cross-provider: %s", out)
	}
}

// TestTransportIRConverter_DialectScopedFieldDroppedCrossProvider 验证方言私有
// 已登记字段在跨 dialect 时被正确裁剪（隔离能力保留）。
//
// Anthropic 私有的 output_config 字段不应泄漏给 DeepSeek 上游。
func TestTransportIRConverter_DialectScopedFieldDroppedCrossProvider(t *testing.T) {
	inner := &mockIRAdapter{
		serializeOpenAIFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"gpt-4o","messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)
	req := &ir.InternalRequest{
		Model:          "gpt-4o",
		SourceProtocol: ir.ProtocolAnthropicMessages,
		TargetProvider: "deepseek",
		Extensions: map[string]json.RawMessage{
			"output_config": json.RawMessage(`{"effort":"high"}`), // Anthropic 私有
		},
	}
	out, err := conv.SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	// output_config 是 Anthropic 方言私有字段，不该出现在 OpenAI/DeepSeek 请求里
	if strings.Contains(string(out), "output_config") {
		t.Errorf("Anthropic private field output_config leaked to OpenAI/DeepSeek upstream: %s", out)
	}
}

func TestTransportIRConverter_ScopedAnthropicPassthroughUnknownField(t *testing.T) {
	inner := &mockIRAdapter{
		serializeAnthropicFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"claude-sonnet-4","messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)
	var scoped irconv.Converter = conv.WithProviderScope(7)
	contextual, ok := scoped.(interface {
		SetContext(*domain.TransportContext)
	})
	if !ok {
		t.Fatal("scoped converter does not expose request context")
	}

	// 未知字段（注册表未登记）无条件透传，无论 catalog 是否相同
	req := &ir.InternalRequest{
		SourceProtocol: ir.ProtocolAnthropicMessages,
		Extensions: map[string]json.RawMessage{
			"totally_unknown_field": json.RawMessage(`true`),
		},
	}

	contextual.SetContext(&domain.TransportContext{ClientCatalogCode: "anthropic", UpstreamCatalogCode: "anthropic"})
	out, err := scoped.SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic same-provider: %v", err)
	}
	if !strings.Contains(string(out), "totally_unknown_field") {
		t.Errorf("unknown extension should always pass through (same-provider): %s", out)
	}

	contextual.SetContext(&domain.TransportContext{ClientCatalogCode: "deepseek", UpstreamCatalogCode: "openai"})
	out, err = scoped.SerializeAnthropic(req)
	if err != nil {
		t.Fatalf("SerializeAnthropic cross-provider: %v", err)
	}
	if !strings.Contains(string(out), "totally_unknown_field") {
		t.Errorf("unknown extension should always pass through (cross-provider): %s", out)
	}
}
func TestTransportIRConverter_NoCatalogHintRestoresWhenProtocolMatches(t *testing.T) {
	inner := &mockIRAdapter{
		serializeOpenAIFunc: func(req *ir.InternalRequest) ([]byte, error) {
			return []byte(`{"model":"gpt-4o","messages":[]}`), nil
		},
	}
	conv := NewTransportIRConverter(inner)
	// No context set, catalog hint unavailable

	req := &ir.InternalRequest{
		Model:          "gpt-4o",
		SourceProtocol: ir.ProtocolOpenAIChat,
		Extensions: map[string]json.RawMessage{
			"legacy_field": json.RawMessage(`"preserved"`),
		},
	}

	out, err := conv.SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	// When catalog hint is unavailable, fall back to protocol-only check
	if !strings.Contains(string(out), "legacy_field") {
		t.Fatalf("extension not restored when catalog hint missing: %s", out)
	}
}
