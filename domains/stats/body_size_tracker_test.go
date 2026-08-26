package stats

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// newTestBodySizeTracker spins up an in-process miniredis and returns a
// BodySizeTracker wired to it. Cleans up automatically at test end.
func newTestBodySizeTracker(t *testing.T) (*BodySizeTracker, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: 0})
	t.Cleanup(func() { _ = client.Close() })
	return NewBodySizeTracker(client, nil), server
}

// TestBodySizeTracker_RecordAndGetStats verifies the happy path: recording
// several entries accumulates sum/count correctly and updates the max.
func TestBodySizeTracker_RecordAndGetStats(t *testing.T) {
	tracker, _ := newTestBodySizeTracker(t)
	ctx := context.Background()

	// Record 3 requests with sizes 100, 200, 300
	sizes := []int{100, 200, 300}
	for _, size := range sizes {
		entry := &telemetry.RequestLogEntry{
			RequestID:     "req-" + string(rune(size)),
			RequestBytes:  &[]int{size}[0],
			ResponseBytes: &[]int{size * 2}[0],
		}
		tracker.Record(entry)
	}

	stats, err := tracker.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	// Request: sum=600, count=3, avg=200, max=300
	if stats.AvgRequestBytes != 200 {
		t.Errorf("AvgRequestBytes = %d, want 200", stats.AvgRequestBytes)
	}
	if stats.MaxRequestBytes != 300 {
		t.Errorf("MaxRequestBytes = %d, want 300", stats.MaxRequestBytes)
	}

	// Response: sum=1200, count=3, avg=400, max=600
	if stats.AvgResponseBytes != 400 {
		t.Errorf("AvgResponseBytes = %d, want 400", stats.AvgResponseBytes)
	}
	if stats.MaxResponseBytes != 600 {
		t.Errorf("MaxResponseBytes = %d, want 600", stats.MaxResponseBytes)
	}
}

// TestBodySizeTracker_EmptyStats verifies that GetStats on a fresh Redis
// returns zero values (cold-start state) rather than an error.
func TestBodySizeTracker_EmptyStats(t *testing.T) {
	tracker, _ := newTestBodySizeTracker(t)
	ctx := context.Background()

	stats, err := tracker.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats on empty Redis should not error, got: %v", err)
	}
	if stats.AvgRequestBytes != 0 || stats.MaxRequestBytes != 0 {
		t.Errorf("expected zero stats on cold start, got %+v", stats)
	}
}

// TestBodySizeTracker_NilInputs verifies that Record is a no-op when
// called with nil inputs — important because telemetry hooks fire on
// every request and cannot panic.
func TestBodySizeTracker_NilInputs(t *testing.T) {
	tracker, _ := newTestBodySizeTracker(t)

	// Should not panic
	tracker.Record(nil)

	entry := &telemetry.RequestLogEntry{RequestID: "test"}
	tracker.Record(entry) // no size fields

	stats, err := tracker.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if stats.AvgRequestBytes != 0 {
		t.Errorf("expected zero stats when no sizes recorded, got %d", stats.AvgRequestBytes)
	}
}

// TestBodySizeTracker_ImplausiblyLargeSize verifies that sizes > 1 GiB
// are rejected (logged + skipped) so a bug or attack cannot poison the
// running max with a number that's larger than any real LLM request.
func TestBodySizeTracker_ImplausiblyLargeSize(t *testing.T) {
	tracker, _ := newTestBodySizeTracker(t)

	// 2 GiB — beyond the sanity threshold
	huge := 2 << 30 // 2 GiB
	entry := &telemetry.RequestLogEntry{
		RequestID:    "huge-req",
		RequestBytes: &huge,
	}
	tracker.Record(entry)

	stats, err := tracker.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if stats.MaxRequestBytes != 0 {
		t.Errorf("implausibly large size should be ignored, got max=%d", stats.MaxRequestBytes)
	}
}

// TestBodySizeTracker_NilRedisClient verifies the constructor handles a
// nil Redis client gracefully — Record becomes a no-op and GetStats
// returns an error rather than panicking.
func TestBodySizeTracker_NilRedisClient(t *testing.T) {
	tracker := NewBodySizeTracker(nil, nil)

	// Should not panic
	tracker.Record(&telemetry.RequestLogEntry{RequestID: "x"})

	if _, err := tracker.GetStats(context.Background()); err == nil {
		t.Error("expected error from GetStats with nil Redis client")
	}
}