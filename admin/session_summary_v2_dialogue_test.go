//go:build broken_pending_repair
// +build broken_pending_repair

// QUARANTINED 2026-08-26 (V6-W1.6 R8 落库轮): references extractDialogueContent,
// dropped by merge d2cbaf88b. Blocked compilation of the admin test package.
// Rewrite against the current dialogue extraction path, then remove the tag.

package admin

import (
	"strings"
	"testing"
)

func TestExtractDialogueContent_SkipsSystem(t *testing.T) {
	delta := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are Cursor IDE tools catalog"},
			map[string]any{"role": "user", "content": "修复登录"},
			map[string]any{"role": "assistant", "content": "好的"},
		},
	}
	got := extractDialogueContent(delta, "")
	if strings.Contains(got, "Cursor IDE") || strings.Contains(got, "system") {
		t.Fatalf("system content leaked: %q", got)
	}
	if !strings.Contains(got, "修复登录") || !strings.Contains(got, "好的") {
		t.Fatalf("expected user/assistant text, got %q", got)
	}

	userOnly := extractDialogueContent(delta, "user")
	if userOnly != "修复登录" {
		t.Fatalf("user filter = %q, want 修复登录", userOnly)
	}
}

func TestBuildConversationText_SkipsSystem(t *testing.T) {
	text := buildConversationText([]turnForSummary{{
		TurnNo: 1,
		RequestDelta: map[string]any{
			"messages": []any{
				map[string]any{"role": "system", "content": "long system prompt"},
				map[string]any{"role": "user", "content": "hello"},
			},
		},
		ResponseDelta: map[string]any{
			"choices": []any{
				map[string]any{"message": map[string]any{"role": "assistant", "content": "hi"}},
			},
		},
	}})
	if strings.Contains(text, "long system prompt") {
		t.Fatalf("system leaked into summary corpus:\n%s", text)
	}
	if !strings.Contains(text, "hello") || !strings.Contains(text, "hi") {
		t.Fatalf("expected dialogue text:\n%s", text)
	}
}
