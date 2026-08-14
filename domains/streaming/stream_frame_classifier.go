package streaming

import (
	"encoding/json"
	"strings"
)

// stream_frame_classifier.go — SR-W1 (doc 18 §5.1 AttemptCommitGate, §10.1)
//
// Classifies client-facing SSE frames for the three supported wire protocols
// (OpenAI Chat, OpenAI Responses, Anthropic Messages) into a coarse
// FrameClass used by AttemptCommitGate to track commit state:
//
//   - keepalive frames (SSE comments, Anthropic ping) never advance commit
//     state and never enter capture/turns/token accounting;
//   - attempt metadata (opening envelopes, block starts) buffers locally and
//     stays droppable until Commit();
//   - content / tool-call frames are semantic: the first one triggers the
//     normal semantic commit;
//   - terminal frames close the stream for the attempt;
//   - anything unrecognized classifies as FrameClassUnknown — fail closed.
//     The gate treats unknown frames as content so an ambiguous frame can
//     never be silently discarded on transparent retry.

// ClientProtocol identifies the client-facing wire protocol of a stream.
type ClientProtocol int

const (
	// ProtocolOpenAIChat is the OpenAI chat.completions SSE dialect.
	ProtocolOpenAIChat ClientProtocol = iota
	// ProtocolOpenAIResponses is the OpenAI Responses event dialect.
	ProtocolOpenAIResponses
	// ProtocolAnthropic is the Anthropic Messages SSE dialect.
	ProtocolAnthropic
)

// FrameClass is the coarse, protocol-independent classification of a single
// client-facing SSE frame. Ordering matters: AttemptCommitGate keeps a
// monotonic commit state and higher classes win.
type FrameClass int

const (
	// FrameClassUnknown is an unrecognized frame. Fail closed: gates treat
	// it as content (forces commit, blocks discard).
	FrameClassUnknown FrameClass = iota
	// FrameClassKeepalive is transport-level keepalive: SSE comments and
	// Anthropic ping events. Not data, not commit-relevant.
	FrameClassKeepalive
	// FrameClassConnectionMetadata is gateway-generated, attempt-independent
	// opening envelope content that may be promoted to connection level.
	FrameClassConnectionMetadata
	// FrameClassAttemptMetadata is per-attempt protocol opening metadata
	// (envelope IDs, block starts, role frames). Droppable until commit.
	FrameClassAttemptMetadata
	// FrameClassContent is semantic model text output.
	FrameClassContent
	// FrameClassToolCall is non-continuable semantic output: tool calls,
	// tool arguments, audio. Forces commit and blocks phase-2 continuation.
	FrameClassToolCall
	// FrameClassTerminal closes the attempt's stream ([DONE],
	// response.completed, message_stop, message_delta).
	FrameClassTerminal
	// FrameClassError is a protocol error frame.
	FrameClassError
)

// ClassifyClientFrame classifies one raw client-facing SSE frame for the
// given protocol. A frame is the full block including "event:"/"data:" lines
// and trailing blank line, exactly as written to the wire.
func ClassifyClientFrame(protocol ClientProtocol, frame string) FrameClass {
	switch protocol {
	case ProtocolOpenAIChat:
		return classifyChatFrame(frame)
	case ProtocolOpenAIResponses:
		return classifyResponsesFrame(frame)
	case ProtocolAnthropic:
		return classifyAnthropicFrame(frame)
	default:
		return FrameClassUnknown
	}
}

func classifyChatFrame(frame string) FrameClass {
	if c, ok := classifyCommonSSEFrame(frame); ok {
		return c
	}
	payload, ok := sseDataPayload(frame)
	if !ok {
		return FrameClassUnknown
	}
	if payload == "[DONE]" {
		return FrameClassTerminal
	}
	return classifyChatDataPayload(payload)
}

// classifyCommonSSEFrame handles protocol-independent frame shapes: SSE
// comments (keepalive) and comment-only frames. The bool reports whether the
// frame was recognized at this layer.
func classifyCommonSSEFrame(frame string) (FrameClass, bool) {
	first := firstSSELine(frame)
	if strings.HasPrefix(first, ":") {
		return FrameClassKeepalive, true
	}
	return FrameClassUnknown, false
}

// firstSSELine returns the first non-empty line of the frame.
func firstSSELine(frame string) string {
	for _, line := range strings.Split(frame, "\n") {
		if line != "" {
			return line
		}
	}
	return ""
}

// sseDataPayload extracts the payload of the frame's "data:" field. Chat
// frames carry at most one data line.
func sseDataPayload(frame string) (string, bool) {
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "data:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
		}
	}
	return "", false
}

// sseEventName extracts the frame's "event:" field, if present.
func sseEventName(frame string) (string, bool) {
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "event:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "event:")), true
		}
	}
	return "", false
}

