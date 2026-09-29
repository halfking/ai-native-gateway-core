// Package threetier - register.go (2026-09-30, 修订审计三十二轮 §四A A-G1)
//
// Production wiring for the three-tier consistency check.
//
// threetier imports compression (DetectMisalignment operates on
// *compression.SessionState), so compression cannot import threetier back —
// a direct call from SessionCache.Set would be an import cycle. The seam is
// inverted instead: this init() pushes VerifySessionState into compression
// via compression.SetThreeTierCheck, and compression.SessionCache.Set — the
// single chokepoint where the merged three-tier state is serialised into
// the "algn"/"san_msg_refs" fields — invokes it. security/sanitize
// (imported by cmd/gateway/main.go) blank-imports this package
// (threetier_activation.go), so the registration runs in every production
// gateway process.
package threetier

import (
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

func init() {
	compression.SetThreeTierCheck(VerifySessionState)
}

// VerifySessionState adapts DetectMisalignment to the compression-side hook
// signature (compression.ThreeTierCheckFn): it returns one human-readable
// string per misalignment (see Misalignment.String), or nil when all three
// tiers are consistent. Pure function — no logging or metrics here; the
// compression side owns observability so failures stay non-blocking.
func VerifySessionState(s *compression.SessionState) []string {
	mis := DetectMisalignment(s)
	if len(mis) == 0 {
		return nil
	}
	out := make([]string, 0, len(mis))
	for _, m := range mis {
		out = append(out, m.String())
	}
	return out
}
