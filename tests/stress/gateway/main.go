// Package main is a stress-test gateway with mock credentials.
//
// IMPORTANT: All provider and credential names used here are NON-REAL
// identifiers (prefix `test-provider-*` / `mock-stress-*`). The gateway
// is wired against local mock upstream servers (started by the runner
// script). It mirrors the real gateway's request flow:
//
//	client → /v1/chat/completions
//	       ↓
//	canon model → weighted router → picked credential → mock upstream
//	       ↓
//	stream/non-stream passthrough; status / error / latency recorded.
//
// Health checks: L1 (TCP) + L2 (GET /healthz). There is no L3/L4
// inference probe in this test gateway. Do not claim production
// credentialhealth behavior.
//
// Endpoints:
//
//	POST /v1/chat/completions   OpenAI-compatible chat
//	GET  /v1/models             list registered canonical models
//	GET  /admin/stats           router + credential counters
//	GET  /admin/health          per-credential health status
//	POST /admin/reset           reset credential state
//
// Usage:
//
//	go run ./tests/stress/gateway -port=18901 \
//	  -cred alpha=http://127.0.0.1:18101 \
//	  -cred beta=http://127.0.0.1:18102 \
//	  ...
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────── types ───────────────────────

// Credential ties a non-real supplier name to a base URL.
//
// ID is `test-provider-<n>` so it is recognisable as a test fixture.
type Credential struct {
	ID      string // test-provider-alpha, test-provider-beta, ...
	BaseURL string // http://127.0.0.1:18101
	APIKey  string // any string; the mock accepts any token
}

// Status values mirror provider.Status.
const (
	StatusActive    = "active"
	StatusDegraded  = "degraded"
	StatusUnhealthy = "unhealthy"
)

type credState struct {
	cred        Credential
	mu          sync.RWMutex
	status      string
	errCount    atomic.Int64
	succCount   atomic.Int64
	lastLatency atomic.Int64 // ns
	lastError   atomic.Value // string
	consecFails atomic.Int64
	consecSucc  atomic.Int64
	// 60 one-second buckets. Tick() advances current; Last1Min() sums
	// the ring. This is a real time window, not an event-count shift.
	errWindow   [60]int64
	errIdx      int
	errLastTick time.Time
	errWindowMu sync.Mutex
	// lastErrorAt is the wall-clock time of the most recent error; used
	// for stale-promotion (auto-recovery).
	lastErrorAt time.Time
	// lastSuccessAt is the wall-clock time of the most recent success.
	lastSuccessAt time.Time
}

// CanaryCanonicalModel maps the public model name to its credential pool.
// We use a special name pattern (`mock-stress-*`) so production routers
// can never accidentally pick one up.
type canonicalBinding struct {
	canonical   string   // mock-stress-fast, mock-stress-large
	credentials []string // credential IDs
}

type Gateway struct {
	mu sync.RWMutex

	creds      map[string]*credState        // credential id → state
	bindings   map[string]*canonicalBinding // canonical model → binding
	httpClient *http.Client
}

