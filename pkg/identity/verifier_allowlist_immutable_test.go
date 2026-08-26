package identity

import (
	"reflect"
	"testing"
)

// TestDefaultIssuerAllowlist_Immutable locks the F6 hardening: callers must
// not be able to mutate the shared state by writing into the returned slice.
// Each invocation returns a fresh slice and contains the canonical 5 issuers
// in the canonical order, with `asm` deliberately absent.
func TestDefaultIssuerAllowlist_Immutable(t *testing.T) {
	first := DefaultIssuerAllowlist()
	if want := []string{"redclaw", "memora", "llm-gateway", "pocket", "acc"}; !reflect.DeepEqual(first, want) {
		t.Fatalf("DefaultIssuerAllowlist() = %v, want %v", first, want)
	}

	// Mutate the first slice — the second call must be unaffected.
	for i := range first {
		first[i] = "TAMPERED"
	}
	first = append(first, "asm")

	second := DefaultIssuerAllowlist()
	if want := []string{"redclaw", "memora", "llm-gateway", "pocket", "acc"}; !reflect.DeepEqual(second, want) {
		t.Fatalf("DefaultIssuerAllowlist() after mutation = %v, want %v", second, want)
	}

	// asm must never leak into the canonical list.
	for _, iss := range second {
		if iss == "asm" {
			t.Fatalf("DefaultIssuerAllowlist() must not include %q", iss)
		}
	}

	// N invocations all share the same elements.
	for i := 0; i < 5; i++ {
		got := DefaultIssuerAllowlist()
		if !reflect.DeepEqual(got, second) {
			t.Fatalf("call #%d: DefaultIssuerAllowlist() = %v, want %v", i, got, second)
		}
	}
}

// TestDefaultIssuerAllowlist_IndependentBacking ensures each call returns a
// distinct backing array. Without this, a caller could mutate a future call's
// result via the previous one.
func TestDefaultIssuerAllowlist_IndependentBacking(t *testing.T) {
	a := DefaultIssuerAllowlist()
	b := DefaultIssuerAllowlist()
	if &a[0] == &b[0] {
		t.Fatalf("DefaultIssuerAllowlist() must return a fresh slice each call (same backing array %p)", &a[0])
	}
}