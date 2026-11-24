package sessionsummary

import (
	"strings"
	"testing"
)

func TestStripThinkingTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple thinking tag",
			input:    `<think>Some thinking content</think>{"title":"test"}`,
			expected: `{"title":"test"}`,
		},
		{
			name:     "thinking variant tag (F-D)",
			input:    `<thinking>Long reasoning here</thinking>{"title":"test"}`,
			expected: `{"title":"test"}`,
		},
		{
			name:     "unclosed thinking variant tag (F-D)",
			input:    `<thinking>Reasoning content{"title":"test"}`,
			expected: `Reasoning content{"title":"test"}`,
		},
		{
			name:     "thinking tag with JSON inside",
			input:    `<think>We need answer JSON based on one truncated message. Need infer.</think>{"title":"来源要点审阅","summary":"本次会话要求审阅"}`,
			expected: `{"title":"来源要点审阅","summary":"本次会话要求审阅"}`,
		},
		{
			name:     "multiple thinking tags",
			input:    `<think>First thought</think><think>Second thought</think>{"title":"test"}`,
			expected: `{"title":"test"}`,
		},
		{
			name:     "unclosed thinking tag",
			input:    `<think>Some thinking content{"title":"test"}`,
			expected: `Some thinking content{"title":"test"}`,
		},
		{
			name:     "unclosed thinking tag - extractJSON will handle",
			input:    `<think>Some text before JSON{"title":"actual","summary":"test"}`,
			expected: `Some text before JSON{"title":"actual","summary":"test"}`,
		},
		{
			name:     "response wrapper tags",
			input:    `<response>{"title":"test"}</response>`,
			expected: `{"title":"test"}`,
		},
		{
			name:     "output wrapper tags",
			input:    `<output>{"title":"test"}</output>`,
			expected: `{"title":"test"}`,
		},
		{
			name:     "thinking tag before JSON in markdown",
			input:    "<think>Need to generate JSON</think>\n```json\n{\"title\":\"test\"}\n```",
			expected: "```json\n{\"title\":\"test\"}\n```",
		},
		{
			name:     "no tags",
			input:    `{"title":"test"}`,
			expected: `{"title":"test"}`,
		},
		{
			name:     "thinking with braces in content",
			input:    `<think>The JSON should have {title} field</think>{"title":"actual"}`,
			expected: `{"title":"actual"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripThinkingTags(tt.input)
			if result != tt.expected {
				t.Errorf("stripThinkingTags() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestExtractJSON_WithThinkingTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "real production error case",
			input: `<think>We need answer JSON based on one truncated message. Need infer. User asks update summary, source only one user payload.</think>{
  "title": "来源要点审阅",
  "summary": "本次会话要求审阅全部来源片段，并按历史顺序提取有证据支持、可长期保留的要点",
  "key_topics": ["审阅", "要点提取", "历史顺序"],
  "user_intent": "data_analysis",
  "agent_type": "unknown",
  "expert_type": "unknown",
  "tags": ["审阅", "数据分析"]
}`,
			expected: `{
  "title": "来源要点审阅",
  "summary": "本次会话要求审阅全部来源片段，并按历史顺序提取有证据支持、可长期保留的要点",
  "key_topics": ["审阅", "要点提取", "历史顺序"],
  "user_intent": "data_analysis",
  "agent_type": "unknown",
  "expert_type": "unknown",
  "tags": ["审阅", "数据分析"]
}`,
		},
		{
			name:     "thinking tag with markdown JSON",
			input:    "<think>Generate JSON response</think>\n```json\n{\"title\":\"test\"}\n```",
			expected: `{"title":"test"}`,
		},
		{
			name: "thinking tag before plain JSON",
			input: `<think>Need to respond with JSON</think>
{"title":"test","summary":"summary text"}`,
			expected: `{"title":"test","summary":"summary text"}`,
		},
		{
			name:     "unclosed thinking tag - extractJSON finds JSON",
			input:    `<think>Some thinking before JSON{"title":"test","summary":"content"}`,
			expected: `{"title":"test","summary":"content"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractJSON(tt.input)
			if result != tt.expected {
				t.Errorf("extractJSON() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestParseSummaryResponse_WithThinkingTags(t *testing.T) {
	s := &Summarizer{}

	// Test with thinking tags
	response := `<think>We need answer JSON based on truncated message.</think>{
  "title": "测试标题",
  "summary": "这是测试摘要内容",
  "key_topics": ["主题1", "主题2"],
  "user_intent": "chat",
  "agent_type": "cursor",
  "expert_type": "software_engineering",
  "tags": ["测试", "开发"]
}`

	summary, err := s.parseSummaryResponse(response, "test-session-key")
	if err != nil {
		t.Fatalf("parseSummaryResponse() error = %v", err)
	}

	if summary.Title != "测试标题" {
		t.Errorf("Title = %q, want %q", summary.Title, "测试标题")
	}
	if summary.Summary != "这是测试摘要内容" {
		t.Errorf("Summary = %q, want %q", summary.Summary, "这是测试摘要内容")
	}
	if len(summary.KeyTopics) != 2 {
		t.Errorf("KeyTopics length = %d, want 2", len(summary.KeyTopics))
	}
	if summary.AgentType != "cursor" {
		t.Errorf("AgentType = %q, want %q", summary.AgentType, "cursor")
	}
}

// 2026-11-24 批判审计轮回归测试：生产 6h 实测三种失败形态的定案样本。
// F-A：thinking 吃光 max_tokens ⇒ 输出只剩 "{"（111 次/6h 的主导形态）。

func TestGenerateTitle_StripsThinkingAndFallsBack(t *testing.T) {
	// F-C：thinking 包裹的标题必须剥出正文；空剥离结果走首句降级不落垃圾。
	if got := stripThinkingTags("<think>计划先分析消息主题</think>数据库迁移审计"); got != "数据库迁移审计" {
		t.Errorf("stripThinkingTags(title) = %q, want 数据库迁移审计", got)
	}
	if got := stripThinkingTags("<think>只有思考没有正文"); got != "只有思考没有正文" {
		t.Errorf("stripThinkingTags(unclosed title) = %q, want 残余正文保留", got)
	}
	if got := stripThinkingTags("<think>思考段</think>"); got != "" {
		t.Errorf("stripThinkingTags(empty body) = %q, want empty (触发首句降级)", got)
	}
}

func TestMaxSummaryCompletionTokens_RaisedForThinking(t *testing.T) {
	// F-A 守护：600 会被 thinking 吃光导致 JSON 截断（生产 111 次/6h）；
	// 上限必须给 thinking + JSON 本体留余量。
	if maxSummaryCompletionTokens < 1024 {
		t.Errorf("maxSummaryCompletionTokens = %d, want >= 1024 (thinking 余量)", maxSummaryCompletionTokens)
	}
}

func TestPromptsForbidEchoAndEllipsis(t *testing.T) {
	// F-E 守护：两条 prompt 都必须包含禁止回声与省略号的硬性要求。
	s := &Summarizer{}
	full := s.buildSummaryPrompt([]SessionMessage{{RequestID: "r1", Role: "user", Content: "hello"}}, "")
	rolling := s.buildRollingPrompt("", []SessionMessage{{RequestID: "r1", Role: "user", Content: "hello"}}, "")
	for name, p := range map[string]string{"full": full, "rolling": rolling} {
		if !strings.Contains(p, "不要复述或引用对话内容") {
			t.Errorf("%s prompt missing echo prohibition", name)
		}
		if !strings.Contains(p, "禁止用省略号") {
			t.Errorf("%s prompt missing ellipsis prohibition", name)
		}
	}
}
