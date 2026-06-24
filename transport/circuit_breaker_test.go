package transport

import (
	"testing"
	"time"
)

func TestStreamCircuitBreaker_StartsClosed(t *testing.T) {
	cb := NewStreamCircuitBreaker()
	if cb.State() != CircuitClosed {
		t.Fatalf("initial state = %s, want closed", cb.State())
	}
	if cb.ShouldFallback() {
		t.Fatal("ShouldFallback should be false when closed")
	}
}

func TestStreamCircuitBreaker_TripsOnThreshold(t *testing.T) {
	cb := NewStreamCircuitBreaker()
	// threshold=3：2 次错误不应熔断
	cb.RecordError()
	cb.RecordError()
	if cb.State() != CircuitClosed {
		t.Fatalf("after 2 errors state = %s, want closed", cb.State())
	}
	if cb.ShouldFallback() {
		t.Fatal("ShouldFallback should be false before threshold")
	}

	// 第 3 次错误 → 熔断
	cb.RecordError()
	if cb.State() != CircuitOpen {
		t.Fatalf("after 3 errors state = %s, want open", cb.State())
	}
	if !cb.ShouldFallback() {
		t.Fatal("ShouldFallback should be true when open")
	}
	if cb.TotalTrips() != 1 {
		t.Errorf("TotalTrips = %d, want 1", cb.TotalTrips())
	}
}

func TestStreamCircuitBreaker_TransitionsToHalfOpen(t *testing.T) {
	cb := &StreamCircuitBreaker{
		threshold: 2,
		window:    time.Minute,
		cooldown:  20 * time.Millisecond, // 短冷却便于测试
		state:     CircuitClosed,
	}

	// 熔断
	cb.RecordError()
	cb.RecordError()
	if cb.State() != CircuitOpen {
		t.Fatalf("state = %s, want open", cb.State())
	}

	// 冷却前仍 Open
	time.Sleep(10 * time.Millisecond)
	if cb.State() != CircuitOpen {
		t.Fatalf("before cooldown state = %s, want open", cb.State())
	}

	// 冷却后 → HalfOpen
	time.Sleep(15 * time.Millisecond)
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("after cooldown state = %s, want half-open", cb.State())
	}
}

func TestStreamCircuitBreaker_HalfOpenSuccessRestores(t *testing.T) {
	cb := &StreamCircuitBreaker{
		threshold: 1,
		window:    time.Minute,
		cooldown:  10 * time.Millisecond,
		state:     CircuitClosed,
	}

	cb.RecordError() // 熔断
	time.Sleep(12 * time.Millisecond)
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("state = %s, want half-open", cb.State())
	}

	// 半开态成功 → 恢复
	cb.RecordSuccess()
	if cb.State() != CircuitClosed {
		t.Fatalf("after success state = %s, want closed", cb.State())
	}
}

func TestStreamCircuitBreaker_HalfOpenErrorReTrips(t *testing.T) {
	cb := &StreamCircuitBreaker{
		threshold: 1,
		window:    time.Minute,
		cooldown:  10 * time.Millisecond,
		state:     CircuitClosed,
	}

	cb.RecordError() // 熔断
	time.Sleep(12 * time.Millisecond)
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("state = %s, want half-open", cb.State())
	}

	// 半开态失败 → 重新熔断
	cb.RecordError()
	if cb.State() != CircuitOpen {
		t.Fatalf("after half-open error state = %s, want open", cb.State())
	}
	if cb.TotalTrips() != 2 {
		t.Errorf("TotalTrips = %d, want 2", cb.TotalTrips())
	}
}

func TestStreamCircuitBreaker_WindowPruning(t *testing.T) {
	cb := &StreamCircuitBreaker{
		threshold: 2,
		window:    15 * time.Millisecond, // 短窗口
		cooldown:  time.Minute,
		state:     CircuitClosed,
	}

	cb.RecordError()
	time.Sleep(20 * time.Millisecond) // 窗口过期
	cb.RecordError()                  // 新错误，旧的被清理

	if cb.State() != CircuitClosed {
		t.Fatalf("state = %s, want closed (pruned old error)", cb.State())
	}
}

func TestStreamCircuitBreaker_Reset(t *testing.T) {
	cb := &StreamCircuitBreaker{
		threshold: 1,
		window:    time.Minute,
		cooldown:  time.Minute,
		state:     CircuitClosed,
	}
	cb.RecordError()
	if cb.State() != CircuitOpen {
		t.Fatal("should be open")
	}

	cb.Reset()
	if cb.State() != CircuitClosed {
		t.Fatalf("after reset state = %s, want closed", cb.State())
	}
}

func TestCircuitState_String(t *testing.T) {
	tests := []struct {
		state CircuitState
		want  string
	}{
		{CircuitClosed, "closed"},
		{CircuitOpen, "open"},
		{CircuitHalfOpen, "half-open"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("%d.String() = %s, want %s", tt.state, got, tt.want)
		}
	}
}
