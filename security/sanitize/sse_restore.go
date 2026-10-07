package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	sseparser "github.com/kaixuan/llm-gateway-go/internal/sse"
)

func nextSSELine(frame []byte, start int) (contentEnd, end int) {
	end = start
	for end < len(frame) && frame[end] != '\r' && frame[end] != '\n' {
		end++
	}
	contentEnd = end
	if end < len(frame) {
		terminator := frame[end]
		end++
		if terminator == '\r' && end < len(frame) && frame[end] == '\n' {
			end++
		}
	}
	return contentEnd, end
}

func splitSSEEvents(chunk []byte) [][]byte {
	var events [][]byte
	start := 0
	for pos := 0; pos < len(chunk); {
		contentEnd, end := nextSSELine(chunk, pos)
		if contentEnd == pos && end > pos {
			events = append(events, chunk[start:end])
			start = end
		}
		pos = end
	}
	if start < len(chunk) {
		events = append(events, chunk[start:])
	}
	return events
}

func markerOutsideSSEData(frame []byte) bool {
	for pos := 0; pos < len(frame); {
		contentEnd, end := nextSSELine(frame, pos)
		line := frame[pos:contentEnd]
		if !bytes.HasPrefix(line, []byte("data:")) && looksLikeUnparsedMarker(line) {
			return true
		}
		if end == pos {
			break
		}
		pos = end
	}
	return false
}

func (it *SanitizeRestoreInterceptor) restoreSSEEvent(ctx context.Context, event []byte, sm SanitizeMap, state *streamRestoreState) ([]byte, bool, bool) {
	if markerOutsideSSEData(event) {
		return nil, false, true
	}
	payload, hasData, rewrite := sseparser.ParseDataFrame(event)
	if !hasData || bytes.Equal(payload, []byte("[DONE]")) {
		return nil, false, false
	}

	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		// An opaque payload cannot be assigned to the lane of a withheld
		// placeholder prefix. A literal/escaped reserved marker also must not
		// leave the interceptor inside malformed JSON.
		if len(state.tails) > 0 || looksLikeUnparsedMarker(payload) {
			return nil, false, true
		}
		return nil, false, false
	}

	changed := false
	for _, restore := range []func(context.Context, map[string]any, SanitizeMap, *streamRestoreState) (bool, bool){
		it.restoreStreamOpenAIDelta,
		it.restoreStreamAnthropicDelta,
		it.restoreStreamResponsesDelta,
	} {
		fieldChanged, blocked := restore(ctx, raw, sm, state)
		if blocked {
			return nil, false, true
		}
		changed = changed || fieldChanged
	}

	if !changed {
		// Decode string tokens when the source could contain an escaped
		// marker. Token iteration also catches duplicate JSON keys that a map
		// unmarshal would otherwise discard before inspecting.
		if containsReservedJSONToken(payload) {
			// 2026-10-08 minimax-m3/glm-5.3 fail-closed incident: models that
			// reason in-band (minimax <think>, glm thinking) echo the gateway's
			// own input placeholders back — often mangled ({SENSITIVE:secret:X}
			// with a non-numeric id), so PlaceholderPattern never matches, the
			// mask pass skips them and this residual probe failed the whole
			// stream even though the upstream tool_calls response was complete.
			// A marker carries no secret bytes (it points into the session map),
			// so mask it in place inside the lanes this interceptor owns and
			// release the frame — mirroring the non-stream maskUnrecognizedBody
			// precedent. Markers outside those lanes (unknown/metadata fields)
			// keep the block: no restorer owns those bytes.
			if it.maskUnmappedStreamMarkers(raw) {
				if out, merr := json.Marshal(raw); merr == nil && !bytes.Contains(out, []byte("{SENSITIVE:")) {
					return rewrite(out), true, false
				}
			}
			return nil, false, true
		}
		return nil, false, false
	}

	out, err := json.Marshal(raw)
	if err != nil || bytes.Contains(out, []byte("{SENSITIVE:")) {
		// A partially-restored frame can still carry an unmapped marker in a
		// lane this interceptor owns (e.g. one marker restored from the map, a
		// mangled sibling left behind). Mask it before giving up on the frame.
		if err == nil && it.maskUnmappedStreamMarkers(raw) {
			if out, merr := json.Marshal(raw); merr == nil && !bytes.Contains(out, []byte("{SENSITIVE:")) {
				return rewrite(out), true, false
			}
		}
		return nil, false, true
	}
	return rewrite(out), true, false
}

// maxDegenerateMarkerSpan bounds one reserved-marker span for the in-place
// mask pass. Issued placeholders are far shorter; anything longer is treated
// as unmaskable and keeps the fail-closed behaviour.
const maxDegenerateMarkerSpan = 128

// maskDegenerateMarkerSpans replaces every {SENSITIVE:...} span — including
// grammar-violating echoes the PlaceholderPattern mask pass cannot match —
// with [REDACTED]. It reports false when a span has no closing brace inside
// the bound, leaving the text unchanged so the caller can fail closed.
func maskDegenerateMarkerSpans(text string) (string, bool) {
	if !strings.Contains(text, "{SENSITIVE:") {
		return text, true
	}
	var b strings.Builder
	b.Grow(len(text))
	for {
		start := strings.Index(text, "{SENSITIVE:")
		if start < 0 {
			b.WriteString(text)
			return b.String(), true
		}
		end := strings.IndexByte(text[start:], '}')
		if end < 0 || end > maxDegenerateMarkerSpan {
			return text, false
		}
		b.WriteString(text[:start])
		b.WriteString("[REDACTED]")
		text = text[start+end+1:]
	}
}

