package compressor

import (
	"os"
	"testing"
)

func TestEstimator_Fraction_Default(t *testing.T) {
	// Unset env → defaultWindowFraction (0.8 per v7 §2).
	os.Unsetenv("LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION")
	e := NewEstimator()
	if got := e.Fraction(); got != defaultWindowFraction {
		t.Fatalf("Fraction(): want %v, got %v", defaultWindowFraction, got)
	}
}

func TestEstimator_Fraction_FromEnv(t *testing.T) {
	os.Setenv("LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION", "0.75")
	defer os.Unsetenv("LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION")
	e := NewEstimator()
	if got := e.Fraction(); got != 0.75 {
		t.Fatalf("Fraction(): want 0.75, got %v", got)
	}
}

func TestEstimator_Fraction_BadEnvFallsBack(t *testing.T) {
	cases := []string{"abc", "0", "-1", "1.5", "2"}
	for _, bad := range cases {
		os.Setenv("LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION", bad)
		e := NewEstimator()
		if got := e.Fraction(); got != defaultWindowFraction {
			t.Errorf("Fraction() with bad env %q: want fallback %v, got %v",
				bad, defaultWindowFraction, got)
		}
		os.Unsetenv("LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION")
	}
}

func TestEstimator_NilSafety(t *testing.T) {
	// e == nil must not panic. All methods must tolerate the receiver being
	// nil so executor wiring can defer Estimator construction.
	var e *Estimator
	if got := e.Fraction(); got != defaultWindowFraction {
		t.Errorf("nil Fraction(): want %v, got %v", defaultWindowFraction, got)
	}
	if e.NeedsCompression([]byte("hello"), 128000) {
		t.Error("nil NeedsCompression() must not return true for tiny body")
	}
	if got := e.ThresholdBytes(128000); got == 0 {
		t.Error("nil ThresholdBytes(128000) should still return non-zero via fallback fraction")
	}
}

func TestEstimator_NeedsCompression_GateLogic(t *testing.T) {
	os.Unsetenv("LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION")
	e := NewEstimator()
	// Default fraction 0.8 over 128K window = 358400 byte trigger.
	cases := []struct {
		name      string
		bodySize  int
		wantCompress bool
	}{
		{"under_128k_threshold", 100000, false},
		{"just_over_128k_threshold", 358401, true},
		{"well_over_128k_threshold", 500000, true},
		{"empty_body", 0, false},
		{"well_over_128k_threshold_huge", 1000000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := e.NeedsCompression(make([]byte, tc.bodySize), 128000)
			if got != tc.wantCompress {
				t.Errorf("NeedsCompression(size=%d, window=128000): want %v, got %v",
					tc.bodySize, tc.wantCompress, got)
			}
		})
	}
	t.Run("unknown_window_no_trigger_128k", func(t *testing.T) {
		// 1MB body but window=0 → must NOT trigger.
		if e.NeedsCompression(make([]byte, 1_000_000), 0) {
			t.Error("NeedsCompression with window=0 must return false even for huge bodies")
		}
	})
}

func TestEstimator_ThresholdBytes_Dynamic(t *testing.T) {
	os.Unsetenv("LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION")
	e := NewEstimator()
	// 0.8 × 3.5 = 2.8 per token. 128K tokens = 358400 bytes.
	if got, want := e.ThresholdBytes(128000), 358400; got != want {
		t.Errorf("ThresholdBytes(128000): want %d, got %d", want, got)
	}
	if got, want := e.ThresholdBytes(64000), 179200; got != want {
		t.Errorf("ThresholdBytes(64000): want %d, got %d", want, got)
	}
	if got := e.ThresholdBytes(0); got != 0 {
		t.Errorf("ThresholdBytes(0) must be 0, got %d", got)
	}
}