func main() {
	port := flag.String("port", "18901", "listen port")
	streamTO := flag.Duration("stream-timeout", 30*time.Second, "stream timeout")
	upstreamTO := flag.Duration("upstream-timeout", 25*time.Second, "non-stream upstream timeout")
	memLimitMB := flag.Int("mem-limit-mb", 0, "soft RSS cap via runtime/debug.SetMemoryLimit; 0 = unlimited")
	gogc := flag.Int("gogc", 100, "GOGC percent; lower = more aggressive GC, higher = less CPU but more memory")
	simProdMem := flag.Int("simulate-prod-mem", 0, "MB of pre-allocated buffers to mimic PG/Redis/URSM pools; 0 = no simulation")
	poolConns := flag.Int("simulate-pg-pool", 32, "simulated pgxpool connections; each ~5 MB")
	redisConns := flag.Int("simulate-redis-pool", 200, "simulated Redis client pool; each ~100 KB")
	ursmSessions := flag.Int("simulate-ursm-sessions", 10000, "simulated URSM v2 session state objects")
	flag.Parse()

	if *memLimitMB > 0 {
		debug.SetMemoryLimit(int64(*memLimitMB) * 1024 * 1024)
		log.Printf("[test-gateway] GOMEMLIMIT set to %d MB", *memLimitMB)
	}
	if *gogc != 100 {
		debug.SetGCPercent(*gogc)
		log.Printf("[test-gateway] GOGC set to %d", *gogc)
	}

	// Pre-allocate buffers to mimic production memory consumers so the
	// heap-pressure measurements reflect realistic GC behaviour.
	if *simProdMem > 0 {
		simulateProdMemory(*simProdMem, *poolConns, *redisConns, *ursmSessions)
	}

	gw := newGateway(*upstreamTO)
	gw.bootstrap()
	go gw.probeLoop(2 * time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", gw.handleChat)
	mux.HandleFunc("/v1/models", gw.handleModels)
	mux.HandleFunc("/admin/stats", gw.handleStats)
	mux.HandleFunc("/admin/health", gw.handleHealth)
	mux.HandleFunc("/admin/reset", gw.handleReset)
	mux.HandleFunc("/admin/memstats", handleMemStats)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	_ = streamTO // (used in stream path)

	log.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Printf("[test-gateway] stress gateway listening :%s", *port)
	log.Printf("[test-gateway] %d canonical models, %d credentials",
		len(gw.bindings), len(gw.creds))
	for can, b := range gw.bindings {
		log.Printf("[test-gateway]   %s → %s", can, strings.Join(b.credentials, ", "))
	}
	log.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	if err := http.ListenAndServe(":"+*port, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// ─────────────────────── bootstrap ───────────────────────

func newGateway(upstreamTimeout time.Duration) *Gateway {
	// directTransport bypasses HTTP_PROXY/HTTPS_PROXY so the harness can
	// run in dev environments where an upstream proxy is set (it would
	// otherwise pollute localhost traffic and inflate latency metrics).
	return &Gateway{
		creds:    make(map[string]*credState),
		bindings: make(map[string]*canonicalBinding),
		httpClient: &http.Client{
			Timeout:   upstreamTimeout,
			Transport: directGatewayTransport,
		},
	}
}

// directGatewayTransport is a shared http.Transport that disables
// HTTP_PROXY/HTTPS_PROXY environment lookup. Required by the capacity
// matrix handoff (CAPACITY_HANDOVER.md): the harness must talk to the
// local mocks via loopback regardless of dev-environment proxy settings.
var directGatewayTransport = &http.Transport{
	Proxy: nil,
}

// bootstrap wires up the test pool based on environment variables:
//
//	STRESS_CRED_<name>=<base_url>    e.g. STRESS_CRED_alpha=http://127.0.0.1:18101
//	STRESS_MODEL_<canon>=<csv>      e.g. STRESS_MODEL_mock-stress-fast=alpha,beta,gamma
//
// Defaults follow the runner script's port plan (18101–18104).
func (g *Gateway) bootstrap() {
	defaultCreds := []Credential{
		{ID: "test-provider-alpha", BaseURL: "http://127.0.0.1:18101", APIKey: "test-token-alpha"},
		{ID: "test-provider-beta", BaseURL: "http://127.0.0.1:18102", APIKey: "test-token-beta"},
		{ID: "test-provider-gamma", BaseURL: "http://127.0.0.1:18103", APIKey: "test-token-gamma"},
		{ID: "test-provider-delta", BaseURL: "http://127.0.0.1:18104", APIKey: "test-token-delta"},
	}
	for _, c := range defaultCreds {
		g.register(c)
	}

	defaultModels := []canonicalBinding{
		{canonical: "mock-stress-fast", credentials: []string{
			"test-provider-alpha", "test-provider-beta", "test-provider-gamma"}},
		{canonical: "mock-stress-large", credentials: []string{
			"test-provider-delta"}},
		{canonical: "mock-stress-stream", credentials: []string{
			"test-provider-alpha", "test-provider-beta"}},
		// mock-stress-pool binds all four. Used by s14 ("three 503, one
		// healthy"). mock-stress-large stays delta-only.
		{canonical: "mock-stress-pool", credentials: []string{
			"test-provider-alpha", "test-provider-beta",
			"test-provider-gamma", "test-provider-delta"}},
	}
	for _, b := range defaultModels {
		g.bindings[b.canonical] = &b
	}

	// Allow env-driven overrides.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "STRESS_CRED_") {
			continue
		}
		kv := strings.SplitN(env[13:], "=", 2)
		if len(kv) != 2 {
			continue
		}
		id := "test-provider-" + kv[0]
		g.register(Credential{ID: id, BaseURL: kv[1], APIKey: "test-token-env"})
	}
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "STRESS_MODEL_") {
			continue
		}
		kv := strings.SplitN(env[14:], "=", 2)
		if len(kv) != 2 {
			continue
		}
		canonical := kv[0]
		var ids []string
		for _, name := range strings.Split(kv[1], ",") {
			ids = append(ids, "test-provider-"+strings.TrimSpace(name))
		}
		g.bindings[canonical] = &canonicalBinding{canonical: canonical, credentials: ids}
	}
}

