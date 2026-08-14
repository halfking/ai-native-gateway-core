package streaming

import "testing"

// SR-W1 (doc 18 §5.1 AttemptCommitGate / §10.1): the frame classifier maps a
// complete client-facing SSE frame to its commit-gate class. Classes decide
// whether an attempt can be transparently discarded (comment / metadata) or
// must be committed (content / tool_call / terminal). Unknown or malformed
// frames fail closed as content — an unclassifiable frame is never droppable.
func TestClassifySSEFrame(t *testing.T) {
	tests := []struct {
		name     string
		protocol FrameProtocol
		frame    string
		want     FrameClass
	}{
		// ── protocol-independent transport frames ──────────────────────
		{
			name:     "sse comment is never semantic (openai)",
			protocol: FrameProtocolOpenAIChat,
			frame:    ": keep-alive\n",
			want:     FrameClassComment,
		},
		{
			name:     "gateway keepalive event is never semantic (anthropic)",
			protocol: FrameProtocolAnthropic,
			frame:    "event: keepalive\ndata: {\"type\":\"keepalive\",\"timestamp\":1}\n",
			want:     FrameClassComment,
		},
		{
			name:     "gateway node_switch event is never semantic (openai)",
			protocol: FrameProtocolOpenAIChat,
			frame:    "event: node_switch\ndata: {\"type\":\"node_switch\"}\n",
			want:     FrameClassComment,
		},

		// ── OpenAI Chat ────────────────────────────────────────────────
		{
			name:     "openai done sentinel is terminal",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: [DONE]\n",
			want:     FrameClassTerminal,
		},
		{
			name:     "openai role-only first chunk is attempt metadata",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n",
			want:     FrameClassAttemptMetadata,
		},
		{
			name:     "openai empty-choices chunk is attempt metadata",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"id\":\"c1\",\"choices\":[]}\n",
			want:     FrameClassAttemptMetadata,
		},
		{
			name:     "openai text delta is content",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n",
			want:     FrameClassContent,
		},
		{
			name:     "openai reasoning delta is content",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n",
			want:     FrameClassContent,
		},
		{
			name:     "openai tool_calls delta is tool_call",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"f\"}}]}}]}\n",
			want:     FrameClassToolCall,
		},
		{
			name:     "openai explicit tool_calls null is attempt metadata",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"tool_calls\":null}}]}\n",
			want:     FrameClassAttemptMetadata,
		},
		{
			name:     "openai empty tool_calls array is attempt metadata",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"tool_calls\":[]}}]}\n",
			want:     FrameClassAttemptMetadata,
		},
		{
			name:     "openai finish_reason chunk is terminal",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n",
			want:     FrameClassTerminal,
		},
		{
			name:     "openai usage-only chunk is terminal",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"usage\":{\"prompt_tokens\":1,\"total_tokens\":1}}\n",
			want:     FrameClassTerminal,
		},
		{
			name:     "openai error frame is terminal",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"error\":{\"message\":\"quota\",\"type\":\"rate_limit\"}}\n",
			want:     FrameClassTerminal,
		},
		{
			name:     "openai malformed json fails closed as content",
			protocol: FrameProtocolOpenAIChat,
			frame:    "data: {\"choices\":[{\"delta\":\n",
			want:     FrameClassContent,
		},
		{
			name:     "openai non-sse garbage fails closed as content",
			protocol: FrameProtocolOpenAIChat,
			frame:    "event: weird\ndata: not json\n",
			want:     FrameClassContent,
		},

		// ── OpenAI Responses ───────────────────────────────────────────
		{
			name:     "responses created carries attempt identity",
			protocol: FrameProtocolResponses,
			frame:    "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"m\"}}\n",
			want:     FrameClassAttemptMetadata,
		},
		{
			name:     "responses output_item.added (message) is attempt metadata",
			protocol: FrameProtocolResponses,
			frame:    "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\"}}\n",
			want:     FrameClassAttemptMetadata,
		},
		{
			name:     "responses output_item.added (function_call) is tool_call",
			protocol: FrameProtocolResponses,
			frame:    "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\"}}\n",
			want:     FrameClassToolCall,
		},
		{
			name:     "responses content_part.added is attempt metadata",
			protocol: FrameProtocolResponses,
			frame:    "event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\"}\n",
			want:     FrameClassAttemptMetadata,
		},
		{
			name:     "responses output_text.delta is content",
			protocol: FrameProtocolResponses,
			frame:    "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n",
			want:     FrameClassContent,
		},
		{
			name:     "responses output_text.done is content",
			protocol: FrameProtocolResponses,
			frame:    "event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"text\":\"Hi\"}\n",
			want:     FrameClassContent,
		},
		{
			name:     "responses function_call_arguments.delta is tool_call",
			protocol: FrameProtocolResponses,
			frame:    "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{}\"}\n",
			want:     FrameClassToolCall,
		},
		{
			name:     "responses completed is terminal",
			protocol: FrameProtocolResponses,
			frame:    "event: response.completed\ndata: {\"type\":\"response.completed\"}\n",
			want:     FrameClassTerminal,
		},
		{
			name:     "responses failed is terminal",
			protocol: FrameProtocolResponses,
			frame:    "event: response.failed\ndata: {\"type\":\"response.failed\"}\n",
			want:     FrameClassTerminal,
		},
		{
			name:     "responses incomplete is terminal",
			protocol: FrameProtocolResponses,
			frame:    "event: response.incomplete\ndata: {\"type\":\"response.incomplete\"}\n",
			want:     FrameClassTerminal,
		},
		{
			name:     "responses unknown event fails closed as content",
			protocol: FrameProtocolResponses,
			frame:    "event: response.audio.delta\ndata: {\"type\":\"response.audio.delta\"}\n",
			want:     FrameClassContent,
		},

		// ── Anthropic Messages ─────────────────────────────────────────
		{
			name:     "anthropic ping is stable connection metadata",
			protocol: FrameProtocolAnthropic,
			frame:    "event: ping\ndata: {\"type\":\"ping\"}\n",
			want:     FrameClassStableConnMetadata,
		},
		{
			name:     "anthropic message_start is attempt metadata",
			protocol: FrameProtocolAnthropic,
			frame:    "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"m\"}}\n",
			want:     FrameClassAttemptMetadata,
		},
		{
			name:     "anthropic content_block_start (text) is attempt metadata",
			protocol: FrameProtocolAnthropic,
			frame:    "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n",
			want:     FrameClassAttemptMetadata,
		},
		{
			name:     "anthropic content_block_start (tool_use) is tool_call",
			protocol: FrameProtocolAnthropic,
			frame:    "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"f\"}}\n",
			want:     FrameClassToolCall,
		},
		{
			name:     "anthropic content_block_start (server_tool_use) is tool_call",
			protocol: FrameProtocolAnthropic,
			frame:    "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"t\",\"name\":\"web_search\"}}\n",
			want:     FrameClassToolCall,
		},
		{
			name:     "anthropic content_block_start (web_search_tool_result) is tool_call",
			protocol: FrameProtocolAnthropic,
			frame:    "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"web_search_tool_result\"}}\n",
			want:     FrameClassToolCall,
		},
		{
			name:     "anthropic text_delta is content",
			protocol: FrameProtocolAnthropic,
			frame:    "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n",
			want:     FrameClassContent,
		},
		{
			name:     "anthropic input_json_delta is tool_call",
			protocol: FrameProtocolAnthropic,
			frame:    "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n",
			want:     FrameClassToolCall,
		},
		{
			name:     "anthropic thinking_delta is content",
			protocol: FrameProtocolAnthropic,
			frame:    "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n",
			want:     FrameClassContent,
		},
		{
			name:     "anthropic message_delta is terminal",
			protocol: FrameProtocolAnthropic,
			frame:    "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n",
			want:     FrameClassTerminal,
		},
		{
			name:     "anthropic message_stop is terminal",
			protocol: FrameProtocolAnthropic,
			frame:    "event: message_stop\ndata: {\"type\":\"message_stop\"}\n",
			want:     FrameClassTerminal,
		},
		{
			name:     "anthropic error event is terminal",
			protocol: FrameProtocolAnthropic,
			frame:    "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n",
			want:     FrameClassTerminal,
		},
		{
			name:     "anthropic unknown event fails closed as content",
			protocol: FrameProtocolAnthropic,
			frame:    "event: mystery\ndata: {\"type\":\"mystery\"}\n",
			want:     FrameClassContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifySSEFrame(tt.protocol, tt.frame); got != tt.want {
				t.Errorf("ClassifySSEFrame(%s, %q) = %q, want %q", tt.protocol, tt.frame, got, tt.want)
			}
		})
	}
}

// unknown protocol itself fails closed: every frame is treated as content so
// the gate can never drop what it cannot classify.
func TestClassifySSEFrameUnknownProtocolFailsClosed(t *testing.T) {
	if got := ClassifySSEFrame(FrameProtocol("graphql"), "data: {}\n"); got != FrameClassContent {
		t.Errorf("unknown protocol = %q, want %q", got, FrameClassContent)
	}
}

// data payload classification must inspect the JSON body, not just the event
// line, so multi-line `data:` frames (one JSON doc split across lines) are
// reassembled before classification.
func TestClassifySSEFrameMultiLineData(t *testing.T) {
	frame := "event: response.output_text.delta\ndata: {\"type\": \"response.output_text.delta\",\n" +
		"data: \"delta\": \"Hi\"}\n"
	if got := ClassifySSEFrame(FrameProtocolResponses, frame); got != FrameClassContent {
		t.Errorf("multi-line data frame = %q, want %q", got, FrameClassContent)
	}
}
