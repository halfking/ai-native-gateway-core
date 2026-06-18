// Package compressor - retain.go (Round 47 / v7 T5)
//
// A-track (system messages) and B-track (first user message) preservation
// during compression. Both are pinned: they must survive ANY compression
// pass (mechanical trim / Memora L1 inject / LLM summary) per v7 §4.
//
// Why pin?
//   - A: system prompts carry task spec, user constraints, behavior rules
//     that must persist across the entire conversation.
//   - B: first user message is the original task statement. Industrial
//     consensus (Anthropic SESSION_MEMORY_PROMPT, LangChain trim_messages
//     example, Claude Code CLAUDE.md) is to anchor "User Intent" at the
//     start so it never gets dropped by sliding-window trim.
//
// See:
//   - docs/llm-gateway-go/2026-06-18-compression-v7-final.md §4 (Q4 decision)
//   - https://platform.claude.com/cookbook/misc-session-memory-compaction
//     (Anthropic official User Intent template)
//   - https://docs.langchain.com/oss/python/langchain/short-term-memory
//     (LangChain `first_msg = messages[0]; recent = messages[-3:]`)
//
// This file does NOT do the rebuilding - that's rebuilder_openai.go /
// rebuilder_anthropic.go. retain.go only extracts the protected slices
// and gives the rebuilder a clean interface to splice them back in.

package compressor

import (
	"encoding/json"
	"fmt"
)

// Retained holds the pinned message slices a compressor pass must preserve.
// Slices are kept as raw JSON to avoid re-marshal cost on the hot path.
type Retained struct {
	// SystemMessages are all role=system messages from the OpenAI chat
	// format OR the top-level "system" field from the Anthropic Messages
	// API. They are returned verbatim and must be re-inserted at the
	// front of the rebuilt messages array (or as the Anthropic top-level
	// system field).
	SystemMessages []json.RawMessage

	// FirstUser is the messages[0] entry when role=user. nil when the
	// request has no user messages (defensive - shouldn't happen for a
	// real chat call). For Anthropic passthrough, FirstUser lives inside
	// Messages too (Anthropic users are in messages[] just like OpenAI).
	FirstUser *json.RawMessage

	// FirstUserIndex is the position of FirstUser in the source messages
	// array. Used by the rebuilder to know whether B-track protection
	// actually means "skip the first user from trim" (idx=0) or "preserve
	// user turn that started the conversation after some system preamble".
	// -1 means FirstUser is nil.
	FirstUserIndex int
}

// extractOpenAI pulls system messages and the first user message from an
// OpenAI chat body ({"messages":[...], "model":"...", ...}). Returns nil
// if the body is not parseable as a chat body or has no messages.
//
// We probe the wire format with a permissive struct (json.RawMessage for
// messages) so a malformed body returns nil instead of erroring out - the
// compressor is best-effort and must never fail the main request path.
func extractOpenAI(body []byte) (*Retained, error) {
	var probe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("extractOpenAI: unmarshal: %w", err)
	}
	if len(probe.Messages) == 0 {
		return nil, fmt.Errorf("extractOpenAI: no messages array")
	}

	ret := &Retained{
		FirstUserIndex: -1,
	}
	for i, m := range probe.Messages {
		role := messageRole(m)
		switch role {
		case "system":
			ret.SystemMessages = append(ret.SystemMessages, m)
		case "user":
			if ret.FirstUser == nil {
				raw := m
				ret.FirstUser = &raw
				ret.FirstUserIndex = i
			}
		}
	}
	return ret, nil
}

// extractAnthropic pulls system (top-level) and the first user message
// from an Anthropic Messages body ({"messages":[...], "system":"...|blocks",
// "model":"..."}). Anthropic carries system in a separate top-level field
// rather than as a messages[] entry. Returns nil if body is not parseable.
//
// The first user message here is the messages[0] entry (Anthropic doesn't
// allow system in messages[] - they live in the top-level "system" field).
func extractAnthropic(body []byte) (*Retained, error) {
	var probe struct {
		System   json.RawMessage   `json:"system"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("extractAnthropic: unmarshal: %w", err)
	}
	if len(probe.Messages) == 0 {
		return nil, fmt.Errorf("extractAnthropic: no messages array")
	}

	ret := &Retained{
		FirstUserIndex: -1,
	}
	if len(probe.System) > 0 && string(probe.System) != "null" {
		ret.SystemMessages = []json.RawMessage{probe.System}
	}
	for i, m := range probe.Messages {
		role := messageRole(m)
		if role == "user" && ret.FirstUser == nil {
			raw := m
			ret.FirstUser = &raw
			ret.FirstUserIndex = i
			break
		}
	}
	return ret, nil
}

// messageRole returns the role of a single message. Returns "" if the
// message is unparseable. Exported as package-private helper shared by
// extractOpenAI / extractAnthropic and the rebuilder files.
func messageRole(raw json.RawMessage) string {
	var probe struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Role
}

// IsPinnedSystem reports whether the request has any system messages
// pinned by A-track. Used by the rebuilder to decide whether to wrap
// the rebuilt body with the system field (Anthropic) or prepend it to
// messages[] (OpenAI).
func (r *Retained) IsPinnedSystem() bool {
	return r != nil && len(r.SystemMessages) > 0
}

// IsPinnedFirstUser reports whether the request has a first user message
// pinned by B-track. If false, the rebuilder must NOT splice a
// synthetic user message in (e.g. Memora L1 snippet injection must use
// a different injection point - see rebuilder_anthropic.go).
func (r *Retained) IsPinnedFirstUser() bool {
	return r != nil && r.FirstUser != nil
}
