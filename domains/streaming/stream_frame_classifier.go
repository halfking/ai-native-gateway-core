package streaming

import (
	"encoding/json"
	"strings"
)

// SR-W1 protocol commit gate (doc 18 §5.1 AttemptCommitGate, §10.1): before
// an attempt is committed, the client-facing stream is classified frame by
// frame so the coordinator can decide whether the attempt buffer is still
// droppable (transparent retry) or must be committed.

// FrameProtocol identifies the client-facing wire protocol of a stream.
// The gate sits between the bridge and the CLIENT, so the protocol is the
// client protocol, regardless of the upstream format the bridge converts from.
type FrameProtocol string

const (
	FrameProtocolOpenAIChat FrameProtocol = "openai_chat"
	FrameProtocolResponses  FrameProtocol = "responses"
	FrameProtocolAnthropic  FrameProtocol = "anthropic"
)

// FrameClass is the commit-gate classification of one complete SSE frame.
type FrameClass string

const (
	// FrameClassComment: SSE comments and gateway keepalive/status events.
	// Transport-only: never semantic content, never buffered per attempt,
	// never counted into tokens or capture.
	FrameClassComment FrameClass = "comment"

	// FrameClassStableConnMetadata: connection-level frames that are
	// independent of any single attempt (e.g. anthropic ping). Safe to
	// forward to the client before commit.
	FrameClassStableConnMetadata FrameClass = "stable_connection_metadata"

	// FrameClassAttemptMetadata: protocol opening frames bound to this
	// attempt's identity (message_start / response.created / role-only
	// chunks). They live in the attempt buffer and are only written on
	// Commit(); they are discarded with the attempt on transparent retry.
	FrameClassAttemptMetadata FrameClass = "attempt_metadata"

	// FrameClassContent: model-visible text/thinking output. First such
	// frame triggers the normal semantic commit.
	FrameClassContent FrameClass = "content"

	// FrameClassToolCall: tool-call output. Never transparently replayable.
	FrameClassToolCall FrameClass = "tool_call"

	// FrameClassTerminal: stream terminator frames ([DONE], message_stop,
	// response.completed, finish_reason/usage chunks, error frames).
	FrameClassTerminal FrameClass = "terminal"
)

// gatewayStatusEvents are gateway-generated status events (keepalive_sender.go).
// They describe the connection, not the attempt, so they are transport class.
var gatewayStatusEvents = map[string]bool{
	"keepalive":   true,
	"node_switch": true,
}

// ClassifySSEFrame classifies one complete SSE frame (with or without its
// trailing blank line) for the given client protocol.
//
// Fail-closed rule (doc 18 §10.3): any frame that cannot be confidently
// classified — malformed JSON, unknown event, unknown protocol — is content.
// Content is the most conservative droppability class: an unclassifiable frame
// forces semantic commit and forbids discarding the attempt.
func ClassifySSEFrame(protocol FrameProtocol, frame string) FrameClass {
	event, data := splitSSEFrame(frame)

	if event != "" && gatewayStatusEvents[event] {
		return FrameClassComment
	}
	// Comment-only frames (": keep-alive") never carry a data payload.
	if event == "" && data == "" {
		return FrameClassComment
	}

	switch protocol {
	case FrameProtocolOpenAIChat:
		return classifyOpenAIChatFrame(event, data)
	case FrameProtocolResponses:
		return classifyResponsesFrame(event, data)
	case FrameProtocolAnthropic:
		return classifyAnthropicFrame(event, data)
	default:
		return FrameClassContent
	}
}

// splitSSEFrame extracts the event name and the reassembled data payload from
// a complete SSE frame. Multi-line `data:` fields are joined per the SSE spec
// (concatenated with "\n" between lines) so the JSON body can be parsed even
// when split across lines.
func splitSSEFrame(frame string) (event string, data string) {
	var dataParts []string
	for _, rawLine := range strings.Split(frame, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		switch {
		case strings.HasPrefix(line, ":"):
			// comment line — ignored
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataParts = append(dataParts, strings.TrimPrefix(line, "data:"))
		}
	}
	data = strings.Join(dataParts, "\n")
	if data != "" {
		data = strings.TrimLeft(data, " ")
	}
	return event, data
}

