package requestfact_test

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

func TestSessionV2AdapterRemainsCanonicalRichContentBoundary(t *testing.T) {
	t.Parallel()

	message := ir.Message{
		Role: "assistant",
		Content: []ir.ContentBlock{
			{Type: "text", Text: "inspect this image"},
			{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://example.invalid/image.png"}},
		},
	}

	projected := v2.IRMessagesToV2([]ir.Message{message})
	if len(projected) != 1 {
		t.Fatalf("IRMessagesToV2() returned %d messages, want 1", len(projected))
	}
	if projected[0].Content != "" || projected[0].RawContent == nil {
		t.Fatalf("rich message did not select Session V2 dual-shape envelope: %#v", projected[0])
	}

	restored := v2.IRMessagesFromV2(projected)
	if len(restored) != 1 || len(restored[0].Content) != 2 {
		t.Fatalf("IRMessagesFromV2() restored %#v, want two content blocks", restored)
	}
	if got := restored[0].Content[1].Image; got == nil || got.URL != "https://example.invalid/image.png" {
		t.Fatalf("image block = %#v, want URL-preserved image", got)
	}
}
