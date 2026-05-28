package relay

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
			"stop":          "stop",
			"STOP":          "stop",
			"end_turn":      "stop",
			"END_TURN":      "stop",
			"tool_calls":    "tool_calls",
			"TOOL_CALLS":    "tool_calls",
			"function_call": "tool_calls",
			"length":        "length",
			"LENGTH":        "length",
			"max_tokens":    "length",
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
		var frStr string
		if err := json.Unmarshal(frRaw, &frStr); err != nil {
			var frNull *string
			if err := json.Unmarshal(frRaw, &frNull); err == nil && frNull == nil {
				continue
			}
		}
		if frStr == "" || frStr == "null" {
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
