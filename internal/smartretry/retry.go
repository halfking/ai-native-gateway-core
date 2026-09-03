package smartretry

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/streamretry"
)

type Config struct {
	Enabled          bool
	MaxRetries       int
	BaseDelay        time.Duration
	MaxDelay         time.Duration
	HighTimeoutRate  float64
	HighErrorRate5xx float64
}

func DefaultConfig() Config {
	return Config{Enabled: true, MaxRetries: 3, BaseDelay: 500 * time.Millisecond, MaxDelay: 5 * time.Second, HighTimeoutRate: .30, HighErrorRate5xx: .50}
}

type ProviderStats struct {
	Provider          string
	SuccessRate       float64
	AvgLatency        time.Duration
	TimeoutRate       float64
	ErrorRate5xx      float64
	ConsecutiveErrors int
	LastErrorTime     time.Time
	requests          int
	successes         int
	latencyTotal      time.Duration
}

type Policy struct {
	mu    sync.RWMutex
	cfg   Config
	stats map[string]*ProviderStats
}

func New(cfg Config) *Policy {
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 500 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 5 * time.Second
	}
	if cfg.HighTimeoutRate <= 0 {
		cfg.HighTimeoutRate = .30
	}
	if cfg.HighErrorRate5xx <= 0 {
		cfg.HighErrorRate5xx = .50
	}
	return &Policy{cfg: cfg, stats: make(map[string]*ProviderStats)}
}
func NewDefault() *Policy { return New(DefaultConfig()) }

func (p *Policy) RecordSuccess(provider string, latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.getLocked(provider)
	s.requests++
	s.successes++
	s.latencyTotal += latency
	s.SuccessRate = float64(s.successes) / float64(s.requests)
	s.AvgLatency = s.latencyTotal / time.Duration(s.requests)
	s.ConsecutiveErrors = 0
}
func (p *Policy) RecordFailure(provider string, err error, latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.getLocked(provider)
	s.requests++
	s.latencyTotal += latency
	s.SuccessRate = float64(s.successes) / float64(s.requests)
	s.AvgLatency = s.latencyTotal / time.Duration(s.requests)
	s.ConsecutiveErrors++
	s.LastErrorTime = time.Now()
	if isTimeout(err) {
		s.TimeoutRate = rollingRate(s.TimeoutRate, s.requests, true)
	}
	if is5xx(err) {
		s.ErrorRate5xx = rollingRate(s.ErrorRate5xx, s.requests, true)
	}
}
func (p *Policy) Stats(provider string) ProviderStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s := p.stats[provider]
	if s == nil {
		return ProviderStats{Provider: provider}
	}
	return *s
}

func (p *Policy) ShouldRetry(ctx context.Context, provider string, attempt int, err error) (bool, time.Duration) {
	if ctx != nil {
		if e := ctx.Err(); e != nil {
			return false, 0
		}
	}
	if err == nil {
		return false, 0
	}
	if !p.cfg.Enabled {
		return false, 0
	}
	if !classifiableRetriable(err) {
		return false, 0
	}
	s := p.Stats(provider)
	max := p.cfg.MaxRetries
	if isTimeout(err) && s.TimeoutRate >= p.cfg.HighTimeoutRate {
		max = 1
	}
	if is5xx(err) && (s.ErrorRate5xx >= p.cfg.HighErrorRate5xx || s.ConsecutiveErrors > 5) {
		max = 1
	}
	if attempt >= max {
		return false, 0
	}
	return true, retryAfterOrBackoff(err, attempt, p.cfg)
}

func (p *Policy) getLocked(provider string) *ProviderStats {
	s := p.stats[provider]
	if s == nil {
		s = &ProviderStats{Provider: provider}
		p.stats[provider] = s
	}
	return s
}
func rollingRate(old float64, n int, event bool) float64 {
	prev := old * float64(n-1)
	if event {
		prev++
	}
	return prev / float64(n)
}
func isTimeout(err error) bool {
	var n net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &n) && n.Timeout()
}
func is5xx(err error) bool {
	var h *streamretry.HTTPError
	return errors.As(err, &h) && h.StatusCode >= 500 && h.StatusCode <= 599
}
func classifiableRetriable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if isTimeout(err) || is5xx(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var h *streamretry.HTTPError
	if errors.As(err, &h) {
		return h.StatusCode == 408 || h.StatusCode == 425 || h.StatusCode == 429
	}
	return false
}
func retryAfterOrBackoff(err error, attempt int, cfg Config) time.Duration {
	var h *streamretry.HTTPError
	_ = h
	d := float64(cfg.BaseDelay) * math.Pow(2, float64(attempt))
	if d > float64(cfg.MaxDelay) {
		d = float64(cfg.MaxDelay)
	}
	return time.Duration(d)
}
