package intent

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ChainedRecognizer orchestrates all three layers with fallback and caching.
//
// Architecture:
//  1. Check cache (if enabled)
//  2. Layer 1: Rule-based (always runs, <1ms)
//  3. Layer 2: Context-based (runs if confidence < threshold)
//  4. Layer 3: Tool-based (LLM, runs if still < threshold)
//  5. Cache result (if enabled)
//
// Design inspired by:
//  - Rasa: NLU pipeline with confidence thresholds
//  - Haystack: Multi-stage router with fallback
//  - Chain of Responsibility pattern
type ChainedRecognizer struct {
	config      ChainConfig
	ruleLayer   *RuleRecognizer
	contextLayer *ContextRecognizer
	toolLayer   *ToolRecognizer
	cache       IntentCache
	mu          sync.RWMutex // Protects stats
	stats       ChainStats
}

// ChainStats tracks performance metrics across all layers.
type ChainStats struct {
	TotalRequests    int64
	CacheHits        int64
	RuleLayerUsed    int64
	ContextLayerUsed int64
	ToolLayerUsed    int64
	AvgLatency       time.Duration
	TotalCostUSD     float64
}

// NewChainedRecognizer creates a complete 3-layer pipeline.
func NewChainedRecognizer(
	ruleLayer *RuleRecognizer,
	contextLayer *ContextRecognizer,
	toolLayer *ToolRecognizer,
	config ChainConfig,
) *ChainedRecognizer {
	if config.ConfidenceThreshold == 0 {
		config = DefaultChainConfig()
	}

	var cache IntentCache
	if config.EnableCaching {
		cache = NewInMemoryCache(config.CacheTTL)
	}

	return &ChainedRecognizer{
		config:       config,
		ruleLayer:    ruleLayer,
		contextLayer: contextLayer,
		toolLayer:    toolLayer,
		cache:        cache,
		stats:        ChainStats{},
	}
}

// Recognize implements IntentRecognizer for the chained pipeline.
func (c *ChainedRecognizer) Recognize(ctx context.Context, signals RecognitionSignals) (*IntentResult, error) {
	start := time.Now()
	c.incrementTotalRequests()

	// Check cache first
	if c.cache != nil && signals.SessionID != "" {
		if cached, found := c.cache.Get(signals.SessionID); found {
			c.incrementCacheHits()
			cached.Latency = time.Since(start)
			cached.Layer = "chained(cached)"
			return cached, nil
		}
	}

	// Apply overall timeout
	if c.config.MaxLatency > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.MaxLatency)
		defer cancel()
	}

	// Layer 1: Rule-based (always runs)
	ruleResult, err := c.ruleLayer.Recognize(ctx, signals)
	if err != nil {
		return c.handleError(err, signals)
	}

	c.incrementRuleLayerUsed()

	// Check if rule layer has sufficient confidence
	if ruleResult.Confidence >= c.config.ConfidenceThreshold {
		result := c.finalizeResult(ruleResult, "chained(rule)", start)
		c.cacheResult(signals.SessionID, result)
		return result, nil
	}

	// Layer 2: Context-based
	contextResult, err := c.contextLayer.Recognize(ctx, signals)
	if err != nil {
		// Context layer failed, but we have rule result as fallback
		result := c.finalizeResult(ruleResult, "chained(rule-fallback)", start)
		c.cacheResult(signals.SessionID, result)
		return result, nil
	}

	c.incrementContextLayerUsed()

	// Check if context layer has sufficient confidence
	if contextResult.Confidence >= c.config.ConfidenceThreshold {
		result := c.finalizeResult(contextResult, "chained(context)", start)
		c.cacheResult(signals.SessionID, result)
		return result, nil
	}

	// Layer 3: Tool-based (LLM)
	// Only proceed if we still have time budget
	elapsed := time.Since(start)
	if c.config.MaxLatency > 0 && elapsed > c.config.MaxLatency*8/10 {
		// Less than 20% of budget remains, skip expensive LLM call
		result := c.finalizeResult(contextResult, "chained(context-timeout)", start)
		c.cacheResult(signals.SessionID, result)
		return result, nil
	}

	if c.toolLayer != nil {
		toolResult, err := c.toolLayer.Recognize(ctx, signals)
		if err != nil {
			// Tool layer failed, use context result as fallback
			result := c.finalizeResult(contextResult, "chained(context-fallback)", start)
			c.cacheResult(signals.SessionID, result)
			return result, nil
		}

		c.incrementToolLayerUsed()
		c.addCost(toolResult)

		// Tool layer succeeded
		result := c.finalizeResult(toolResult, "chained(tool)", start)
		c.cacheResult(signals.SessionID, result)
		return result, nil
	}

	// No tool layer configured, use context result
	result := c.finalizeResult(contextResult, "chained(context-final)", start)
	c.cacheResult(signals.SessionID, result)
	return result, nil
}

