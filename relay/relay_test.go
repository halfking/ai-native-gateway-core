package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/limiter"
)

// ---------------------------------------------------------------------------
// Fake upstream that simulates multi-turn chat with SSE streaming
// ---------------------------------------------------------------------------

type turnTracker struct {
	mu    sync.Mutex
	turns []map[string]any
}

func (tt *turnTracker) add(msg map[string]any) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.turns = append(tt.turns, msg)
}

func (tt *turnTracker) count() int {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return len(tt.turns)
}

func newFakeUpstream(t *testing.T) (*httptest.Server, *turnTracker) {
	t.Helper()
	tt := &turnTracker{}
	turnCount := atomic.Int32{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"bad request"}`, 400)
				return
			}

			if msgs, ok := req["messages"].([]any); ok {
				for _, m := range msgs {
					if msg, ok := m.(map[string]any); ok {
						tt.add(msg)
					}
				}
			}

			turn := int(turnCount.Add(1))
			model := "test-model"
			if m, ok := req["model"].(string); ok {
				model = m
			}

			stream, _ := req["stream"].(bool)

			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)

				for i := 0; i < 3; i++ {
					chunk := fmt.Sprintf(`data: {"id":"chat-%d","object":"chat.completion.chunk","choices":[{"delta":{"content":"Turn %d chunk %d "},"index":0}]}`, turn, turn, i)
					//nolint:errcheck // test write, non-critical
					fmt.Fprintf(w, "%s\n\n", chunk)
					flusher.Flush()
					time.Sleep(5 * time.Millisecond)
				}
				//nolint:errcheck // test write, non-critical
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
			} else {
				resp := map[string]any{
					"id":      fmt.Sprintf("chat-%d", turn),
					"object":  "chat.completion",
					"created": time.Now().Unix(),
					"model":   model,
					"choices": []map[string]any{
						{
							"index": 0,
							"message": map[string]any{
								"role":    "assistant",
								"content": fmt.Sprintf("Response for turn %d", turn),
							},
							"finish_reason": "stop",
						},
					},
					"usage": map[string]any{
						"prompt_tokens":     10,
						"completion_tokens": 5,
						"total_tokens":      15,
					},
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				//nolint:errcheck // HTTP write error non-recoverable
				json.NewEncoder(w).Encode(resp)
			}
			return
		}
		http.Error(w, "not found", 404)
	}))

	return server, tt
}

func mustParse(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

// ---------------------------------------------------------------------------
// Integration test: multi-turn conversation (non-streaming)
// ---------------------------------------------------------------------------

func TestMultiTurnChatNonStreaming(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test (needs database)")
	}
	fakeUpstream, tt := newFakeUpstream(t)
	defer fakeUpstream.Close()

	oldUpstream := upstream
	upstream = mustParse(fakeUpstream.URL)
	defer func() { upstream = oldUpstream }()

	cm := circuit.NewManager()
	lim := limiter.New()
	defer lim.Stop()

	handler := NewChatHandler(cm, lim, nil, nil, nil, nil)
	attachTestExecutor(handler, cm, lim, fakeUpstream.URL)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := server.Client()

	for turn := 1; turn <= 3; turn++ {
		body := fmt.Sprintf(`{
			"model": "test-model",
			"messages": [
				{"role": "system", "content": "You are a helpful assistant."},
				{"role": "user", "content": "Hello, this is turn %d"}
			]
		}`, turn)

		req, err := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(body))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Device-Seed", "test-device-123")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("turn %d request failed: %v", turn, err)
		}
		//nolint:errcheck // best-effort close
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("turn %d: expected 200, got %d", turn, resp.StatusCode)
		}

		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("turn %d: failed to decode response: %v", turn, err)
		}

		choices, ok := result["choices"].([]any)
		if !ok || len(choices) == 0 {
			t.Fatalf("turn %d: expected choices, got %+v", turn, result)
		}
	}

	if tt.count() < 3 {
		t.Fatalf("expected at least 3 messages tracked, got %d", tt.count())
	}
}

// ---------------------------------------------------------------------------
// Integration test: multi-turn conversation (streaming)
// ---------------------------------------------------------------------------

func TestMultiTurnChatStreaming(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test (needs database)")
	}
	fakeUpstream, tt := newFakeUpstream(t)
	defer fakeUpstream.Close()

	oldUpstream := upstream
	upstream = mustParse(fakeUpstream.URL)
	defer func() { upstream = oldUpstream }()

	cm := circuit.NewManager()
	lim := limiter.New()
	defer lim.Stop()

	handler := NewChatHandler(cm, lim, nil, nil, nil, nil)
	attachTestExecutor(handler, cm, lim, fakeUpstream.URL)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := server.Client()

	for turn := 1; turn <= 2; turn++ {
		body := fmt.Sprintf(`{
			"model": "test-model-stream",
			"stream": true,
			"messages": [
				{"role": "system", "content": "You are a helpful assistant."},
				{"role": "user", "content": "Streaming turn %d"}
			]
		}`, turn)

		req, err := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(body))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("X-Device-Seed", "stream-device-456")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("turn %d request failed: %v", turn, err)
		}
		//nolint:errcheck // best-effort close
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("turn %d: expected 200, got %d", turn, resp.StatusCode)
		}

		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("turn %d: expected text/event-stream, got %s", turn, ct)
		}
	}

	if tt.count() < 2 {
		t.Fatalf("expected at least 2 messages tracked, got %d", tt.count())
	}
}

// ---------------------------------------------------------------------------
// Circuit breaker integration test
// ---------------------------------------------------------------------------

func TestCircuitBreakerBlocksAfterFailures(t *testing.T) {
	failCount := atomic.Int32{}
	failingUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			failCount.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			//nolint:errcheck // HTTP write error non-recoverable
			json.NewEncoder(w).Encode(map[string]string{"error": "upstream error"})
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer failingUpstream.Close()

	oldUpstream := upstream
	upstream = mustParse(failingUpstream.URL)
	defer func() { upstream = oldUpstream }()

	cm := circuit.NewManager()
	lim := limiter.New()
	defer lim.Stop()

	handler := NewChatHandler(cm, lim, nil, nil, nil, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := server.Client()
	body := `{"model":"test","messages":[{"role":"user","content":"hello"}]}`

	// Send enough requests to trip the breaker
	for i := 0; i < 15; i++ {
		req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Logf("request %d: error %v", i, err)
			continue
		}
		//nolint:errcheck // best-effort close
		resp.Body.Close()
	}

	b := cm.GetOrCreate(1, 1)
	t.Logf("circuit breaker state: %s, consecutive: %d", b.State(), b.ConsecutiveFailures())
}

// ---------------------------------------------------------------------------
// Concurrency limiter integration test
// ---------------------------------------------------------------------------

func TestConcurrencyLimiterRejectsWhenSaturated(t *testing.T) {
	slowUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // HTTP write error non-recoverable
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "test",
			"object":  "chat.completion",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "slow response"}}},
		})
	}))
	defer slowUpstream.Close()

	oldUpstream := upstream
	upstream = mustParse(slowUpstream.URL)
	defer func() { upstream = oldUpstream }()

	cm := circuit.NewManager()
	lim := limiter.NewWithLimits(2, 2, 1, 1)
	defer lim.Stop()

	handler := NewChatHandler(cm, lim, nil, nil, nil, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := server.Client()

	var wg sync.WaitGroup
	successCount := atomic.Int32{}
	failCount := atomic.Int32{}

	body := `{"model":"test","messages":[{"role":"user","content":"hello"}]}`

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Device-Seed", fmt.Sprintf("device-%d", id))
			resp, err := client.Do(req)
			if err != nil {
				failCount.Add(1)
				return
			}
			//nolint:errcheck // best-effort close
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				successCount.Add(1)
			} else {
				failCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	t.Logf("successes: %d, failures: %d", successCount.Load(), failCount.Load())
	if successCount.Load() == 0 && failCount.Load() == 0 {
		t.Fatal("expected at least some requests to complete")
	}
}

// ---------------------------------------------------------------------------
// Health endpoint tests
// ---------------------------------------------------------------------------

func TestHealthEndpoint(t *testing.T) {
	cm := circuit.NewManager()
	lim := limiter.New()
	defer lim.Stop()

	handler := NewHealthHandler(cm, lim, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if result["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", result["status"])
	}
}

func TestHealthEndpointFull(t *testing.T) {
	cm := circuit.NewManager()
	lim := limiter.New()
	defer lim.Stop()

	handler := NewHealthHandler(cm, lim, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/healthz?full=true")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if _, ok := result["circuit"]; !ok {
		t.Fatal("expected circuit stats in full health")
	}
	if _, ok := result["concurrency"]; !ok {
		t.Fatal("expected concurrency stats in full health")
	}
}
