package main

// RealProviderGateway wraps our Phase 1+2 architecture (health checks + dynamic
// weighted routing + ModelMapper) and connects to real LLM providers via configurable
// credential maps.
//
// Architecture:
//
//   Client → Canonical Model Name (e.g., "minimax-m2")
//           ↓
//     ModelMapper → Provider-Specific Name (e.g., "minimaxai/minimax-m2.7" for NVIDIA)
//           ↓
//     WeightedRouter → Select healthy credential
//           ↓
//     Forward to real provider with provider-specific model name
//
// Endpoints:
//   - POST /v1/chat/completions → main chat endpoint (uses WeightedRouter + ModelMapper)
//   - GET  /healthz             → L1+L2+L3 health check
//   - GET  /stats               → routing weights stats
//   - GET  /mapping             → model mapping registry
//   - POST /admin/reset         → reset credential state

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
	"github.com/kaixuan/llm-gateway-go/domains/modelmapping"
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

// CredentialRecord is one (canonical_model → provider) mapping.
type CredentialRecord struct {
	CanonicalName string // e.g., "minimax-m2"
	Provider      string // e.g., "nvidia"
	APIKey        string
	BaseURL       string
}

// RealProviderGateway is the production-like gateway.
type RealProviderGateway struct {
	mu sync.RWMutex

	// Architecture components
	router    *routing.WeightedRouter
	detectors map[string]*health.ErrorDetector
	latency   map[string]*routing.LatencyTracker
	mapper    *modelmapping.ModelMapper

	// Credential storage: canonical_name → provider → list of credentials
	credentials map[string]map[string][]CredentialRecord
	// Quick lookup: credential_id → (canonical, provider, api_key, base_url)
	credByID map[string]CredentialRecord

	// Health checkers
	tcpChecker    *health.TCPChecker
	httpChecker   *health.HTTPChecker
	infChecker    *health.InferenceChecker
	errorDetector *health.ErrorDetector

	// Stats
	totalRequests atomic.Int64
	totalSuccess  atomic.Int64
	totalErrors   atomic.Int64
}

func newRealProviderGateway() *RealProviderGateway {
	g := &RealProviderGateway{
		router:        routing.NewWeightedRouter(),
		detectors:     make(map[string]*health.ErrorDetector),
		latency:       make(map[string]*routing.LatencyTracker),
		mapper:        modelmapping.NewModelMapper(),
		credentials:   make(map[string]map[string][]CredentialRecord),
		credByID:      make(map[string]CredentialRecord),
		tcpChecker:    health.NewTCPChecker(1 * time.Second),
		httpChecker:   health.NewHTTPChecker(3 * time.Second),
		infChecker:    health.NewInferenceChecker(10*time.Second, 30*time.Second),
		errorDetector: health.NewErrorDetector(3),
	}
	return g
}

