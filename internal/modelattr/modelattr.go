// Package modelattr holds the single definition of the gateway's
// "unknown model" sentinel.
//
// # Why one package for one constant
//
// The gateway stamps a request whose model cannot be determined with a
// literal string. That literal then flows into model columns
// (session_turns.model, session_turn_details.client_model,
// usage_ledger_hot.raw_model_name) and from there into every aggregation
// that groups by model. If two call sites spell the sentinel differently, or
// if a read-side query fails to recognise it, the sentinel stops meaning
// "no model" and starts meaning "a model literally named <unknown>" — which
// is exactly the bug this package exists to prevent.
//
// 211 production, 2026-11-24 audit: 1,320 failure rows carried
// client_model = '<unknown>' while a legitimate SQL-side COALESCE fallback
// produced a *second*, differently-spelled bucket called 'unknown'. The
// model leaderboard therefore showed two phantom models where there should
// have been one "no model" bucket.
//
// # The two sentinels
//
// UnknownPlaceholder is the write-side marker: "the gateway looked and there
// was no model to attribute". It is deliberately distinct from the empty
// string so that "we looked and found nothing" stays distinguishable from
// "we never looked". Every write site must use this constant rather than an
// inline literal — the regression gate
// TestInlineUnknownPlaceholderLiteralIsCentralized in
// internal/modelattribution counts the inline literals and goes red when one
// appears outside this package (code-level literals only; SQL string
// literals inside COALESCE/NULLIF chains are governed by the SQL-chain
// ratchet in the same package, and comment lines are ignored).
//
// UnknownBucket is the read-side bucket: "no model could be attributed". It
// is what every SQL aggregation should group into when nothing else is known.
// The read side is SQL, so this constant is documentation rather than code —
// but it is the single place where the bucket's spelling is defined, and the
// gate in internal/modelattribution asserts the SQL literals match it.
//
// # Why the read side is SQL, not a Go helper
//
// A Go helper that emits the COALESCE chain would hide the model columns from
// admin/canonical_read_points_registry_test.go, which analyses SQL by walking
// string literals and their `+` concatenation chains. A function call in the
// chain becomes a "hole" in that analysis, so the registry would stop seeing
// which columns a read point touches. The chains therefore stay as inline SQL
// literals, and internal/modelattribution pins their shape.
package modelattr

// UnknownPlaceholder is the write-side marker for "no model could be
// attributed".
//
// It is a literal string, so any SQL that treats model columns as opaque text
// will see it as a model named "<unknown>". That is why every read-side
// aggregation must pair it with a NULLIF(x, '<unknown>') fold — see
// UnknownBucket and the gate in internal/modelattribution.
const UnknownPlaceholder = "<unknown>"

// UnknownBucket is the read-side bucket for "no model could be attributed".
//
// Every aggregation that groups by model should collapse both the empty
// string and UnknownPlaceholder into this single bucket:
//
//	COALESCE(..., NULLIF(NULLIF(TRIM(col), ''), '<unknown>'), 'unknown')
//
// Folding only the empty string (NULLIF(col, ”)) lets the placeholder
// through as a phantom model — the 211 production bug this package prevents.
const UnknownBucket = "unknown"
