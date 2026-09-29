// Package sanitize - threetier_activation_test.go (2026-09-30, 修订审计三十二轮
// §四A A-G1)
//
// Proves the activation blank-import works: a binary that links
// security/sanitize (as cmd/gateway does) ends up with the three-tier
// provenance verifier registered inside compression — i.e. the check is
// armed in production, not only in threetier's own tests.
package sanitize

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

func TestThreetierActivation_RegistersVerifier(t *testing.T) {
	if !compression.ThreeTierCheckRegistered() {
		t.Fatal("three-tier verifier not registered: activation import failed to arm the check")
	}
}
