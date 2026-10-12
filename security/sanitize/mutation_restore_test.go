package sanitize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	sseparser "github.com/kaixuan/llm-gateway-go/internal/sse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mutatedMarkerRestorer builds a restore interceptor whose session map holds
// the tokens the end-to-end tests below refer to.
func mutatedMarkerRestorer(t *testing.T, tenant, session string, entries map[string]string) *SanitizeRestoreInterceptor {
	t.Helper()
	rdb := setupSaniGuardRedis(t)
	for token, value := range entries {
		require.NoError(t, rdb.HSet(context.Background(), sanitizeMapKey(tenant, session), token, value).Err())
	}
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	return it
}

// TestNonStreamRestoresRespacedMarker is the end-to-end non-stream regression:
// a model that re-spaces the marker still gets its value back, on every
// visible lane, instead of losing it to [REDACTED].
func TestNonStreamRestoresRespacedMarker(t *testing.T) {
	const tenant, session = "tenant-mutant", "session-mutant"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		"{SENSITIVE:server_ip:1}": "10.0.7.21",
		"{SENSITIVE:secret:1}":    "p@ss w0rd",
	})
	body := []byte(`{"choices":[{"message":{"role":"assistant",` +
		`"content":"connect to {SENSITIVE: server_ip : 1} with {SENSITIVE:secret : 1}",` +
		`"refusal":"refused {sensitive:server_ip:1}",` +
		`"reasoning_content":"think {ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１}",` +
		`"function_call":{"arguments":"{\"host\":\"{SENSITIVE: server_ip : 1}\"}"},` +
		`"tool_calls":[{"function":{"arguments":"{\"pwd\":\"{SENSITIVE:secret : 1}\"}"}}]}},` +
		`{"text":"completion {SENSITIVE: server_ip : 1}"}]}`)

	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ClientProtocol: "openai-chat", ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock, "a re-spelled marker must not fail the response closed")

	var got struct {
		Choices []struct {
			Text    string `json:"text"`
			Message struct {
				Content          string `json:"content"`
				Refusal          string `json:"refusal"`
				ReasoningContent string `json:"reasoning_content"`
				FunctionCall     struct {
					Arguments string `json:"arguments"`
				} `json:"function_call"`
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))

	require.Equal(t, "connect to 10.0.7.21 with p@ss w0rd", got.Choices[0].Message.Content)
	require.Equal(t, "refused 10.0.7.21", got.Choices[0].Message.Refusal)
	require.Equal(t, "think p@ss w0rd", got.Choices[0].Message.ReasoningContent)
	require.Equal(t, "completion 10.0.7.21", got.Choices[1].Text)

	var legacyArgs map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.Choices[0].Message.FunctionCall.Arguments), &legacyArgs))
	require.Equal(t, "10.0.7.21", legacyArgs["host"])

	var toolArgs map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.Choices[0].Message.ToolCalls[0].Function.Arguments), &toolArgs))
	require.Equal(t, "p@ss w0rd", toolArgs["pwd"])
	assert.NotContains(t, string(result.ModifiedBody), "SENSITIVE")
}

