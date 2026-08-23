package sessionmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseMessagesSupportsBlocksAndCaps(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"system","content":"You are ZCode"},{"role":"tool","content":"ignore this tool output"},{"role":"user","content":[{"type":"text","text":"修复登录失败"},{"type":"image_url","image_url":{"url":"secret"}}]}]}`)
	messages := ParseMessages(raw)
	if len(messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(messages))
	}
	if messages[2].Content != "修复登录失败" {
		t.Fatalf("content = %q", messages[2].Content)
	}
}

func TestExtractPrioritizesExplicitFacts(t *testing.T) {
	got := Extract(Input{
		Messages:  []Message{{Role: "system", Content: "You are OpenCode"}, {Role: "user", Content: "deploy the app"}},
		AgentName: "cursor", ClientType: "cursor", WorkType: "release_work", ProjectRef: "acc-42", ProjectLabel: "Gateway",
	})
	if got.Agent.Name != "cursor" || got.Agent.Source != "header" {
		t.Fatalf("agent = %+v", got.Agent)
	}
	if got.Client.Type != "cursor" || got.Client.Source != "header" {
		t.Fatalf("client = %+v", got.Client)
	}
	if len(got.WorkTypes) != 1 || got.WorkTypes[0].Value != "release_work" {
		t.Fatalf("work types = %+v", got.WorkTypes)
	}
	if got.Project.Ref != "acc-42" || got.Project.Source != "authoritative" || got.Project.Status != "confirmed" {
		t.Fatalf("project = %+v", got.Project)
	}
}

func TestExtractDetectsSystemAgentAndProjectHints(t *testing.T) {
	got := Extract(Input{Messages: []Message{
		{Role: "system", Content: "You are ZCode. Work in /Users/me/llm-gateway-go."},
		{Role: "user", Content: "请修复 session summary 的 bug"},
		{Role: "tool", Content: "rm -rf /do-not-use"},
	}})
	if got.Agent.Name != "zcode" || got.Agent.Role != "coding_agent" || got.Agent.Source != "system_prompt" {
		t.Fatalf("agent = %+v", got.Agent)
	}
	if got.Client.Type != "zcode" || got.Client.Source != "agent" {
		t.Fatalf("client = %+v", got.Client)
	}
	if got.Title != "[zcode] 请修复 session summary 的 bug" {
		t.Fatalf("title = %q", got.Title)
	}
	if got.Project.Label != "llm-gateway-go" || got.Project.Status != "pending" {
		t.Fatalf("project = %+v", got.Project)
	}
	if len(got.Evidence) != 1 || strings.Contains(got.Evidence[0].Value, "do-not-use") {
		t.Fatalf("evidence = %+v", got.Evidence)
	}
	if got.WorkTypes[0].Value != "debugging" {
		t.Fatalf("work types = %+v", got.WorkTypes)
	}
}

func TestExtractDetectsRemoteAndUTF8TitleBound(t *testing.T) {
	long := strings.Repeat("修复", 80)
	got := Extract(Input{Messages: []Message{
		{Role: "system", Content: "git remote https://github.com/acme/llm-gateway-go.git"},
		{Role: "user", Content: long},
	}})
	if got.Project.Label != "llm-gateway-go" {
		t.Fatalf("project = %+v", got.Project)
	}
	if n := len([]rune(got.Title)); n != MaxTitleRunes {
		t.Fatalf("title rune length = %d, want %d", n, MaxTitleRunes)
	}
	if !strings.HasSuffix(got.Title, "…") {
		t.Fatalf("title should end with ellipsis: %q", got.Title)
	}
}

func TestExtractUnknownDoesNotInvent(t *testing.T) {
	got := Extract(Input{Messages: []Message{{Role: "user", Content: "hello"}}})
	if got.Agent.Name != Unknown || got.Client.Type != Unknown || got.Project.Label != "" || got.Project.Ref != "" {
		t.Fatalf("got invented metadata: %+v", got)
	}
	if got.WorkTypes[0].Value != "unknown" {
		t.Fatalf("work types = %+v", got.WorkTypes)
	}
}

func TestExtractRequestBodyAndJSONContract(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "You are OpenCode"},
		map[string]any{"role": "user", "content": "refactor API"},
	}})
	got := Extract(Input{RequestBody: raw})
	if got.AnalysisKind != AnalysisKind || got.SchemaVersion != SchemaVersion || got.Status != StatusProvisional {
		t.Fatalf("header = %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || !strings.Contains(string(encoded), `"input_hash"`) {
		t.Fatalf("encoded result = %s, err=%v", encoded, err)
	}
}
