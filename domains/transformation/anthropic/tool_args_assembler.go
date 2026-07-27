package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateStreamingToolArgs is the public streaming tool-argument validator.
func ValidateStreamingToolArgs(concatenated string) (string, bool, error) {
	return validateStreamingToolArgs(concatenated)
}

// validateStreamingToolArgs checks whether the concatenated streaming tool-call
// arguments form valid JSON, and best-effort repairs the single most common
// truncation shape: an object/array left structurally open at end-of-stream.
//
// 2026-07-27 (F-5): Anthropic streams tool-call arguments as a series of
// input_json_delta partial-JSON fragments. The legacy converter concatenates
// them into a single string and emits it at content_block_stop WITHOUT
// validating the result is well-formed JSON. A truncated/malformed stream
// (mid-stream error, upstream bug) therefore forwarded invalid JSON to the
// client, which the OpenAI SDK rejects or mis-parses — silently.
//
// Strategy:
//  1. If already valid JSON, return it unchanged (the common, fast path).
//  2. If not, attempt a conservative structural repair: close an unterminated
//     string, then balance unclosed { } and [ ]. This recovers ONLY the case
//     where the cut left a complete key:value pair with an open container
//     (e.g. {"city":"Tokyo"} → already valid; {"city":"Tokyo","temp":21 →
//     repaired). It CANNOT repair a cut that leaves a dangling key with no
//     value (e.g. {"city":"Tokyo","temp") — that requires a real partial-JSON
//     parser and is left as future work.
//  3. If repair does not yield valid JSON, return the ORIGINAL unchanged so
//     behavior is no-worse-than-before, but surface a non-nil error so the
//     caller can log an anomaly (the failure is now observable, not silent).
//
// The key win over the pre-fix behavior is observability: every malformed
// tool-args emission now produces a logged anomaly instead of silently
// forwarding broken JSON. The repair is a bonus that recovers the easy cases.
func validateStreamingToolArgs(concatenated string) (result string, repaired bool, err error) {
	if concatenated == "" {
		return "", false, nil
	}
	// Fast path: already valid.
	if json.Valid([]byte(concatenated)) {
		return concatenated, false, nil
	}

	original := concatenated
	repairedJSON := balanceJSONContainers(concatenated)
	if json.Valid([]byte(repairedJSON)) {
		// Repair succeeded — the stream was truncated mid-container and we
		// closed it. This is a recovery, not an anomaly: return nil error so
		// callers treat it as success. (If observability of *successful*
		// repairs is wanted later, add a separate bool/counter rather than
		// overloading the error channel.)
		return repairedJSON, true, nil
	}
	// Repair did not yield valid JSON. Return the original unchanged (no-worse
	// than historical behavior) but surface a non-nil error so the anomaly is
	// observable rather than silently forwarding broken JSON.
	return original, false, fmt.Errorf("tool args are not valid JSON and could not be auto-repaired (len=%d); forwarding original unchanged", len(original))
}

// balanceJSONContainers performs a conservative structural repair of a JSON
// fragment by closing an unterminated string and balancing unclosed object/
// array containers. See validateStreamingToolArgs for the limitations.
func balanceJSONContainers(s string) string {
	var stack []byte
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inString {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if n := len(stack); n > 0 {
				opener := stack[n-1]
				if (c == '}' && opener == '{') || (c == ']' && opener == '[') {
					stack = stack[:n-1]
				}
			}
		}
	}

	var b strings.Builder
	b.WriteString(s)
	if inString {
		b.WriteByte('"') // close an unterminated string
	}
	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i] {
		case '{':
			b.WriteByte('}')
		case '[':
			b.WriteByte(']')
		}
	}
	return b.String()
}
