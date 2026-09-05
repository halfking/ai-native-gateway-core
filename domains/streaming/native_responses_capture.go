package streaming

import (
	"encoding/json"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard
)

// observeNativeResponsesEvent updates request-scoped telemetry without changing
// the native event sent to the client. The audit capture's legacy text funnel is
// fed synthetic internal payloads because it intentionally knows Chat-shaped
// deltas only; those payloads never leave this process.
func observeNativeResponsesEvent(capture *audit.StreamCapture, event NativeResponsesEvent) {
	if capture == nil || len(event.Payload) == 0 {
		return
	}

	if model := nativeString(event.Payload, "model"); model != "" {
		capture.SetRespModelIfEmpty(model)
	}
	if response, ok := nativeObject(event.Payload, "response"); ok {
		if model := nativeString(response, "model"); model != "" {
			capture.SetRespModelIfEmpty(model)
		}
		if usage := nativeObjectValue(response, "usage"); usage != nil {
			observeNativeUsage(capture, usage)
		}
	}
	if usage := nativeObjectValue(event.Payload, "usage"); usage != nil {
		observeNativeUsage(capture, usage)
	}

	switch event.Name {
	case "response.output_text.delta", "response.refusal.delta", "response.audio_transcript.delta":
		if delta := nativeString(event.Payload, "delta"); delta != "" {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": delta}}}})
			capture.ObservePayload(string(b), "", false)
		}
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		if delta := nativeString(event.Payload, "delta"); delta != "" {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"reasoning_content": delta}}}})
			capture.ObservePayload(string(b), "", false)
		}
	case "response.function_call_arguments.delta":
		if delta := nativeString(event.Payload, "delta"); delta != "" {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]string{"arguments": delta}}}}}}})
			capture.ObservePayload(string(b), "", false)
		}
	}
}

func observeNativeUsage(capture *audit.StreamCapture, usage map[string]json.RawMessage) {
	input := nativeInt(usage, "input_tokens")
	output := nativeInt(usage, "output_tokens")
	if input != nil || output != nil {
		capture.ObserveUsage(input, output, nil, nil)
	}
}

func nativeString(obj map[string]json.RawMessage, key string) string {
	var value string
	if raw, ok := obj[key]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	return strings.TrimSpace(value)
}

func nativeObject(obj map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	raw, ok := obj[key]
	if !ok {
		return nil, false
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	return value, true
}

func nativeObjectValue(obj map[string]json.RawMessage, key string) map[string]json.RawMessage {
	value, _ := nativeObject(obj, key)
	return value
}

func nativeInt(obj map[string]json.RawMessage, key string) *int {
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	var value int
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}