func classifyOpenAIChatFrame(event, data string) FrameClass {
	if event != "" {
		// OpenAI Chat wire format has no event lines; only gateway status
		// events (handled above) are legal. Anything else is foreign.
		return FrameClassContent
	}
	if data == "" {
		return FrameClassComment
	}
	if strings.TrimSpace(data) == "[DONE]" {
		return FrameClassTerminal
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return FrameClassContent // malformed JSON: fail closed
	}
	if _, ok := obj["error"]; ok {
		return FrameClassTerminal
	}
	if _, ok := obj["usage"]; ok {
		if _, hasChoices := obj["choices"]; !hasChoices {
			return FrameClassTerminal
		}
	}

	if choicesRaw, ok := obj["choices"]; ok {
		var choices []struct {
			Delta struct {
				Content          string          `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		}
		if err := json.Unmarshal(choicesRaw, &choices); err != nil {
			return FrameClassContent
		}
		if len(choices) == 0 {
			return FrameClassAttemptMetadata
		}
		c := choices[0]
		if c.FinishReason != nil && *c.FinishReason != "" {
			return FrameClassTerminal
		}
		// "null"/"[]"/absent all mean no tool calls; several vendors emit an
		// explicit null on role-only chunks and that must stay metadata.
		var toolCalls []json.RawMessage
		if string(c.Delta.ToolCalls) != "null" {
			//nolint:errcheck // malformed tool_calls falls through to content
			json.Unmarshal(c.Delta.ToolCalls, &toolCalls)
		}
		if len(toolCalls) > 0 {
			return FrameClassToolCall
		}
		if c.Delta.Content != "" || c.Delta.ReasoningContent != "" {
			return FrameClassContent
		}
		return FrameClassAttemptMetadata
	}
	return FrameClassAttemptMetadata
}

func classifyResponsesFrame(event, data string) FrameClass {
	switch event {
	case "":
		// Responses passthrough always emits event lines; data-only frames
		// cannot be attributed safely.
		return FrameClassContent
	case "response.created",
		"response.in_progress",
		"response.output_item.added", // message/tool-added envelope; tool identity checked below
		"response.content_part.added":
		if class := responsesAddedItemToolCheck(data); class != "" {
			return class
		}
		return FrameClassAttemptMetadata
	case "response.output_text.delta",
		"response.output_text.done",
		"response.output_text.annotation.added",
		"response.reasoning_summary.delta",
		"response.reasoning_summary.done",
		"response.reasoning_text.delta",
		"response.reasoning_text.done":
		return FrameClassContent
	case "response.output_item.done",
		"response.content_part.done",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_part.done":
		// Structural closing frames. They follow their semantic deltas, and
		// for an empty stream they close an empty part — treating them as
		// content keeps the state monotonic and fail-closed.
		if class := responsesAddedItemToolCheck(data); class != "" {
			return class
		}
		return FrameClassContent
	case "response.function_call_arguments.delta",
		"response.function_call_arguments.done":
		return FrameClassToolCall
	case "response.completed",
		"response.failed",
		"response.incomplete",
		"response.queued",
		"response.ambiguous":
		return FrameClassTerminal
	default:
		return FrameClassContent
	}
}

// responsesAddedItemToolCheck returns FrameClassToolCall when the frame's
// item is a function/tool call (doc 18 §10.3: tool calls are never
// transparently replayable, even the announcement envelope).
func responsesAddedItemToolCheck(data string) FrameClass {
	var obj struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return FrameClassContent
	}
	switch obj.Item.Type {
	case "function_call", "local_shell_call", "custom_tool_call", "mcp_call", "web_search_call", "computer_call", "code_interpreter_call":
		return FrameClassToolCall
	default:
		return ""
	}
}

func classifyAnthropicFrame(event, data string) FrameClass {
	switch event {
	case "":
		// Anthropic wire format always emits event lines.
		return FrameClassContent
	case "ping":
		return FrameClassStableConnMetadata
	case "message_start":
		return FrameClassAttemptMetadata
	case "content_block_start":
		var obj struct {
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			return FrameClassContent
		}
		switch obj.ContentBlock.Type {
		case "tool_use", "server_tool_use", "web_search_tool_result":
			// Any tool-shaped block (incl. server-side/side-effect tools,
			// doc 18 §10.3) is never droppable metadata.
			return FrameClassToolCall
		}
		return FrameClassAttemptMetadata
	case "content_block_delta":
		var obj struct {
			Delta struct {
				Type string `json:"type"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			return FrameClassContent
		}
		switch obj.Delta.Type {
		case "input_json_delta":
			return FrameClassToolCall
		case "text_delta", "thinking_delta", "signature_delta", "citations_delta":
			return FrameClassContent
		default:
			return FrameClassContent
		}
	case "content_block_stop":
		// Structural close of a block; monotonic state makes this safe.
		return FrameClassContent
	case "message_delta", "message_stop", "error":
		return FrameClassTerminal
	default:
		return FrameClassContent
	}
}
