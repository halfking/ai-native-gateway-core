package admin

import (
	"sync"
	"time"
)

// RateLimiter implements a simple per-key fixed-window rate limit.
// In-memory only; resets on gateway restart.
type RateLimiter struct {
	mu     sync.Mutex
	counts map[string]*window
	limit  int
	win time.Duration
}

type window struct {
	count   int
	resetAt time.Time
}

// NewRateLimiter creates a fixed-window rate limiter.
// limit: max requests per window. window: e.g. 1*time.Minute.
func NewRateLimiter(limit int, win time.Duration) *RateLimiter {
	return &RateLimiter{
		counts: make(map[string]*window),
		limit:  limit,
		win: win,
	}
}

// Allow checks if a request from the given key is permitted.
// Returns false if the limit is exceeded.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	w, ok := r.counts[key]
	if !ok || now.After(w.resetAt) {
		r.counts[key] = &window{count: 1, resetAt: now.Add(r.win)}
		return true
	}
	w.count++
	return w.count <= r.limit
}

// Reset clears the limit for a key (e.g. on successful login).
func (r *RateLimiter) Reset(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.counts, key)
}

// Size returns the number of tracked keys (for tests).
func (r *RateLimiter) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.counts)
}

// Global login rate limiter: 5 attempts per IP per minute.
var loginLimiter = NewRateLimiter(5, time.Minute)
