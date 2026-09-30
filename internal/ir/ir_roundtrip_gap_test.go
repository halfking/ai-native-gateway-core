package ir

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IR round-trip gaps named in the audit handoff: refs identity/occurrence,
// prompt-only Completions, and assistant/tool history.
//
// Each test here was written against a real observed behavior, not an assumed
// one. Scenario B (prompt-only) started as a live data-loss defect: the legacy
// Completions `prompt` string was dropped on the floor and the request went
// upstream with no content at all.

// TestIR_PromptOnlyCompletionsIsNotLost pins the legacy Completions form,
// where the input arrives in a top-level `prompt` string instead of
// `messages`.
//
// The defect this guards: `prompt` reached Extensions, where paramreg treats
// it as a DialectResponses-only key (the reusable prompt-TEMPLATE reference
// {id,version,variables}) and drops it for an openai-chat source. Same key,
// two unrelated meanings. The observable consequence was catastrophic —
// {"model":"gpt-4o","prompt":"once upon a time"} round-tripped to
// {"model":"gpt-4o"}: the user's entire input silently disappeared.
func TestIR_PromptOnlyCompletionsIsNotLost(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","prompt":"once upon a time"}`)

	req, err := ParseOpenAI(body)
	require.NoError(t, err)
	require.Len(t, req.Messages, 1, "prompt must be promoted into a user message")
	assert.Equal(t, "user", req.Messages[0].Role)
	require.Len(t, req.Messages[0].Content, 1)
	assert.Equal(t, "text", req.Messages[0].Content[0].Type)
	assert.Equal(t, "once upon a time", req.Messages[0].Content[0].Text)

	// It must also survive serialization — the loss was observable there.
	out, err := SerializeOpenAI(req)
	require.NoError(t, err)
	assert.Contains(t, string(out), "once upon a time",
		"the prompt text must reach the serialized wire body")

	var reparsed map[string]any
	require.NoError(t, json.Unmarshal(out, &reparsed))
	msgs, ok := reparsed["messages"].([]any)
	require.True(t, ok, "serialized body must carry messages, not an empty request")
	require.Len(t, msgs, 1)
}

// TestIR_LegacyPromptIsNotRoutedThroughExtensions pins the second, subtler
// half of the fix — a defect that the round-trip assertions above CANNOT see.
//
// The message promotion alone is not sufficient. `prompt` must also be listed
// in ParseOpenAI's knownFields set, otherwise it is *additionally* captured
// into req.Extensions. The serialized body is then still correct (paramreg
// classifies the key as DialectResponses-only and simply declines to re-add
// it), so every round-trip assertion stays green — but the anomaly reporter
// fires a bogus `ir_protocol_loss / dialect_scoped_field` entry for a field
// the gateway handled perfectly well.
//
// The consequence is audit integrity rather than data loss: a permanently
// recurring false "protocol loss" on every legacy Completions request trains
// operators to ignore the very signal D-item audits depend on. Asserting on
// Extensions is the only way to catch it.
func TestIR_LegacyPromptIsNotRoutedThroughExtensions(t *testing.T) {
	req, err := ParseOpenAI([]byte(`{"model":"gpt-4o","prompt":"once upon a time"}`))
	require.NoError(t, err)

	_, inExtensions := req.Extensions["prompt"]
	assert.False(t, inExtensions,
		"prompt is IR-handled, not an extension: leaving it in Extensions makes "+
			"paramreg report a bogus dialect_scoped_field protocol loss")
}

