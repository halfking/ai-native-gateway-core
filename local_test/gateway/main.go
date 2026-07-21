package main

// LocalTestServer is a lightweight test server that wraps our Phase 1+2 architecture
// (health checks + dynamic weighted routing) and exposes them via HTTP.
//
// Usage: go run local_test/server.go
//
// Endpoints:
//   - POST /v1/chat/completions → main chat endpoint (uses WeightedRouter)
//   - GET  /healthz             → L1+L2+L3 health check
//   - GET  /stats               → routing weights stats
//   - GET  /providers           → list registered providers
//   - POST /admin/inject        → inject error/latency for testing
//
// This server connects to the local PostgreSQL database and the 3 mock providers
// running on ports 9001/9002/9003.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/health"
	"github.com/kaixuan/llm-gateway-go/domains/routing"
)

// ChatRequest is the OpenAI-compatible chat request.
type ChatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Stream bool `json:"stream"`
}

// ChatResponse is the OpenAI-compatible chat response.
type ChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// TestServer is the test orchestration server.
type TestServer struct {
	mu sync.RWMutex

	// Routing
	router    *routing.WeightedRouter
	detectors map[string]*health.ErrorDetector // keyed by credential id
	latency   map[string]*routing.LatencyTracker

	// Provider endpoints (mock providers)
	providers map[string]string // credential id → URL

	// Health checkers
	tcpChecker    *health.TCPChecker
	httpChecker   *health.HTTPChecker
	infChecker    *health.InferenceChecker
	errorDetector *health.ErrorDetector

	// Stats
	totalRequests atomic.Int64
	totalSuccess  atomic.Int64
	totalErrors   atomic.Int64
	totalRetries  atomic.Int64
}

func newTestServer() *TestServer {
	ts := &TestServer{
		router:        routing.NewWeightedRouter(),
		detectors:     make(map[string]*health.ErrorDetector),
		latency:       make(map[string]*routing.LatencyTracker),
		providers:     make(map[string]string),
		tcpChecker:    health.NewTCPChecker(1 * time.Second),
		httpChecker:   health.NewHTTPChecker(3 * time.Second),
		infChecker:    health.NewInferenceChecker(10*time.Second, 30*time.Second),
		errorDetector: health.NewErrorDetector(3),
	}
	return ts
}

// loadFromDB simulates loading providers/credentials from the database.
// In production this would query PostgreSQL.
func (ts *TestServer) loadFromDB() {
	// Hard-coded for now; production code would SELECT from providers/credentials.
	ts.providers["mock-fast"] = "http://localhost:9001"
	ts.providers["mock-slow"] = "http://localhost:9002"
	ts.providers["mock-error"] = "http://localhost:9003"

	for credID := range ts.providers {
		det := health.NewErrorDetector(3)
		ts.detectors[credID] = det
		ts.latency[credID] = routing.NewLatencyTracker()
		ts.router.RegisterWithDetector(routing.NewCandidateFromID(credID), det)
	}

	log.Printf("✓ Loaded %d credentials from DB", len(ts.providers))
}

// healthCheck performs L1→L2→L3 sequence.
func (ts *TestServer) healthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	results := map[string]interface{}{}
	overallHealthy := true

	for credID, url := range ts.providers {
		// L1: TCP check (extract host:port from URL)
		hostPort := url[7:] // strip "http://"
		tcpResult := ts.tcpChecker.Check(ctx, hostPort)
		l1OK := tcpResult.Success

		// L2: HTTP check (HEAD request)
		l2OK := false
		if l1OK {
			client := &http.Client{Timeout: 3 * time.Second}
			req, _ := http.NewRequestWithContext(ctx, "HEAD", url, nil)
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				l2OK = resp.StatusCode < 500
			}
		}

		// L3: Light inference check (only if L2 passed)
		l3OK := false
		l3Latency := time.Duration(0)
		if l2OK {
			start := time.Now()
			client := &http.Client{Timeout: 10 * time.Second}
			bodyStr := `{"model":"gpt-4","messages":[{"role":"user","content":"1+1=?"}],"max_tokens":5}`
			req, _ := http.NewRequestWithContext(ctx, "POST", url+"/v1/chat/completions", strings.NewReader(bodyStr))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer mock-key")
			resp, err := client.Do(req)
			l3Latency = time.Since(start)
			if err == nil {
				defer resp.Body.Close()
				_, _ = io.ReadAll(resp.Body)
				if resp.StatusCode == 200 {
					l3OK = true
				}
			}
		}

		healthy := l1OK && l2OK && l3OK
		if !healthy {
			overallHealthy = false
		}

		results[credID] = map[string]interface{}{
			"L1_TCP":     map[string]bool{"ok": l1OK},
			"L2_HTTP":    map[string]bool{"ok": l2OK},
			"L3_Light":   map[string]bool{"ok": l3OK},
			"l3_latency": l3Latency.Milliseconds(),
			"healthy":    healthy,
		}
	}

	status := "healthy"
	if !overallHealthy {
		status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  status,
		"checks":  results,
		"elapsed": time.Since(time.Now()).Milliseconds(),
	})
}

