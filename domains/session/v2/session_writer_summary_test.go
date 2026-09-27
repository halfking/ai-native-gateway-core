package v2

import (
	"strings"
	"testing"
)

// R69 24h 审计轮：会话摘要面的人类可读性（审计 checklist"每一个轮会话的
// 摘要方式，能够抽取会话的信息，便于人类查看（去除格式）"）。
// summarizeMessages 产出 title/last_request_summary/last_response_summary
// 等四处供人阅读的摘要，此前 markdown 装饰原样进入存储与展示。

func TestStripMarkdownNoiseRemovesDecorations(t *testing.T) {
	in := "## 任务目标\n\n- 第一条 **要点**\n* 第二条 `code` 要点\n```go\nfmt.Println(\"隐藏\")\n```\n尾行"
	got := stripMarkdownNoise(in)
	for _, banned := range []string{"##", "**", "`", "fmt.Println", "任务目标#"} {
		if strings.Contains(got, banned) {
			t.Errorf("stripMarkdownNoise output contains %q: %q", banned, got)
		}
	}
	for _, want := range []string{"任务目标", "第一条 要点", "第二条 code 要点", "尾行"} {
		if !strings.Contains(got, want) {
			t.Errorf("stripMarkdownNoise output missing %q: %q", want, got)
		}
	}
}

func TestSummarizeMessagesStripsMarkdownBeforeTruncation(t *testing.T) {
	long := "## 标题\n" + strings.Repeat("正文句子。 ", 60) // 远超 200 runes
	msgs := []Message{{Role: "user", Content: long}}
	got := summarizeMessages(msgs)
	if !strings.HasPrefix(got, "标题 正文句子。") {
		t.Errorf("summary should start with de-formatted text, got %q", truncateForLog(got))
	}
	runes := []rune(strings.TrimSuffix(got, "..."))
	if len(runes) > 200 {
		t.Errorf("summary exceeds 200 runes before ellipsis: %d", len(runes))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("long summary must be truncated with ellipsis")
	}
}

// R71 审计：响应整体是一个代码围栏（编码网关最常见的形态之一）或围栏
// 未闭合（截断响应）时，stripMarkdownNoise 把全部行丢弃返回空串——摘要
// 信息量从"有"变"无"。修复后剥噪为空且原文非空时回退原文。
func TestSummarizeMessages_FenceOnlyContentFallsBackToRaw(t *testing.T) {
	fence := "```go\nfmt.Println(\"hello\")\n```"
	got := summarizeMessages([]Message{{Role: "assistant", Content: fence}})
	if got == "" {
		t.Fatal("fence-only content must fall back to raw content, got empty summary")
	}
	if !strings.Contains(got, "fmt.Println") {
		t.Errorf("fallback summary should keep raw content, got %q", truncateForLog(got))
	}

	unclosed := "```text\npartial response truncated"
	got2 := summarizeMessages([]Message{{Role: "assistant", Content: unclosed}})
	if !strings.Contains(got2, "partial response truncated") {
		t.Errorf("unclosed fence must fall back to raw content, got %q", truncateForLog(got2))
	}
}

func TestSummarizeMessagesEmptyAndCount(t *testing.T) {
	if got := summarizeMessages(nil); got != "" {
		t.Errorf("empty messages → %q, want empty", got)
	}
	msgs := []Message{
		{Role: "user", Content: "# 目标\n做一件事"},
		{Role: "assistant", Content: "好的"},
	}
	got := summarizeMessages(msgs)
	if !strings.Contains(got, "(2 messages)") {
		t.Errorf("multi-message summary must keep count suffix, got %q", got)
	}
	if strings.Contains(got, "#") {
		t.Errorf("summary still contains markdown decoration: %q", got)
	}
}

func truncateForLog(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