func (g *Gateway) register(c Credential) {
	cs := &credState{
		cred:        c,
		status:      StatusActive,
		errLastTick: time.Now(),
	}
	cs.lastError.Store("")
	g.creds[c.ID] = cs
}

// ─────────────────────── routing ───────────────────────

// pickCredential selects a healthy credential for a canonical model
// using weighted random + adaptive degradation.
//
// Weight formula (mirrors docs/05-testing/02-test-plans/testing/comprehensive-test-plan.md
// §3.1):
//
//	w = base × errorPenalty × latencyPenalty
//	  where:
//	    base           = 1.0
//	    errorPenalty   = max(0.1, 1 - errorsPerMin/10)
//	    latencyPenalty = max(0.5, 1 - (avgMs - 2000) / 10000)
//	    status × 0     if Unhealthy (excluded)
//	    status × 0.5   if Degraded
func (g *Gateway) pickCredential(canonical string) (*credState, string) {
	return g.pickCredentialExcluding(canonical, nil)
}

func (g *Gateway) pickCredentialExcluding(canonical string, exclude map[string]bool) (*credState, string) {
	binding := g.bindings[canonical]
	if binding == nil {
		return nil, "no binding"
	}
	candidates := make([]*credState, 0, len(binding.credentials))
	for _, id := range binding.credentials {
		if exclude[id] {
			continue
		}
		cs, ok := g.creds[id]
		if !ok {
			continue
		}
		cs.autoRecoverIfStale(30 * time.Second)
		cs.mu.RLock()
		status := cs.status
		cs.mu.RUnlock()
		if status != StatusUnhealthy {
			candidates = append(candidates, cs)
		}
	}
	if len(candidates) == 0 {
		return nil, "no healthy credential"
	}

	type witem struct {
		cs     *credState
		weight float64
	}
	var items []witem
	total := 0.0
	for _, cs := range candidates {
		cs.mu.RLock()
		errPerMin := float64(cs.errorsInLast1Min())
		avgMs := float64(cs.lastLatency.Load()) / float64(time.Millisecond)
		statusMul := 1.0
		if cs.status == StatusDegraded {
			statusMul = 0.5
		}
		cs.mu.RUnlock()
		errPenalty := 1.0 - errPerMin/10.0
		if errPenalty < 0.1 {
			errPenalty = 0.1
		}
		latPenalty := 1.0
		if avgMs > 2000 {
			latPenalty = 1.0 - (avgMs-2000.0)/10000.0
			if latPenalty < 0.5 {
				latPenalty = 0.5
			}
		}
		w := statusMul * errPenalty * latPenalty
		items = append(items, witem{cs: cs, weight: w})
		total += w
	}
	if total <= 0 {
		return candidates[0], "fallback-first"
	}
	roll := rngFloat() * total
	cum := 0.0
	for _, it := range items {
		cum += it.weight
		if roll <= cum {
			return it.cs, "weighted"
		}
	}
	return items[len(items)-1].cs, "weighted-tail"
}

// ─────────────────────── chat ───────────────────────

