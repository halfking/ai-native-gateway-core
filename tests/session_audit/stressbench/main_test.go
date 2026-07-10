package main

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

func TestRunConcurrentDoesNotLeakGoroutines(t *testing.T) {
	detector := sessionaudit.NewFastDetector(sessionaudit.DefaultDetectorConfig())
	var s stats
	s.minLatencyNS.Store(int64(^uint64(0) >> 1))
	start := time.Now()
	run(context.Background(), detector, []testCase{{"safe", "safe"}, {"risk", "jailbreak"}}, 8, 1, 10, &s)
	if s.total.Load() == 0 || s.failed.Load() != 0 {
		t.Fatalf("unexpected run stats: total=%d failed=%d", s.total.Load(), s.failed.Load())
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("short stress run exceeded expected duration")
	}
}