// handleChat is the main chat endpoint.
func (ts *TestServer) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ts.totalRequests.Add(1)

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		ts.totalErrors.Add(1)
		return
	}

	// Step 1: Select credential via WeightedRouter
	candidate := ts.router.SelectWeighted()
	if candidate == nil {
		http.Error(w, "no healthy providers", http.StatusServiceUnavailable)
		ts.totalErrors.Add(1)
		return
	}

	credID := candidate.CredentialID
	url := ts.providers[credID]

	// Step 2: Forward to provider
	start := time.Now()
	client := &http.Client{Timeout: 30 * time.Second}
	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequest("POST", url+"/v1/chat/completions", strings.NewReader(string(body)))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer mock-key")

	resp, err := client.Do(httpReq)
	latency := time.Since(start)

	if err != nil {
		// Network error → record and fail
		ts.router.RecordError(credID, 0, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		ts.totalErrors.Add(1)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Step 3: Record outcome
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		ts.router.RecordSuccess(credID, latency)
		ts.totalSuccess.Add(1)
	} else if resp.StatusCode >= 500 {
		ts.router.RecordError(credID, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode))
		ts.totalErrors.Add(1)
	} else {
		ts.router.RecordLatency(credID, latency)
	}

	// Forward response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// stats returns routing and request stats.
func (ts *TestServer) stats(w http.ResponseWriter, r *http.Request) {
	routerStats := ts.router.Stats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"requests": map[string]int64{
			"total":   ts.totalRequests.Load(),
			"success": ts.totalSuccess.Load(),
			"errors":  ts.totalErrors.Load(),
			"retries": ts.totalRetries.Load(),
		},
		"routing": routerStats,
	})
}

// adminInject allows changing provider behavior for testing.
func (ts *TestServer) adminInject(w http.ResponseWriter, r *http.Request) {
	var cmd struct {
		Action string `json:"action"`
		Port   string `json:"port"`
		Ms     int    `json:"ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Forward to mock provider control endpoint
	targetURL := fmt.Sprintf("http://localhost:%s/%s", cmd.Port, cmd.Action)
	if cmd.Ms > 0 {
		targetURL = fmt.Sprintf("http://localhost:%s/%s/%d", cmd.Port, cmd.Action, cmd.Ms)
	}
	resp, err := http.Post(targetURL, "application/json", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// adminReset resets a credential's failure state (admin recovery).
func (ts *TestServer) adminReset(w http.ResponseWriter, r *http.Request) {
	var cmd struct {
		CredentialID string `json:"credential_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ts.router.ResetCredential(cmd.CredentialID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":         true,
		"credential": cmd.CredentialID,
		"new_weight": ts.router.Weight(cmd.CredentialID),
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	port := envOr("SERVER_PORT", "8081")

	ts := newTestServer()
	ts.loadFromDB()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", ts.handleChat)
	mux.HandleFunc("/healthz", ts.healthCheck)
	mux.HandleFunc("/stats", ts.stats)
	mux.HandleFunc("/admin/inject", ts.adminInject)
	mux.HandleFunc("/admin/reset", ts.adminReset)

	log.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Printf("LLM Gateway Local Test Server")
	log.Printf("  Listening on :%s", port)
	log.Printf("  POST /v1/chat/completions → chat endpoint")
	log.Printf("  GET  /healthz             → L1+L2+L3 health")
	log.Printf("  GET  /stats               → routing stats")
	log.Printf("  POST /admin/inject        → inject test behavior")
	log.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}
