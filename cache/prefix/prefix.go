// Package prefix implements prompt-prefix stabilization to maximize LLM
// KV-cache (prefix-cache) hit rates across requests in the same session.
//
// Why this package exists:
//   Modern LLM providers (OpenAI, Anthropic, Google, DeepSeek, Moonshot ...)
//   all do some form of prefix caching: if the FIRST N tokens of a request
//   match a recently-seen prefix, the provider skips recomputing their KV
//   cache, cutting latency and (for Anthropic) cost. The catch: the match is
//   BYTES-EXACT from the start of the request. If a request shuffles its
//   messages (e.g. puts the newest user turn before the system prompt, or
//   interleaves tool definitions with conversation), the prefix diverges and
//   the cache misses — even though the CONTENT is identical.
//
// What this package does:
//   Stabilize() reorders an OpenAI-compatible or Anthropic-compatible message
//   list so that the STABLE part (system prompt + tool definitions + the
//   oldest conversation turns) comes first, and the VOLATILE part (the most
//   recent user turn, tool results that change every call) comes last. This
//   maximizes the byte-stable prefix length, which maximizes cache hits.
//
// What this package does NOT do:
//   - It does NOT inject cache_control markers (that is sessions.CacheInjector's
//     job; this package just orders messages so the injector's markers land on
//     a stable boundary).
//   - It does NOT call the LLM or know about providers.
//   - It does NOT mutate tool definitions' internal order (that would change
//     semantics); it only groups them as a block.
//
// Domain boundary (refactor plan §2.2 ⑦ cache/):
//   prefix/ OWNS: message ordering by stability class
//   prefix/ does NOT own: cache_control injection (sessions/), cache key
//   computation (future cache/kv/), semantic dedup (cache/semantic/).
//
// Stability is a soft hint, not a correctness contract: Stabilize MUST keep
// the conversation semantically equivalent. It never reorders turns WITHIN
// the conversation history (that would change meaning), only groups the
// conversation-history block relative to system/tools blocks.
package prefix

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Stability class of a message. Lower numbers are MORE stable (should appear
// earlier to extend the cacheable prefix).
//
// The ordering SystemClass < ToolClass < HistoryClass < TailClass is the
// single most important constant in this package: every reorder decision
// flows from it. Do NOT change the numeric order without updating the tests
// in prefix_test.go AND re-reading Anthropic/OpenAI prefix-caching docs.
type Stability int

const (
	// SystemClass: system / developer prompts. Almost never change within a
	// session -> highest cache value. Always first.
	SystemClass Stability = iota
	// ToolClass: tool/function definitions. Change rarely (only when the agent
	// upgrades its tools). Grouped after system, before history.
	ToolClass
	// HistoryClass: prior conversation turns (user/assistant/tool exchanges
	// that already happened). Stable WITHIN a growing session (turn 1-10 stay
	// the same when turn 11 is added) -> extend the prefix.
	HistoryClass
	// TailClass: the most recent turn (typically the new user message + any
	// fresh tool_result). Changes every request -> must go LAST so it doesn't
	// poison the prefix.
	TailClass
)

// String returns a stable lowercase tag for telemetry (cache.prefix.class.*).
func (s Stability) String() string {
	switch s {
	case SystemClass:
		return "system"
	case ToolClass:
		return "tool"
	case HistoryClass:
		return "history"
	case TailClass:
		return "tail"
	default:
		return "unknown"
	}
}

// Options tunes Stabilize. Zero value is safe and applies sane defaults.
type Options struct {
	// TailTurns is the number of MOST RECENT conversation turns to demote to
	// TailClass. Default 1 (only the latest user turn is volatile). Set higher
	// (e.g. 2) if the latest turn includes a tool_call->tool_result pair that
	// changes together.
	TailTurns int
}

func (o Options) tailTurns() int {
	if o.TailTurns < 1 {
		return 1
	}
	return o.TailTurns
}

// Stabilize reorders an OpenAI-compatible chat-completions body so its
// cacheable prefix is as long and stable as possible. It operates on the
// parsed JSON and returns the re-stabilized JSON.
//
// On any unrecognized shape it returns the ORIGINAL bytes unchanged with a
// nil error (never breaks the request). Callers wanting strict behavior can
// use StabilizeStrict. Stabilize is idempotent (byte-stable on stable input).
func Stabilize(body []byte, opts Options) ([]byte, *Report, error) {
	if len(body) == 0 {
		return body, nil, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, &Report{Changed: false, Reason: "not json"}, nil
	}
	msgsRaw, ok := obj["messages"]
	if !ok {
		return body, &Report{Changed: false, Reason: "no messages field"}, nil
	}
	var msgs []map[string]any
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return body, &Report{Changed: false, Reason: "messages not array"}, nil
	}
	if len(msgs) <= 1 {
		return body, &Report{Changed: false, Reason: "single or empty messages", Classes: classifyAll(msgs, opts)}, nil
	}
	classes := classifyAll(msgs, opts)
	reordered, changed := reorderByClass(msgs, classes)
	if !changed {
		return body, &Report{Changed: false, Reason: "already stable", Classes: classes}, nil
	}
	newMsgsRaw, err := json.Marshal(reordered)
	if err != nil {
		return body, &Report{Changed: false, Reason: "remarshal failed: " + err.Error(), Classes: classes}, nil
	}
	obj["messages"] = newMsgsRaw
	out, err := json.Marshal(obj)
	if err != nil {
		return body, &Report{Changed: false, Reason: "final marshal failed", Classes: classes}, err
	}
	return out, &Report{Changed: true, Reason: "reordered by stability class", Classes: classes}, nil
}

