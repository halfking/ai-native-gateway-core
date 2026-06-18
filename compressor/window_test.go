// Package compressor - window_test.go (v3 T26 unit tests)
package compressor

import (
	"testing"
	"time"
)

func makeState(msgCount, tokenEst int, lastCompressedAt, recentlyCompressedAt int64) *SessionState {
	return &SessionState{
		SchemaVersion:        1,
		MsgCount:             msgCount,
		TokenEstimate:        tokenEst,
		LastCompressedAt:     lastCompressedAt,
		RecentlyCompressedAt: recentlyCompressedAt,
	}
}

// trigger-1: token threshold
func TestWindow_TokenTrigger(t *testing.T) {
	// contextWindow=1000 tokens, threshold = 1000 * 0.85 * 3.5 = 2975 chars
	// body is 3000 chars → should trigger
	body := make([]byte, 3000)
	state := makeState(5, 850, 0, 0)
	res := ShouldTriggerWindow(body, state, 1000, false, time.Now())
	if !res.ShouldTrigger {
		t.Errorf("expected ShouldTrigger=true for token trigger, got reason=%q", res.Reason)
	}
	if res.Reason != "sliding_window_token" {
		t.Errorf("expected reason=sliding_window_token, got %q", res.Reason)
	}
}

// trigger-2: message count
func TestWindow_CountTrigger(t *testing.T) {
	body := make([]byte, 100) // small body
	state := makeState(DefaultMaxMsgCount, 100, 0, 0)
	res := ShouldTriggerWindow(body, state, 0, false, time.Now())
	if !res.ShouldTrigger {
		t.Errorf("expected ShouldTrigger=true for count trigger")
	}
	if res.Reason != "sliding_window_count" {
		t.Errorf("expected reason=sliding_window_count, got %q", res.Reason)
	}
}

// trigger-3: idle
func TestWindow_IdleTrigger(t *testing.T) {
	now := time.Now()
	oldCompress := now.Unix() - int64(DefaultIdleSeconds) - 1
	state := makeState(DefaultMinIdleMsgCount, 100, oldCompress, 0)
	body := make([]byte, 100)
	res := ShouldTriggerWindow(body, state, 0, false, now)
	if !res.ShouldTrigger {
		t.Errorf("expected ShouldTrigger=true for idle trigger")
	}
	if res.Reason != "sliding_window_idle" {
		t.Errorf("expected reason=sliding_window_idle, got %q", res.Reason)
	}
}

// trigger-4: mutual exclusion (recently compressed)
func TestWindow_MutualExclusion(t *testing.T) {
	now := time.Now()
	recent := now.Unix() - 10 // compressed 10s ago → inside 60s guard
	state := makeState(DefaultMaxMsgCount, 100, 0, recent)
	body := make([]byte, 100)
	res := ShouldTriggerWindow(body, state, 0, false, now)
	if res.ShouldTrigger {
		t.Error("expected ShouldTrigger=false inside mutual-exclusion window")
	}
	if !res.Degraded {
		t.Error("expected Degraded=true inside mutual-exclusion window")
	}
}

// trigger-5: stream already started → SkipStream
func TestWindow_StreamStarted(t *testing.T) {
	state := makeState(DefaultMaxMsgCount, 100, 0, 0)
	body := make([]byte, 10000)
	res := ShouldTriggerWindow(body, state, 1000, true, time.Now())
	if !res.SkipStream {
		t.Error("expected SkipStream=true when stream already started")
	}
	if res.ShouldTrigger {
		t.Error("expected ShouldTrigger=false when stream started")
	}
}

// trigger-6: nil state → never trigger
func TestWindow_NilState(t *testing.T) {
	body := make([]byte, 100000)
	res := ShouldTriggerWindow(body, nil, 1000, false, time.Now())
	if res.ShouldTrigger {
		t.Error("expected ShouldTrigger=false for nil state")
	}
}

// trigger-7: boundary — exactly at token threshold → should NOT trigger
func TestWindow_TokenThresholdExact(t *testing.T) {
	// threshold = 1000 * 0.85 * 3.5 = 2975
	body := make([]byte, 2975)
	state := makeState(5, 850, 0, 0)
	res := ShouldTriggerWindow(body, state, 1000, false, time.Now())
	if res.Reason == "sliding_window_token" {
		t.Error("expected no token trigger at exactly threshold bytes")
	}
}