// handleError returns a fallback result when all layers fail.
func (c *ChainedRecognizer) handleError(err error, signals RecognitionSignals) (*IntentResult, error) {
	return &IntentResult{
		Primary:    c.config.FallbackIntent,
		Confidence: 0.1,
		Layer:      "chained(error)",
		Reason:     fmt.Sprintf("all layers failed, using fallback: %v", err),
		Metadata: map[string]interface{}{
			"error": err.Error(),
		},
	}, nil
}

// finalizeResult updates the result's layer and latency before returning.
func (c *ChainedRecognizer) finalizeResult(result *IntentResult, layer string, start time.Time) *IntentResult {
	result.Layer = layer
	result.Latency = time.Since(start)
	c.updateAvgLatency(result.Latency)
	return result
}

// cacheResult stores the result in cache if enabled.
func (c *ChainedRecognizer) cacheResult(sessionID string, result *IntentResult) {
	if c.cache != nil && sessionID != "" {
		c.cache.Set(sessionID, result)
	}
}

// Statistics tracking methods

func (c *ChainedRecognizer) incrementTotalRequests() {
	c.mu.Lock()
	c.stats.TotalRequests++
	c.mu.Unlock()
}

func (c *ChainedRecognizer) incrementCacheHits() {
	c.mu.Lock()
	c.stats.CacheHits++
	c.mu.Unlock()
}

func (c *ChainedRecognizer) incrementRuleLayerUsed() {
	c.mu.Lock()
	c.stats.RuleLayerUsed++
	c.mu.Unlock()
}

func (c *ChainedRecognizer) incrementContextLayerUsed() {
	c.mu.Lock()
	c.stats.ContextLayerUsed++
	c.mu.Unlock()
}

func (c *ChainedRecognizer) incrementToolLayerUsed() {
	c.mu.Lock()
	c.stats.ToolLayerUsed++
	c.mu.Unlock()
}

func (c *ChainedRecognizer) updateAvgLatency(latency time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Simple moving average
	if c.stats.TotalRequests == 1 {
		c.stats.AvgLatency = latency
	} else {
		c.stats.AvgLatency = (c.stats.AvgLatency*time.Duration(c.stats.TotalRequests-1) + latency) / time.Duration(c.stats.TotalRequests)
	}
}

func (c *ChainedRecognizer) addCost(result *IntentResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cost, ok := result.Metadata["cost_usd"].(float64); ok {
		c.stats.TotalCostUSD += cost
	}
}

// GetStats returns a copy of current statistics.
func (c *ChainedRecognizer) GetStats() ChainStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stats
}

// ResetStats clears all statistics.
func (c *ChainedRecognizer) ResetStats() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats = ChainStats{}
}

func (c *ChainedRecognizer) Name() string { return "chained" }
func (c *ChainedRecognizer) Layer() int   { return 0 } // Meta-layer

// InMemoryCache implements a simple TTL-based cache.
type InMemoryCache struct {
	mu      sync.RWMutex
	cache   map[string]*cacheEntry
	ttl     time.Duration
	janitor *time.Ticker
	stop    chan bool
}

type cacheEntry struct {
	result    *IntentResult
	expiresAt time.Time
}

// NewInMemoryCache creates a new cache with TTL-based expiration.
func NewInMemoryCache(ttl time.Duration) *InMemoryCache {
	c := &InMemoryCache{
		cache: make(map[string]*cacheEntry),
		ttl:   ttl,
		stop:  make(chan bool),
	}

	// Start janitor to clean expired entries
	c.janitor = time.NewTicker(ttl / 2)
	go c.runJanitor()

	return c
}

func (c *InMemoryCache) Get(sessionID string) (*IntentResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, found := c.cache[sessionID]
	if !found {
		return nil, false
	}

	if time.Now().After(entry.expiresAt) {
		return nil, false
	}

	// Return a copy to prevent mutation
	return c.cloneResult(entry.result), true
}

func (c *InMemoryCache) Set(sessionID string, result *IntentResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache[sessionID] = &cacheEntry{
		result:    c.cloneResult(result),
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *InMemoryCache) Invalidate(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, sessionID)
}

func (c *InMemoryCache) cloneResult(result *IntentResult) *IntentResult {
	// Shallow clone is sufficient for our use case
	clone := *result
	if result.Alternatives != nil {
		clone.Alternatives = make([]IntentScore, len(result.Alternatives))
		copy(clone.Alternatives, result.Alternatives)
	}
	if result.Metadata != nil {
		clone.Metadata = make(map[string]interface{})
		for k, v := range result.Metadata {
			clone.Metadata[k] = v
		}
	}
	return &clone
}

func (c *InMemoryCache) runJanitor() {
	for {
		select {
		case <-c.janitor.C:
			c.cleanup()
		case <-c.stop:
			return
		}
	}
}

func (c *InMemoryCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for sessionID, entry := range c.cache {
		if now.After(entry.expiresAt) {
			delete(c.cache, sessionID)
		}
	}
}

// Stop halts the cache janitor.
func (c *InMemoryCache) Stop() {
	c.janitor.Stop()
	close(c.stop)
}
