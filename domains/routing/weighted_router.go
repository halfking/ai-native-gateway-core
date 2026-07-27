package routing

import (
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/health"
)

// WeightConfig defines parameters for weight calculation.
type WeightConfig struct {
	// BaseWeight is the initial weight (default 1.0).
	BaseWeight float64
	// ErrorRateBaseline is the errors/minute that maps to 50% penalty (default 10).
	ErrorRateBaseline float64
	// LatencyBaseline is the latency (ms) that starts to apply penalty (default 2000ms).
	LatencyBaseline float64
	// MinWeight is the floor for any penalty (default 0.1).
	MinWeight float64
}

// DefaultWeightConfig returns sensible defaults.
func DefaultWeightConfig() WeightConfig {
	return WeightConfig{
		BaseWeight:        1.0,
		ErrorRateBaseline: 10,
		LatencyBaseline:   2000,
		MinWeight:         0.1,
	}
}

// weightCache 缓存 computeWeight 的结果 (1 秒 TTL),避免每次选路重算。
// 通过 atomic.Pointer 原子发布,读侧 Load、写侧 Store 新指针,
// 因此 cachedWeight/cachedAt 的读写无数据竞争,且写者之间 last-writer-wins
// (对 1 秒缓存可接受)。
type weightCache struct {
	weight float64
	at     time.Time
}

// WeightedCandidate represents a routing candidate with health metadata.
type WeightedCandidate struct {
	Candidate      *Candidate
	LatencyTracker *LatencyTracker
	ErrorDetector  *health.ErrorDetector
	// Optional cached values to avoid repeated computations.
	// 用 atomic.Pointer 发布,使 RecordError/RecordSuccess (无锁路径) 与
	// computeWeight (RLock 路径) 之间的缓存读写无竞争。
	cache atomic.Pointer[weightCache]
}

// loadCache 原子读取缓存,未缓存或 nil 返回零值 + ok=false。
func (wc *WeightedCandidate) loadCache() (weight float64, at time.Time, ok bool) {
	c := wc.cache.Load()
	if c == nil {
		return 0, time.Time{}, false
	}
	return c.weight, c.at, true
}

// storeCache 原子发布一份新的缓存快照。
func (wc *WeightedCandidate) storeCache(weight float64, at time.Time) {
	wc.cache.Store(&weightCache{weight: weight, at: at})
}

// invalidateCache 清空缓存 (下次 computeWeight 会重算)。
func (wc *WeightedCandidate) invalidateCache() {
	wc.cache.Store(&weightCache{weight: 0, at: time.Time{}})
}

// WeightedRouter implements weighted round-robin based on dynamic error rates
// and latency penalties.
//
// Weight formula:
//
//	weight = max(MinWeight,
//	             BaseWeight * ErrorRatePenalty * LatencyPenalty)
//
//	where:
//
//	ErrorRatePenalty = max(MinWeight, 1 - errorsPerMin / ErrorRateBaseline)
//	LatencyPenalty   = max(MinWeight, 1 - max(0, avgLatencyMs - LatencyBaseline) / 10000)
type WeightedRouter struct {
	mu      sync.RWMutex
	config  WeightConfig
	rng     *rand.Rand
	rngLock sync.Mutex

	// candidates keyed by credential id for fast lookup
	candidates map[string]*WeightedCandidate
	// candidate order for round-robin fairness
	order []string
}

// NewWeightedRouter creates a new weighted router with default config.
func NewWeightedRouter() *WeightedRouter {
	return NewWeightedRouterWithConfig(DefaultWeightConfig())
}

// NewWeightedRouterWithConfig creates a weighted router with custom config.
func NewWeightedRouterWithConfig(cfg WeightConfig) *WeightedRouter {
	if cfg.BaseWeight <= 0 {
		cfg.BaseWeight = 1.0
	}
	if cfg.ErrorRateBaseline <= 0 {
		cfg.ErrorRateBaseline = 10
	}
	if cfg.LatencyBaseline <= 0 {
		cfg.LatencyBaseline = 2000
	}
	if cfg.MinWeight <= 0 {
		cfg.MinWeight = 0.1
	}

	return &WeightedRouter{
		config:     cfg,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
		candidates: make(map[string]*WeightedCandidate),
	}
}

// Register adds or replaces a candidate in the weighted pool.
func (wr *WeightedRouter) Register(c *Candidate) {
	if c == nil || c.CredentialID == "" {
		return
	}
	wr.mu.Lock()
	defer wr.mu.Unlock()

	if existing, ok := wr.candidates[c.CredentialID]; ok {
		existing.Candidate = c
		// Reset cached weight so the next selection recomputes
		existing.invalidateCache()
		return
	}

	wr.candidates[c.CredentialID] = &WeightedCandidate{
		Candidate:      c,
		LatencyTracker: NewLatencyTracker(),
	}
	wr.order = append(wr.order, c.CredentialID)
}

