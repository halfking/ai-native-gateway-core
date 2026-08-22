package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseSerialize_ProviderSpecificTools guards the 2026-07-27 fix (F-1):
// provider-specific tool types (Anthropic computer_use/bash/text_editor,
// OpenAI web_search/code_interpreter/file_search) must round-trip on their
// native protocol instead of being silently dropped.
//
// Before the fix, ToolDefinition had only Name/Description/Parameters, so a
// tool like Anthropic's computer_use (which has type "computer_20250124" and
// no input_schema) was parsed into a ToolDefinition with empty Name and no
// Parameters, then serialized as {"name":""} — broken and useless. OpenAI's
// web_search_preview (type "web_search_preview", no function object) was
// dropped entirely by the legacy converter.
func TestParseSerialize_ProviderSpecificTools(t *testing.T) {
	t.Run("anthropic computer_use round-trips", func(t *testing.T) {
		// Anthropic built-in tool: identified by type, carries name but no
		// input_schema (the SDK owns the schema).
		input := `{
			"model": "claude-sonnet-4-20250514",
			"max_tokens": 1024,
			"tools": [
				{"type": "computer_20250124", "name": "computer", "display_width_px": 1024},
				{"name": "get_weather", "description": "weather", "input_schema": {"type":"object","properties":{"q":{"type":"string"}}}}
			],
			"messages": [{"role":"user","content":"use the computer"}]
		}`

		ir, err := ParseAnthropic([]byte(input))
		require.NoError(t, err)
		require.Len(t, ir.Tools, 2, "both tools must be parsed (builtin + custom)")

		// Built-in captured as provider-specific (Type set, Raw populated).
		builtin := ir.Tools[0]
		assert.False(t, builtin.IsFunction(), "computer_use is not a function tool")
		assert.Equal(t, "computer_20250124", builtin.Type)
		require.NotEmpty(t, builtin.Raw, "Raw bytes must be captured for passthrough")

		// Custom tool still parsed normally.
		custom := ir.Tools[1]
		assert.True(t, custom.IsFunction())
		assert.Equal(t, "get_weather", custom.Name)

		// Serialize back to Anthropic — the built-in must re-emerge verbatim.
		body, err := SerializeAnthropic(ir)
		require.NoError(t, err)

		var out map[string]any
		require.NoError(t, json.Unmarshal(body, &out))
		tools := out["tools"].([]any)
		require.Len(t, tools, 2, "both tools must be serialized")

		gotBuiltin := tools[0].(map[string]any)
		assert.Equal(t, "computer_20250124", gotBuiltin["type"], "computer_use type must survive round-trip")
		assert.Equal(t, "computer", gotBuiltin["name"], "computer_use name must survive round-trip")
		assert.EqualValues(t, 1024, gotBuiltin["display_width_px"], "extra fields must survive verbatim")
	})

	t.Run("anthropic bash tool round-trips", func(t *testing.T) {
		// bash has type but NO name field at all.
		input := `{
			"model": "claude-sonnet-4-20250514",
			"max_tokens": 1024,
			"tools": [{"type": "bash_20250124"}],
			"messages": [{"role":"user","content":"run ls"}]
		}`
		ir, err := ParseAnthropic([]byte(input))
		require.NoError(t, err)
		require.Len(t, ir.Tools, 1)
		assert.Equal(t, "bash_20250124", ir.Tools[0].Type)
		assert.False(t, ir.Tools[0].IsFunction())

		body, err := SerializeAnthropic(ir)
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(body, &out))
		tools := out["tools"].([]any)
		require.Len(t, tools, 1)
		assert.Equal(t, "bash_20250124", tools[0].(map[string]any)["type"])
	})

	t.Run("openai web_search_preview round-trips", func(t *testing.T) {
		// OpenAI built-in: type != "function", no function sub-object.
		input := `{
			"model": "gpt-4o",
			"tools": [
				{"type": "web_search_preview"},
				{"type": "function", "function": {"name": "calc", "parameters": {"type":"object"}}}
			],
			"messages": [{"role":"user","content":"search the web"}]
		}`
		ir, err := ParseOpenAI([]byte(input))
		require.NoError(t, err)
		require.Len(t, ir.Tools, 2, "web_search + function tool")

		assert.Equal(t, "web_search_preview", ir.Tools[0].Type)
		assert.False(t, ir.Tools[0].IsFunction())
		require.NotEmpty(t, ir.Tools[0].Raw)

		assert.True(t, ir.Tools[1].IsFunction())
		assert.Equal(t, "calc", ir.Tools[1].Name)

		body, err := SerializeOpenAI(ir)
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(body, &out))
		tools := out["tools"].([]any)
		require.Len(t, tools, 2)
		assert.Equal(t, "web_search_preview", tools[0].(map[string]any)["type"])
		// Function tool still emits the standard shape.
		fnTool := tools[1].(map[string]any)
		assert.Equal(t, "function", fnTool["type"])
	})

	t.Run("openai code_interpreter round-trips with config", func(t *testing.T) {
		input := `{
			"model": "gpt-4o",
			"tools": [{"type": "code_interpreter", "container": {"type": "auto"}}],
			"messages": [{"role":"user","content":"run code"}]
		}`
		ir, err := ParseOpenAI([]byte(input))
		require.NoError(t, err)
		require.Len(t, ir.Tools, 1)
		assert.Equal(t, "code_interpreter", ir.Tools[0].Type)

		body, err := SerializeOpenAI(ir)
		require.NoError(t, err)
		var out map[string]any
		require.NoError(t, json.Unmarshal(body, &out))
		tool := out["tools"].([]any)[0].(map[string]any)
		assert.Equal(t, "code_interpreter", tool["type"])
		require.NotNil(t, tool["container"], "extra config fields must survive verbatim")
	})
}

// TestToolDefinition_IsFunction verifies the helper used by the serializers.
func TestToolDefinition_IsFunction(t *testing.T) {
	cases := []struct {
		tool ToolDefinition
		want bool
	}{
		{ToolDefinition{Name: "x"}, true},                   // empty type
		{ToolDefinition{Name: "x", Type: "function"}, true}, // explicit function
		{ToolDefinition{Type: "computer_20250124"}, false},
		{ToolDefinition{Type: "web_search_preview"}, false},
	}
	for i, c := range cases {
		if got := c.tool.IsFunction(); got != c.want {
			t.Errorf("case %d: IsFunction() = %v, want %v (type=%q)", i, got, c.want, c.tool.Type)
		}
	}
}
