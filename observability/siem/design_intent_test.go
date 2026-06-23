package siem

import (
	"os"
	"strings"
	"testing"
)

// design_intent_test.go guards the NON-NEGOTIABLE invariants of the siem
// package. These are security/compliance guardrails, not behavior tests.

// TestNoContentInCEF_Documented: the package doc MUST state that CEF lines
// never include prompt/completion content.
func TestNoContentInCEF_Documented(t *testing.T) {
	src := mustRead(t, "siem.go")
	if !strings.Contains(src, "NEVER include prompt/completion content") {
		t.Error("siem.go must document that CEF never includes content")
	}
}

// TestDefaultIsLocalFile_Documented: the safe-default invariant (local file,
// never remote URL) must be documented.
func TestDefaultIsLocalFile_Documented(t *testing.T) {
	src := mustRead(t, "siem.go")
	if !strings.Contains(src, "local file, never a remote URL") && !strings.Contains(src, "never a remote URL") {
		t.Error("siem.go must document the local-file default invariant")
	}
}

// TestNoAuditImport_Documented: the anti-corruption layer (no audit import)
// must be documented so nobody later couples siem/ to audit/.
func TestNoAuditImport_Documented(t *testing.T) {
	src := mustRead(t, "siem.go")
	if !strings.Contains(src, "does NOT import the audit package") {
		t.Error("siem.go must document the anti-corruption (no audit import) rule")
	}
}

// TestVendorProductStable_Documented: CEFVendor/CEFProduct are SIEM-parser
// keys; changing them breaks downstream. Document the stability contract.
func TestVendorProductStable_Documented(t *testing.T) {
	src := mustRead(t, "siem.go")
	if !strings.Contains(src, "treat as stable") && !strings.Contains(src, "invalidates downstream") {
		t.Error("siem.go must document that Vendor/Product are stable SIEM keys")
	}
}

// TestDomainBoundary_Documented: OWNS / does NOT own list.
func TestDomainBoundary_Documented(t *testing.T) {
	src := mustRead(t, "siem.go")
	if !strings.Contains(src, "OWNS") || !strings.Contains(src, "does NOT own") {
		t.Error("siem.go must document the domain boundary")
	}
}

// TestNoAuditImport_Actual: grep guard — the source must not import audit.
func TestNoAuditImport_Actual(t *testing.T) {
	src := mustRead(t, "siem.go")
	if strings.Contains(src, "llm-gateway-go/audit") {
		t.Error("siem.go must NOT import the audit package (anti-corruption)")
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("cannot read %s: %v", name, err)
	}
	return string(b)
}
