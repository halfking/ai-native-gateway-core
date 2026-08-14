package streaming

import "testing"

// SR-W1 stream_frame_classifier: classify client-facing SSE frames for the
// three supported wire protocols so AttemptCommitGate can track commit state
// without re-parsing protocol internals. Unknown frames must classify as
// FrameClassUnknown (fail closed) — the gate treats them as content so an
// ambiguous frame can never be silently discarded on retry.

func TestClassifyClientFrameOpenAIChatDoneSentinel(t *testing.T) {
	got := ClassifyClientFrame(ProtocolOpenAIChat, "data: [DONE]\n\n")
	if got != FrameClassTerminal {
		t.Fatalf("[DONE] sentinel: want FrameClassTerminal, got %v", got)
	}
}

func TestClassifyClientFrameTable(t *testing.T) {
	cases := []struct {
		name     string
		protocol ClientProtocol
		frame    string
		want     FrameClass
	}{
		// --- protocol-independent ---
		{"chat sse comment", ProtocolOpenAIChat, ": keep-alive\n\n", FrameClassKeepalive},
		{"responses sse comment", ProtocolOpenAIResponses, ": gateway-status: waiting_for_provider\n\n", FrameClassKeepalive},
		{"anthropic sse comment", ProtocolAnthropic, ": thinking\n\n", FrameClassKeepalive},

		// --- OpenAI Chat ---
		{"chat role frame", ProtocolOpenAIChat,
			`data: {"id":"x","choices":[{"delta":{"role":"assistant"}}]}` + "\n\n",
			FrameClassAttemptMetadata},
		{"chat text delta", ProtocolOpenAIChat,
			`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
			FrameClassContent},
		{"chat reasoning delta", ProtocolOpenAIChat,
			`data: {"choices":[{"delta":{"reasoning_content":"hm"}}]}` + "\n\n",
			FrameClassContent},
		{"chat tool_calls delta", ProtocolOpenAIChat,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"f1"}]}}]}` + "\n\n",
			FrameClassToolCall},
		{"chat audio delta", ProtocolOpenAIChat,
			`data: {"choices":[{"delta":{"audio":{"id":"a1"}}}]}` + "\n\n",
			FrameClassToolCall},
		{"chat usage-only frame", ProtocolOpenAIChat,
			`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5}}` + "\n\n",
			FrameClassTerminal},
		{"chat error frame", ProtocolOpenAIChat,
			`data: {"error":{"message":"boom","type":"upstream_error","code":"x"}}` + "\n\n",
			FrameClassError},
		{"chat unknown json shape", ProtocolOpenAIChat,
			`data: {"something":"else"}` + "\n\n", FrameClassUnknown},
		{"chat non-json payload", ProtocolOpenAIChat,
			"data: not-json-at-all\n\n", FrameClassUnknown},

		// --- OpenAI Responses ---
		{"responses created", ProtocolOpenAIResponses,
			"event: response.created\ndata: {\"type\":\"response.created\"}\n\n",
			FrameClassAttemptMetadata},
		{"responses item added", ProtocolOpenAIResponses,
			"event: response.output_item.added\ndata: {}\n\n",
			FrameClassAttemptMetadata},
		{"responses text delta", ProtocolOpenAIResponses,
			"event: response.output_text.delta\ndata: {}\n\n",
			FrameClassContent},
		{"responses reasoning delta", ProtocolOpenAIResponses,
			"event: response.reasoning_text.delta\ndata: {}\n\n",
			FrameClassContent},
		{"responses function args delta", ProtocolOpenAIResponses,
			"event: response.function_call_arguments.delta\ndata: {}\n\n",
			FrameClassToolCall},
		{"responses completed", ProtocolOpenAIResponses,
			"event: response.completed\ndata: {}\n\n",
			FrameClassTerminal},
		{"responses incomplete", ProtocolOpenAIResponses,
			"event: response.incomplete\ndata: {}\n\n",
			FrameClassTerminal},
		{"responses unknown event", ProtocolOpenAIResponses,
			"event: response.mystery.event\ndata: {}\n\n",
			FrameClassUnknown},
		{"responses no event line", ProtocolOpenAIResponses,
			"data: {}\n\n", FrameClassUnknown},

		// --- Anthropic ---
		{"anthropic ping", ProtocolAnthropic,
			"event: ping\ndata: {\"type\":\"ping\"}\n\n", FrameClassKeepalive},
		{"anthropic message_start", ProtocolAnthropic,
			"event: message_start\ndata: {\"message\":{\"usage\":{\"output_tokens\":1}}}\n\n",
			FrameClassAttemptMetadata},
		{"anthropic text block start", ProtocolAnthropic,
			"event: content_block_start\ndata: {\"content_block\":{\"type\":\"text\"}}\n\n",
			FrameClassAttemptMetadata},
		{"anthropic tool_use block start", ProtocolAnthropic,
			"event: content_block_start\ndata: {\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\"}}\n\n",
			FrameClassToolCall},
		{"anthropic block type unknown start", ProtocolAnthropic,
			"event: content_block_start\ndata: {\"content_block\":{\"type\":\"redacted_thinking\"}}\n\n",
			FrameClassUnknown},
		{"anthropic text delta", ProtocolAnthropic,
			"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n",
			FrameClassContent},
		{"anthropic input_json delta", ProtocolAnthropic,
			"event: content_block_delta\ndata: {\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\"}}\n\n",
			FrameClassToolCall},
		{"anthropic signature delta", ProtocolAnthropic,
			"event: content_block_delta\ndata: {\"delta\":{\"type\":\"signature_delta\"}}\n\n",
			FrameClassToolCall},
		{"anthropic block stop", ProtocolAnthropic,
			"event: content_block_stop\ndata: {}\n\n",
			FrameClassAttemptMetadata},
		{"anthropic message_delta", ProtocolAnthropic,
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
			FrameClassTerminal},
		{"anthropic message_stop", ProtocolAnthropic,
			"event: message_stop\ndata: {}\n\n", FrameClassTerminal},
		{"anthropic error", ProtocolAnthropic,
			"event: error\ndata: {\"error\":{\"type\":\"timeout\"}}\n\n",
			FrameClassError},
		{"anthropic unknown event", ProtocolAnthropic,
			"event: mystery\ndata: {}\n\n", FrameClassUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyClientFrame(tc.protocol, tc.frame); got != tc.want {
				t.Fatalf("ClassifyClientFrame(%v, %q) = %v, want %v",
					tc.protocol, tc.frame, got, tc.want)
			}
		})
	}
}

