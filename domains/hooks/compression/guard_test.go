// Package compressor - guard_test.go (rtk borrowing, 2026-07-06)
//
// Tests for NeverWorse, the rtk-inspired safety contract that a transform
// must never emit more bytes than the raw input.

package compression

import (
	"bytes"
	"testing"
)

func TestNeverWorse_TransformShrinks_ReturnsProcessed(t *testing.T) {
	raw := []byte("hello world this is a long raw body")
	processed := []byte("short")
	ResetGuardMetrics()

	out, regressed := NeverWorse(raw, processed, GuardStageCompress)
	if regressed {
		t.Fatalf("expected regressed=false, got true")
	}
	if !bytes.Equal(out, processed) {
		t.Fatalf("expected processed body returned, got %q", out)
	}
	if c := GuardRegressedCount(GuardStageCompress); c != 0 {
		t.Fatalf("expected 0 regressions, got %v", c)
	}
}

func TestNeverWorse_TransformGrows_RevertsToRaw(t *testing.T) {
	// processed is LONGER than raw → regression.
	raw := []byte("short")
	processed := []byte("this is much longer than the raw body so it regressed")
	ResetGuardMetrics()

	out, regressed := NeverWorse(raw, processed, GuardStageCompress)
	if !regressed {
		t.Fatalf("expected regressed=true for grown body, got false")
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("expected raw body returned on regression, got %q", out)
	}
	if c := GuardRegressedCount(GuardStageCompress); c != 1 {
		t.Fatalf("expected 1 regression recorded, got %v", c)
	}
}

func TestNeverWorse_EqualLength_RevertsToRaw(t *testing.T) {
	// Equal length is the pessimistic case: the transform did NO useful work
	// (no shrinkage), so we skip it to avoid paying re-serialization cost.
	raw := []byte("1234567890")
	processed := []byte("abcdefghij") // same length
	ResetGuardMetrics()

	out, regressed := NeverWorse(raw, processed, GuardStageStabilize)
	if !regressed {
		t.Fatalf("expected regressed=true for equal-length body (>= contract), got false")
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("expected raw body returned, got %q", out)
	}
	if c := GuardRegressedCount(GuardStageStabilize); c != 1 {
		t.Fatalf("expected 1 stabilize regression, got %v", c)
	}
}

func TestNeverWorse_EmptyRaw_ReturnsProcessed(t *testing.T) {
	// Nothing to guard against → forward processed unchanged.
	out, regressed := NeverWorse([]byte{}, []byte("something"), GuardStageInject)
	if regressed {
		t.Fatalf("expected regressed=false for empty raw, got true")
	}
	if string(out) != "something" {
		t.Fatalf("expected processed returned, got %q", out)
	}
}

func TestNeverWorse_EmptyProcessed_ReturnsProcessed(t *testing.T) {
	out, regressed := NeverWorse([]byte("raw"), []byte{}, GuardStageCompress)
	if regressed {
		t.Fatalf("expected regressed=false for empty processed, got true")
	}
	if len(out) != 0 {
		t.Fatalf("expected empty processed returned, got %q", out)
	}
}

func TestNeverWorse_NilProcessed_ReturnsNil(t *testing.T) {
	out, regressed := NeverWorse([]byte("raw"), nil, GuardStageCompress)
	if regressed {
		t.Fatalf("expected regressed=false for nil processed, got true")
	}
	if out != nil {
		t.Fatalf("expected nil returned, got %v", out)
	}
}

func TestNeverWorse_StageLabelRecordedSeparately(t *testing.T) {
	// Regressions for different stages are tracked on separate label series.
	ResetGuardMetrics()
	_, _ = NeverWorse([]byte("raw1"), []byte("processed-longer-than-raw1"), GuardStageCompress)
	_, _ = NeverWorse([]byte("raw2"), []byte("processed-longer-than-raw2"), GuardStageStabilize)
	_, _ = NeverWorse([]byte("raw3"), []byte("processed-longer-than-raw3"), GuardStageInject)

	if c := GuardRegressedCount(GuardStageCompress); c != 1 {
		t.Fatalf("compress: expected 1, got %v", c)
	}
	if c := GuardRegressedCount(GuardStageStabilize); c != 1 {
		t.Fatalf("stabilize: expected 1, got %v", c)
	}
	if c := GuardRegressedCount(GuardStageInject); c != 1 {
		t.Fatalf("inject: expected 1, got %v", c)
	}
}

func TestNeverWorse_OneByteShorter_Kept(t *testing.T) {
	// Boundary: exactly one byte shorter is a valid shrink → kept.
	raw := []byte("1234567890")
	processed := []byte("123456789")
	out, regressed := NeverWorse(raw, processed, GuardStageCompress)
	if regressed {
		t.Fatalf("expected regressed=false for 1-byte shrink, got true")
	}
	if string(out) != "123456789" {
		t.Fatalf("expected processed kept, got %q", out)
	}
}
