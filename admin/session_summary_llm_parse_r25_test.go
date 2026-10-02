package admin

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// R25-T1 (audit round 25, test debt from 81acf4056): the session-summary
// LLM pipeline replaced a placeholder stub on 2026-09-29 but shipped with
// zero coverage on its parsing helpers. These tests pin the wire shapes the
// admin LLM task actually returns so a future revert to byte truncation or
// a stub cannot pass silently.

func TestParseSummaryLLMContentShapes(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantTitle  string
		wantSumHas string
	}{
		{"plain json", `{"title":"模型选型","summary":"用户询问了模型选型方案"}`, "模型选型", "模型选型方案"},
		{"fenced json", "```json\n{\"title\":\"路由审计\",\"summary\":\"审计了路由链路\"}\n```", "路由审计", "路由链路"},
		{"json without title defaults", `{"summary":"只有摘要"}`, "会话总结", "只有摘要"},
		{"heuristic text", "标题行\n正文第一行\n正文第二行", "标题行", "正文第一行"},
		// Leading whitespace is trimmed before the heuristic runs, so the
		// first non-empty line becomes the title and the summary falls back
		// to the whole text (no second line left).
		{"leading blank line", "\n正文内容", "正文内容", "正文内容"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, summary, err := parseSummaryLLMContent(tc.content)
			if err != nil {
				t.Fatalf("parseSummaryLLMContent: %v", err)
			}
			if title != tc.wantTitle {
				t.Fatalf("title = %q, want %q", title, tc.wantTitle)
			}
			if !strings.Contains(summary, tc.wantSumHas) {
				t.Fatalf("summary %q missing %q", summary, tc.wantSumHas)
			}
		})
	}
}

// generateFallbackSummary must truncate by rune, never mid-rune: a CJK
// prefix cut must still be valid UTF-8 (审计二十一轮 root fix).
func TestGenerateFallbackSummaryRuneTruncation(t *testing.T) {
	long := strings.Repeat("轮对话内容长文本", 100) // 800 runes
	title, summary, err := generateFallbackSummary("=== Turn 1 ===\n" + long)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(title, "(1轮)") {
		t.Fatalf("title %q missing turn count", title)
	}
	runes := []rune(summary)
	if len(runes) != 201 { // 200 + ellipsis
		t.Fatalf("summary runes = %d, want 201 (200 + …)", len(runes))
	}
	if !utf8.ValidString(summary) {
		t.Fatal("summary is not valid UTF-8 after truncation")
	}
	if !strings.HasSuffix(summary, "…") {
		t.Fatal("truncated summary missing ellipsis marker")
	}
}