func TestClassifyClientFrameUnknownProtocolFailsClosed(t *testing.T) {
	if got := ClassifyClientFrame(ClientProtocol(99), "data: [DONE]\n\n"); got != FrameClassUnknown {
		t.Fatalf("unknown protocol: want FrameClassUnknown, got %v", got)
	}
}

// TestClassifyClientFrameCommentWithDataFrameIsNotKeepalive pins doc 20
// A-P2-4: a frame whose first line is a comment but which also carries
// data/event lines is ONE SSE event — the data must never be classified as
// transport keepalive (a deferred gate would otherwise stream it past the
// commit decision and a later Discard could drop it).
func TestClassifyClientFrameCommentWithDataFrameIsNotKeepalive(t *testing.T) {
	cases := []struct {
		name     string
		protocol ClientProtocol
		frame    string
		want     FrameClass
	}{
		{"anthropic comment before content delta", ProtocolAnthropic,
			": vendor-ka\nevent: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n",
			FrameClassContent},
		{"anthropic comment before tool_use block", ProtocolAnthropic,
			": vendor-ka\nevent: content_block_start\ndata: {\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"n\"}}\n\n",
			FrameClassToolCall},
		{"chat comment before data payload", ProtocolOpenAIChat,
			": vendor-ka\ndata: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
			FrameClassContent},
		{"pure comment frame stays keepalive", ProtocolAnthropic,
			": vendor-ka\n\n", FrameClassKeepalive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyClientFrame(tc.protocol, tc.frame); got != tc.want {
				t.Fatalf("ClassifyClientFrame(%v, %q) = %v, want %v",
					tc.protocol, tc.frame, got, tc.want)
			}
		})
	}
}

// TestClassifyClientFrameToolShapeRefinements pins the fa0baf822 refinements
// with direct cases (doc 20 A-P2-1): tool-shaped Anthropic block starts
// (incl. server-side tools) are never droppable metadata; explicit
// tool_calls null/[] on role-only chat chunks stays droppable.
func TestClassifyClientFrameToolShapeRefinements(t *testing.T) {
	cases := []struct {
		name     string
		protocol ClientProtocol
		frame    string
		want     FrameClass
	}{
		{"server_tool_use block start", ProtocolAnthropic,
			"event: content_block_start\ndata: {\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srv\",\"name\":\"web_search\"}}\n\n",
			FrameClassToolCall},
		{"web_search_tool_result block start", ProtocolAnthropic,
			"event: content_block_start\ndata: {\"content_block\":{\"type\":\"web_search_tool_result\",\"tool_use_id\":\"srv\"}}\n\n",
			FrameClassToolCall},
		{"tool_use block start", ProtocolAnthropic,
			"event: content_block_start\ndata: {\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"n\"}}\n\n",
			FrameClassToolCall},
		{"chat tool_calls null on role chunk stays metadata", ProtocolOpenAIChat,
			"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"tool_calls\":null,\"content\":\"\"}}]}\n\n",
			FrameClassAttemptMetadata},
		{"chat tool_calls empty array stays metadata", ProtocolOpenAIChat,
			"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"tool_calls\":[]}}]}\n\n",
			FrameClassAttemptMetadata},
		{"chat tool_calls present is tool output", ProtocolOpenAIChat,
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"f\"}}]}}]}\n\n",
			FrameClassToolCall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyClientFrame(tc.protocol, tc.frame); got != tc.want {
				t.Fatalf("ClassifyClientFrame(%v, %q) = %v, want %v",
					tc.protocol, tc.frame, got, tc.want)
			}
		})
	}
}

// TestClassifyClientFrameCRLFLineEndings pins CRLF tolerance end-to-end:
// event/data field extraction and tool-shape detection must survive
// \r\n-framed events (the GateWriter accepts both boundaries).
func TestClassifyClientFrameCRLFLineEndings(t *testing.T) {
	cases := []struct {
		name     string
		protocol ClientProtocol
		frame    string
		want     FrameClass
	}{
		{"anthropic tool_use via crlf", ProtocolAnthropic,
			"event: content_block_start\r\ndata: {\"content_block\":{\"type\":\"tool_use\",\"id\":\"t\",\"name\":\"n\"}}\r\n\r\n",
			FrameClassToolCall},
		{"anthropic text delta via crlf", ProtocolAnthropic,
			"event: content_block_delta\r\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\r\n\r\n",
			FrameClassContent},
		{"anthropic ping via crlf stays keepalive", ProtocolAnthropic,
			"event: ping\r\ndata: {}\r\n\r\n", FrameClassKeepalive},
		{"chat done sentinel via crlf", ProtocolOpenAIChat,
			"data: [DONE]\r\n\r\n", FrameClassTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyClientFrame(tc.protocol, tc.frame); got != tc.want {
				t.Fatalf("ClassifyClientFrame(%v, %q) = %v, want %v",
					tc.protocol, tc.frame, got, tc.want)
			}
		})
	}
}