func classifyChatDataPayload(payload string) FrameClass {
	var f struct {
		Error   *json.RawMessage `json:"error"`
		Choices []struct {
			Delta struct {
				Role         string          `json:"role"`
				Content      json.RawMessage `json:"content"`
				Reasoning    json.RawMessage `json:"reasoning_content"`
				ToolCalls    json.RawMessage `json:"tool_calls"`
				Audio        json.RawMessage `json:"audio"`
				FunctionCall json.RawMessage `json:"function_call"`
			} `json:"delta"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &f); err != nil {
		return FrameClassUnknown
	}
	if f.Error != nil {
		return FrameClassError
	}
	if len(f.Choices) == 0 {
		// usage-only terminal frame (choices:[] + usage)
		if len(f.Usage) > 0 {
			return FrameClassTerminal
		}
		return FrameClassUnknown
	}
	d := f.Choices[0].Delta
	// "null"/"[]" mean no tool calls: several vendors emit an explicit null
	// on role-only chunks and that must stay droppable metadata.
	hasToolCalls := len(d.ToolCalls) > 0 && string(d.ToolCalls) != "null" && string(d.ToolCalls) != "[]"
	switch {
	case hasToolCalls, len(d.Audio) > 0, len(d.FunctionCall) > 0:
		return FrameClassToolCall
	case nonEmptyJSONString(d.Content), nonEmptyJSONString(d.Reasoning):
		return FrameClassContent
	default:
		// role-only structural opening frame ("content":"" or null carries
		// no semantic text and must stay droppable metadata)
		return FrameClassAttemptMetadata
	}
}

// nonEmptyJSONString reports whether raw is a JSON string with non-empty
// text. null / absent / empty / non-string values all report false.
func nonEmptyJSONString(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		// Non-string content (unexpected shape): fail closed as present.
		return true
	}
	return s != ""
}

func classifyResponsesFrame(frame string) FrameClass {
	if c, ok := classifyCommonSSEFrame(frame); ok {
		return c
	}
	name, ok := sseEventName(frame)
	if !ok {
		return FrameClassUnknown
	}
	switch name {
	case "response.output_text.delta", "response.reasoning_text.delta":
		return FrameClassContent
	case "response.function_call_arguments.delta":
		return FrameClassToolCall
	case "response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.annotation.added":
		return FrameClassAttemptMetadata
	case "response.output_text.done", "response.output_item.done",
		"response.content_part.done", "response.completed",
		"response.incomplete", "response.failed":
		return FrameClassTerminal
	default:
		return FrameClassUnknown
	}
}

func classifyAnthropicFrame(frame string) FrameClass {
	if c, ok := classifyCommonSSEFrame(frame); ok {
		return c
	}
	name, ok := sseEventName(frame)
	if !ok {
		return FrameClassUnknown
	}
	switch name {
	case "ping":
		// Transport-level keepalive in Anthropic's own dialect: visible to
		// SSE parsers but carries no model output and must not advance
		// commit state.
		return FrameClassKeepalive
	case "message_start", "content_block_stop":
		return FrameClassAttemptMetadata
	case "content_block_start":
		return classifyAnthropicContentBlockStart(frame)
	case "content_block_delta":
		return classifyAnthropicContentBlockDelta(frame)
	case "message_delta", "message_stop":
		return FrameClassTerminal
	case "error":
		return FrameClassError
	default:
		return FrameClassUnknown
	}
}

// classifyAnthropicContentBlockStart distinguishes text/thinking block starts
// (structural metadata) from tool_use block starts (semantic tool output).
func classifyAnthropicContentBlockStart(frame string) FrameClass {
	payload, ok := sseDataPayload(frame)
	if !ok {
		return FrameClassUnknown
	}
	var f struct {
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
	}
	if err := json.Unmarshal([]byte(payload), &f); err != nil {
		return FrameClassUnknown
	}
	switch f.ContentBlock.Type {
	case "text", "thinking":
		return FrameClassAttemptMetadata
	case "tool_use", "server_tool_use", "web_search_tool_result":
		// Any tool-shaped block (incl. server-side/side-effect tools,
		// doc 18 §10.3) is never droppable metadata.
		return FrameClassToolCall
	default:
		return FrameClassUnknown
	}
}

func classifyAnthropicContentBlockDelta(frame string) FrameClass {
	payload, ok := sseDataPayload(frame)
	if !ok {
		return FrameClassUnknown
	}
	var f struct {
		Delta struct {
			Type string `json:"type"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(payload), &f); err != nil {
		return FrameClassUnknown
	}
	switch f.Delta.Type {
	case "text_delta", "thinking_delta":
		return FrameClassContent
	case "input_json_delta", "signature_delta":
		return FrameClassToolCall
	default:
		return FrameClassUnknown
	}
}