// maskUnmappedStreamMarkers masks reserved-marker spans inside the stream
// delta lanes this interceptor restores (the same lanes the three
// restoreStream*Delta walkers own). It reports whether any marker was masked;
// the caller must still verify the re-marshalled payload is marker-free so
// markers in fields no restorer owns keep failing closed.
func (it *SanitizeRestoreInterceptor) maskUnmappedStreamMarkers(raw map[string]any) bool {
	masked := false
	if value, ok := raw["choices"].([]any); ok {
		for _, cAny := range value {
			c, ok := cAny.(map[string]any)
			if !ok {
				continue
			}
			if maskStringLeaf(c, "text") {
				masked = true
			}
			delta, ok := c["delta"].(map[string]any)
			if !ok {
				continue
			}
			for _, field := range []string{"content", "refusal", "reasoning_content"} {
				if maskStringLeaf(delta, field) {
					masked = true
				}
			}
			if audio, ok := delta["audio"].(map[string]any); ok {
				if maskStringLeaf(audio, "transcript") {
					masked = true
				}
			}
			if functionCall, ok := delta["function_call"].(map[string]any); ok {
				if maskStringLeaf(functionCall, "arguments") {
					masked = true
				}
			}
			toolCalls, _ := delta["tool_calls"].([]any)
			for _, toolAny := range toolCalls {
				tool, ok := toolAny.(map[string]any)
				if !ok {
					continue
				}
				fn, ok := tool["function"].(map[string]any)
				if !ok {
					continue
				}
				if maskStringLeaf(fn, "arguments") {
					masked = true
				}
			}
		}
	}
	if t, _ := raw["type"].(string); t == "content_block_delta" {
		if delta, ok := raw["delta"].(map[string]any); ok {
			for _, field := range []string{"text", "input", "partial_json"} {
				if maskStringLeaf(delta, field) {
					masked = true
				}
			}
		}
	}
	switch t, _ := raw["type"].(string); t {
	case "response.output_text.delta", "response.function_call_arguments.delta",
		"response.refusal.delta", "response.audio_transcript.delta",
		"response.reasoning_text.delta", "response.reasoning_summary_text.delta",
		"response.custom_tool_call_input.delta":
		if maskStringLeaf(raw, "delta") {
			masked = true
		}
	case "response.output_text.done", "response.reasoning_text.done",
		"response.reasoning_summary_text.done":
		if maskStringLeaf(raw, "text") {
			masked = true
		}
	case "response.function_call_arguments.done":
		if maskStringLeaf(raw, "arguments") {
			masked = true
		}
	case "response.custom_tool_call_input.done":
		if maskStringLeaf(raw, "input") {
			masked = true
		}
	case "response.refusal.done", "response.audio_transcript.done":
		field := "text"
		if _, ok := raw[strings.TrimSuffix(t, ".done")]; ok {
			field = strings.TrimSuffix(strings.TrimPrefix(t, "response."), ".done")
		}
		if maskStringLeaf(raw, field) {
			masked = true
		}
	}
	return masked
}

// maskStringLeaf replaces reserved-marker spans in one owned string field.
// Tool-argument lanes stay JSON-safe: [REDACTED] replaces a span that lived
// inside a JSON string value, never across structural bytes.
func maskStringLeaf(object map[string]any, field string) bool {
	value, ok := object[field].(string)
	if !ok || !strings.Contains(value, "{SENSITIVE:") {
		return false
	}
	masked, complete := maskDegenerateMarkerSpans(value)
	if !complete {
		return false
	}
	object[field] = masked
	return true
}

func looksLikeUnparsedMarker(payload []byte) bool {
	if bytes.Contains(payload, []byte("{SENSITIVE:")) {
		return true
	}
	if !bytes.Contains(payload, []byte("\\u")) {
		return false
	}
	// Malformed JSON can still carry a Unicode-escaped marker. Decode only
	// ASCII \uXXXX escapes for this safety probe; ordinary malformed control
	// data with an unrelated Unicode escape remains passthrough.
	normalized := make([]byte, 0, len(payload))
	for i := 0; i < len(payload); i++ {
		if payload[i] == '\\' && i+5 < len(payload) && payload[i+1] == 'u' {
			value, err := strconv.ParseUint(string(payload[i+2:i+6]), 16, 16)
			if err == nil && value <= 0x7f {
				normalized = append(normalized, byte(value))
				i += 5
				continue
			}
		}
		normalized = append(normalized, payload[i])
	}
	return bytes.Contains(normalized, []byte("{SENSITIVE:"))
}

func containsReservedJSONToken(payload []byte) bool {
	if !bytes.Contains(payload, []byte("SENSITIVE")) && !bytes.Contains(payload, []byte("\\u")) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
		if value, ok := token.(string); ok && strings.Contains(value, "{SENSITIVE:") {
			return true
		}
	}
}
