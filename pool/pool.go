// Package pool implements identity-bound HTTP connection pools.
//
// Each pool is keyed by (identity_hash[:16], provider_id, credential_id) so
// that connections are isolated per virtual identity + provider + credential.
// This prevents credential mix-up and allows per-identity connection limits.
package pool

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxIdleConnsPerHost = 16
	maxConnsPerHost     = 64
	idleConnTimeout     = 90 * time.Second
	healthCheckInterval = 30 * time.Second
	healthCheckTimeout  = 5 * time.Second
	degradedThreshold   = 3
	deadThreshold       = 10 // Consecutive failures to mark pool as dead
	successThreshold    = 3  // Consecutive successes to recover from degraded
	poolIdleTTL         = 3 * time.Minute
	poolMaxPools        = 256
	poolEvictInterval   = 30 * time.Second
	poolMaxActiveConns  = 32
)

var ErrPoolClosed = errors.New("pool closed")

// PoolState represents the health state of a connection pool.
type PoolState int32

const (
	PoolActive   PoolState = 0
	PoolDegraded PoolState = 1
	PoolDead     PoolState = 2
)

func (s PoolState) String() string {
	switch s {
	case PoolActive:
		return "active"
	case PoolDegraded:
		return "degraded"
	case PoolDead:
		return "dead"
	default:
		return "unknown"
	}
}

// PoolKey uniquely identifies a connection pool.
type PoolKey struct {
	IdentityHash string
	ProviderID   int
	CredentialID int
}

func (k PoolKey) String() string {
	id := k.IdentityHash
	if len(id) > 16 {
		id = id[:16]
	}
	return fmt.Sprintf("%s/%d/%d", id, k.ProviderID, k.CredentialID)
}

// Pool manages a set of HTTP connections for a specific identity+provider+credential.
type Pool struct {
	key       PoolKey
	transport *http.Transport
	client    *http.Client
	probeURL  string

	state        atomic.Int32
	failCount    atomic.Int32
	successCount atomic.Int32
	lastUsed     atomic.Int64
	mu           sync.Mutex
	stopCh       chan struct{}
	closed       atomic.Bool
	wg           sync.WaitGroup
	activeConns  chan struct{}
}

// NewPool creates a new connection pool.
func NewPool(key PoolKey, probeURL string) *Pool {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		MaxIdleConns:        maxConnsPerHost,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		IdleConnTimeout:     idleConnTimeout,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}

	p := &Pool{
		key:       key,
		transport: transport,
		client:    &http.Client{Transport: transport, Timeout: 120 * time.Second},
		probeURL:  probeURL,
		stopCh:    make(chan struct{}),
		activeConns: make(chan struct{}, poolMaxActiveConns),
	}
	p.state.Store(int32(PoolActive))
	return p
}

// Client returns the HTTP client for this pool.
func (p *Pool) Client() *http.Client { return p.client }

// State returns the current pool health state.
func (p *Pool) State() PoolState { return PoolState(p.state.Load()) }

func (p *Pool) Acquire(ctx context.Context) error {
	if p.closed.Load() {
		return ErrPoolClosed
	}
	select {
	case p.activeConns <- struct{}{}:
		p.touch()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.stopCh:
		return ErrPoolClosed
	}
}

func (p *Pool) Release() {
	select {
	case <-p.activeConns:
	default:
	}
}

func (p *Pool) touch() {
	p.lastUsed.Store(time.Now().UnixMilli())
}

func (p *Pool) LastUsed() time.Time {
	ms := p.lastUsed.Load()
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// StartHealthCheck begins periodic health probing.
func (p *Pool) StartHealthCheck() {
	if p.closed.Load() {
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.healthLoop()
	}()
}

// StopHealthCheck stops periodic health probing.
func (p *Pool) StopHealthCheck() {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

// RecordFailure increments the failure counter and may mark the pool degraded or dead.
func (p *Pool) RecordFailure() {
	count := p.failCount.Add(1)
	p.successCount.Store(0)

	currentState := p.State()

	// Transition to dead if consecutive failures exceed dead threshold
	if count >= deadThreshold && currentState != PoolDead {
		p.state.Store(int32(PoolDead))
		slog.Warn("pool marked dead",
			"key", p.key.String(),
			"failures", count,
		)
		return
	}

	// Transition to degraded if consecutive failures exceed degraded threshold
	if count >= degradedThreshold && currentState == PoolActive {
		p.state.Store(int32(PoolDegraded))
		slog.Warn("pool marked degraded",
			"key", p.key.String(),
			"failures", count,
		)
	}
}

// RecordSuccess resets the failure counter and may recover the pool state.
func (p *Pool) RecordSuccess() {
	p.failCount.Store(0)
	n := p.successCount.Add(1)

	currentState := p.State()

	// Recover from degraded to active after enough consecutive successes
	if currentState == PoolDegraded && n >= successThreshold {
		p.state.Store(int32(PoolActive))
		p.successCount.Store(0)
		slog.Info("pool recovered to active",
			"key", p.key.String(),
			"successes", n,
		)
	}
}

// Close shuts down the pool's idle connections and waits for health loop to stop.
func (p *Pool) Close() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	p.StopHealthCheck()
	p.wg.Wait()
	p.transport.CloseIdleConnections()
}

func (p *Pool) healthLoop() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("pool healthLoop panic", "recover", r)
		}
	}()
	interval := healthCheckInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.probe()
			// Speed up probing when degraded: check every 10s instead of 30s
			newInterval := healthCheckInterval
			if p.State() == PoolDegraded {
				newInterval = 10 * time.Second
			}
			if newInterval != interval {
				interval = newInterval
				ticker.Reset(interval)
			}
		case <-p.stopCh:
			return
		}
	}
}

