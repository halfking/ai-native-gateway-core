package smartretry

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
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
	now   func() time.Time
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
	return &Policy{cfg: cfg, stats: make(map[string]*ProviderStats), now: time.Now}
}
func NewDefault() *Policy { return New(DefaultConfig()) }

func (p *Policy) SetClock(now func() time.Time) {
	if now != nil {
		p.mu.Lock()
		p.now = now
		p.mu.Unlock()
	}
}

func (p *Policy) RecordSuccess(provider string, latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.getLocked(provider)
	s.requests++
	s.successes++
	s.latencyTotal += latency
	s.SuccessRate = float64(s.successes) / float64(s.requests)
	s.AvgLatency = s.latencyTotal / time.Duration(s.requests)
	s.TimeoutRate = rollingRate(s.TimeoutRate, s.requests, false)
	s.ErrorRate5xx = rollingRate(s.ErrorRate5xx, s.requests, false)
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
	s.LastErrorTime = p.now()
	s.TimeoutRate = rollingRate(s.TimeoutRate, s.requests, isTimeout(err))
	s.ErrorRate5xx = rollingRate(s.ErrorRate5xx, s.requests, is5xx(err))
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
	return true, retryAfterOrBackoff(err, attempt, p.cfg, p.now)
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
func retryAfterOrBackoff(err error, attempt int, cfg Config, now func() time.Time) time.Duration {
	var h *streamretry.HTTPError
	if errors.As(err, &h) && h.RetryAfter != "" {
		clock := time.Now
		if now != nil {
			clock = now
		}
		if delay, ok := parseRetryAfter(h.RetryAfter, clock(), cfg.MaxDelay); ok {
			return delay
		}
	}
	d := float64(cfg.BaseDelay) * math.Pow(2, float64(attempt))
	if d > float64(cfg.MaxDelay) {
		d = float64(cfg.MaxDelay)
	}
	return time.Duration(d)
}

func parseRetryAfter(value string, now time.Time, max time.Duration) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return 0, false
		}
		delay := time.Duration(seconds) * time.Second
		if delay > max {
			delay = max
		}
		return delay, true
	}
	at, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := at.Sub(now)
	if delay < 0 {
		return 0, true
	}
	if delay > max {
		delay = max
	}
	return delay, true
}
