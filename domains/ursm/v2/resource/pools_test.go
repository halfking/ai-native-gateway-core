package resource

import "testing"

func TestLocalConcurrencyAcquireRelease(t *testing.T) {
	lim := NewLocalConcurrency(2)
	if _, err := lim.Acquire("c1"); err != nil {
		t.Fatalf("acquire1: %v", err)
	}
	if _, err := lim.Acquire("c1"); err != nil {
		t.Fatalf("acquire2: %v", err)
	}
	if _, err := lim.Acquire("c1"); err == nil {
		t.Fatalf("acquire3 must fail when saturated")
	}
	if err := lim.Release("c1", Token{}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := lim.Acquire("c1"); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}