func (p *Pool) probe() {
	if p.probeURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.probeURL, nil)
	if err != nil {
		return
	}
	resp, err := p.client.Do(req)
	if err != nil || resp.StatusCode >= 500 {
		p.RecordFailure()
		if resp != nil {
			resp.Body.Close()
		}
		return
	}
	resp.Body.Close()
	p.RecordSuccess()
}

// ---------------------------------------------------------------------------
// PoolManager — global registry of identity-bound pools
// ---------------------------------------------------------------------------

// PoolManager manages all connection pools keyed by (identity, provider, credential).
type PoolManager struct {
	mu      sync.RWMutex
	pools   map[PoolKey]*Pool
	stopCh  chan struct{}
	stopped atomic.Bool
	wg      sync.WaitGroup
}

func NewPoolManager() *PoolManager {
	pm := &PoolManager{
		pools:  make(map[PoolKey]*Pool),
		stopCh: make(chan struct{}),
	}
	pm.wg.Add(1)
	go pm.evictLoop()
	return pm
}

// GetOrCreate returns the pool for the given key, creating it if needed.
func (pm *PoolManager) GetOrCreate(key PoolKey, probeURL string) *Pool {
	if pm.stopped.Load() {
		return nil
	}
	pm.mu.RLock()
	p, ok := pm.pools[key]
	pm.mu.RUnlock()
	if ok {
		p.touch()
		return p
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	if p, ok = pm.pools[key]; ok {
		p.touch()
		return p
	}

	if len(pm.pools) >= poolMaxPools {
		pm.evictOldestLocked()
	}

	p = NewPool(key, probeURL)
	p.touch()
	p.StartHealthCheck()
	pm.pools[key] = p
	slog.Info("pool created", "key", key.String(), "probe", probeURL, "total", len(pm.pools))
	return p
}

// Get returns the pool for the given key, or nil.
func (pm *PoolManager) Get(key PoolKey) *Pool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.pools[key]
}

func (pm *PoolManager) evictOldestLocked() {
	var oldestKey PoolKey
	var oldestTime int64 = math.MaxInt64
	for k, p := range pm.pools {
		lu := p.lastUsed.Load()
		if lu < oldestTime {
			oldestTime = lu
			oldestKey = k
		}
	}
	if p, ok := pm.pools[oldestKey]; ok {
		p.Close()
		delete(pm.pools, oldestKey)
		slog.Info("pool evicted (max reached)", "key", oldestKey.String())
	}
}

func (pm *PoolManager) evictLoop() {
	defer pm.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("pool evictLoop panic", "recover", r)
		}
	}()
	ticker := time.NewTicker(poolEvictInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			pm.evictIdle()
		case <-pm.stopCh:
			return
		}
	}
}

func (pm *PoolManager) evictIdle() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	now := time.Now()
	for key, p := range pm.pools {
		lu := p.LastUsed()
		if !lu.IsZero() && now.Sub(lu) > poolIdleTTL {
			p.Close()
			delete(pm.pools, key)
			slog.Info("pool evicted (idle)", "key", key.String(), "idle_for", now.Sub(lu).Round(time.Second))
		}
	}
}

func (pm *PoolManager) Stop() {
	if !pm.stopped.CompareAndSwap(false, true) {
		return
	}
	close(pm.stopCh)
	pm.wg.Wait()
}

// CloseAll stops and closes all pools.
func (pm *PoolManager) CloseAll() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for key, p := range pm.pools {
		p.Close()
		delete(pm.pools, key)
	}
	pm.pools = make(map[PoolKey]*Pool)
}

// Stats returns the count of pools by state.
func (pm *PoolManager) Stats() map[string]int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	stats := map[string]int{"active": 0, "degraded": 0, "dead": 0}
	for _, p := range pm.pools {
		switch p.State() {
		case PoolActive:
			stats["active"]++
		case PoolDegraded:
			stats["degraded"]++
		case PoolDead:
			stats["dead"]++
		}
	}
	return stats
}
