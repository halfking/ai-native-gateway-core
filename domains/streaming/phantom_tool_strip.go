package streaming

import (
	"encoding/json"
	"strings"
)

// stripPhantomToolCallsInPayload removes nameless tool_calls slots from an
// OpenAI chat-completions JSON payload (streaming delta or non-stream
// message). MiniMax routinely prepends empty slots
// ({"id":"","function":{"name":"","arguments":""}}) before real tools;
// Magpie / ZCode Zod-validate each entry and treat an empty name as a
// failed turn (empty_model_response) even when later slots are valid.
//
// Malformed tool_calls entries are kept (R16). Kept entries: non-empty
// function name, flat "name", non-empty arguments (streaming
// continuation frames after the name arrived in an earlier chunk), or a
// **non-empty id**. Only fully empty slots are dropped.
//
// Why non-empty id counts as evidence of a real tool call (2026-10-17):
// the previous rule dropped "id-only" slots, which silently ate the
// **first frame of a legitimately streamed tool call** for providers that
// emit the id before the name:
//
//	frame 1: {"index":0,"id":"call_x"}          ← dropped by the old rule
//	frame 2: {"index":0,"function":{"name":"run",...}}  ← kept, but id is gone
//
// Two consequences, both observed as red tests in
// executor_chat_continuation_p03_test.go:
// TestP1ContinuationNeverResumesStartedToolCall: with the id frame gone the
// continuation logic no longer sees "a tool call has started" and fires a
// text-prefill continuation on top of a cut tool call (hook calls=1), and
// the client wire loses the tool-call precondition entirely. The real
// MiniMax phantom shape is `{"id":"","function":{"name":"","arguments":""}}`
// — **empty id** — so an id-based rule still drops it.
func stripPhantomToolCallsInPayload(payload string) string {
	payload = strings.TrimSpace(payload)
	if payload == "" || payload == "[DONE]" {
		return payload
	}
	var root map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &root) != nil {
		return payload
	}
	choicesRaw, ok := root["choices"]
	if !ok {
		return payload
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(choicesRaw, &choices) != nil || len(choices) == 0 {
		return payload
	}
	changed := false
	for i := range choices {
		for _, key := range []string{"delta", "message"} {
			containerRaw, ok := choices[i][key]
			if !ok {
				continue
			}
			var container map[string]json.RawMessage
			if json.Unmarshal(containerRaw, &container) != nil {
				continue
			}
			tcRaw, ok := container["tool_calls"]
			if !ok {
				continue
			}
			var tcs []json.RawMessage
			if json.Unmarshal(tcRaw, &tcs) != nil {
				continue
			}
			kept := make([]json.RawMessage, 0, len(tcs))
			for _, tc := range tcs {
				if toolCallJSONKeepable(tc) {
					kept = append(kept, tc)
					continue
				}
				changed = true
			}
			if len(kept) == len(tcs) {
				continue
			}
			changed = true
			if len(kept) == 0 {
				delete(container, "tool_calls")
			} else {
				newTC, err := json.Marshal(kept)
				if err != nil {
					continue
				}
				container["tool_calls"] = newTC
			}
			newContainer, err := json.Marshal(container)
			if err != nil {
				continue
			}
			choices[i][key] = newContainer
		}
	}
	if !changed {
		return payload
	}
	newChoices, err := json.Marshal(choices)
	if err != nil {
		return payload
	}
	root["choices"] = newChoices
	out, err := json.Marshal(root)
	if err != nil {
		return payload
	}
	return string(out)
}

func toolCallJSONKeepable(tc json.RawMessage) bool {
	var probe struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Function *struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal(tc, &probe) != nil {
		return true // R16: unparseable → keep
	}
	// A non-empty id means the provider already committed to this call.
	// The name may arrive in a later frame, so dropping the slot here would
	// orphan the following frames (and make "did a tool call start?"
	// unanswerable for the continuation gate).
	if strings.TrimSpace(probe.ID) != "" {
		return true
	}
	if probe.Function != nil {
		if strings.TrimSpace(probe.Function.Name) != "" {
			return true
		}
		// Arguments-only continuation (name was in an earlier frame).
		if strings.TrimSpace(probe.Function.Arguments) != "" {
			return true
		}
	}
	return strings.TrimSpace(probe.Name) != ""
}

// stripPhantomToolCallsFromSSELine rewrites a "data: {...}" SSE line.
// Non-data lines and [DONE] are returned unchanged.
func stripPhantomToolCallsFromSSELine(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return line
	}
	cleaned := stripPhantomToolCallsInPayload(payload)
	if cleaned == payload {
		return line
	}
	return "data: " + cleaned + "\n"
}
