package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseAnthropic_MCPServers verifies Claude 4.5+ MCP server parsing.
func TestParseAnthropic_MCPServers(t *testing.T) {
	body := []byte(`{
		"model": "claude-4-5-sonnet",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "Hi"}],
		"mcp_servers": [
			{
				"type": "url",
				"url": "https://mcp.example.com/sse",
				"name": "weather-server",
				"authorization_token": "tok_123"
			}
		]
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(ir.MCPServers) != 1 {
		t.Fatalf("MCPServers = %d, want 1", len(ir.MCPServers))
	}
	mcp := ir.MCPServers[0]
	if mcp.URL != "https://mcp.example.com/sse" {
		t.Errorf("URL = %q", mcp.URL)
	}
	if mcp.Name != "weather-server" {
		t.Errorf("Name = %q", mcp.Name)
	}
	if mcp.AuthorizationToken != "tok_123" {
		t.Errorf("Auth token = %q", mcp.AuthorizationToken)
	}
}

// TestSerializeAnthropic_MCPServers verifies Claude 4.5+ MCP server serialization.
func TestSerializeAnthropic_MCPServers(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-4-5-sonnet",
		MaxTokens: 1024,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}}},
		MCPServers: []MCPServer{
			{
				Type: "url",
				URL:  "https://mcp.example.com/sse",
				Name: "weather-server",
			},
		},
		SourceProtocol: ProtocolAnthropicMessages,
	}

	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	servers, ok := out["mcp_servers"].([]any)
	if !ok {
		t.Fatal("mcp_servers not in output")
	}
	if len(servers) != 1 {
		t.Fatalf("mcp_servers len = %d, want 1", len(servers))
	}
	server := servers[0].(map[string]any)
	if server["url"] != "https://mcp.example.com/sse" {
		t.Errorf("url = %v", server["url"])
	}
	if server["name"] != "weather-server" {
		t.Errorf("name = %v", server["name"])
	}
}

// TestParseAnthropic_ContextManagement verifies Claude 4.5+ context_management parsing.
func TestParseAnthropic_ContextManagement(t *testing.T) {
	body := []byte(`{
		"model": "claude-4-5-sonnet",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "Hi"}],
		"context_management": {
			"edits": [
				{
					"type": "clear_tool_uses_20250919",
					"threshold": 80,
					"keep": 3,
					"clear_tool_inputs": true
				},
				{
					"type": "clear_thinking_20251015"
				}
			]
		}
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if ir.ContextManagement == nil {
		t.Fatal("ContextManagement is nil")
	}
	if len(ir.ContextManagement.Edits) != 2 {
		t.Fatalf("Edits = %d, want 2", len(ir.ContextManagement.Edits))
	}

	edit1 := ir.ContextManagement.Edits[0]
	if edit1.Type != "clear_tool_uses_20250919" {
		t.Errorf("Edit[0].Type = %q", edit1.Type)
	}
	if edit1.Threshold == nil || *edit1.Threshold != 80 {
		t.Errorf("Threshold = %v, want 80", edit1.Threshold)
	}
	if edit1.Keep == nil || *edit1.Keep != 3 {
		t.Errorf("Keep = %v, want 3", edit1.Keep)
	}
	if edit1.ClearToolInputs == nil || !*edit1.ClearToolInputs {
		t.Errorf("ClearToolInputs = %v, want true", edit1.ClearToolInputs)
	}

	edit2 := ir.ContextManagement.Edits[1]
	if edit2.Type != "clear_thinking_20251015" {
		t.Errorf("Edit[1].Type = %q", edit2.Type)
	}
}

// TestSerializeAnthropic_ContextManagement verifies Claude 4.5+ context_management serialization.
func TestSerializeAnthropic_ContextManagement(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-4-5-sonnet",
		MaxTokens: 1024,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}}},
		ContextManagement: &ContextManagement{
			Edits: []ContextEdit{
				{Type: "clear_tool_uses_20250919", Threshold: ptrInt(80), Keep: ptrInt(3)},
			},
		},
		SourceProtocol: ProtocolAnthropicMessages,
	}

	body, _ := SerializeAnthropic(ir)
	var out map[string]any
	json.Unmarshal(body, &out)

	cm := out["context_management"].(map[string]any)
	edits := cm["edits"].([]any)
	if len(edits) != 1 {
		t.Fatalf("edits = %d, want 1", len(edits))
	}
	edit := edits[0].(map[string]any)
	if edit["type"] != "clear_tool_uses_20250919" {
		t.Errorf("type = %v", edit["type"])
	}
	if edit["threshold"].(float64) != 80 {
		t.Errorf("threshold = %v, want 80", edit["threshold"])
	}
	if edit["keep"].(float64) != 3 {
		t.Errorf("keep = %v, want 3", edit["keep"])
	}
}

