// stress/serialize_p99_test.go
//
// S-02: 50 并发下 IR serialize P99 ≤10ms（mock loopback，不含真实 LLM）。
// 跑测：
//   go test -race -timeout 240s ./tests/48h-audit/D01-ir-lifecycle/stress/...
// 警告：阈值是 mock 下的数字，生产 path 引入网络/调度等不可控因素，需另行基准。

package stress

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

func newReq() *ir.InternalRequest {
	return &ir.InternalRequest{
		Model:    "mock-stress-fast",
		Stream:   false,
		Messages: []ir.Message{{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "stress prompt"}}}},
		MaxTokens: 32,
	}
}

// TestStress_IRSerialize_Concurrency50_P99 50 并发跑 5000 次 OpenAI serialize。
func TestStress_IRSerialize_Concurrency50_P99(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	const (
		concurrency = 50
		total       = 5000
	)
	req := newReq()
	latencies := make([]time.Duration, total)

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	var mu sync.Mutex
	idx := 0
	for i := 0; i < total; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			start := time.Now()
			body, err := ir.SerializeOpenAI(req)
			elapsed := time.Since(start)
			if err != nil || len(body) == 0 {
				t.Errorf("serialize failed: err=%v body_len=%d", err, len(body))
				return
			}
			mu.Lock()
			latencies[idx] = elapsed
			idx++
			mu.Unlock()
		}()
	}
	wg.Wait()
	latencies = latencies[:idx]
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	p50 := latencies[len(latencies)*50/100]
	p95 := latencies[len(latencies)*95/100]
	p99 := latencies[len(latencies)*99/100]
	t.Logf("serialize latency over %d reqs @ conc=%d: p50=%v p95=%v p99=%v",
		len(latencies), concurrency, p50, p95, p99)

	if p99 > 10*time.Millisecond {
		t.Errorf("p99=%v exceeds 10ms threshold", p99)
	}
}