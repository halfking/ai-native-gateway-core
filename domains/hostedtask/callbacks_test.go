package hostedtask

import (
	"testing"
	"time"
)

// 矩阵 E：回调退避 30s ×2 封顶 1h。
func TestCallbackBackoffCapped(t *testing.T) {
	if got := CallbackBackoff(0); got != 30*time.Second {
		t.Errorf("first backoff = %v", got)
	}
	if got := CallbackBackoff(1); got != time.Minute {
		t.Errorf("second backoff = %v", got)
	}
	if got := CallbackBackoff(20); got != time.Hour {
		t.Errorf("backoff must cap at 1h, got %v", got)
	}
}
