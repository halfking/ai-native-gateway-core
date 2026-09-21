package tenant

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// HistoricalStats is the minimum data needed to tune a tenant quota.
type HistoricalStats struct {
	ConcurrentP95 float64
	QPSP95        float64
}

type HistoricalStatsSource interface {
	Stats(ctx context.Context, tenantID string, since time.Time) (HistoricalStats, error)
}

type QuotaError struct {
	TenantID   string
	Kind       string
	RetryAfter time.Duration
}

func (e *QuotaError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("tenant %s quota exceeded: %s (retry after %s)", e.TenantID, e.Kind, e.RetryAfter)
	}
	return fmt.Sprintf("tenant %s quota exceeded: %s", e.TenantID, e.Kind)
}

type usageWindow struct {
	requests []time.Time
	tokens   []tokenUse
	active   int
}
type tokenUse struct {
	at    time.Time
	count int64
}

// QuotaChecker is a process-local quota manager. It preserves the original
// checker API while adding bounded QPS, token/minute, concurrency, and dynamic
// quota adjustment support.
type QuotaChecker struct {
	mu     sync.RWMutex
	quotas map[string]*TenantQuota
	usage  map[string]*usageWindow
	stats  HistoricalStatsSource
	now    func() time.Time
}

func NewQuotaChecker() *QuotaChecker {
	return &QuotaChecker{quotas: make(map[string]*TenantQuota), usage: make(map[string]*usageWindow), now: time.Now}
}
func (c *QuotaChecker) SetHistoricalStatsSource(s HistoricalStatsSource) {
	c.mu.Lock()
	c.stats = s
	c.mu.Unlock()
}
func (c *QuotaChecker) SetClock(now func() time.Time) {
	if now != nil {
		c.mu.Lock()
		c.now = now
		c.mu.Unlock()
	}
}
func (c *QuotaChecker) SetQuota(quota *TenantQuota) {
	if quota == nil || quota.TenantID == "" {
		return
	}
	copyQuota := *quota
	if copyQuota.BurstMultiplier <= 0 {
		copyQuota.BurstMultiplier = 1
	}
	c.mu.Lock()
	c.quotas[quota.TenantID] = &copyQuota
	c.mu.Unlock()
}

func (c *QuotaChecker) GetQuota(tenantID string) (*TenantQuota, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	quota, ok := c.quotas[tenantID]
	if !ok {
		return nil, fmt.Errorf("no quota configured for tenant %s", tenantID)
	}
	copyQuota := *quota
	return &copyQuota, nil
}

// CheckQuota validates and accounts a request against QPS, minute-token, and
// daily-token limits. A nil context is treated as Background for compatibility.
func (c *QuotaChecker) CheckQuota(ctx context.Context, tenantID string, tokensRequested int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if tokensRequested < 0 {
		return fmt.Errorf("tokens requested cannot be negative")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	quota, ok := c.quotas[tenantID]
	if !ok {
		return nil
	}
	u := c.usageWindowLocked(tenantID)
	now := c.now()
	if err := checkQuotaLocked(quota, u, tenantID, tokensRequested, now); err != nil {
		return err
	}
	commitUsage(u, now, tokensRequested)
	return nil
}

func (c *QuotaChecker) usageWindowLocked(tenantID string) *usageWindow {
	u := c.usage[tenantID]
	if u == nil {
		u = &usageWindow{}
		c.usage[tenantID] = u
	}
	return u
}

func checkQuotaLocked(quota *TenantQuota, u *usageWindow, tenantID string, tokensRequested int64, now time.Time) error {
	trimUsage(u, now)
	burst := quota.BurstMultiplier
	if burst <= 0 {
		burst = 1
	}
	if quota.MaxQPS > 0 && float64(len(u.requests)+1) > float64(quota.MaxQPS)*burst {
		return &QuotaError{TenantID: tenantID, Kind: "qps", RetryAfter: time.Second}
	}
	if quota.MaxTokensPerMin > 0 && tokensInWindow(u.tokens, now, time.Minute)+tokensRequested > int64(float64(quota.MaxTokensPerMin)*burst) {
		return &QuotaError{TenantID: tenantID, Kind: "tokens_per_minute", RetryAfter: time.Second}
	}
	if quota.MaxTokensPerDay > 0 && tokensInWindow(u.tokens, now, 24*time.Hour)+tokensRequested > int64(float64(quota.MaxTokensPerDay)*burst) {
		return &QuotaError{TenantID: tenantID, Kind: "tokens_per_day", RetryAfter: time.Minute}
	}
	return nil
}

func commitUsage(u *usageWindow, now time.Time, tokensRequested int64) {
	u.requests = append(u.requests, now)
	if tokensRequested > 0 {
		u.tokens = append(u.tokens, tokenUse{at: now, count: tokensRequested})
	}
}

// Acquire atomically reserves one concurrent slot and accounts all request
// usage. A failed admission does not consume QPS or token quota.
func (c *QuotaChecker) Acquire(ctx context.Context, tenantID string, tokensRequested int64) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tokensRequested < 0 {
		return nil, fmt.Errorf("tokens requested cannot be negative")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	quota, ok := c.quotas[tenantID]
	if !ok {
		return func() {}, nil
	}
	u := c.usageWindowLocked(tenantID)
	now := c.now()
	trimUsage(u, now)
	if quota.MaxConcurrent > 0 {
		burst := quota.BurstMultiplier
		if burst <= 0 {
			burst = 1
		}
		limit := int(math.Ceil(float64(quota.MaxConcurrent) * burst))
		if u.active >= limit {
			return nil, &QuotaError{TenantID: tenantID, Kind: "concurrent", RetryAfter: time.Second}
		}
	}
	if err := checkQuotaLocked(quota, u, tenantID, tokensRequested, now); err != nil {
		return nil, err
	}
	commitUsage(u, now, tokensRequested)
	if quota.MaxConcurrent <= 0 {
		return func() {}, nil
	}
	u.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if u.active > 0 {
				u.active--
			}
			c.mu.Unlock()
		})
	}, nil
}

func (c *QuotaChecker) AdjustQuota(ctx context.Context, tenantID string) error {
	c.mu.RLock()
	source := c.stats
	now := c.now()
	current, ok := c.quotas[tenantID]
	c.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no quota configured for tenant %s", tenantID)
	}
	if source == nil {
		return fmt.Errorf("historical stats unavailable")
	}
	stats, err := source.Stats(ctx, tenantID, now.Add(-7*24*time.Hour))
	if err != nil {
		return err
	}
	updated := *current
	if stats.ConcurrentP95 > 0 {
		updated.MaxConcurrent = maxInt(1, int(math.Ceil(stats.ConcurrentP95*1.5)))
	}
	if stats.QPSP95 > 0 {
		updated.MaxQPS = maxInt(1, int(math.Ceil(stats.QPSP95*1.5)))
	}
	updated.HistoricalAvgQPS = stats.QPSP95
	c.SetQuota(&updated)
	return nil
}

func trimUsage(u *usageWindow, now time.Time) {
	cut := now.Add(-24 * time.Hour)
	i := 0
	for i < len(u.requests) && u.requests[i].Before(now.Add(-time.Minute)) {
		i++
	}
	u.requests = u.requests[i:]
	j := 0
	for j < len(u.tokens) && u.tokens[j].at.Before(cut) {
		j++
	}
	u.tokens = u.tokens[j:]
}
func tokensInWindow(tokens []tokenUse, now time.Time, window time.Duration) int64 {
	cut := now.Add(-window)
	var n int64
	for _, t := range tokens {
		if !t.at.Before(cut) {
			n += t.count
		}
	}
	return n
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