func (g *Gateway) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		http.Error(w, "model required", http.StatusBadRequest)
		return
	}

	tried := map[string]bool{}
	var lastFail bool
	maxAttempts := 3
	if b := g.bindings[req.Model]; b != nil && len(b.credentials) > maxAttempts {
		maxAttempts = len(b.credentials)
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		cs, reason := g.pickCredentialExcluding(req.Model, tried)
		if cs == nil {
			if attempt == 0 {
				log.Printf("[test-gateway] 503 no-credential model=%s reason=%s", req.Model, reason)
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{
					"error": map[string]any{
						"message": "no healthy credential",
						"type":    "api_error",
						"code":    "no_available",
						"reason":  reason,
					},
				})
				return
			}
			break
		}
		tried[cs.cred.ID] = true
		log.Printf("[test-gateway] → %s model=%s stream=%v attempt=%d", cs.cred.ID, req.Model, req.Stream, attempt+1)
		failed, committed := g.proxyRequest(w, r, cs, req.Model, req.Stream, req.Messages, req.MaxTokens)
		if committed {
			return
		}
		if !failed {
			return
		}
		lastFail = true
	}
	if lastFail {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"message": "all candidate credentials failed",
				"type":    "api_error",
				"code":    "upstream_failover_exhausted",
			},
		})
	}
}

// proxyRequest forwards one attempt to a credential.
//
// Returns:
//
//	failed    — this attempt should be retried on another credential
//	committed — response headers/body have already been written to w;
//	            the caller MUST NOT write anything else.
//
// Failover is only possible before the first byte is written. This
// test gateway therefore buffers a mock SSE body until [DONE] or EOF
// and only then writes to the client. A truncated stream (no [DONE])
// is not committed, so the caller can try the next credential.
// Production cmd/gateway is NOT this code path.
func (g *Gateway) proxyRequest(
	w http.ResponseWriter, r *http.Request,
	cs *credState, model string, stream bool,
	messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}, maxTokens int,
) (failed, committed bool) {
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"stream":     stream,
		"messages":   messages,
		"max_tokens": maxTokens,
	})

	upstreamURL := strings.TrimRight(cs.cred.BaseURL, "/") + "/v1/chat/completions"
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL,
		strings.NewReader(string(body)))
	if err != nil {
		cs.recordError("build-request: "+err.Error(), 0)
		return true, false
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+cs.cred.APIKey)

	started := time.Now()
	resp, err := g.httpClient.Do(upReq)
	latency := time.Since(started)
	if err != nil {
		isTimeout := strings.Contains(err.Error(), "context deadline exceeded") ||
			strings.Contains(err.Error(), "Client.Timeout")
		kind := "upstream-error"
		if isTimeout {
			kind = "upstream-timeout"
		}
		cs.recordError(kind+": "+err.Error(), 0)
		return true, false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		cs.recordError(fmt.Sprintf("upstream-%d", resp.StatusCode), resp.StatusCode)
		// Non-2xx: consume body and failover without writing to client.
		io.Copy(io.Discard, resp.Body)
		return true, false
	}

	if !stream {
		bodyBytes, _ := io.ReadAll(resp.Body)
		if len(bytes.TrimSpace(bodyBytes)) == 0 {
			cs.recordError("upstream-empty-200", 0)
			return true, false
		}
		cs.recordSuccess(latency)
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(bodyBytes)
		return false, true
	}

	// Stream: buffer the mock SSE until [DONE] or EOF. Plan §11.6:
	// HTTP 200 without [DONE] must not be returned to the client.
	// Buffering is acceptable here because mock streams are short
	// (a few hundred bytes). This is NOT the production executor.
	ctx := r.Context()
	var bodyBuf bytes.Buffer
	tmp := make([]byte, 16*1024)
	for {
		select {
		case <-ctx.Done():
			cs.recordError("stream-client-cancel", 0)
			return true, false
		default:
		}
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			bodyBuf.Write(tmp[:n])
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			cs.recordError("stream-read: "+err.Error(), 0)
			return true, false
		}
		if bodyBuf.Len() > 1<<20 {
			cs.recordError("stream-buffer-overflow", 0)
			return true, false
		}
	}
	sawDone := bytes.Contains(bodyBuf.Bytes(), []byte("data: [DONE]"))
	if !sawDone || bodyBuf.Len() == 0 {
		kind := "stream-missing-done"
		if bodyBuf.Len() == 0 {
			kind = "stream-empty"
		}
		cs.recordError(kind, 0)
		return true, false
	}

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.WriteHeader(resp.StatusCode)
	if _, werr := w.Write(bodyBuf.Bytes()); werr != nil {
		cs.recordError("stream-client-write: "+werr.Error(), 0)
		return true, true
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	cs.recordSuccess(latency)
	return false, true
}

