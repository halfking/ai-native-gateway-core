// Package kv computes stable cache keys for LLM upstream prefix-caching and
// tracks hit/miss rates of those keys across requests.
//
// What this package does:
//
//	Given an OpenAI-compatible (or Anthropic-compatible) chat-completions
//	request body, compute a SHA256 fingerprint of the STABLE prefix — the
//	bytes the upstream provider will actually cache. Two requests with the
//	same system prompt, tool definitions, and history (regardless of how the
//	client ordered them) produce the SAME key. The most recent turn (the
//	"tail") is excluded by default because it changes every request.
//
// Why this matters (cost reduction / "降本能力"):
//
//	Modern LLM providers (Anthropic, OpenAI, Google, DeepSeek, Moonshot …)
//	bill for INPUT tokens but skip recomputation when the prompt starts with
//	a byte-stable prefix they have KV-cached. The match is BYTES-EXACT from
//	the start. If a client shuffles its messages, the prefix diverges and
//	the cache misses — even though the content is identical. By computing a
//	key that depends ONLY on the stabilized prefix, this package lets the
//	gateway:
//	  1. Decide whether to inject cache_control markers (only when the
//	     prefix is long enough to be worth caching).
//	  2. Build telemetry dashboards ("X% of requests hit upstream prefix cache").
//	  3. Pre-compute the cacheable byte length for billing estimates.
//
// Domain boundary (see cache/prefix/prefix.go:30 for the parent boundary):
//
//	kv/ OWNS: cache key computation, hit/miss telemetry, Store interface
//	kv/ does NOT own:
//	  - message stabilization (cache/prefix/ — kv/ DELEGATES to it)
//	  - cache_control marker injection (domains/session/cache_injector.go)
//	  - semantic similarity matching (cache/semantic/)
//	  - actual upstream HTTP calls (provider/)
//
// What kv/ guarantees:
//   - Key() is a pure function of (body, opts). Same input -> same output.
//   - Key() never errors on bad input — it produces an empty Key for
//     un-cacheable bodies so the hot path never breaks.
//   - Tail-class messages are excluded from the hash (configurable count).
//   - The returned key is hex-encoded SHA256 (64 chars), safe as a Redis key,
//     URL path component, or DB column value.
//   - The Store interface is concurrency-safe and bounded by TTL.
package kv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/kaixuan/llm-gateway-go/cache/prefix"
)

// MaxPayloadBytes is the per-entry payload cap for Store implementations.
// Mirrors cache/semantic.MaxPayloadBytes so callers can use one constant.
const MaxPayloadBytes = 64 * 1024

// KeyOptions tunes the cache key computation. Zero value defaults to
// TailTurns=1 (only the most recent conversation turn is excluded from the
// hash).
type KeyOptions struct {
	// TailTurns is the number of MOST RECENT conversation turns to EXCLUDE
	// from the hash. These turns are volatile (change every request) and
	// would poison the upstream prefix-cache. Default: 1. Higher values
	// (e.g. 2) are appropriate when the latest turn is a tool_call +
	// tool_result pair that changes together.
	TailTurns int
}

// DefaultKeyOptions returns the standard configuration: exclude only the
// most recent turn. This matches the most common chat-completions pattern.
func DefaultKeyOptions() KeyOptions {
	return KeyOptions{TailTurns: 1}
}

// KeyResult is the output of Key(). It bundles the cache key with metadata
// for observability. Callers log Metadata to telemetry; never log PrefixBytes
// (it contains prompt content).
type KeyResult struct {
	// Key is the hex-encoded SHA256 of the stabilized prefix. Empty when
	// the body has no cacheable prefix (e.g. nil/empty input, single
	// message, non-chat body). Callers MUST treat empty key as
	// "always miss" and skip cache lookups.
	Key string

	// PrefixBytes is the actual byte slice that was hashed. Exposed for
	// debugging and integration with session.CacheInjector (which needs the
	// raw bytes to know where to insert cache_control markers). NEVER
	// LOG THIS — it contains prompt content.
	PrefixBytes []byte

	// Length is the number of bytes that contributed to the hash. Useful
	// for telemetry: "saved N bytes of prefix cache work".
	Length int

	// Truncated is true iff TailTurns > 0 messages were excluded. Callers
	// can use this to decide whether to inject cache_control markers
	// (only worth doing when Truncated=true and Length is non-trivial).
	Truncated bool
}

// ErrInvalidOptions is reserved for future strict variants. Key() never
// returns this — it is tolerant by design.
var ErrInvalidOptions = errors.New("kv: invalid key options")