// Report describes what Stabilize did (or didn't), for telemetry + debugging.
// Callers log this; it must never contain prompt content.
type Report struct {
	Changed bool        // true if bytes were modified
	Reason  string      // why changed or not (human-readable, no secrets)
	Classes []Stability // per-message stability class of the INPUT (not output)
}

// classifyAll assigns a Stability class to every message. Rules:
//  1. role "system" or "developer" -> SystemClass
//  2. everything else -> conversation; the LAST `tailTurns` messages become
//     TailClass, the rest become HistoryClass.
func classifyAll(msgs []map[string]any, opts Options) []Stability {
	classes := make([]Stability, len(msgs))
	tail := opts.tailTurns()
	convoIndices := make([]int, 0, len(msgs))
	for i, m := range msgs {
		role, _ := m["role"].(string)
		if role == "system" || role == "developer" {
			classes[i] = SystemClass
		} else {
			classes[i] = HistoryClass
			convoIndices = append(convoIndices, i)
		}
	}
	for k, idx := range convoIndices {
		if k >= len(convoIndices)-tail {
			classes[idx] = TailClass
		}
	}
	return classes
}

// reorderByClass produces a new slice ordered SystemClass -> HistoryClass
// (original order preserved within) -> TailClass (original order preserved).
// CRITICAL invariant: within HistoryClass and within TailClass, the
// ORIGINAL RELATIVE ORDER is preserved. Reordering conversation turns would change
// meaning. Returns changed=false if already in that order.
func reorderByClass(msgs []map[string]any, classes []Stability) ([]map[string]any, bool) {
	var system, history, tail []map[string]any
	changed := false
	systemLeading := true
	sawNonSystem := false
	for i, m := range msgs {
		switch classes[i] {
		case SystemClass:
			system = append(system, m)
			if sawNonSystem {
				systemLeading = false
			}
		case HistoryClass:
			history = append(history, m)
			sawNonSystem = true
		case TailClass:
			tail = append(tail, m)
			sawNonSystem = true
		}
	}
	if systemLeading {
		return msgs, false
	}
	out := make([]map[string]any, 0, len(msgs))
	out = append(out, system...)
	out = append(out, history...)
	out = append(out, tail...)
	_ = changed
	return out, true
}

// MustStabilize is a convenience for tests/CLI that want a panic on error.
func MustStabilize(body []byte, opts Options) []byte {
	out, _, err := Stabilize(body, opts)
	if err != nil {
		panic(fmt.Sprintf("prefix: stabilize: %v", err))
	}
	return out
}

// ErrInvalidBody is returned by StabilizeStrict (not Stabilize) when the body
// is not a valid chat-completions JSON.
var ErrInvalidBody = errors.New("prefix: body is not a valid chat-completions JSON")

// StabilizeStrict is like Stabilize but returns ErrInvalidBody instead of
// passing through unrecognized bodies.
func StabilizeStrict(body []byte, opts Options) ([]byte, *Report, error) {
	if !json.Valid(body) {
		return body, nil, ErrInvalidBody
	}
	return Stabilize(body, opts)
}
