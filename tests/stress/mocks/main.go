// Package main is a dedicated stress-test mock LLM upstream.
//
// IMPORTANT: All provider names and model names used here are NON-REAL test
// identifiers (prefix `mock-` / `test-`). No real supplier identifier appears
// in the wire shape, so the harness cannot accidentally be mistaken for a
// production provider.
//
// Modes (controllable at runtime via /admin/state):
//
//	healthy        → 200 with full content; latency configurable
//	slow           → 5–30 s artificial latency before 200
//	server_error   → 500 with stable JSON body
//	no_available   → 503 with no_available_accounts body
//	rate_limited   → 429 with Retry-After: 30
//	quota_exceeded → 429 with insufficient_quota body
//	timeout        → handler sleeps past upstream_timeout, client times out
//	broken_stream  → sends 1 chunk then EOF (stream only)
//	flaky          → 50% success, 50% server_error
//
// Usage:
//
//	go run ./tests/stress/mocks -name=mock-alpha -port=18101 -token=tok-alpha
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// AllowedMode is the mode whitelist for /admin/state.
var allowedMode = map[string]bool{
	"healthy":        true,
	"slow":           true,
	"server_error":   true,
	"no_available":   true,
	"rate_limited":   true,
	"quota_exceeded": true,
	"timeout":        true,
	"broken_stream":  true,
	"flaky":          true,
}

// MockProvider is one upstream mock.
//
//   - name   = "mock-alpha" (NON-REAL supplier name, special prefix)
//   - port   = listen port
//   - token  = bearer token accepted on the chat endpoint (any token works)
type MockProvider struct {
	name  string
	port  string
	token string

	mu      sync.RWMutex
	mode    string
	latency struct {
		min time.Duration
		max time.Duration
	}
	recoveryAt time.Time

	counters struct {
		requests    atomic.Int64
		success     atomic.Int64
		serverErr   atomic.Int64
		clientErr   atomic.Int64
		rateLimited atomic.Int64
		streamErr   atomic.Int64
		stateChange atomic.Int64
	}
}

func main() {
	name := flag.String("name", "mock-alpha", "provider name (NON-REAL; use mock-* prefix)")
	port := flag.String("port", "18101", "listen port")
	token := flag.String("token", "test-token", "bearer token (any token accepted)")
	defaultLatMin := flag.Duration("latency-min", 50*time.Millisecond, "healthy-mode min latency")
	defaultLatMax := flag.Duration("latency-max", 200*time.Millisecond, "healthy-mode max latency")
	flag.Parse()

	p := &MockProvider{name: *name, port: *port, token: *token}
	p.mode = "healthy"
	p.latency.min = *defaultLatMin
	p.latency.max = *defaultLatMax

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", p.handleChat)
	mux.HandleFunc("/healthz", p.handleHealthz)
	mux.HandleFunc("/admin/state", p.handleAdminState)
	mux.HandleFunc("/admin/stats", p.handleAdminStats)

	log.Printf("[%s] mock upstream listening :%s (mode=%s, latency=%s..%s)",
		p.name, p.port, p.mode, p.latency.min, p.latency.max)
	if err := http.ListenAndServe(":"+p.port, mux); err != nil {
		log.Fatalf("[%s] listen: %v", p.name, err)
	}
}

// ─────────────────────────── HTTP handlers ───────────────────────────

func (p *MockProvider) handleHealthz(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	mode := p.mode
	recovered := time.Now().After(p.recoveryAt)
	p.mu.RUnlock()
	// L2 probe must see process-level failure, not inference-path
	// slowness. timeout/slow/broken_stream still serve /healthz 200
	// because a real vendor health endpoint is often up while chat
	// hangs or truncates — those are request-path detections.
	status := http.StatusOK
	switch mode {
	case "server_error":
		status = http.StatusInternalServerError
	case "no_available":
		status = http.StatusServiceUnavailable
	case "rate_limited", "quota_exceeded":
		status = http.StatusTooManyRequests
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"name":      p.name,
		"mode":      mode,
		"recovered": recovered,
		"counters":  p.snapshotCounters(),
	})
}

