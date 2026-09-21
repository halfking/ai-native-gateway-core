package streaming

import (
	"encoding/json"
	"strings"
)

// validateSSEDataFrame checks if an SSE data line contains valid JSON.
// It returns true if the payload is valid JSON or the [DONE] marker.
// Invalid frames (e.g., incomplete JSON like "{" or malformed data) return false.
//
// This validator protects clients from receiving unparseable JSON when unstable
// upstreams (minimax-m3, glm-5.2) send incomplete or corrupted frames. When combined
// with the FR-12 L1 holdback window, invalid initial frames are discarded transparently
// rather than triggering gateway_survival_resume_blocked after committing garbage to the client.
//
// 2026-08-29: Created to address the minimax-m3/glm-5.2 regression where upstreams
// occasionally send bare "{" or other incomplete JSON, causing client-side parsing
// errors and premature resume-blocked failures.
func validateSSEDataFrame(line string) bool {
	payload := extractPayload(line)
	if payload == "" {
		// Empty data lines are valid (SSE spec allows them as keepalives)
		return true
	}
	if payload == "[DONE]" {
		// OpenAI completion marker
		return true
	}

	// Attempt JSON parse. Any parse error means the frame is invalid.
	var v interface{}
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		return false
	}

	return true
}

// isRecoverableInvalidFrame determines if an invalid SSE frame should trigger
// a transparent retry (when within the holdback window) or fail the attempt.
//
// Returns true for frames that are clearly malformed upstream responses (e.g., single
// "{", truncated JSON, bare text without proper structure) — these indicate an
// upstream issue worth retrying. Returns false for frames that parse successfully
// but carry semantic errors (e.g., valid JSON error objects) — those should propagate
// to the client or be handled by existing error classification.
func isRecoverableInvalidFrame(line string) bool {
	payload := extractPayload(line)
	if payload == "" || payload == "[DONE]" {
		return false // valid frames
	}

	// Single-character payloads like "{" are clearly incomplete
	trimmed := strings.TrimSpace(payload)
	if len(trimmed) <= 2 {
		return true
	}

	// Try parsing as JSON
	var v interface{}
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		// JSON parse failed — this is a malformed frame worth retrying
		return true
	}

	// Valid JSON — let normal error classification handle it
	return false
}