// RegisterWithDetector adds a candidate pre-wired with an external ErrorDetector.
func (wr *WeightedRouter) RegisterWithDetector(c *Candidate, det *health.ErrorDetector) {
	wr.Register(c)
	wr.mu.Lock()
	defer wr.mu.Unlock()
	if det != nil {
		wr.candidates[c.CredentialID].ErrorDetector = det
	}
}

// NewCandidateFromID is a helper for tests and integration code that builds a
// minimal Candidate from just a credential id.
func NewCandidateFromID(id string) *Candidate {
	return &Candidate{
		CredentialID: id,
		Provider:     "openai",
		Model:        "gpt-4",
	}
}

// UpdateCandidate replaces the candidate pointer without resetting the trackers.
func (wr *WeightedRouter) UpdateCandidate(c *Candidate) {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	if wc, ok := wr.candidates[c.CredentialID]; ok {
		wc.Candidate = c
		wc.invalidateCache()
	}
}

// Unregister removes a candidate from the pool.
func (wr *WeightedRouter) Unregister(credentialID string) {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	if _, ok := wr.candidates[credentialID]; !ok {
		return
	}
	delete(wr.candidates, credentialID)
	for i, id := range wr.order {
		if id == credentialID {
			wr.order = append(wr.order[:i], wr.order[i+1:]...)
			break
		}
	}
}

// RecordLatency records a latency sample for the given credential.
func (wr *WeightedRouter) RecordLatency(credentialID string, latency time.Duration) {
	wr.mu.RLock()
	wc, ok := wr.candidates[credentialID]
	wr.mu.RUnlock()
	if !ok {
		return
	}
	wc.LatencyTracker.Record(latency)
}

// RecordError reports a 5xx (or other error) for the given credential.
// It updates the ErrorDetector's failure count and the last-error timestamp.
func (wr *WeightedRouter) RecordError(credentialID string, statusCode int, err error) {
	wr.mu.RLock()
	wc, ok := wr.candidates[credentialID]
	wr.mu.RUnlock()
	if !ok {
		return
	}
	if wc.ErrorDetector != nil {
		wc.ErrorDetector.OnError(health.ErrorEvent{
			CredentialID: credentialID,
			StatusCode:   statusCode,
			Error:        err,
			Timestamp:    time.Now(),
		})
	}
	wc.invalidateCache()
}

// RecordSuccess reports a successful request.
func (wr *WeightedRouter) RecordSuccess(credentialID string, latency time.Duration) {
	wr.RecordLatency(credentialID, latency)
	wr.mu.RLock()
	wc, ok := wr.candidates[credentialID]
	wr.mu.RUnlock()
	if !ok {
		return
	}
	if wc.ErrorDetector != nil {
		wc.ErrorDetector.OnSuccess(credentialID)
	}
	wc.invalidateCache()
}

// ResetCredential fully resets a credential's failure state, allowing it to
// re-enter routing even if it was previously marked Unhealthy.
// Use this for admin-triggered recovery (e.g., after manual investigation).
func (wr *WeightedRouter) ResetCredential(credentialID string) {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	wc, ok := wr.candidates[credentialID]
	if !ok {
		return
	}
	if wc.ErrorDetector != nil {
		wc.ErrorDetector.ResetFailures(credentialID)
	}
	// Also reset latency tracker for a fresh start.
	wc.LatencyTracker.Reset()
	wc.invalidateCache()
}

// Weight returns the computed weight for a candidate (read-only).
func (wr *WeightedRouter) Weight(credentialID string) float64 {
	wr.mu.RLock()
	wc, ok := wr.candidates[credentialID]
	wr.mu.RUnlock()
	if !ok {
		return 0
	}
	return wr.computeWeight(wc)
}