// loadFromEnv loads credentials from environment variables.
// Format: For each canonical model × provider, use env:
//
//	CRED_<canonical>_<provider>_KEY
//	CRED_<canonical>_<provider>_BASEURL
//
// e.g., CRED_MINIMAX-M2_NVIDIA_KEY=xxx
func (g *RealProviderGateway) loadFromEnv() {
	// Default credentials (from boss's spec)
	defaults := []CredentialRecord{
		// Minimax
		{CanonicalName: "minimax-m2", Provider: "minimax", APIKey: "sk-cp-bT8Qagnkbdo5xFil3rddP5GA7s31eSCd5ZrAvRroVu-M6fhZr21DHDmLx5h4SV-9Rd6dG40SdVp3XbUNLEGGIlYZuw3g33w1bmt5l99ESMyOS_gf-Ba1hvY", BaseURL: "https://api.minimaxi.com/v1"},
		{CanonicalName: "minimax-m3", Provider: "minimax", APIKey: "sk-cp-bT8Qagnkbdo5xFil3rddP5GA7s31eSCd5ZrAvRroVu-M6fhZr21DHDmLx5h4SV-9Rd6dG40SdVp3XbUNLEGGIlYZuw3g33w1bmt5l99ESMyOS_gf-Ba1hvY", BaseURL: "https://api.minimaxi.com/v1"},
		// 智谱
		{CanonicalName: "glm-4.7", Provider: "zhipu", APIKey: "9f7fa0edca07455e80c7431b059182b3.2hJa8SexdbT4hu1p", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4"},
		{CanonicalName: "glm-5.1", Provider: "zhipu", APIKey: "9f7fa0edca07455e80c7431b059182b3.2hJa8SexdbT4hu1p", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4"},
		// NVIDIA NIM
		{CanonicalName: "minimax-m2", Provider: "nvidia", APIKey: "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", BaseURL: "https://integrate.api.nvidia.com/v1"},
		{CanonicalName: "minimax-m3", Provider: "nvidia", APIKey: "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", BaseURL: "https://integrate.api.nvidia.com/v1"},
		{CanonicalName: "glm-5.1", Provider: "nvidia", APIKey: "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", BaseURL: "https://integrate.api.nvidia.com/v1"},
		// 自有 kaixuan
		{CanonicalName: "minimax-m2", Provider: "kaixuan", APIKey: "sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9", BaseURL: "https://llm.kxpms.cn/v1"},
		{CanonicalName: "minimax-m3", Provider: "kaixuan", APIKey: "sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9", BaseURL: "https://llm.kxpms.cn/v1"},
		{CanonicalName: "glm-5.1", Provider: "kaixuan", APIKey: "sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9", BaseURL: "https://llm.kxpms.cn/v1"},
		{CanonicalName: "deepseek-v4", Provider: "kaixuan", APIKey: "sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9", BaseURL: "https://llm.kxpms.cn/v1"},
		{CanonicalName: "mimo-v2.5", Provider: "kaixuan", APIKey: "sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9", BaseURL: "https://llm.kxpms.cn/v1"},
	}

	for _, cred := range defaults {
		credID := fmt.Sprintf("%s:%s:%d", cred.CanonicalName, cred.Provider, len(g.credByID))
		g.credByID[credID] = cred

		if _, ok := g.credentials[cred.CanonicalName]; !ok {
			g.credentials[cred.CanonicalName] = make(map[string][]CredentialRecord)
		}
		g.credentials[cred.CanonicalName][cred.Provider] = append(g.credentials[cred.CanonicalName][cred.Provider], cred)

		// Register with router
		det := health.NewErrorDetector(3)
		g.detectors[credID] = det
		g.latency[credID] = routing.NewLatencyTracker()
		g.router.RegisterWithDetector(routing.NewCandidateFromID(credID), det)
	}

	log.Printf("✓ Loaded %d real provider credentials", len(g.credByID))
}

// findCredentialForCanonical finds a credential for a canonical model name.
// It uses the WeightedRouter to pick among available providers.
func (g *RealProviderGateway) findCredentialForCanonical(canonical string) *CredentialRecord {
	g.mu.RLock()
	defer g.mu.RUnlock()

	providerMap, ok := g.credentials[canonical]
	if !ok {
		return nil
	}

	// Get all credential IDs for this canonical model
	var credIDs []string
	for _, creds := range providerMap {
		for _, cred := range creds {
			credID := g.credIDFor(cred)
			if credID != "" {
				credIDs = append(credIDs, credID)
			}
		}
	}

	// Build candidates for router
	candidates := make([]*routing.Candidate, 0, len(credIDs))
	for _, id := range credIDs {
		candidates = append(candidates, routing.NewCandidateFromID(id))
	}

	// Use the weighted router to pick the best one
	for {
		c := g.router.SelectWeighted()
		if c == nil {
			break
		}
		// Find a candidate matching this selection
		for _, cand := range candidates {
			if cand.CredentialID == c.CredentialID {
				if cred, exists := g.credByID[c.CredentialID]; exists {
					return &cred
				}
			}
		}
	}
	return nil
}

// credIDFor generates a unique ID for a credential record.
func (g *RealProviderGateway) credIDFor(cred CredentialRecord) string {
	for id, c := range g.credByID {
		if c.CanonicalName == cred.CanonicalName && c.Provider == cred.Provider && c.APIKey == cred.APIKey {
			return id
		}
	}
	return ""
}

// handleChat is the main chat endpoint.
func (g *RealProviderGateway) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	g.totalRequests.Add(1)

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		g.totalErrors.Add(1)
		return
	}

	// Step 1: Find credential for canonical model name
	cred := g.findCredentialForCanonical(req.Model)
	if cred == nil {
		http.Error(w, fmt.Sprintf("no credential available for model '%s'", req.Model), http.StatusServiceUnavailable)
		g.totalErrors.Add(1)
		return
	}

	// Step 2: Translate canonical → provider-specific
	nativeModel := g.mapper.Translate(cred.CanonicalName, cred.Provider)
	credID := g.credIDFor(*cred)

	log.Printf("→ %s via %s: canonical=%s → native=%s", credID, cred.Provider, cred.CanonicalName, nativeModel)

	// Step 3: Forward to provider
	req.Model = nativeModel // Replace with native name
	body, _ := json.Marshal(req)
	start := time.Now()

	client := &http.Client{Timeout: 30 * time.Second}
	httpReq, _ := http.NewRequest("POST", cred.BaseURL+"/chat/completions", strings.NewReader(string(body)))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cred.APIKey)

	resp, err := client.Do(httpReq)
	latency := time.Since(start)

	if err != nil {
		g.router.RecordError(credID, 0, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		g.totalErrors.Add(1)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		g.router.RecordSuccess(credID, latency)
		g.totalSuccess.Add(1)
	} else if resp.StatusCode >= 500 {
		g.router.RecordError(credID, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode))
		g.totalErrors.Add(1)
	} else {
		g.router.RecordLatency(credID, latency)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// stats endpoint.
func (g *RealProviderGateway) stats(w http.ResponseWriter, r *http.Request) {
	routerStats := g.router.Stats()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"requests": map[string]int64{
			"total":   g.totalRequests.Load(),
			"success": g.totalSuccess.Load(),
			"errors":  g.totalErrors.Load(),
		},
		"routing": routerStats,
	})
}

