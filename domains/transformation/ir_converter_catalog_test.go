package transformation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
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

func TestTransportIRConverter_CrossProviderDoesNotRestoreExtensions(t *testing.T) {
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
			"deepseek_only": json.RawMessage(`true`),
		},
	}

	out, err := conv.SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	if strings.Contains(string(out), "deepseek_only") {
		t.Fatalf("cross-provider extension leaked: %s", out)
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
