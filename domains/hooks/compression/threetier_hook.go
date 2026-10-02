// Package compressor - threetier_hook.go (2026-09-30, 修订审计三十二轮 §四A A-G1)
//
// Three-tier provenance consistency check — production wiring seam.
//
// domains/hooks/compression/threetier implements the actual validation
// (DetectMisalignment over the raw/compressed/sanitized tiers) but CANNOT be
// imported by this package: threetier imports compression (its API operates
// on *SessionState), so a direct back-import is a cycle. The same constraint
// already forced sanitize_info.go to mirror security/sanitize helpers instead
// of importing them.
//
// The dependency is inverted here: threetier registers its verifier via
// SetThreeTierCheck at init() time (threetier/register.go), and
// SessionCache.Set — the single chokepoint where the merged three-tier state
// is persisted and the "algn" / "san_msg_refs" fields get serialised —
// invokes it via runThreeTierCheck. The registration is pulled into the
// gateway binary by security/sanitize's activation import
// (threetier_activation.go; cmd/gateway/main.go imports security/sanitize).
//
// Failure semantics (A-G1 hard requirement): a failed check NEVER blocks the
// request or the cache write. Misalignments are emitted as a structured
// slog warning and counted in compression_threetier_check_total{result} so
// operators can alert on cross-tier drift without paying availability.
package compression

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// ThreeTierCheckFn validates the three-tier provenance of a session state
// (raw → compressed → sanitized) and returns one human-readable string per
// misalignment; nil/empty means the tiers are consistent. Implemented by
// threetier.VerifySessionState (registered at init).
type ThreeTierCheckFn func(s *SessionState) []string

var (
	threeTierMu sync.RWMutex
	threeTierFn ThreeTierCheckFn
)

// SetThreeTierCheck registers (or, with nil, clears) the three-tier verifier.
// Intended for init()-time registration; tests may swap it, hence the lock.
func SetThreeTierCheck(fn ThreeTierCheckFn) {
	threeTierMu.Lock()
	threeTierFn = fn
	threeTierMu.Unlock()
}

// ThreeTierCheckRegistered reports whether a verifier is currently wired.
// Used by tests to prove the production binary actually activates the check.
func ThreeTierCheckRegistered() bool {
	threeTierMu.RLock()
	defer threeTierMu.RUnlock()
	return threeTierFn != nil
}

// runThreeTierCheck executes the registered verifier against the state that
// is about to be persisted. Observability only: a misalignment is logged and
// counted, never surfaced to the caller as an error, so a provenance
// inconsistency can neither fail nor delay the request it was detected on.
func runThreeTierCheck(ctx context.Context, tenantID, gwSessionID string, state *SessionState) {
	if state == nil {
		return
	}
	threeTierMu.RLock()
	fn := threeTierFn
	threeTierMu.RUnlock()
	if fn == nil {
		RecordThreeTierCheck(ThreeTierResultUnregistered)
		return
	}
	reasons := fn(state)
	if len(reasons) == 0 {
		RecordThreeTierCheck(ThreeTierResultPass)
		return
	}
	RecordThreeTierCheck(ThreeTierResultFail)
	slog.WarnContext(ctx, "session_cache: three-tier provenance misaligned",
		"session", gwSessionID, "tenant", tenantID,
		"misalignments", strings.Join(reasons, "; "))
}
