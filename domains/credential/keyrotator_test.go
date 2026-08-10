package credential

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestKeyRotator_SingleKeyUnregistered(t *testing.T) {
	kr := NewKeyRotator()
	// credential not registered → always primary (0)
	if idx := kr.ResolveKey(1, -1); idx != 0 {
		t.Fatalf("unregistered credential: ResolveKey = %d, want 0", idx)
	}
	if kr.AllKeysInvalid(1) {
		t.Fatal("unregistered credential should not be AllKeysInvalid")
	}
}

func TestKeyRotator_RoundRobin(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(10, 3)
	// first three resolves should cycle 0,1,2 (or a rotation thereof)
	seen := map[int]int{}
	for i := 0; i < 6; i++ {
		idx := kr.ResolveKey(10, -1)
		seen[idx]++
	}
	if len(seen) != 3 {
		t.Fatalf("round-robin over 3 keys hit only %d distinct keys: %v", len(seen), seen)
	}
	for idx, c := range seen {
		if c != 2 {
			t.Errorf("key %d resolved %d times, want 2 (even distribution)", idx, c)
		}
	}
}

func TestKeyRotator_SkipInvalid(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(20, 3)
	// mark key 0 invalid via 2 consecutive failures
	kr.RecordKeyFailure(20, 0, errorsx.KindTransient)
	kr.RecordKeyFailure(20, 0, errorsx.KindTransient)
	// key 0 should never be returned now
	for i := 0; i < 10; i++ {
		if idx := kr.ResolveKey(20, -1); idx == 0 {
			t.Fatal("invalid key 0 was returned by ResolveKey")
		}
	}
	if kr.AllKeysInvalid(20) {
		t.Fatal("not all keys invalid yet (1,2 still active)")
	}
}

func TestKeyRotator_TerminalImmediate(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(30, 2)
	// 402 balance → terminal immediately, no threshold
	changed := kr.RecordKeyFailure(30, 0, errorsx.KindQuotaPermanent)
	if !changed {
		t.Fatal("terminal failure should report status change")
	}
	// key 0 skipped
	for i := 0; i < 5; i++ {
		if idx := kr.ResolveKey(30, -1); idx == 0 {
			t.Fatal("terminal key 0 was returned")
		}
	}
}

func TestKeyRotator_AllInvalid(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(40, 3)
	for i := 0; i < 3; i++ {
		kr.RecordKeyFailure(40, i, errorsx.KindQuotaPermanent) // terminal
	}
	if !kr.AllKeysInvalid(40) {
		t.Fatal("expected AllKeysInvalid after all 3 keys terminal")
	}
	// ResolveKey returns -1
	if idx := kr.ResolveKey(40, -1); idx != -1 {
		t.Fatalf("ResolveKey when all invalid = %d, want -1", idx)
	}
}

func TestKeyRotator_StickyReused(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(50, 3)
	// sticky key 1 should be reused
	if idx := kr.ResolveKey(50, 1); idx != 1 {
		t.Fatalf("sticky key 1: ResolveKey = %d, want 1", idx)
	}
	if idx := kr.ResolveKey(50, 1); idx != 1 {
		t.Fatalf("sticky key 1 second call: ResolveKey = %d, want 1", idx)
	}
	// sticky key that became invalid → fallback to round-robin
	kr.RecordKeyFailure(50, 1, errorsx.KindQuotaPermanent)
	idx := kr.ResolveKey(50, 1)
	if idx == 1 {
		t.Fatal("sticky key 1 is terminal but was still returned")
	}
}

func TestKeyRotator_SuccessClears(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(60, 2)
	kr.RecordKeyFailure(60, 0, errorsx.KindTransient) // 1 failure → warning
	kr.RecordKeySuccess(60, 0)
	// key 0 should be active again and eligible
	found := false
	for i := 0; i < 10; i++ {
		if kr.ResolveKey(60, -1) == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("key 0 not eligible after RecordKeySuccess")
	}
}

func TestKeyRotator_ResetKey(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(70, 2)
	kr.RecordKeyFailure(70, 0, errorsx.KindQuotaPermanent) // terminal
	kr.ResetKey(70, 0)
	found := false
	for i := 0; i < 10; i++ {
		if kr.ResolveKey(70, -1) == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("key 0 not eligible after ResetKey")
	}
}

func TestKeyRotator_ResetCredential(t *testing.T) {
	kr := NewKeyRotator()
	kr.EnsureCred(80, 3)
	for i := 0; i < 3; i++ {
		kr.RecordKeyFailure(80, i, errorsx.KindQuotaPermanent)
	}
	if !kr.AllKeysInvalid(80) {
		t.Fatal("expected all keys invalid before reset")
	}

	kr.ResetCredential(80)
	if got := kr.KeyCount(80); got != 0 {
		t.Fatalf("KeyCount after ResetCredential = %d, want 0", got)
	}
	if kr.AllKeysInvalid(80) {
		t.Fatal("ResetCredential should clear all-invalid state")
	}
	if idx := kr.ResolveKey(80, -1); idx != 0 {
		t.Fatalf("unregistered after ResetCredential should resolve primary 0, got %d", idx)
	}
}
