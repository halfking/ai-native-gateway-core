package sessionsummary

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSystemPromptFromRequestBody 覆盖三种主流协议的系统提示词提取，
// 输入样例直接来自真实客户端（Cursor / ZCode / opencode）的系统提示词首部。
func TestSystemPromptFromRequestBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			name: "openai chat with cursor system prompt",
			body: `{"model":"gpt-4","messages":[
				{"role":"system","content":"You are an AI coding assistant, powered by Composer. You operate in Cursor."},
				{"role":"user","content":"hello"}]}`,
			wantSub: "operate in Cursor",
		},
		{
			name:    "openai chat with opencode",
			body:    `{"messages":[{"role":"system","content":"You are opencode, an interactive CLI tool that helps users with software engineering tasks."}]}`,
			wantSub: "You are opencode",
		},
		{
			name:    "anthropic top-level system string",
			body:    `{"model":"claude-4","system":"You are ZCode, an interactive coding agent","messages":[{"role":"user","content":"hi"}]}`,
			wantSub: "You are ZCode",
		},
		{
			name:    "anthropic system blocks",
			body:    `{"system":[{"type":"text","text":"You are ZCode, an interactive coding agent"},{"type":"text","text":"more instructions"}],"messages":[]}`,
			wantSub: "You are ZCode",
		},
		{
			name:    "openai responses instructions",
			body:    `{"instructions":"You are Codex, a coding agent","input":"hi"}`,
			wantSub: "You are Codex",
		},
		{
			name:    "no system prompt",
			body:    `{"messages":[{"role":"user","content":"hello"}]}`,
			wantSub: "",
		},
		{
			name:    "invalid json",
			body:    `{"messages":[broken`,
			wantSub: "",
		},
		{
			name:    "empty body",
			body:    ``,
			wantSub: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := systemPromptFromRequestBody([]byte(tc.body))
			if tc.wantSub == "" {
				if got != "" {
					t.Fatalf("expected empty, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSub) {
				t.Fatalf("expected %q to contain %q", got, tc.wantSub)
			}
		})
	}
}

// TestSystemPromptPrefixTruncation 超长 system prompt 必须被截断到上限之内，
// 且不在多字节 rune 中间断开（UTF-8 安全）。
func TestSystemPromptPrefixTruncation(t *testing.T) {
	// 构造一个含中文的超大 system prompt（超 4096 字节）
	long := strings.Repeat("中文系统提示词。", 400) // ~ 400 * 21 bytes ≈ 8400B
	body := `{"system":` + quoteJSON(long) + `,"messages":[]}`
	got := systemPromptFromRequestBody([]byte(body))
	if got == "" {
		t.Fatal("expected non-empty prefix")
	}
	if len(got) > SystemPromptPrefixBytes {
		t.Fatalf("prefix exceeds cap: got %d bytes > %d", len(got), SystemPromptPrefixBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("prefix contains invalid UTF-8 (truncation broke a rune)")
	}
}

// TestSystemPromptPrefixMasksSecrets system prompt 里粘的 API key 必须被脱敏
// （这是喂给下游总结 LLM 的内容，同款要求见 summarizer 消息脱敏）。
func TestSystemPromptPrefixMasksSecrets(t *testing.T) {
	body := `{"messages":[{"role":"system","content":"You are ZCode. key: sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUV0123456789abcdefghijklmnopqrstuvwx"}]}`
	got := systemPromptFromRequestBody([]byte(body))
	if strings.Contains(got, "sk-ant-api03-ABCDEFGHIJ") {
		t.Fatalf("secret not masked in prefix: %q", got)
	}
	if !strings.Contains(got, "ZCode") {
		t.Fatalf("identity should survive masking: %q", got)
	}
}

// TestParseSummaryResponseAgentExpertTags 验证 LLM 响应里的新字段被解析、
// unknown 归并为空、tags 规整。
func TestParseSummaryResponseAgentExpertTags(t *testing.T) {
	s := &Summarizer{}
	resp := "```json\n" + `{
		"title": "调试登录问题",
		"summary": "用户排查登录失败",
		"key_topics": ["登录","401"],
		"user_intent": "code",
		"agent_type": "Cursor",
		"expert_type": "unknown",
		"tags": [" Debugging ", "GO", "go", "SQL 迁移 "]
	}` + "\n```"
	sum, err := s.parseSummaryResponse(resp, "sess-1")
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if sum.AgentType != "cursor" {
		t.Fatalf("agent_type: want cursor, got %q", sum.AgentType)
	}
	if sum.ExpertType != "" {
		t.Fatalf("expert_type should collapse unknown to empty, got %q", sum.ExpertType)
	}
	if len(sum.Tags) != 3 || sum.Tags[0] != "debugging" || sum.Tags[1] != "go" || sum.Tags[2] != "sql 迁移" {
		t.Fatalf("tags normalized wrong: %#v", sum.Tags)
	}
}

// TestParseSummaryResponseLegacyFields LLM 不回新字段时必须向后兼容。
func TestParseSummaryResponseLegacyFields(t *testing.T) {
	s := &Summarizer{}
	resp := `{"title":"测试","summary":"摘要","key_topics":["a"],"user_intent":"chat"}`
	sum, err := s.parseSummaryResponse(resp, "sess-2")
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if sum.AgentType != "" || sum.ExpertType != "" || len(sum.Tags) != 0 {
		t.Fatalf("legacy parse should leave new fields empty, got %#v/%#v/%#v",
			sum.AgentType, sum.ExpertType, sum.Tags)
	}
}

// TestEnrichSessionIdentityRuleFallback LLM 未识别时，规则兜底必须命中；
// LLM 已有识别时规则不得覆盖。
func TestEnrichSessionIdentityRuleFallback(t *testing.T) {
	const zcodePrompt = "You are ZCode, an interactive coding agent\nYou are an agent for ZCode CLI."

	// 1. LLM 空 → 规则兜底
	sum := &SessionSummary{}
	enrichSessionIdentity(sum, zcodePrompt)
	if sum.AgentType != "zcode" {
		t.Fatalf("expected rule fallback zcode, got %q", sum.AgentType)
	}
	if sum.ExpertType != "software_engineering" {
		t.Fatalf("expected expert software_engineering, got %q", sum.ExpertType)
	}

	// 2. LLM 已识别 → 不覆盖
	sum2 := &SessionSummary{AgentType: "cursor", ExpertType: "security"}
	enrichSessionIdentity(sum2, zcodePrompt)
	if sum2.AgentType != "cursor" || sum2.ExpertType != "security" {
		t.Fatalf("rule fallback must not override LLM values, got %#v", sum2)
	}

	// 3. 空前缀 → 不产生任何值
	sum3 := &SessionSummary{}
	enrichSessionIdentity(sum3, "")
	if sum3.AgentType != "" || sum3.ExpertType != "" {
		t.Fatal("empty prefix must not produce identity")
	}
}

func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteString("\"")
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteString(string(r))
		}
	}
	b.WriteString("\"")
	return b.String()
}