// Key computes a stable cache key for the chat-completions body.
//
// Algorithm:
//  1. Stabilize() the body via cache/prefix (reorders system prompts to front).
//  2. Extract the messages up to (but not including) the last `TailTurns`.
//  3. Hash the remaining JSON bytes with SHA256.
//  4. Return hex-encoded hash + metadata.
//
// On any unrecognized shape (empty body, non-JSON, no messages field, single
// message) Key returns (empty key, nil error). It is intentionally tolerant:
// the cache layer is an optimization, never a correctness requirement.
func Key(body []byte, opts KeyOptions) (KeyResult, error) {
	if opts.TailTurns < 1 {
		opts.TailTurns = 1
	}
	if len(body) == 0 {
		return KeyResult{}, nil
	}

	// Step 1: stabilize. Stabilize is also tolerant — non-JSON passes
	// through unchanged, no-messages-field passes through, etc.
	stabilized, _, err := prefix.Stabilize(body, prefix.Options{TailTurns: opts.TailTurns})
	if err != nil {
		// Stabilize only returns error on JSON marshal failure (very rare).
		// Fall back to hashing the raw body so we still produce something.
		return hashRaw(body), nil
	}

	// Step 2: extract cacheable prefix bytes (everything except tail turns).
	prefixBytes, truncated, ok := extractCacheablePrefix(stabilized, opts.TailTurns)
	if !ok {
		// Body wasn't a chat-completions shape; hash the raw bytes as a
		// fallback so we still produce a deterministic key.
		return hashRaw(stabilized), nil
	}
	if len(prefixBytes) == 0 {
		// Single message or all-tail — nothing stable to hash.
		return KeyResult{Truncated: truncated}, nil
	}

	// Step 3+4: SHA256 + hex.
	sum := sha256.Sum256(prefixBytes)
	return KeyResult{
		Key:         hex.EncodeToString(sum[:]),
		PrefixBytes: prefixBytes,
		Length:      len(prefixBytes),
		Truncated:   truncated,
	}, nil
}

// hashRaw produces a key from the raw body bytes. Used as a fallback when
// the body isn't a chat-completions shape (non-JSON, no messages field).
// This still gives a deterministic fingerprint; we just can't promise
// tail-exclusion.
func hashRaw(body []byte) KeyResult {
	sum := sha256.Sum256(body)
	return KeyResult{
		Key:         hex.EncodeToString(sum[:]),
		PrefixBytes: body,
		Length:      len(body),
	}
}

// extractCacheablePrefix extracts the cacheable portion of the body:
//   - The messages up to (but not including) the last `TailTurns`
//   - Plus the `tools` array (as-is, in original order — tools are NOT
//     reordered by prefix.Stabilize because reordering them would change
//     agent semantics; but they ARE part of the upstream's cacheable prefix).
//
// The two are concatenated with a length-prefix separator to avoid collisions
// like {tools:[a], messages:[]} vs {tools:[], messages:[a]}.
//
// Returns (prefixBytes, truncated, ok). ok=false means the body isn't a
// recognizable chat-completions shape (caller should fall back to raw hash).
func extractCacheablePrefix(body []byte, tailTurns int) ([]byte, bool, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, false, false
	}
	rawMsgs, hasMsgs := obj["messages"]
	rawTools, hasTools := obj["tools"]

	if !hasMsgs && !hasTools {
		return nil, false, false
	}

	var msgs []json.RawMessage
	truncated := false
	if hasMsgs {
		if err := json.Unmarshal(rawMsgs, &msgs); err != nil {
			return nil, false, false
		}
		cutoff := len(msgs) - tailTurns
		if cutoff < 0 {
			cutoff = 0
		}
		truncated = cutoff < len(msgs)
		msgs = msgs[:cutoff]
	}

	// No cacheable prefix at all (single message, no tools, no history).
	if len(msgs) == 0 && !hasTools {
		return nil, truncated, true
	}

	// Canonicalize: re-marshal so the bytes are stable across input
	// formatting variations (whitespace, key ordering). Use a wrapper map
	// so the JSON ordering is deterministic: tools first, then messages.
	canonical, err := json.Marshal(struct {
		Tools    []json.RawMessage `json:"tools,omitempty"`
		Messages []json.RawMessage `json:"messages,omitempty"`
	}{
		Tools:    toolsList(rawTools),
		Messages: msgs,
	})
	if err != nil {
		return nil, truncated, false
	}
	return canonical, truncated, true
}

// toolsList parses the raw tools field into a slice for canonical marshaling.
// Returns nil if the field is missing or not an array (malformed).
func toolsList(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	return tools
}
