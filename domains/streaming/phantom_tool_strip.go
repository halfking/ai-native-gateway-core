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
// function name, flat "name", OR non-empty arguments (streaming
// continuation frames after the name arrived in an earlier chunk). Only
// fully empty / id-only phantoms are dropped.
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
		Name     string `json:"name"`
		Function *struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal(tc, &probe) != nil {
		return true // R16: unparseable → keep
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
