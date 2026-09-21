package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestToolCallValidator_AnthropicStreamComplete tests a complete Anthropic
// stream with tool_use + tool_result pairs.
func TestToolCallValidator_AnthropicStreamComplete(t *testing.T) {
	body := buildAnthropicStreamWithToolCall(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp, "claude-sonnet-4", "claude-sonnet-4", "test-req", capture, nil)

	// Should complete successfully
	assert.False(t, outcome.Interrupted, "stream should not be interrupted")
	assert.Equal(t, "", outcome.Reason)
}

// TestToolCallValidator_AnthropicStreamIncomplete tests a stream where
// tool_use is sent but tool_result is missing when some tool_results were
// provided (indicating incomplete execution, not just a request for execution).
func TestToolCallValidator_AnthropicStreamIncomplete(t *testing.T) {
	// Build a stream with 2 tool_uses, but only 1 tool_result
	body := buildAnthropicStreamWithMultipleTools(2, false)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp, "claude-sonnet-4", "claude-sonnet-4", "test-req", capture, nil)

	// Should be interrupted with incomplete_tool_call
	assert.True(t, outcome.Interrupted, "stream should be interrupted")
	assert.Contains(t, outcome.Reason, "incomplete_tool_call")
	assert.Equal(t, errorsx.KindUpstreamDown, outcome.Kind)
	assert.True(t, outcome.Resumable, "should be resumable for retry")
}

// TestToolCallValidator_AnthropicStreamToolUseOnly tests a stream with only
// tool_use blocks (no tool_result). This is valid - the assistant is requesting
// tool execution, and the client will provide results in the next turn.
func TestToolCallValidator_AnthropicStreamToolUseOnly(t *testing.T) {
	body := buildAnthropicStreamWithToolCallRequestOnly()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp, "claude-sonnet-4", "claude-sonnet-4", "test-req", capture, nil)

	// Should complete successfully - tool_use without tool_result is valid
	assert.False(t, outcome.Interrupted, "stream should not be interrupted")
	assert.Equal(t, "", outcome.Reason)
}

// TestToolCallValidator_AnthropicEOFDuringToolExecution tests a stream that
// gets EOF while a tool_use is pending (upstream crashed mid-execution).
func TestToolCallValidator_AnthropicEOFDuringToolExecution(t *testing.T) {
	// Build stream that starts tool_use but gets cut off (no message_stop)
	body := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_123\",\"name\":\"get_weather\",\"input\":{}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"location\\\":\\\"\"}}\n\n"
	// EOF here - no content_block_stop, no message_stop

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp, "claude-sonnet-4", "claude-sonnet-4", "test-req", capture, nil)

	// Should be interrupted with incomplete_tool_call_interrupted
	assert.True(t, outcome.Interrupted, "stream should be interrupted")
	assert.Equal(t, "incomplete_tool_call_interrupted", outcome.Reason)
	assert.True(t, outcome.Resumable, "should be resumable")
}

// TestToolCallValidator_MultipleToolCalls tests a stream with multiple
// tool_use blocks, all properly completed.
func TestToolCallValidator_MultipleToolCalls(t *testing.T) {
	body := buildAnthropicStreamWithMultipleTools(3, true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp, "claude-sonnet-4", "claude-sonnet-4", "test-req", capture, nil)

	// Should complete successfully
	assert.False(t, outcome.Interrupted)
}

// TestToolCallValidator_PartiallyIncompleteMultipleTools tests a stream
// with 3 tool_use blocks but only 2 tool_result blocks.
func TestToolCallValidator_PartiallyIncompleteMultipleTools(t *testing.T) {
	body := buildAnthropicStreamWithMultipleTools(3, false)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamAnthropicSSEToOpenAI(context.Background(), rec, resp, "claude-sonnet-4", "claude-sonnet-4", "test-req", capture, nil)

	// Should be interrupted
	assert.True(t, outcome.Interrupted)
	assert.Contains(t, outcome.Reason, "incomplete_tool_call")
	assert.True(t, outcome.Resumable)
}