func (p *MockProvider) handleAdminState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Mode          string `json:"mode"`
		LatencyMinMS  int    `json:"latency_min_ms"`
		LatencyMaxMS  int    `json:"latency_max_ms"`
		RecoverInSecs int    `json:"recover_in_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !allowedMode[req.Mode] {
		http.Error(w, "unknown mode: "+req.Mode, http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	old := p.mode
	p.mode = req.Mode
	if req.LatencyMinMS > 0 {
		p.latency.min = time.Duration(req.LatencyMinMS) * time.Millisecond
	}
	if req.LatencyMaxMS > 0 {
		p.latency.max = time.Duration(req.LatencyMaxMS) * time.Millisecond
	}
	if req.Mode == "quota_exceeded" || req.Mode == "rate_limited" {
		if req.RecoverInSecs > 0 {
			p.recoveryAt = time.Now().Add(time.Duration(req.RecoverInSecs) * time.Second)
		} else {
			p.recoveryAt = time.Time{}
		}
	}
	p.mu.Unlock()

	p.counters.stateChange.Add(1)
	log.Printf("[%s] state %s → %s", p.name, old, req.Mode)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":           true,
		"mode":         req.Mode,
		"previous":     old,
		"latency_min":  p.latency.min.String(),
		"latency_max":  p.latency.max.String(),
		"recovered_at": p.recoveryAt,
	})
}

func (p *MockProvider) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p.snapshotCounters())
}

// handleChat is the OpenAI-compatible chat endpoint.
func (p *MockProvider) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p.counters.requests.Add(1)

	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.counters.clientErr.Add(1)
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	p.mu.RLock()
	mode := p.mode
	latMin := p.latency.min
	latMax := p.latency.max
	recovered := time.Now().After(p.recoveryAt)
	p.mu.RUnlock()

	// Auto-recovery from quota/rate-limit windows.
	if (mode == "quota_exceeded" || mode == "rate_limited") && recovered && !p.recoveryAt.IsZero() {
		p.mu.Lock()
		p.mode = "healthy"
		mode = "healthy"
		p.recoveryAt = time.Time{}
		p.mu.Unlock()
		log.Printf("[%s] auto-recovered → healthy", p.name)
	}

	// Latency distribution per mode.
	switch mode {
	case "slow":
		// 5–30 s random
		latMin = 5 * time.Second
		latMax = 30 * time.Second
	case "timeout":
		// Sleep past the test-gateway upstream timeout (25 s). 130 s is
		// long enough that the gateway client *must* fire first. The
		// sleep is interruptible: if the caller cancels (gateway timeout
		// or client abort) we return without writing a body.
		latMin = 130 * time.Second
		latMax = 130 * time.Second
	}
	// timeout MUST sleep. The previous implementation excluded it from
	// this block, so s16 completed in ~220 ms with HTTP 200 — the
	// timeout path was never exercised.
	if mode == "healthy" || mode == "slow" || mode == "timeout" || mode == "flaky" {
		d := jitter(latMin, latMax)
		timer := time.NewTimer(d)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}

	// Mode decision tree.
	switch mode {
	case "server_error":
		p.counters.serverErr.Add(1)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("[%s] simulated server_error", p.name),
				"type":    "server_error",
				"code":    "internal",
			},
		})
		return

	case "no_available":
		p.counters.serverErr.Add(1)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"message": "no available accounts",
				"type":    "api_error",
				"code":    "no_available_accounts",
			},
		})
		return

	case "rate_limited":
		p.counters.rateLimited.Add(1)
		w.Header().Set("Retry-After", "30")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": map[string]any{
				"message": "rate limit exceeded",
				"type":    "rate_limit_exceeded",
				"code":    "rate_limit",
			},
		})
		return

	case "quota_exceeded":
		p.counters.rateLimited.Add(1)
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": map[string]any{
				"message": "insufficient_quota",
				"type":    "insufficient_quota",
				"code":    "quota",
			},
		})
		return

	case "timeout":
		// Already sleeping; if we ever get here, the upstream timeout was
		// longer than ours, so we still return success to keep behaviour
		// observable in stats.
		p.counters.success.Add(1)
		p.replyOK(w, req.Model, req.Stream, req.Messages, req.MaxTokens)
		return

	case "broken_stream":
		if !req.Stream {
			p.counters.success.Add(1)
			p.replyOK(w, req.Model, req.Stream, req.Messages, req.MaxTokens)
			return
		}
		// Stream: send one chunk then EOF.
		p.counters.streamErr.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		chunk := map[string]any{
			"id":      fmt.Sprintf("chatcmpl-%s-broken", p.name),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   req.Model,
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"content": "partial-"},
			}},
		}
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(chunk))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hijack to drop connection abruptly.
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
				return
			}
		}
		return

	case "flaky":
		if rand.Intn(2) == 0 {
			p.counters.serverErr.Add(1)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]any{
					"message": fmt.Sprintf("[%s] flaky failure", p.name),
					"type":    "server_error",
				},
			})
			return
		}
		p.counters.success.Add(1)
		p.replyOK(w, req.Model, req.Stream, req.Messages, req.MaxTokens)
		return

	default: // healthy
		p.counters.success.Add(1)
		p.replyOK(w, req.Model, req.Stream, req.Messages, req.MaxTokens)
	}
}

func (p *MockProvider) replyOK(w http.ResponseWriter, model string, stream bool,
	messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}, maxTokens int) {

	if stream {
		p.replyStream(w, model, messages)
		return
	}

	// Non-stream: echo a deterministic body referencing the model + provider.
	content := buildContent(p.name, model, messages, maxTokens)

	usage := map[string]any{
		"prompt_tokens": approxTokens(messages),
		"completion_tokens": approxTokens([]struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{Role: "assistant", Content: content}}),
		"total_tokens": 0,
	}
	usage["total_tokens"] = usage["prompt_tokens"].(int) + usage["completion_tokens"].(int)

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%s-%d", p.name, time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": usage,
	})
}

func (p *MockProvider) replyStream(w http.ResponseWriter, model string, messages []struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	content := buildContent(p.name, model, messages, 64)
	words := splitWords(content)

	id := fmt.Sprintf("chatcmpl-%s-%d", p.name, time.Now().UnixNano())
	created := time.Now().Unix()
	flusher, _ := w.(http.Flusher)

	// Stream word by word, ~5 ms apart, so client TTFB is real and the
	// stream covers a non-trivial time window (useful for client-cancel
	// tests).
	for i, word := range words {
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{"content": word + " "},
			}},
		}
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(chunk))
		if flusher != nil {
			flusher.Flush()
		}
		// Pause briefly between chunks; first chunk is sent immediately to
		// keep TTFB <100 ms.
		if i == 0 {
			continue
		}
		time.Sleep(5 * time.Millisecond)
		if i > 200 {
			// Cap length so a long stream doesn't drag out.
			break
		}
	}
	// Final stop chunk.
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}},
	}))
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// ─────────────────────────── helpers ───────────────────────────

func (p *MockProvider) snapshotCounters() map[string]any {
	return map[string]any{
		"requests":     p.counters.requests.Load(),
		"success":      p.counters.success.Load(),
		"server_error": p.counters.serverErr.Load(),
		"client_error": p.counters.clientErr.Load(),
		"rate_limited": p.counters.rateLimited.Load(),
		"stream_error": p.counters.streamErr.Load(),
		"state_change": p.counters.stateChange.Load(),
	}
}

func jitter(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	span := max - min
	return min + time.Duration(rand.Int63n(int64(span)))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// approxTokens: rough 4-chars-per-token heuristic.
func approxTokens(messages []struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}) int {
	total := 0
	for _, m := range messages {
		total += len(m.Content) / 4
	}
	return total
}

func buildContent(name, model string, messages []struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}, maxTokens int) string {
	last := ""
	for _, m := range messages {
		if m.Role == "user" {
			last = m.Content
		}
	}
	if len(last) > 80 {
		last = last[:80] + "..."
	}
	reply := fmt.Sprintf("[%s reply] model=%s saw user=%q. ACK.",
		name, model, last)
	for i := 0; i < maxTokens/8 && len(reply) < maxTokens*4; i++ {
		reply += " stress-test-payload-word"
	}
	return reply
}

func splitWords(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