// TestIR_MessagesWinOverPrompt pins the precedence rule: when a body carries
// BOTH `messages` and `prompt`, the structured form wins and the redundant
// legacy echo does not add a second, phantom user turn. Without this, a client
// sending both would get duplicated input.
func TestIR_MessagesWinOverPrompt(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","prompt":"echoed legacy text",
		"messages":[{"role":"user","content":"structured text"}]}`)

	req, err := ParseOpenAI(body)
	require.NoError(t, err)
	require.Len(t, req.Messages, 1, "messages must win; prompt must not add a phantom turn")
	assert.Equal(t, "structured text", req.Messages[0].Content[0].Text)
}

// TestIR_EmptyPromptAddsNoMessage keeps the promotion from inventing content:
// an absent or empty `prompt` must not manufacture an empty user turn, which
// would change behavior for every ordinary chat request.
func TestIR_EmptyPromptAddsNoMessage(t *testing.T) {
	for name, body := range map[string]string{
		"no prompt key":    `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
		"empty prompt":     `{"model":"gpt-4o","prompt":"","messages":[{"role":"user","content":"hi"}]}`,
		"prompt only null": `{"model":"gpt-4o","prompt":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			req, err := ParseOpenAI([]byte(body))
			require.NoError(t, err)
			for _, m := range req.Messages {
				require.Len(t, m.Content, 1)
				assert.NotEmpty(t, m.Content[0].Text, "promotion must never create an empty text block")
			}
		})
	}
}

// TestIR_ThinkingSignatureRefIdentityAndOccurrence pins the "refs" half of the
// handoff item: a thinking block's signature is a reference the provider
// requires back verbatim on the next turn. The contract is identity (the exact
// string) AND occurrence (exactly one copy — a duplicated or dropped signature
// invalidates the reasoning chain upstream).
func TestIR_ThinkingSignatureRefIdentityAndOccurrence(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-8","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"reasoning","signature":"SIG-ABC"}]},
		{"role":"user","content":"more"}]}`)

	req, err := ParseAnthropic(body)
	require.NoError(t, err)
	require.Len(t, req.Messages, 2+1)

	var signatures []string
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Type == "thinking" {
				require.NotNil(t, b.Thinking, "thinking block must carry its payload")
				signatures = append(signatures, b.Thinking.Signature)
			}
		}
	}
	require.Len(t, signatures, 1, "exactly one thinking block must survive")
	assert.Equal(t, "SIG-ABC", signatures[0], "signature identity must be preserved verbatim")

	out, err := SerializeAnthropic(req)
	require.NoError(t, err)
	assert.Equal(t, 1, countOccurrences(string(out), "SIG-ABC"),
		"the signature must appear exactly once on the wire")
}

func countOccurrences(haystack, needle string) int {
	n, i := 0, 0
	for {
		j := indexFrom(haystack, needle, i)
		if j < 0 {
			return n
		}
		n++
		i = j + len(needle)
	}
}

func indexFrom(s, sub string, from int) int {
	if from >= len(s) {
		return -1
	}
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestIR_AssistantToolHistoryRoundTrip pins the third handoff item: a
// multi-turn history containing an assistant tool_call turn, the matching tool
// result, and a following assistant turn must survive the round-trip with the
// tool_call_id linkage intact. A broken linkage here silently invalidates the
// provider's tool-result matching.
func TestIR_AssistantToolHistoryRoundTrip(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","name":"get_weather","content":"sunny"},
		{"role":"assistant","content":"it is sunny in SF"}]}`)

	req, err := ParseOpenAI(body)
	require.NoError(t, err)
	require.Len(t, req.Messages, 4)
	assert.Equal(t, "assistant", req.Messages[1].Role)
	require.Len(t, req.Messages[1].ToolCalls, 1)
	assert.Equal(t, "call_1", req.Messages[1].ToolCalls[0].ID)
	assert.Equal(t, "tool", req.Messages[2].Role)
	assert.Equal(t, "call_1", req.Messages[2].ToolCallID, "tool result must stay linked to its call")

	out, err := SerializeOpenAI(req)
	require.NoError(t, err)
	assert.Contains(t, string(out), "call_1", "tool_call_id linkage must reach the wire")

	// Re-parsing the serialized body must reproduce the same linkage.
	round, err := ParseOpenAI(out)
	require.NoError(t, err)
	require.Len(t, round.Messages, 4)
	assert.Equal(t, "call_1", round.Messages[1].ToolCalls[0].ID)
	assert.Equal(t, "call_1", round.Messages[2].ToolCallID)
}
