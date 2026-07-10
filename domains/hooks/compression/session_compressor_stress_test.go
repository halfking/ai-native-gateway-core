package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionCompressorConcurrentSessionsPreserveOrderedState(t *testing.T) {
	t.Setenv("LLM_GATEWAY_WINDOW_MAX_MSG_COUNT", "8")

	cache := NewSessionCache(nil, nil)
	compressor := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
	const sessions = 24
	const turns = 12

	before := runtime.NumGoroutine()
	var deltaAppends atomic.Int64
	var trims atomic.Int64
	var failures atomic.Int64
	var wg sync.WaitGroup

	for session := 0; session < sessions; session++ {
		wg.Add(1)
		go func(session int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("gw_%08d-0000-4000-8000-000000000000", session)
			messages := []map[string]string{{"role": "system", "content": "System prompt."}}
			for turn := 0; turn < turns; turn++ {
				messages = append(messages,
					map[string]string{"role": "user", "content": fmt.Sprintf("turn %d: %s", turn, strings.Repeat("user context ", 450))},
					map[string]string{"role": "assistant", "content": fmt.Sprintf("turn %d: %s", turn, strings.Repeat("assistant context ", 350))},
				)
				body, err := json.Marshal(map[string]any{"model": "test", "messages": messages})
				if err != nil {
					failures.Add(1)
					return
				}
				// First verify the cache-backed delta path. Then lower the window
				// to exercise sliding-window fallback against the same session.
				contextWindow := 100_000
				if turn >= 2 {
					contextWindow = 512
				}
				result := compressor.Prepare(context.Background(), body, "stress", sessionID, "openai", contextWindow, false)
				switch result.CompressionStrategy {
				case "delta_append":
					deltaAppends.Add(1)
				case "mechanical_trim":
					trims.Add(1)
				}
				if result.MsgCount == 0 || result.TokenEst == 0 {
					failures.Add(1)
					return
				}
			}
		}(session)
	}
	wg.Wait()

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if failures.Load() != 0 {
		t.Fatalf("%d session replays returned invalid compression metadata", failures.Load())
	}
	if deltaAppends.Load() == 0 {
		t.Fatal("expected delta_append after the first turn in at least one session")
	}
	if trims.Load() == 0 {
		t.Fatal("expected mechanical_trim when the sliding window is exceeded")
	}
	if after > before+4 {
		t.Fatalf("goroutine count grew from %d to %d after workers completed", before, after)
	}
}
