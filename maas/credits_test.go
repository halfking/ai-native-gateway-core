package maas

import "testing"

func TestCalcCredits_baseRate(t *testing.T) {
	got := CalcCredits(500_000, 500_000, 0, 0, 10000)
	if got != 10000 {
		t.Fatalf("expected 10000, got %d", got)
	}
}

func TestCalcCredits_asymmetricRates(t *testing.T) {
	got := CalcCredits(1_000_000, 0, 8000, 12000, 10000)
	if got != 8000 {
		t.Fatalf("expected 8000, got %d", got)
	}
}

func TestCalcCredits_zeroTokens(t *testing.T) {
	if got := CalcCredits(0, 0, 10000, 10000, 10000); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

func TestCalcCredits_ceil(t *testing.T) {
	// 1 token at 10000/1M => ceil(0.01) = 1
	got := CalcCredits(1, 0, 10000, 10000, 10000)
	if got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}
}

func TestCalcCredits_completionOnly(t *testing.T) {
	got := CalcCredits(0, 2_000_000, 10000, 10000, 10000)
	if got != 20000 {
		t.Fatalf("expected 20000, got %d", got)
	}
}

func TestCalcCredits_negativeBaseUsesDefault(t *testing.T) {
	got := CalcCredits(1_000_000, 0, 0, 0, 0)
	if got != 10000 {
		t.Fatalf("expected default 10000, got %d", got)
	}
}
