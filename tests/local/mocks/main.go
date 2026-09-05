package main

// LocalMockProviders starts three mock LLM providers for local integration testing.
//
// Usage: go run local_test/mock_providers.go
//
// Endpoints:
//   - :9001 → Fast Provider (10ms response, 200 OK)
//   - :9002 → Slow Provider (5s response, 200 OK, configurable)
//   - :9003 → Error Provider (500 Internal Server Error, configurable)
//
// This program spawns three HTTP servers simulating real LLM providers.
// Configuration is done via environment variables:
//
//   MOCK_FAST_LATENCY=10ms
//   MOCK_SLOW_LATENCY=5s
//   MOCK_ERROR_BEHAVIOR=error  // error | healthy
//   MOCK_FAST_PORT=9001
//   MOCK_SLOW_PORT=9002
//   MOCK_ERROR_PORT=9003

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// Behavior constants matching integration package.
const (
	BehaviorHealthy = "healthy"
	BehaviorError   = "error"
)

// mockProvider is a single mock provider with configurable behavior.
type mockProvider struct {
	name     string
	port     string
	latency  atomic.Int64 // nanoseconds
	behavior atomic.Value // string
	healthy  atomic.Int64 // count of healthy responses
	errored  atomic.Int64 // count of error responses
}

func (mp *mockProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	behavior, _ := mp.behavior.Load().(string)
	latency := time.Duration(mp.latency.Load())

	// Simulate latency
	if latency > 0 {
		time.Sleep(latency)
	}

	if behavior == BehaviorError {
		mp.errored.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":{"message":"mock error from %s","type":"server_error"}}`, mp.name)
		return
	}

	mp.healthy.Add(1)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{
		"id": "chatcmpl-mock-%s-%d",
		"object": "chat.completion",
		"created": %d,
		"model": "gpt-4",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "Mock response from %s"},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`, mp.name, mp.healthy.Load(), time.Now().Unix(), mp.name)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		var n int64
		fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			return n
		}
	}
	return def
}

func main() {
	fastPort := envOr("MOCK_FAST_PORT", "9001")
	slowPort := envOr("MOCK_SLOW_PORT", "9002")
	errorPort := envOr("MOCK_ERROR_PORT", "9003")

	fastLatency := envIntOr("MOCK_FAST_LATENCY_MS", 10)
	slowLatency := envIntOr("MOCK_SLOW_LATENCY_MS", 5000)
	errorLatency := envIntOr("MOCK_ERROR_LATENCY_MS", 50)

	fast := &mockProvider{name: "fast", port: fastPort}
	fast.latency.Store(fastLatency * int64(time.Millisecond))
	fast.behavior.Store(BehaviorHealthy)

	slow := &mockProvider{name: "slow", port: slowPort}
	slow.latency.Store(slowLatency * int64(time.Millisecond))
	slow.behavior.Store(BehaviorHealthy)

	errProv := &mockProvider{name: "error", port: errorPort}
	errProv.latency.Store(errorLatency * int64(time.Millisecond))
	errProv.behavior.Store(BehaviorError)

	go func() {
		log.Printf("✓ Fast provider on :%s (10ms)", fastPort)
		if err := http.ListenAndServe(":"+fastPort, fast); err != nil {
			log.Fatalf("fast: %v", err)
		}
	}()
	go func() {
		log.Printf("✓ Slow provider on :%s (5s)", slowPort)
		if err := http.ListenAndServe(":"+slowPort, slow); err != nil {
			log.Fatalf("slow: %v", err)
		}
	}()
	go func() {
		log.Printf("✓ Error provider on :%s (500)", errorPort)
		if err := http.ListenAndServe(":"+errorPort, errProv); err != nil {
			log.Fatalf("error: %v", err)
		}
	}()

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("Mock providers running. Press Ctrl+C to stop.")
	log.Println("Control panel:")
	log.Println("  curl -X POST http://localhost:9100/error/slow      # switch to slow")
	log.Println("  curl -X POST http://localhost:9100/error/error     # switch to error")
	log.Println("  curl -X POST http://localhost:9100/error/healthy   # switch to healthy")
	log.Println("  curl -X POST http://localhost:9100/slow/3000       # change latency to 3s")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Start control server
	controlMux := http.NewServeMux()
	controlMux.HandleFunc("/slow/", func(w http.ResponseWriter, r *http.Request) {
		var ms int64
		fmt.Sscanf(r.URL.Path[6:], "%d", &ms)
		slow.latency.Store(ms * int64(time.Millisecond))
		log.Printf("→ slow.latency = %dms", ms)
		fmt.Fprintf(w, "ok: slow=%dms\n", ms)
	})
	controlMux.HandleFunc("/fast/", func(w http.ResponseWriter, r *http.Request) {
		var ms int64
		fmt.Sscanf(r.URL.Path[6:], "%d", &ms)
		fast.latency.Store(ms * int64(time.Millisecond))
		log.Printf("→ fast.latency = %dms", ms)
		fmt.Fprintf(w, "ok: fast=%dms\n", ms)
	})
	controlMux.HandleFunc("/error/", func(w http.ResponseWriter, r *http.Request) {
		behavior := r.URL.Path[7:]
		errProv.behavior.Store(behavior)
		log.Printf("→ error.behavior = %s", behavior)
		fmt.Fprintf(w, "ok: error=%s\n", behavior)
	})
	controlMux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "fast: healthy=%d errored=%d\n", fast.healthy.Load(), fast.errored.Load())
		fmt.Fprintf(w, "slow: healthy=%d errored=%d\n", slow.healthy.Load(), slow.errored.Load())
		fmt.Fprintf(w, "error: healthy=%d errored=%d\n", errProv.healthy.Load(), errProv.errored.Load())
	})
	log.Printf("✓ Control panel on :9100")
	if err := http.ListenAndServe(":9100", controlMux); err != nil {
		log.Fatalf("control: %v", err)
	}
}