// buildAnthropicStreamWithToolCall creates a synthetic Anthropic SSE stream
// with a single tool_use block. If complete=true, includes the matching
// tool_result; otherwise, ends after tool_use.
func buildAnthropicStreamWithToolCall(complete bool) string {
	var b strings.Builder
	
	// message_start
	b.WriteString("event: message_start\n")
	b.WriteString("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")

	// content_block_start (tool_use)
	b.WriteString("event: content_block_start\n")
	b.WriteString("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_123\",\"name\":\"get_weather\",\"input\":{}}}\n\n")

	// content_block_delta (streaming tool args)
	b.WriteString("event: content_block_delta\n")
	b.WriteString("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"location\\\":\\\"San Francisco\\\"}\"}}\n\n")

	// content_block_stop (tool_use)
	b.WriteString("event: content_block_stop\n")
	b.WriteString("data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")

	if complete {
		// content_block_start (tool_result)
		b.WriteString("event: content_block_start\n")
		b.WriteString("data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_result\",\"tool_use_id\":\"toolu_123\",\"content\":\"Temperature: 72F\"}}\n\n")

		// content_block_stop (tool_result)
		b.WriteString("event: content_block_stop\n")
		b.WriteString("data: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
	}

	// message_delta (usage)
	b.WriteString("event: message_delta\n")
	b.WriteString("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":50}}\n\n")

	// message_stop
	b.WriteString("event: message_stop\n")
	b.WriteString("data: {\"type\":\"message_stop\"}\n\n")

	return b.String()
}

// buildAnthropicStreamWithMultipleTools creates a stream with multiple tool
// calls. If complete=true, all tools get tool_result; otherwise, the last
// tool is missing its result.
func buildAnthropicStreamWithMultipleTools(count int, complete bool) string {
	var b strings.Builder

	b.WriteString("event: message_start\n")
	b.WriteString("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")

	for i := 0; i < count; i++ {
		toolID := "toolu_" + string('0'+rune(i+1))
		
		// tool_use
		b.WriteString("event: content_block_start\n")
		b.WriteString("data: {\"type\":\"content_block_start\",\"index\":")
		b.WriteString(string('0' + rune(i*2)))
		b.WriteString(",\"content_block\":{\"type\":\"tool_use\",\"id\":\"")
		b.WriteString(toolID)
		b.WriteString("\",\"name\":\"tool")
		b.WriteString(string('0' + rune(i)))
		b.WriteString("\",\"input\":{}}}\n\n")

		b.WriteString("event: content_block_stop\n")
		b.WriteString("data: {\"type\":\"content_block_stop\",\"index\":")
		b.WriteString(string('0' + rune(i*2)))
		b.WriteString("}\n\n")

		// tool_result (skip last one if incomplete)
		if complete || i < count-1 {
			b.WriteString("event: content_block_start\n")
			b.WriteString("data: {\"type\":\"content_block_start\",\"index\":")
			b.WriteString(string('0' + rune(i*2+1)))
			b.WriteString(",\"content_block\":{\"type\":\"tool_result\",\"tool_use_id\":\"")
			b.WriteString(toolID)
			b.WriteString("\",\"content\":\"result\"}}\n\n")

			b.WriteString("event: content_block_stop\n")
			b.WriteString("data: {\"type\":\"content_block_stop\",\"index\":")
			b.WriteString(string('0' + rune(i*2+1)))
			b.WriteString("}\n\n")
		}
	}

	b.WriteString("event: message_delta\n")
	b.WriteString("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":50}}\n\n")

	b.WriteString("event: message_stop\n")
	b.WriteString("data: {\"type\":\"message_stop\"}\n\n")

	return b.String()
}

// buildAnthropicStreamWithToolCallRequestOnly creates a stream with tool_use
// but no tool_result (assistant requesting tool execution).
func buildAnthropicStreamWithToolCallRequestOnly() string {
	var b strings.Builder
	
	// message_start
	b.WriteString("event: message_start\n")
	b.WriteString("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")

	// content_block_start (tool_use)
	b.WriteString("event: content_block_start\n")
	b.WriteString("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_123\",\"name\":\"get_weather\",\"input\":{}}}\n\n")

	// content_block_delta (streaming tool args)
	b.WriteString("event: content_block_delta\n")
	b.WriteString("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"location\\\":\\\"San Francisco\\\"}\"}}\n\n")

	// content_block_stop (tool_use)
	b.WriteString("event: content_block_stop\n")
	b.WriteString("data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")

	// message_delta (usage) - note stop_reason is "tool_use" (requesting execution)
	b.WriteString("event: message_delta\n")
	b.WriteString("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":50}}\n\n")

	// message_stop
	b.WriteString("event: message_stop\n")
	b.WriteString("data: {\"type\":\"message_stop\"}\n\n")

	return b.String()
}