// computeWeight applies the penalty formula. Must be called with at least RLock or no lock.
// (The caller may hold wr.mu.RLock; we do not re-acquire it here.)
//
// 缓存读写通过 atomic.Pointer,因此即便 RecordError/RecordSuccess 在无锁路径
// 并发失效缓存,这里也不会产生数据竞争。
func (wr *WeightedRouter) computeWeight(wc *WeightedCandidate) float64 {
	// Cache for 1 second to avoid recomputing on every selection.
	if w, at, ok := wc.loadCache(); ok && time.Since(at) < time.Second && w > 0 {
		return w
	}

	// Hard floor: if marked Unhealthy (>= failThreshold consecutive errors), set weight to 0
	// so this credential is excluded from routing entirely.
	if wc.ErrorDetector != nil && wc.ErrorDetector.IsUnhealthy(wc.Candidate.CredentialID) {
		wc.storeCache(0, time.Now())
		return 0
	}

	errorsPerMin := 0.0
	if wc.ErrorDetector != nil {
		errorsPerMin = float64(wc.ErrorDetector.GetErrorsPerMinute(wc.Candidate.CredentialID))
	}

	errorPenalty := 1.0 - (errorsPerMin / wr.config.ErrorRateBaseline)
	if errorPenalty < wr.config.MinWeight {
		errorPenalty = wr.config.MinWeight
	}

	avgLatencyMs := float64(wc.LatencyTracker.Avg()) / float64(time.Millisecond)
	excessMs := avgLatencyMs - wr.config.LatencyBaseline
	if excessMs < 0 {
		excessMs = 0
	}
	latencyPenalty := 1.0 - (excessMs / 10000.0)
	if latencyPenalty < wr.config.MinWeight {
		latencyPenalty = wr.config.MinWeight
	}

	weight := wr.config.BaseWeight * errorPenalty * latencyPenalty
	if weight < wr.config.MinWeight {
		weight = wr.config.MinWeight
	}
	if math.IsNaN(weight) || math.IsInf(weight, 0) {
		weight = wr.config.MinWeight
	}

	wc.storeCache(weight, time.Now())
	return weight
}

// SelectWeighted picks a candidate using weighted random sampling.
// Returns nil when no candidates are registered.
func (wr *WeightedRouter) SelectWeighted() *Candidate {
	wr.mu.RLock()
	defer wr.mu.RUnlock()

	if len(wr.order) == 0 {
		return nil
	}

	// Snapshot weights to avoid holding the lock while computing.
	weights := make([]float64, 0, len(wr.order))
	for _, id := range wr.order {
		wc := wr.candidates[id]
		weights = append(weights, wr.computeWeight(wc))
	}

	totalWeight := 0.0
	for _, w := range weights {
		totalWeight += w
	}

	if totalWeight <= 0 {
		// All weights are zero; fall back to first candidate
		return wr.candidates[wr.order[0]].Candidate
	}

	wr.rngLock.Lock()
	pick := wr.rng.Float64() * totalWeight
	wr.rngLock.Unlock()

	cumWeight := 0.0
	for i, w := range weights {
		cumWeight += w
		if pick < cumWeight {
			return wr.candidates[wr.order[i]].Candidate
		}
	}
	return wr.candidates[wr.order[len(weights)-1]].Candidate
}

// SelectTopN returns up to n candidates ordered by descending weight.
// Useful for shortlist-based routing where downstream code picks the best.
func (wr *WeightedRouter) SelectTopN(n int) []*Candidate {
	wr.mu.RLock()
	defer wr.mu.RUnlock()

	type cw struct {
		c *Candidate
		w float64
	}
	all := make([]cw, 0, len(wr.order))
	for _, id := range wr.order {
		wc := wr.candidates[id]
		all = append(all, cw{c: wc.Candidate, w: wr.computeWeight(wc)})
	}
	// Simple selection sort for top-N
	for i := 0; i < len(all); i++ {
		maxIdx := i
		for j := i + 1; j < len(all); j++ {
			if all[j].w > all[maxIdx].w {
				maxIdx = j
			}
		}
		all[i], all[maxIdx] = all[maxIdx], all[i]
	}

	if n <= 0 || n > len(all) {
		n = len(all)
	}
	out := make([]*Candidate, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, all[i].c)
	}
	return out
}

// Size returns the number of registered candidates.
func (wr *WeightedRouter) Size() int {
	wr.mu.RLock()
	defer wr.mu.RUnlock()
	return len(wr.order)
}

// Stats returns weight stats for observability.
type WeightStats struct {
	CredentialID string
	Weight       float64
	ErrorsPerMin int
	AvgLatencyMs float64
	SampleCount  int
}

// Stats returns per-candidate weight stats for monitoring.
func (wr *WeightedRouter) Stats() []WeightStats {
	wr.mu.RLock()
	defer wr.mu.RUnlock()

	out := make([]WeightStats, 0, len(wr.order))
	for _, id := range wr.order {
		wc := wr.candidates[id]
		stats := WeightStats{
			CredentialID: id,
			Weight:       wr.computeWeight(wc),
		}
		if wc.ErrorDetector != nil {
			stats.ErrorsPerMin = wc.ErrorDetector.GetErrorsPerMinute(id)
		}
		stats.AvgLatencyMs = float64(wc.LatencyTracker.Avg()) / float64(time.Millisecond)
		stats.SampleCount = wc.LatencyTracker.Count()
		out = append(out, stats)
	}
	return out
}
