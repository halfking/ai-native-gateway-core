package transformation

import "encoding/json"

// RewriteResponsesModel changes only the top-level model field and preserves
// the original bytes when the requested model is already present.
func RewriteResponsesModel(bodyBytes []byte, model string) []byte {
	if len(bodyBytes) == 0 || model == "" {
		return bodyBytes
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return bodyBytes
	}
	var current string
	if raw, ok := envelope["model"]; ok {
		_ = json.Unmarshal(raw, &current)
	}
	if current == model {
		return bodyBytes
	}
	value, err := json.Marshal(model)
	if err != nil {
		return bodyBytes
	}
	envelope["model"] = value
	out, err := json.Marshal(envelope)
	if err != nil {
		return bodyBytes
	}
	return out
}

// CompressResponsesIfNeeded applies the provider-aware 80% trigger and the
// shared 60%/200K target to an OpenAI Responses envelope. The envelope and all
// non-input fields are preserved.
func CompressResponsesIfNeeded(bodyBytes []byte, contextWindow int) []byte {
	return compressResponsesWithTarget(bodyBytes, contextWindow, providerCompressionTargetTokens(contextWindow), false)
}

// CompressResponsesAggressively applies the same target as normal provider
// compression without requiring the 80% trigger. It is used after a provider
// has explicitly rejected the request for exceeding its context window.
func CompressResponsesAggressively(bodyBytes []byte, contextWindow int) []byte {
	return compressResponsesWithTarget(bodyBytes, contextWindow, providerCompressionTargetTokens(contextWindow), true)
}

func compressResponsesWithTarget(bodyBytes []byte, contextWindow, target int, force bool) []byte {
	if contextWindow <= 0 || target <= 0 {
		return bodyBytes
	}
	if !force && estimatePromptTokens(bodyBytes) <= int(float64(contextWindow)*defaultTriggerFraction) {
		return bodyBytes
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return bodyBytes
	}
	rawInput, ok := envelope["input"]
	if !ok {
		return bodyBytes
	}

	var inputText string
	if json.Unmarshal(rawInput, &inputText) == nil {
		runes := []rune(inputText)
		if len(runes) == 0 {
			return bodyBytes
		}
		for len(runes) > 1 {
			candidate := cloneResponsesEnvelope(envelope)
			trimmedText, err := json.Marshal(string(runes))
			if err != nil {
				return bodyBytes
			}
			candidate["input"] = trimmedText
			out, err := json.Marshal(candidate)
			if err != nil {
				return bodyBytes
			}
			if estimatePromptTokens(out) <= target {
				return out
			}
			runes = runes[len(runes)/2:]
		}
		return bodyBytes
	}

	var input []json.RawMessage
	if json.Unmarshal(rawInput, &input) != nil || len(input) <= 1 {
		return bodyBytes
	}
	for len(input) > 1 {
		candidate := cloneResponsesEnvelope(envelope)
		trimmed, err := json.Marshal(input[1:])
		if err != nil {
			return bodyBytes
		}
		candidate["input"] = trimmed
		out, err := json.Marshal(candidate)
		if err != nil {
			return bodyBytes
		}
		if estimatePromptTokens(out) <= target {
			return out
		}
		input = input[1:]
	}

	candidate := cloneResponsesEnvelope(envelope)
	trimmed, err := json.Marshal(input)
	if err != nil {
		return bodyBytes
	}
	candidate["input"] = trimmed
	out, err := json.Marshal(candidate)
	if err != nil {
		return bodyBytes
	}
	return out
}

func cloneResponsesEnvelope(envelope map[string]json.RawMessage) map[string]json.RawMessage {
	clone := make(map[string]json.RawMessage, len(envelope))
	for key, value := range envelope {
		clone[key] = value
	}
	return clone
}