// TestParseAnthropic_Container verifies Claude 4.5+ container parsing.
func TestParseAnthropic_Container(t *testing.T) {
	body := []byte(`{
		"model": "claude-4-5-sonnet",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "Hi"}],
		"container": {
			"id": "container_abc123",
			"skills": [
				{"name": "pdf", "type": "anthropic"},
				{"name": "code-interpreter", "type": "custom"}
			]
		}
	}`)

	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if ir.Container == nil {
		t.Fatal("Container is nil")
	}
	if ir.Container.ID != "container_abc123" {
		t.Errorf("ID = %q", ir.Container.ID)
	}
	if len(ir.Container.Skills) != 2 {
		t.Fatalf("Skills = %d, want 2", len(ir.Container.Skills))
	}
	if ir.Container.Skills[0].Name != "pdf" {
		t.Errorf("Skills[0].Name = %q", ir.Container.Skills[0].Name)
	}
	if ir.Container.Skills[0].Type != "anthropic" {
		t.Errorf("Skills[0].Type = %q", ir.Container.Skills[0].Type)
	}
}

// TestSerializeAnthropic_Container verifies Claude 4.5+ container serialization.
func TestSerializeAnthropic_Container(t *testing.T) {
	ir := &InternalRequest{
		Model:     "claude-4-5-sonnet",
		MaxTokens: 1024,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}}},
		Container: &Container{
			ID: "container_xyz",
			Skills: []ContainerSkill{
				{Name: "web-search", Type: "anthropic"},
			},
		},
		SourceProtocol: ProtocolAnthropicMessages,
	}

	body, _ := SerializeAnthropic(ir)
	var out map[string]any
	json.Unmarshal(body, &out)

	c := out["container"].(map[string]any)
	if c["id"] != "container_xyz" {
		t.Errorf("id = %v", c["id"])
	}
	skills := c["skills"].([]any)
	if len(skills) != 1 {
		t.Fatalf("skills = %d", len(skills))
	}
	skill := skills[0].(map[string]any)
	if skill["name"] != "web-search" {
		t.Errorf("skill name = %v", skill["name"])
	}
}

// TestRoundTripClaude45 verifies lossless round-trip of Claude 4.5+ fields.
func TestRoundTripClaude45(t *testing.T) {
	original := []byte(`{
		"model": "claude-4-5-sonnet",
		"max_tokens": 2048,
		"messages": [{"role": "user", "content": "Hi"}],
		"mcp_servers": [{"type": "url", "url": "https://mcp.test", "name": "test"}],
		"context_management": {"edits": [{"type": "clear_tool_uses_20250919", "threshold": 90}]},
		"container": {"id": "container_roundtrip"}
	}`)

	// Parse
	ir, err := ParseAnthropic(original)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Serialize
	body, err := SerializeAnthropic(ir)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	// Verify all fields present
	var out map[string]any
	json.Unmarshal(body, &out)

	if _, ok := out["mcp_servers"]; !ok {
		t.Error("mcp_servers lost")
	}
	if _, ok := out["context_management"]; !ok {
		t.Error("context_management lost")
	}
	if _, ok := out["container"]; !ok {
		t.Error("container lost")
	}
}

// Helper
func ptrInt(i int) *int { return &i }

// Sanity test: protocol detection still works
func TestClaude45NotInStandardFields(t *testing.T) {
	// Verify Claude 4.5+ fields are NOT preserved as Extensions (they should
	// be promoted to structured IR fields). This is the inverse of the
	// domains/transformation standardRequestFields check.
	original := []byte(`{
		"model": "claude-4-5-sonnet",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "Hi"}],
		"mcp_servers": [{"type": "url", "url": "https://mcp.test"}],
		"context_management": {"edits": []},
		"container": {"id": "abc"}
	}`)
	ir, err := ParseAnthropic(original)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := ir.Extensions["mcp_servers"]; ok {
		t.Error("mcp_servers should be in IR structured field, not Extensions")
	}
	if _, ok := ir.Extensions["context_management"]; ok {
		t.Error("context_management should be in IR structured field")
	}
	if _, ok := ir.Extensions["container"]; ok {
		t.Error("container should be in IR structured field")
	}
}

func TestClaude45NoLegacyRegression(t *testing.T) {
	// Make sure basic Anthropic requests still work
	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "Hi"}]
	}`)
	ir, err := ParseAnthropic(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ir.MCPServers != nil {
		t.Error("MCPServers should be nil for legacy requests")
	}
	if ir.ContextManagement != nil {
		t.Error("ContextManagement should be nil for legacy requests")
	}
	if ir.Container != nil {
		t.Error("Container should be nil for legacy requests")
	}
	if !strings.Contains(ir.Model, "claude") {
		t.Error("Model not parsed")
	}
}