// ─────────────────────── cred state ───────────────────────

func (cs *credState) recordSuccess(latency time.Duration) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.succCount.Add(1)
	cs.lastLatency.Store(int64(latency))
	cs.consecFails.Store(0)
	cs.consecSucc.Add(1)
	cs.lastSuccessAt = time.Now()
	// Test-plan §4.2: Unhealthy does not jump to Active on a single
	// success. Degraded requires 5 consecutive successes.
	switch cs.status {
	case StatusDegraded:
		if cs.consecSucc.Load() >= 5 {
			cs.status = StatusActive
		}
	case StatusUnhealthy:
		if cs.consecSucc.Load() >= 1 {
			cs.status = StatusDegraded
			cs.consecSucc.Store(0)
		}
	}
}

func (cs *credState) recordError(reason string, httpStatus int) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.errCount.Add(1)
	cs.lastError.Store(reason)
	cs.consecFails.Add(1)
	cs.consecSucc.Store(0)
	cs.lastErrorAt = time.Now()
	cs.recordWindowedErr()

	// Adaptive degradation (mirrors §4 of the test plan).
	// 4xx (quota/rate-limit) and 5xx are both recorded, but only 5xx
	// and timeouts drive Unhealthy. 429 stays Degraded so recovery
	// after Retry-After remains possible.
	is5xx := httpStatus >= 500 || httpStatus == 0 // 0 = timeout / transport
	switch cs.status {
	case StatusActive:
		if cs.consecFails.Load() >= 3 {
			cs.status = StatusDegraded
		}
		if is5xx && cs.consecFails.Load() >= 5 {
			cs.status = StatusUnhealthy
		}
	case StatusDegraded:
		if is5xx && cs.consecFails.Load() >= 3 {
			cs.status = StatusUnhealthy
		}
	}
}

// autoRecoverIfStale promotes a credential back to Active if it has had
// no recent errors and no successes for at least staleThreshold. This
// mirrors the L1+L2 detection path described in §1.3 of the test plan:
// once the broken upstream recovers (e.g. flaky 500 → 200), the gateway
// must re-enable traffic without waiting for human intervention.
//
// The check is invoked from pickCredential on every call; the cost is one
// time.Load and one atomic Load, so it is safe to call per-request.
func (cs *credState) autoRecoverIfStale(staleThreshold time.Duration) {
	cs.mu.RLock()
	status := cs.status
	lastErr := cs.lastErrorAt
	cs.mu.RUnlock()
	if status == StatusActive {
		return
	}
	if lastErr.IsZero() || time.Since(lastErr) <= staleThreshold {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.status == StatusActive {
		return
	}
	if cs.lastErrorAt.IsZero() || time.Since(cs.lastErrorAt) <= staleThreshold {
		return
	}
	// Stale Unhealthy → Degraded only. Active still requires 5
	// consecutive successes on the request path (test-plan §4.2).
	if cs.status == StatusUnhealthy {
		cs.status = StatusDegraded
		cs.consecFails.Store(0)
		cs.consecSucc.Store(0)
	}
}

func (cs *credState) tickWindowLocked() {
	now := time.Now()
	if cs.errLastTick.IsZero() {
		cs.errLastTick = now
		return
	}
	elapsed := int(now.Sub(cs.errLastTick) / time.Second)
	if elapsed <= 0 {
		return
	}
	if elapsed > 60 {
		elapsed = 60
	}
	for i := 0; i < elapsed; i++ {
		cs.errIdx = (cs.errIdx + 1) % 60
		cs.errWindow[cs.errIdx] = 0
	}
	cs.errLastTick = cs.errLastTick.Add(time.Duration(elapsed) * time.Second)
}

func (cs *credState) recordWindowedErr() {
	cs.errWindowMu.Lock()
	defer cs.errWindowMu.Unlock()
	cs.tickWindowLocked()
	cs.errWindow[cs.errIdx]++
}

func (cs *credState) errorsInLast1Min() int {
	cs.errWindowMu.Lock()
	defer cs.errWindowMu.Unlock()
	cs.tickWindowLocked()
	sum := int64(0)
	for _, v := range cs.errWindow {
		sum += v
	}
	return int(sum)
}

// ─────────────────────── admin endpoints ───────────────────────

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := map[string]any{
		"object": "list",
		"data":   []map[string]any{},
	}
	for can := range g.bindings {
		out["data"] = append(out["data"].([]map[string]any), map[string]any{
			"id":       can,
			"object":   "model",
			"owned_by": "test",
		})
	}
	json.NewEncoder(w).Encode(out)
}

