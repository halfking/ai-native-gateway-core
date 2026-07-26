package streaming

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestShouldDropEmptyChoicesFrame guards the 2026-07-27 refinement of the
// empty-choices SSE drop filter (D-1 in the request-flow audit).
//
// Regression contract:
//   - A bare {"choices":[]} frame MUST be dropped (original glm-5.2 fix).
//   - A terminal {"choices":[],"usage":{...}} frame MUST be forwarded — this
//     is the canonical OpenAI stream_options.include_usage frame and dropping
//     it silently loses token accounting.
//   - Content-moderation frames (prompt_annotations / prompt_filter_results)
//     with empty choices MUST be forwarded.
//   - Malformed JSON or non-empty choices MUST be forwarded (best-effort).
func TestShouldDropEmptyChoicesFrame(t *testing.T) {
	cases := []struct {
		name string
		line string
		drop bool
	}{
		{
			name: "bare empty choices boilerplate — drop (glm-5.2 case)",
			line: `data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"glm-5.2","choices":[]}`,
			drop: true,
		},
		{
			name: "usage terminal frame — KEEP (include_usage)",
			line: `data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"gpt-4","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`,
			drop: false,
		},
		{
			name: "prompt_annotations moderation frame — KEEP",
			line: `data: {"choices":[],"prompt_annotations":[{"prompt_index":0,"content_filter_results":{}}]}`,
			drop: false,
		},
		{
			name: "prompt_filter_results Azure frame — KEEP",
			line: `data: {"choices":[],"prompt_filter_results":[{"prompt_index":0}]}`,
			drop: false,
		},
		{
			name: "non-empty choices — KEEP",
			line: `data: {"choices":[{"delta":{"content":"hi"}}]}`,
			drop: false,
		},
		{
			name: "DONE marker — KEEP (not a drop target)",
			line: "data: [DONE]",
			drop: false,
		},
		{
			name: "non-data line (comment) — KEEP",
			line: ": keepalive",
			drop: false,
		},
		{
			name: "malformed JSON — KEEP (best-effort, do not drop data)",
			line: `data: {"choices":[]` + "\n", // truncated
			drop: false,
		},
		{
			name: "empty choices with unrecognized key — KEEP (conservative)",
			line: `data: {"choices":[],"weird_field":"x"}`,
			drop: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldDropEmptyChoicesFrame(tc.line)
			assert.Equalf(t, tc.drop, got, "line=%q", tc.line)
		})
	}
}
