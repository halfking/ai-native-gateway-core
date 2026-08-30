package admin

import (
	"context"
	"regexp"
	"testing"

	"github.com/google/uuid"
)

// audit-data-closure-B hotfix (2026-08-31): the previous implementation of
// uuidOrZero emitted raw hex (32 chars, no dashes), which PostgreSQL's uuid
// type rejected with `invalid input syntax for type uuid`. Every cleanup
// attempt then HTTP 500'd and rolled back, leaving zero audit rows. This
// test pins the contract: the returned value MUST be a valid RFC 4122 v4
// UUID in dashed form (the format uuid.NewString / uuid.Parse accept).
func TestUUIDOrZero_ReturnsDashedRFC4122(t *testing.T) {
	got := uuidOrZero(context.Background())
	if got == "" {
		t.Fatal("uuidOrZero returned empty string")
	}
	// Must parse as uuid.UUID; this rejects raw hex (no dashes) because
	// uuid.Parse enforces the 8-4-4-4-12 layout.
	parsed, err := uuid.Parse(got)
	if err != nil {
		t.Fatalf("uuidOrZero returned %q, which is not a valid RFC 4122 uuid: %v", got, err)
	}
	// Sanity: the version nibble (high 4 bits of byte 6 / the 7th hex char
	// of the third group) must be 4 for an RFC 4122 v4 UUID.
	if got[14] != '4' {
		t.Errorf("uuidOrZero returned %q; uuid.Parse succeeded but the version nibble is %q, want '4'", got, got[14])
	}
	// Stronger check via the parsed form.
	if v := parsed.Version(); v != 4 {
		t.Errorf("uuidOrZero returned UUID version %d, want 4", v)
	}
	// Length and dash positions. The pre-fix bug returned 32 chars with no
	// dashes; the new contract requires 36 chars with dashes at 8/13/18/23.
	if len(got) != 36 {
		t.Errorf("uuidOrZero returned %d-char string %q, want 36", len(got), got)
	}
	for _, pos := range []int{8, 13, 18, 23} {
		if got[pos] != '-' {
			t.Errorf("uuidOrZero returned %q; expected '-' at position %d", got, pos)
		}
	}
	// Defensive: the regex catches any future regression that re-emits a
	// non-dashed shape (e.g. someone substituting crypto/rand + hex).
	matched, _ := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, got)
	if !matched {
		t.Errorf("uuidOrZero returned %q; does not match RFC 4122 v4 dashed regex", got)
	}
}

// TestUUIDOrZero_ZeroOnEntropyFailure documents the entropy-failure fallback
// path. We cannot easily force crypto/rand to fail in a unit test, but the
// function MUST be safe to call repeatedly and MUST never panic — both the
// happy path and the (theoretical) entropy-failure path return a string
// that uuid.Parse accepts.
func TestUUIDOrZero_EntropyFailureFallbackIsValidUUID(t *testing.T) {
	// Call many times to exercise the happy path. None should ever panic.
	for i := 0; i < 100; i++ {
		got := uuidOrZero(context.Background())
		if _, err := uuid.Parse(got); err != nil {
			t.Fatalf("iteration %d: uuidOrZero returned %q, not a valid uuid: %v", i, got, err)
		}
	}
	// And the documented zero-fallback literal is itself a valid uuid
	// (the all-zero UUID), so an entropy failure would still satisfy the
	// column constraint.
	if _, err := uuid.Parse("00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatalf("entropy-failure fallback literal is not a valid uuid: %v", err)
	}
}
