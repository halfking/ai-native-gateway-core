// Package compressor - lossiness_test.go (rtk borrowing, 2026-07-06)
//
// Tests for classifyLossiness, the rtk-inspired recoverability classifier
// that maps a compression strategy to a Lossiness class (none|tail|whole).

package compression

import (
	"testing"
)

func TestClassifyLossiness_NoStrategy_None(t *testing.T) {
	if got := classifyLossiness("", ""); got != LossinessNone {
		t.Fatalf("empty strategy: want none, got %s", got)
	}
}

func TestClassifyLossiness_DeltaAppend_None(t *testing.T) {
	// Delta-append only adds new turns; nothing is lost.
	if got := classifyLossiness("delta_append", ""); got != LossinessNone {
		t.Fatalf("delta_append: want none, got %s", got)
	}
}

func TestClassifyLossiness_MechanicalTrim_Tail(t *testing.T) {
	// Mechanical trim drops the middle but it stays recoverable in the cache.
	if got := classifyLossiness("mechanical_trim", ""); got != LossinessTail {
		t.Fatalf("mechanical_trim: want tail, got %s", got)
	}
}

func TestClassifyLossiness_SlidingWindowWithSummary_Whole(t *testing.T) {
	// An LLM summary replaced the history → wording is NOT recoverable.
	if got := classifyLossiness("sliding_window_msg_count", "smm_v1:abc123"); got != LossinessWhole {
		t.Fatalf("sliding_window + summary: want whole, got %s", got)
	}
}

func TestClassifyLossiness_SlidingWindowNoSummary_Tail(t *testing.T) {
	// Sliding-window trigger that fell back to trim (no marker) → recoverable.
	if got := classifyLossiness("sliding_window_token", ""); got != LossinessTail {
		t.Fatalf("sliding_window without summary: want tail, got %s", got)
	}
}

func TestClassifyLossiness_UnknownStrategyWithSummary_Whole(t *testing.T) {
	// Conservative default: unknown strategy + summary marker → whole.
	if got := classifyLossiness("future_strategy", "smm_v1:xyz"); got != LossinessWhole {
		t.Fatalf("unknown + summary: want whole (conservative), got %s", got)
	}
}

func TestClassifyLossiness_UnknownStrategyNoSummary_Tail(t *testing.T) {
	// Conservative default: unknown strategy, no marker → tail.
	if got := classifyLossiness("future_strategy", ""); got != LossinessTail {
		t.Fatalf("unknown no summary: want tail (conservative), got %s", got)
	}
}

func TestRecordLossiness_IncrementsCounter(t *testing.T) {
	ResetMetrics()
	RecordLossiness(LossinessNone)
	RecordLossiness(LossinessTail)
	RecordLossiness(LossinessTail)
	RecordLossiness(LossinessWhole)

	if c := LossinessCount(LossinessNone); c != 1 {
		t.Fatalf("none: want 1, got %v", c)
	}
	if c := LossinessCount(LossinessTail); c != 2 {
		t.Fatalf("tail: want 2, got %v", c)
	}
	if c := LossinessCount(LossinessWhole); c != 1 {
		t.Fatalf("whole: want 1, got %v", c)
	}
}
