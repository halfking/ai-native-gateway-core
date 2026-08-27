package requestfact

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

func TestDecodedRequestDocumentUsesExistingSessionV2Adapter(t *testing.T) {
	request := &ir.InternalRequest{
		Model:          "model",
		SourceProtocol: ir.ProtocolOpenAIChat,
		Messages: []ir.Message{{
			Role: "user",
			Content: []ir.ContentBlock{
				{Type: "text", Text: "hello"},
				{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://example.invalid/image.png"}},
			},
		}},
	}
	document, err := ir.EncodeRequestDocument(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ir.DecodeRequestDocument(document)
	if err != nil {
		t.Fatal(err)
	}

	persisted := v2.IRMessagesToV2(decoded.Messages)
	if len(persisted) != 1 || persisted[0].Content != "" || persisted[0].RawContent == nil {
		t.Fatalf("IRMessagesToV2() = %#v, want rich dual-shape message", persisted)
	}
	restored := v2.IRMessagesFromV2(persisted)
	if len(restored) != 1 || len(restored[0].Content) != 2 {
		t.Fatalf("IRMessagesFromV2() = %#v, want two content blocks", restored)
	}
	if got := restored[0].Content[1].Image; got == nil || got.URL != "https://example.invalid/image.png" {
		t.Fatalf("restored image = %#v", got)
	}
}
