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
			return nil, false, true
		}
		return nil, false, false
	}

	out, err := json.Marshal(raw)
	if err != nil || bytes.Contains(out, []byte("{SENSITIVE:")) {
		return nil, false, true
	}
	return rewrite(out), true, false
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
