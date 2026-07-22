package executors

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/hotconfig"
	"github.com/kaixuan/llm-gateway-go/provider"
)

var globalHotCfg atomic.Pointer[hotconfig.Config]

func SetHotConfig(cfg *hotconfig.Config) {
	globalHotCfg.Store(cfg)
}

func LoadHotConfig() *hotconfig.Config {
	return globalHotCfg.Load()
}

type NodeFailoverConfig struct {
	NodeTimeoutSeconds          int
	RetryCount                  int
	SingleNodeRetryDelaySeconds int
}

type NodeJumpEvent struct {
	Type         string `json:"type"`
	FromCredID   int    `json:"from_credential_id"`
	FromProvider int    `json:"from_provider_id"`
	ToCredID     int    `json:"to_credential_id"`
	ToProvider   int    `json:"to_provider_id"`
	Reason       string `json:"reason"`
	Attempt      int    `json:"attempt"`
}

type NodeTracker struct {
	triedCredentials map[int]bool
	triedProviders   map[int]bool
	attempts         int
	startTime        time.Time
	maxRetries       int
	hotCfg           *hotconfig.Config
}

func NewNodeTracker(hotCfg *hotconfig.Config) *NodeTracker {
	cfg := DefaultNodeFailoverConfig()
	if hotCfg != nil {
		cfg = LoadNodeFailoverConfig(hotCfg)
	}
	return &NodeTracker{
		triedCredentials: make(map[int]bool),
		triedProviders:   make(map[int]bool),
		startTime:        time.Now(),
		maxRetries:       cfg.RetryCount,
		hotCfg:           hotCfg,
	}
}

func (nt *NodeTracker) Record(cand provider.Candidate) {
	nt.triedCredentials[cand.CredentialID] = true
	nt.triedProviders[cand.ProviderID] = true
	nt.attempts++
}

func (nt *NodeTracker) HasTriedCredential(cand provider.Candidate) bool {
	return nt.triedCredentials[cand.CredentialID]
}

func (nt *NodeTracker) ShouldFailover(currentCredID int) bool {
	return nt.triedCredentials[currentCredID]
}

func (nt *NodeTracker) Attempts() int {
	return nt.attempts
}

func (nt *NodeTracker) IsSingleNode(candidates []provider.Candidate) bool {
	uniqueCreds := make(map[int]bool)
	for _, c := range candidates {
		uniqueCreds[c.CredentialID] = true
	}
	return len(uniqueCreds) <= 1
}

func (nt *NodeTracker) SingleNodeRetryDelay() time.Duration {
	cfg := DefaultNodeFailoverConfig()
	if nt.hotCfg != nil {
		cfg = LoadNodeFailoverConfig(nt.hotCfg)
	}
	if cfg.SingleNodeRetryDelaySeconds < 5 {
		return 10 * time.Second
	}
	return time.Duration(cfg.SingleNodeRetryDelaySeconds) * time.Second
}

func (nt *NodeTracker) MaxRetries() int {
	cfg := DefaultNodeFailoverConfig()
	if nt.hotCfg != nil {
		cfg = LoadNodeFailoverConfig(nt.hotCfg)
	}
	return cfg.RetryCount
}

func LoadNodeFailoverConfig(hotCfg *hotconfig.Config) NodeFailoverConfig {
	return NodeFailoverConfig{
		NodeTimeoutSeconds:          clampInt(hotCfg.GetInt("llmgw_node_timeout_seconds", 30), 10, 300),
		RetryCount:                  clampInt(hotCfg.GetInt("llmgw_retry_count", 2), 0, 5),
		SingleNodeRetryDelaySeconds: clampInt(hotCfg.GetInt("llmgw_single_node_retry_delay_seconds", 10), 5, 60),
	}
}

func DefaultNodeFailoverConfig() NodeFailoverConfig {
	return NodeFailoverConfig{
		NodeTimeoutSeconds:          30,
		RetryCount:                  2,
		SingleNodeRetryDelaySeconds: 10,
	}
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func LoadRetryKeywords(hotCfg *hotconfig.Config) (continueKeywords, retryKeywords []string) {
	defaultContinue := []string{"继续", "continue", "go", "come on", "请继续", "接着", "keep going", "继续回答", "接着说"}
	defaultRetry := []string{"重试", "retry", "请重试", "再试一次", "try again", "重新回答", "再来"}

	ck := hotCfg.GetString("llmgw_continue_keywords", "")
	if ck == "" {
		continueKeywords = defaultContinue
	} else {
		if err := json.Unmarshal([]byte(ck), &continueKeywords); err != nil {
			slog.Warn("failed to parse llmgw_continue_keywords, using defaults", "error", err)
			continueKeywords = defaultContinue
		}
	}

	rk := hotCfg.GetString("llmgw_retry_keywords", "")
	if rk == "" {
		retryKeywords = defaultRetry
	} else {
		if err := json.Unmarshal([]byte(rk), &retryKeywords); err != nil {
			slog.Warn("failed to parse llmgw_retry_keywords, using defaults", "error", err)
			retryKeywords = defaultRetry
		}
	}

	return continueKeywords, retryKeywords
}

func SendNodeJumpEvent(w http.ResponseWriter, fromCred, fromProv, toCred, toProv int, reason string, attempt int) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	event := NodeJumpEvent{
		Type:         "node_jump",
		FromCredID:   fromCred,
		FromProvider: fromProv,
		ToCredID:     toCred,
		ToProvider:   toProv,
		Reason:       reason,
		Attempt:      attempt,
	}

	data, err := json.Marshal(event)
	if err != nil {
		slog.Warn("failed to marshal node_jump event", "error", err)
		return
	}

	fmt.Fprintf(w, "event: node_jump\ndata: %s\n\n", data)
	flusher.Flush()

	slog.Info("sent node_jump SSE event",
		"from_credential", fromCred,
		"from_provider", fromProv,
		"to_credential", toCred,
		"to_provider", toProv,
		"reason", reason,
		"attempt", attempt,
	)
}

func SendRetrySSE(w http.ResponseWriter, credID, provID int, attempt int, delayMs int) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	payload := map[string]interface{}{
		"type":          "node_retry",
		"credential_id": credID,
		"provider_id":   provID,
		"attempt":       attempt,
		"delay_ms":      delayMs,
	}

	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: node_retry\ndata: %s\n\n", data)
	flusher.Flush()
}

func NodeTimeout(hotCfg *hotconfig.Config) time.Duration {
	cfg := DefaultNodeFailoverConfig()
	if hotCfg != nil {
		cfg = LoadNodeFailoverConfig(hotCfg)
	}
	return time.Duration(cfg.NodeTimeoutSeconds) * time.Second
}

func IsContinuationOrRetry(body []byte, hotCfg *hotconfig.Config) (isContinue bool, isRetry bool) {
	if hotCfg == nil {
		return false, false
	}

	continueKw, retryKw := LoadRetryKeywords(hotCfg)

	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false, false
	}

	var lastUserContent string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			lastUserContent = req.Messages[i].Content
			break
		}
	}

	if lastUserContent == "" {
		return false, false
	}

	for _, kw := range continueKw {
		if contains(lastUserContent, kw) {
			return true, false
		}
	}

	for _, kw := range retryKw {
		if contains(lastUserContent, kw) {
			return false, true
		}
	}

	return false, false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
