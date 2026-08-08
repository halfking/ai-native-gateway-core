package v2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// TestPickBodies_PrefersIRShadow verifies the dual-shape selector picks
// the IR shadow field when populated (Phase 2 acceptance) and falls back
// to the legacy v2.Message slice otherwise (Phase-1 compatibility).
func TestPickBodies_PrefersIRShadow(t *testing.T) {
	legacy := []Message{{Role: "user", Content: "old"}}
	irShadow := []ir.Message{{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "new"}}}}

	got := pickBodies(legacy, irShadow)
	if len(got) != 1 || got[0].Content != "new" {
		t.Fatalf("expected IR-derived slice, got %+v", got)
	}
	if got[0].RawContent != nil {
		t.Fatalf("text-only IR should not produce RawContent envelope, got %v", got[0].RawContent)
	}
}

// TestPickBodies_FallsBackToLegacy ensures the legacy v2.Message slice is
// returned verbatim when the IR field is empty (the typical case for
// Phase-1 call sites that have not migrated).
func TestPickBodies_FallsBackToLegacy(t *testing.T) {
	legacy := []Message{{Role: "user", Content: "legacy"}}
	got := pickBodies(legacy, nil)
	if len(got) != 1 || got[0].Content != "legacy" {
		t.Fatalf("expected legacy slice, got %+v", got)
	}
}

// TestPickBodies_MultimodalIRSurvivesRoundTrip — when the IR shadow
// carries a multimodal payload the resulting v2.Message slice must
// preserve it under the RawContent envelope. This is the load-bearing
// property of Phase 2: the IR Message survives the consumer hop without
// silently dropping image/audio/tool_use blocks.
func TestPickBodies_MultimodalIRSurvivesRoundTrip(t *testing.T) {
	irShadow := []ir.Message{
		{
			Role: "user",
			Content: []ir.ContentBlock{
				{Type: "text", Text: "describe"},
				{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://example.com/x.png"}},
			},
		},
	}
	v2Slice := pickBodies(nil, irShadow)
	if len(v2Slice) != 1 {
		t.Fatalf("expected 1 message, got %d", len(v2Slice))
	}
	if v2Slice[0].Content != "" {
		t.Fatalf("multimodal IR must collapse content to empty string for wire shape, got %q", v2Slice[0].Content)
	}
	raw, ok := v2Slice[0].RawContent.(json.RawMessage)
	if !ok {
		t.Fatalf("RawContent should carry envelope JSON, got %T", v2Slice[0].RawContent)
	}
	if !strings.Contains(string(raw), `"image"`) {
		t.Fatalf("envelope missing image block: %s", raw)
	}
	// And the round-trip back to IR is lossless.
	recovered, ok := recoverIRRaw(v2Slice[0])
	if !ok {
		t.Fatalf("recoverIRRaw returned false on Phase-2 produced envelope")
	}
	if len(recovered.Content) != 2 || recovered.Content[1].Image == nil {
		t.Fatalf("round-trip lost blocks: %+v", recovered.Content)
	}
	if recovered.Content[1].Image.URL != "https://example.com/x.png" {
		t.Fatalf("image URL lost: %+v", recovered.Content[1].Image)
	}
}

// TestBodiesRecord_IRFieldsRoundTripThroughSafeJSONMarshal — the IR
// shadow fields on BodiesRecord, when populated, must produce wire-JSON
// byte-identical to the legacy form for text-only payloads (the dominant
// case in production today). This is the regression-guard for the
// Phase 2 acceptance criterion: "wire JSON shape is preserved for the
// dominant case".
//
// Multimodal payloads now persist too: MarshalJSON writes the IR content-block
// array into the `content` column, so the blocks survive a DB round-trip.
// Text-only rows are unaffected and stay byte-identical. Full block-by-block
// coverage lives in ir_message_adapter_lossless_test.go; this test only pins
// the two shapes' wire skeleton.
func TestBodiesRecord_IRFieldsRoundTripThroughSafeJSONMarshal(t *testing.T) {
	cases := []struct {
		name       string
		legacy     []Message
		irShadow   []ir.Message
		wantSubstr []string // substrings that must appear in the marshaled JSON
	}{
		{
			name:   "text-only IR (legacy wire shape)",
			legacy: []Message{{Role: "user", Content: "hi"}},
			irShadow: []ir.Message{{
				Role:    "user",
				Content: []ir.ContentBlock{{Type: "text", Text: "hi"}},
			}},
			wantSubstr: []string{`"role":"user"`, `"content":"hi"`},
		},
		{
			name:   "multimodal IR persists its content block array",
			legacy: nil,
			irShadow: []ir.Message{{
				Role: "user",
				Content: []ir.ContentBlock{
					{Type: "text", Text: "look"},
					{Type: "image", Image: &ir.ImageSource{Type: "url", URL: "https://example.com/y.png"}},
				},
			}},
			// The block array lands in `content`, so the image URL is on the
			// wire rather than living only in the in-memory RawContent.
			wantSubstr: []string{`"role":"user"`, `"image"`, `https://example.com/y.png`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			merged := pickBodiesForWrite(c.legacy, c.irShadow)
			b, err := safeJSONMarshal(merged)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			s := string(b)
			for _, want := range c.wantSubstr {
				if !strings.Contains(s, want) {
					t.Fatalf("marshaled body missing %q: %s", want, s)
				}
			}
		})
	}
}

// TestBodiesRecord_LegacyFieldUsedWhenIREmpty — Phase-1 call sites that
// have not migrated must keep working unchanged. When IRShadow is nil the
// helper returns the legacy slice verbatim and the wire JSON is byte-
// identical to the pre-Phase-2 output.
func TestBodiesRecord_LegacyFieldUsedWhenIREmpty(t *testing.T) {
	legacy := []Message{{Role: "user", Content: "legacy"}}
	merged := pickBodiesForWrite(legacy, nil)
	b, err := safeJSONMarshal(merged)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `[{"role":"user","content":"legacy"}]` {
		t.Fatalf("legacy wire JSON regressed: %s", b)
	}
}

// TestIRMessagesToV2_StableForTextOnly asserts the IR→v2 path is fully
// byte-stable for text-only IR Messages: every test sees the same v2
// wire shape regardless of how the IR Message was built.
func TestIRMessagesToV2_StableForTextOnly(t *testing.T) {
	a := []ir.Message{
		{Role: "system", Content: []ir.ContentBlock{{Type: "text", Text: "be brief"}}},
		{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "hi"}}},
	}
	v := IRMessagesToV2(a)
	if len(v) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(v))
	}
	if v[0].Content != "be brief" || v[1].Content != "hi" {
		t.Fatalf("text-only IR did not collapse to v2 string: %+v", v)
	}
	if v[0].RawContent != nil || v[1].RawContent != nil {
		t.Fatalf("text-only IR must not produce envelope RawContent: %+v", v)
	}
}
