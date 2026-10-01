package streaming

import (
	"encoding/json"
	"strings"
)

type Normalizer struct {
	FinishReasonMap map[string]string
	UsageEnabled    bool
}

func NewNormalizer() *Normalizer {
	return &Normalizer{
		FinishReasonMap: map[string]string{
			"stop":           "stop",
			"STOP":           "stop",
			"end_turn":       "stop",
			"END_TURN":       "stop",
			"tool_calls":     "tool_calls",
			"TOOL_CALLS":     "tool_calls",
			"function_call":  "tool_calls",
			"length":         "length",
			"LENGTH":         "length",
			"max_tokens":     "length",
			"content_filter": "content_filter",
			"CONTENT_FILTER": "content_filter",
		},
		UsageEnabled: true,
	}
}

func (n *Normalizer) NormalizeFinishReason(reason string) string {
	if reason == "" {
		return "stop"
	}
	if mapped, ok := n.FinishReasonMap[reason]; ok {
		return mapped
	}
	lowered := strings.ToLower(reason)
	if mapped, ok := n.FinishReasonMap[lowered]; ok {
		return mapped
	}
	return reason
}

func (n *Normalizer) NormalizeChunk(chunk []byte, isStream bool) []byte {
	if !isStream {
		return n.normalizeNonStream(chunk)
	}
	return n.normalizeStreamChunk(chunk)
}

func (n *Normalizer) normalizeNonStream(body []byte) []byte {
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}

	choices, ok := resp["choices"]
	if !ok {
		return body
	}

	var choicesArr []map[string]json.RawMessage
	if err := json.Unmarshal(choices, &choicesArr); err != nil {
		return body
	}

	modified := false
	for i, choice := range choicesArr {
		frRaw, ok := choice["finish_reason"]
		if !ok {
			continue
		}
		var fr string
		if err := json.Unmarshal(frRaw, &fr); err != nil {
			continue
		}
		normalized := n.NormalizeFinishReason(fr)
		if normalized != fr {
			choicesArr[i]["finish_reason"], _ = json.Marshal(normalized)
			modified = true
		}
	}

	if !modified {
		return body
	}

	choicesRaw, _ := json.Marshal(choicesArr)
	resp["choices"] = choicesRaw

	out, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return out
}

func (n *Normalizer) normalizeStreamChunk(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	line := string(data)
	if !strings.HasPrefix(line, "data: ") {
		return data
	}

	payload := strings.TrimPrefix(line, "data: ")
	payload = strings.TrimRight(payload, "\n\r")

	if payload == "[DONE]" {
		return data
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return data
	}

	choices, ok := obj["choices"]
	if !ok {
		return data
	}

	var choicesArr []map[string]json.RawMessage
	if err := json.Unmarshal(choices, &choicesArr); err != nil {
		return data
	}

	modified := false
	for i, choice := range choicesArr {
		frRaw, ok := choice["finish_reason"]
		if !ok {
			continue
		}
		// 2026-10-01 Xcode/glm-5.2: an empty string is NOT a legal OpenAI
		// `finish_reason`. The spec (and every OpenAI-shaped frame) carries
		// JSON `null` until the stream terminates. Zhipu/GLM — and every
		// reseller that forwards its payload verbatim, e.g. SenseNova
		// (token.sensenova.cn) — emit `"finish_reason":""` on every
		// non-final chunk instead. Strict decoders (Xcode's Coding Assistant
		// is one) fail the whole event on it and drop the entire response.
		// Normalize `""` to `null` before anything else so the rest of this
		// function only ever sees a real reason or null.
		if isJSONNullOrEmptyString(frRaw) {
			choice["finish_reason"] = json.RawMessage("null")
			choicesArr[i] = choice
			modified = true
			continue
		}
		var frStr string
		if err := json.Unmarshal(frRaw, &frStr); err != nil {
			continue
		}
		normalized := n.NormalizeFinishReason(frStr)
		if normalized != frStr {
			choicesArr[i]["finish_reason"], _ = json.Marshal(normalized)
			modified = true
		}
	}

	if !modified {
		return data
	}

	choicesRaw, _ := json.Marshal(choicesArr)
	obj["choices"] = choicesRaw

	out, err := json.Marshal(obj)
	if err != nil {
		return data
	}

	return []byte("data: " + string(out) + "\n")
}

// isJSONNullOrEmptyString reports whether a raw JSON value is either `null` or
// the empty string `""`. Both are "no finish reason yet" in a streaming chunk,
// and neither is a legal literal for a client that decodes finish_reason into a
// non-optional enum (see normalizeStreamChunk).
func isJSONNullOrEmptyString(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return true
	}
	if trimmed != `""` {
		return false
	}
	// Only treat it as empty when it really decodes to "" — a non-string
	// value that happens to print the same way is left untouched.
	var s string
	return json.Unmarshal(raw, &s) == nil && s == ""
}
