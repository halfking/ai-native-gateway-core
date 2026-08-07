// Package tokenest provides the single, canonical character→token estimate
// shared by the compression, window-trigger, and V2 outbound-builder paths.
//
// Context (docs/omni-ref3 C4): the codebase had three divergent heuristics —
// domains/hooks/compression used chars/3.5 (diff.go estimateBodyTokens and
// compressor.go's `*10/35`), while domains/session/v2/outbound_builder.go used
// chars/4 despite its own comment saying "1 token ≈ 3.5 characters". Mixed
// constants mean the compression window trigger and the V2 token estimate can
// disagree on the same body, so a session may be flagged for compression by one
// path and not the other. Consolidating on one function removes that drift.
//
// The constant is CharsPerToken = 3.5, the value already used everywhere except
// the V2 builder. This is a heuristic for mixed English/Chinese content; it is
// intentionally cheap (no tokenizer). A real tiktoken pass is out of scope for
// C4 and would belong in a separate, opt-in estimator.
package tokenest

// CharsPerToken is the canonical chars-per-token ratio for heuristic estimates
// across the session/compression paths. Mixed English/Chinese average.
const CharsPerToken = 3.5

// FromChars returns a heuristic token count for a string of the given byte
// length. It matches the prior `len(body)/3.5` semantics exactly, so callers
// that previously divided by 3.5 are unchanged; the V2 builder (which divided
// by 4) now agrees with everyone else.
//
// Returns 0 for empty/negative input.
func FromChars(numChars int) int {
	if numChars <= 0 {
		return 0
	}
	return int(float64(numChars) / CharsPerToken)
}

// FromString is a convenience wrapper for FromChars(len(s)).
func FromString(s string) int {
	return FromChars(len(s))
}