func (g *Gateway) handleStats(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := map[string]any{
		"credentials": map[string]any{},
		"models":      map[string]any{},
	}
	for id, cs := range g.creds {
		cs.mu.RLock()
		out["credentials"].(map[string]any)[id] = map[string]any{
			"status":       cs.status,
			"success":      cs.succCount.Load(),
			"errors":       cs.errCount.Load(),
			"errors_1min":  cs.errorsInLast1Min(),
			"consec_fails": cs.consecFails.Load(),
			"last_latency": cs.lastLatency.Load(),
			"last_error":   cs.lastError.Load(),
		}
		cs.mu.RUnlock()
	}
	for can := range g.bindings {
		out["models"].(map[string]any)[can] = map[string]any{
			"credentials": g.bindings[can].credentials,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	type probe struct {
		L1 bool
		L2 bool
	}
	out := map[string]any{}
	for id, cs := range g.creds {
		p := probe{L1: tcpProbe(cs.cred.BaseURL), L2: httpProbe(cs.cred.BaseURL)}
		cs.mu.RLock()
		out[id] = map[string]any{
			"status":  cs.status,
			"L1_TCP":  p.L1,
			"L2_HTTP": p.L2,
			"healthy": p.L1 && p.L2 && cs.status != StatusUnhealthy,
		}
		cs.mu.RUnlock()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (g *Gateway) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, cs := range g.creds {
		cs.mu.Lock()
		cs.status = StatusActive
		cs.consecFails.Store(0)
		cs.consecSucc.Store(0)
		cs.lastError.Store("")
		cs.lastErrorAt = time.Time{}
		cs.lastSuccessAt = time.Time{}
		cs.errCount.Store(0)
		cs.succCount.Store(0)
		cs.errWindowMu.Lock()
		cs.errWindow = [60]int64{}
		cs.errIdx = 0
		cs.errLastTick = time.Now()
		cs.errWindowMu.Unlock()
		cs.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "reset": len(g.creds)})
}

// ─────────────────────── probes ───────────────────────

func tcpProbe(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func httpProbe(baseURL string) bool {
	client := &http.Client{Timeout: 2 * time.Second, Transport: directGatewayTransport}
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/healthz", nil)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 400
}

// probeLoop is the L1/L2 background health path (test-plan §1.2).
//
// It does NOT promote Unhealthy → Active. A successful L1+L2 probe
// only lifts Unhealthy → Degraded. Active still requires consecutive
// successes on the request path (test-plan §4.2).
//
// L2 uses GET /healthz. Mock error modes return 5xx on /healthz so a
// still-broken upstream cannot be auto-promoted.
func (g *Gateway) probeLoop(interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		g.mu.RLock()
		states := make([]*credState, 0, len(g.creds))
		for _, cs := range g.creds {
			states = append(states, cs)
		}
		g.mu.RUnlock()
		for _, cs := range states {
			l1 := tcpProbe(cs.cred.BaseURL)
			l2 := false
			if l1 {
				l2 = httpProbe(cs.cred.BaseURL)
			}
			cs.mu.Lock()
			switch {
			case !l1:
				if cs.status != StatusUnhealthy {
					cs.status = StatusUnhealthy
					cs.consecFails.Add(1)
					cs.consecSucc.Store(0)
					cs.lastError.Store("probe-l1-tcp")
					cs.lastErrorAt = time.Now()
				}
			case !l2:
				if cs.status == StatusActive {
					cs.status = StatusDegraded
				}
				cs.consecSucc.Store(0)
				cs.lastError.Store("probe-l2-http")
				cs.lastErrorAt = time.Now()
			default:
				// L1+L2 healthy: Unhealthy → Degraded only.
				if cs.status == StatusUnhealthy {
					cs.status = StatusDegraded
					cs.consecFails.Store(0)
					cs.consecSucc.Store(0)
				}
			}
			cs.mu.Unlock()
		}
	}
}

// ─────────────────────── misc ───────────────────────

// simulateProdMemory pre-allocates buffers that mimic the steady-state
// memory footprint of a production gateway. Numbers are calibrated
// against the real cmd/gateway in §11 of the comprehensive test plan:
//
//	pgxpool connection     ≈ 5 MB (TLS + per-conn buffers)
//	redis client connection ≈ 100 KB (RESP buf + TCP state)
//	URSM v2 SessionState    ≈ 256 B (per active session)
//
// The buffers are kept alive in package-level slices so the GC cannot
// reclaim them; this guarantees the allocated bytes reflect realistic
// PG/Redis pool pressure and let us model each host spec.
func simulateProdMemory(totalMiB, pgConns, redisConns, ursmSessions int) {
	log.Printf("[test-gateway] simulating production memory: target=%d MB pg=%d conn redis=%d ursm=%d",
		totalMiB, pgConns, redisConns, ursmSessions)

	pgPool := make([][]byte, pgConns)
	for i := range pgPool {
		// 5 MB per PG connection.
		buf := make([]byte, 5*1024*1024)
		// Touch to ensure pages are resident.
		for j := range buf {
			buf[j] = byte(i + j)
		}
		pgPool[i] = buf
	}
	log.Printf("[test-gateway]   pgxpool: %d conns × 5 MB = %d MB",
		pgConns, pgConns*5)

	redisPool := make([][]byte, redisConns)
	for i := range redisPool {
		buf := make([]byte, 100*1024)
		for j := range buf {
			buf[j] = byte(i)
		}
		redisPool[i] = buf
	}
	log.Printf("[test-gateway]   redis:   %d conns × 100 KB = %d MB",
		redisConns, redisConns/10)

	ursmStore := make([]byte, ursmSessions*256)
	for i := range ursmStore {
		ursmStore[i] = byte(i)
	}
	log.Printf("[test-gateway]   ursm:    %d sessions × 256 B = %d MB",
		ursmSessions, ursmSessions*256/(1024*1024))

	// Keep refs alive at package scope via globals.
	prodMemRefs.Lock()
	prodMemRefs.pgPool = pgPool
	prodMemRefs.redisPool = redisPool
	prodMemRefs.ursmStore = ursmStore
	prodMemRefs.Unlock()
	log.Printf("[test-gateway]   total simulated: %d MB live",
		pgConns*5+redisConns/10+ursmSessions*256/(1024*1024))
}

var prodMemRefs struct {
	sync.Mutex
	pgPool    [][]byte
	redisPool [][]byte
	ursmStore []byte
}

// handleMemStats returns runtime memory stats so the validation harness
// can measure heap pressure under GOMEMLIMIT.
func handleMemStats(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"alloc_mb":       ms.Alloc / 1e6,
		"sys_mb":         ms.Sys / 1e6,
		"heap_alloc_mb":  ms.HeapAlloc / 1e6,
		"heap_inuse_mb":  ms.HeapInuse / 1e6,
		"heap_objects":   ms.HeapObjects,
		"num_gc":         ms.NumGC,
		"gc_pause_total": ms.PauseTotalNs,
		"gc_pause_last":  ms.PauseNs[(ms.NumGC+255)%256],
		"goroutines":     runtime.NumGoroutine(),
		"heap_sys_mb":    ms.HeapSys / 1e6,
		"next_gc_mb":     ms.NextGC / 1e6,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

var rng = rand.New(rand.NewSource(time.Now().UnixNano()))
var rngMu sync.Mutex

// rngFloat returns [0,1). Uses math/rand, not UnixNano%1e6 — the latter
// collides under concurrent picks and biased s15 toward whatever
// credential happened to be first in the map walk.
func rngFloat() float64 {
	rngMu.Lock()
	v := rng.Float64()
	rngMu.Unlock()
	return v
}
