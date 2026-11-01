package ir

import "encoding/json"

// reasoningAliasField is the **Ollama OpenAI-compat** name for the thinking
// trace.
//
// OpenAI's Chat Completions dialect carries the trace in
// `choices[].message.reasoning_content` (non-stream) and
// `choices[].delta.reasoning_content` (stream). Ollama's `/v1` compatibility
// layer instead emits the very same trace under the shorter name
// `reasoning` — in *both* shapes:
//
//	POST http://<host>:11434/v1/chat/completions  (stream=false)
//	{"choices":[{"message":{"role":"assistant","content":"",
//	 "reasoning":"Thinking Process:\n\n1. **Analyze…"},"finish_reason":"length"}]}
//
//	POST http://<host>:11434/v1/chat/completions  (stream=true)
//	data: {"choices":[{"delta":{"role":"assistant","content":"",
//	 "reasoning":"Thinking"},"finish_reason":null}]}
//
// Measured on Ollama 0.40.11 (local /v1 endpoint, model qwen3.5:4b,
// 2026-11-02): the `message` object carries exactly the keys
// {role, content, reasoning} — `reasoning_content` is **absent**, so the
// generic OpenAI parser silently dropped the whole trace.
//
// Consequences of the gap (before this file existed):
//
//  1. The thinking trace never reached the client. A caller asking a
//     reasoning model got an answer with neither content nor reasoning.
//  2. `usage.completion_tokens_details.reasoning_tokens` is absent for
//     Ollama, so `emptyoutcome.InspectEmptyBody` could only attribute the
//     empty answer via criterion ① (finish_reason == "length"). The
//     attribution itself stayed correct — this alias does not change any
//     empty/non-empty decision.
//
// What this helper deliberately does NOT do:
//
//   - It does not make reasoning count as assistant output. The 2026-10-17
//     owner decision (see internal/emptyoutcome) stands: a reasoning delta
//     is progress, not an answer, so a reasoning-only body is still an
//     empty outcome and still fails over. Only the **relay** of the trace
//     is restored here.
//   - It does not touch the Responses API, where top-level `reasoning` is a
//     *configuration object* (`{"effort":"high"}`) and never a trace. That
//     parser is a different code path (parse_responses.go) and is not
//     affected by this helper.
//
// The value is read as json.RawMessage rather than a typed string so that a
// provider that echoes back an **object** under `reasoning` (e.g.
// `{"reasoning":{"effort":"high"}}`) does not make the whole envelope fail
// to unmarshal — a plain `string` field would abort the parse of the entire
// response.
const reasoningAliasField = "reasoning"

// reasoningAliasString extracts a reasoning trace from a raw JSON value that
// may be either a string (Ollama /v1 dialect — the trace) or an object
// (Responses-style `reasoning` config — not a trace, ignored).
//
// Returns "" for a missing, null, non-string, or empty value.
func reasoningAliasString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