// TestNonStreamNativeResponsesRestoresRespacedMarker walks the native
// Responses envelope, including a tool-call argument lane.
func TestNonStreamNativeResponsesRestoresRespacedMarker(t *testing.T) {
	const tenant, session = "tenant-mutant-native", "session-mutant-native"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		"{SENSITIVE:server_ip:1}": "10.0.7.21",
		"{SENSITIVE:secret:1}":    "p@ss w0rd",
	})
	body := []byte(`{"object":"response","output":[` +
		`{"type":"message","content":[{"type":"output_text","text":"host {SENSITIVE: server_ip : 1}"}]},` +
		`{"type":"function_call","arguments":"{\"host\":\"{SENSITIVE: server_ip : 1}\",\"pwd\":\"{sensitive:secret:01}\"}"}]}`)

	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)

	var got struct {
		Output []struct {
			Type      string `json:"type"`
			Content   []any  `json:"content"`
			Arguments string `json:"arguments"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	require.Len(t, got.Output, 2)

	blocks, ok := got.Output[0].Content[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "host 10.0.7.21", blocks["text"])

	var arg map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.Output[1].Arguments), &arg))
	require.Equal(t, "10.0.7.21", arg["host"])
	require.Equal(t, "p@ss w0rd", arg["pwd"])
	assert.NotContains(t, string(result.ModifiedBody), "SENSITIVE")
}

// TestStreamRestoresMarkerSplitAcrossChunks is the streaming regression. A
// marker cut between deltas — and re-spelled while being cut — must still be
// restored, and no part of it may reach the client.
func TestStreamRestoresMarkerSplitAcrossChunks(t *testing.T) {
	const tenant, session = "tenant-mutant-stream", "session-mutant-stream"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		"{SENSITIVE:server_ip:1}": "10.0.7.21",
		"{SENSITIVE:secret:1}":    "p@ss w0rd",
	})
	meta := &response.StreamMeta{SessionID: session, TenantID: tenant, State: response.NewStreamState()}

	deltas := []string{
		`{"choices":[{"delta":{"content":"connect to "}}]}`,
		`{"choices":[{"delta":{"content":"{"}}]}`,
		`{"choices":[{"delta":{"content":"SENSITIVE: server_"}}]}`,
		`{"choices":[{"delta":{"content":"ip : 1"}}]}`,
		`{"choices":[{"delta":{"content":"}"}}]}`,
		`{"choices":[{"delta":{"content":" with {SENSITIVE:"}}]}`,
		`{"choices":[{"delta":{"content":"secret : 1}"}}]}`,
		`{"choices":[{"delta":{"content":", then stop"}}]}`,
		`{"choices":[{"delta":{"content":""}}]}`,
	}
	var wire strings.Builder
	for _, delta := range deltas {
		chunk := []byte("data: " + delta + "\n\n")
		result, err := it.InterceptStreamChunk(context.Background(), chunk, meta)
		require.NoError(t, err)
		if result != nil {
			require.False(t, result.ShouldBlock, delta)
		}
		if result != nil && result.ModifiedChunk != nil {
			wire.Write(result.ModifiedChunk)
		} else {
			wire.Write(chunk)
		}
	}

	out := wire.String()
	require.NotContains(t, out, "SENSITIVE", "no part of a marker may reach the client")
	require.NotContains(t, out, "connect to {")
	var content strings.Builder
	for _, event := range splitSSEEvents([]byte(out)) {
		payload, ok, _ := sseparser.ParseDataFrame(event)
		if !ok {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(payload, &raw); err != nil {
			continue
		}
		choices, ok := raw["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if text, ok := delta["content"].(string); ok {
			content.WriteString(text)
		}
	}
	require.Equal(t, "connect to 10.0.7.21 with p@ss w0rd, then stop", content.String())
}

// TestStreamToolArgumentsRestoreRespacedMarker covers the JSON-escaping lane:
// a restored value has to be spliced back as a JSON string literal, and an
// exact-token index cannot see a marker the model re-spelled.
func TestStreamToolArgumentsRestoreRespacedMarker(t *testing.T) {
	const tenant, session = "tenant-mutant-args", "session-mutant-args"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		"{SENSITIVE:secret:1}": "he said \"hi\"\n",
	})
	meta := &response.StreamMeta{SessionID: session, TenantID: tenant, State: response.NewStreamState()}
	chunk := []byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,` +
		`"function":{"arguments":"{\"pwd\":\"{SENSITIVE: secret : 1}\"}"}}]}}]}` + "\n\n")

	result, err := it.InterceptStreamChunk(context.Background(), chunk, meta)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	require.NotContains(t, string(result.ModifiedChunk), "SENSITIVE")

	var raw map[string]any
	require.NoError(t, json.Unmarshal(extractDataPayload(t, result.ModifiedChunk), &raw))
	choices := raw["choices"].([]any)
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	toolCalls := delta["tool_calls"].([]any)
	fn := toolCalls[0].(map[string]any)["function"].(map[string]any)

	var arg map[string]string
	require.NoError(t, json.Unmarshal([]byte(fn["arguments"].(string)), &arg))
	require.Equal(t, "he said \"hi\"\n", arg["pwd"])
}

// TestNonStreamReSpelledForgedMarkerStillMasks keeps the tolerant stage from
// becoming an oracle: a re-spelled marker whose normalized form names nothing
// this session issued is still masked.
func TestNonStreamReSpelledForgedMarkerStillMasks(t *testing.T) {
	const tenant, session = "tenant-mutant-forged", "session-mutant-forged"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		"{SENSITIVE:secret:1}": "p@ss w0rd",
	})
	body := []byte(`{"choices":[{"message":{"role":"assistant",` +
		`"content":"a {SENSITIVE: secret : 1} b {SENSITIVE: secret : 2}"}}]}`)

	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	require.Equal(t, "a p@ss w0rd b [REDACTED]", got.Choices[0].Message.Content)
	assert.NotContains(t, string(result.ModifiedBody), "SENSITIVE")
}

// TestValidatePlaceholdersCountsReSpelledForgery keeps the tamper metric honest
// for the new spelling: a re-spelled marker that resolves is not tampering, and
// one that does not resolve is.
func TestValidatePlaceholdersCountsReSpelledForgery(t *testing.T) {
	const tenant, session = "tenant-mutant-observe", "session-mutant-observe"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		"{SENSITIVE:secret:1}": "p@ss w0rd",
	})

	clean := []byte(`{"choices":[{"message":{"content":"{SENSITIVE: secret : 1} ok"}}]}`)
	assert.Empty(t, it.validatePlaceholders(context.Background(), clean, SanitizeMap{
		"{SENSITIVE:secret:1}": "p@ss w0rd",
	}), "a re-spelled marker that resolves is normal operation")

	forged := []byte(`{"choices":[{"message":{"content":"{SENSITIVE: secret : 9}"}}]}`)
	assert.Equal(t, []string{"{SENSITIVE: secret : 9}"},
		it.validatePlaceholders(context.Background(), forged, SanitizeMap{
			"{SENSITIVE:secret:1}": "p@ss w0rd",
		}))
}

func extractDataPayload(t *testing.T, chunk []byte) []byte {
	t.Helper()
	for _, event := range splitSSEEvents(chunk) {
		if payload, ok, _ := sseparser.ParseDataFrame(event); ok {
			return payload
		}
	}
	t.Fatalf("no data frame in %q", chunk)
	return nil
}
