package sessionsummary

import (
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