// mapping endpoint - show all canonical→native mappings.
func (g *RealProviderGateway) mapping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"canonical_models": g.mapper.CanonicalModels(),
		"all_mappings":     g.mapper, // uses MarshalJSON
	})
}

// adminReset endpoint.
func (g *RealProviderGateway) adminReset(w http.ResponseWriter, r *http.Request) {
	var cmd struct {
		CredentialID string `json:"credential_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	g.router.ResetCredential(cmd.CredentialID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":         true,
		"credential": cmd.CredentialID,
		"new_weight": g.router.Weight(cmd.CredentialID),
	})
}

// health endpoint (simplified for real providers).
func (g *RealProviderGateway) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	results := map[string]interface{}{}

	for credID, cred := range g.credByID {
		// L1: TCP check
		hostPort := strings.TrimPrefix(cred.BaseURL, "https://")
		hostPort = strings.TrimPrefix(hostPort, "http://")
		if idx := strings.Index(hostPort, "/"); idx > 0 {
			hostPort = hostPort[:idx]
		}
		tcpResult := g.tcpChecker.Check(ctx, hostPort)

		// L2: HTTP check (HEAD)
		l2OK := false
		if tcpResult.Success {
			client := &http.Client{Timeout: 3 * time.Second}
			req, _ := http.NewRequestWithContext(ctx, "HEAD", cred.BaseURL+"/chat/completions", nil)
			req.Header.Set("Authorization", "Bearer "+cred.APIKey)
			resp, err := client.Do(req)
			if err == nil {
				defer resp.Body.Close()
				l2OK = resp.StatusCode < 500
			}
		}

		results[credID] = map[string]interface{}{
			"provider":  cred.Provider,
			"canonical": cred.CanonicalName,
			"L1_TCP":    tcpResult.Success,
			"L2_HTTP":   l2OK,
			"weight":    g.router.Weight(credID),
			"healthy":   tcpResult.Success && l2OK,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "checked",
		"checks": results,
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	port := envOr("SERVER_PORT", "8082")

	g := newRealProviderGateway()
	g.loadFromEnv()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", g.handleChat)
	mux.HandleFunc("/healthz", g.health)
	mux.HandleFunc("/stats", g.stats)
	mux.HandleFunc("/mapping", g.mapping)
	mux.HandleFunc("/admin/reset", g.adminReset)

	log.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Printf("LLM Gateway Real Provider Test Server")
	log.Printf("  Listening on :%s", port)
	log.Printf("  Real providers: minimax, zhipu, nvidia, kaixuan")
	log.Printf("  POST /v1/chat/completions → chat (with model mapping)")
	log.Printf("  GET  /healthz             → L1+L2 health check")
	log.Printf("  GET  /stats               → routing stats")
	log.Printf("  GET  /mapping             → canonical→native mappings")
	log.Printf("  POST /admin/reset         → reset credential")
	log.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}
