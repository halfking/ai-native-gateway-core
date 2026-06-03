package ratelimit

import (
	"sync"
	"time"
)

// RPMLimiter is the minimal interface used by relay/handler.go.
// Both SlidingWindowLimiter and RedisLimiter satisfy it.
type RPMLimiter interface {
	CheckRPM(keyID int, limit int) bool
	RPMStatus(keyID int, limit int) (used int, remaining int)
}

type SlidingWindowLimiter struct {
	mu        sync.Mutex
	windows   map[int]*rpmWindow
	tokenWins map[int]*tpmWindow
}

type rpmWindow struct {
	timestamps []float64
}

type tpmWindow struct {
	entries []tokenEntry
}

type tokenEntry struct {
	ts     float64
	tokens int
}

func NewSlidingWindowLimiter() *SlidingWindowLimiter {
	return &SlidingWindowLimiter{
		windows:   make(map[int]*rpmWindow),
		tokenWins: make(map[int]*tpmWindow),
	}
}

func (l *SlidingWindowLimiter) CheckRPM(keyID int, limit int) bool {
	if limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := float64(time.Now().UnixMilli()) / 1000.0
	cutoff := now - 60.0

	w, ok := l.windows[keyID]
	if !ok {
		w = &rpmWindow{}
		l.windows[keyID] = w
	}

	if len(w.timestamps) > 0 {
		filtered := w.timestamps[:0]
		for _, t := range w.timestamps {
			if t > cutoff {
				filtered = append(filtered, t)
			}
		}
		w.timestamps = filtered
	}

	if len(w.timestamps) >= limit {
		return false
	}

	w.timestamps = append(w.timestamps, now)
	return true
}

func (l *SlidingWindowLimiter) CheckTPM(keyID int, estimatedTokens int, limit int) bool {
	if limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := float64(time.Now().UnixMilli()) / 1000.0
	cutoff := now - 60.0

	w, ok := l.tokenWins[keyID]
	if !ok {
		w = &tpmWindow{}
		l.tokenWins[keyID] = w
	}

	if len(w.entries) > 0 {
		filtered := w.entries[:0]
		for _, e := range w.entries {
			if e.ts > cutoff {
				filtered = append(filtered, e)
			}
		}
		w.entries = filtered
	}

	currentTotal := 0
	for _, e := range w.entries {
		currentTotal += e.tokens
	}

	if currentTotal+estimatedTokens > limit {
		return false
	}

	w.entries = append(w.entries, tokenEntry{ts: now, tokens: estimatedTokens})
	return true
}

func (l *SlidingWindowLimiter) RPMStatus(keyID int, limit int) (used int, remaining int) {
	if limit <= 0 {
		return 0, -1
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := float64(time.Now().UnixMilli()) / 1000.0
	cutoff := now - 60.0

	w, ok := l.windows[keyID]
	if !ok {
		return 0, limit
	}

	count := 0
	for _, t := range w.timestamps {
		if t > cutoff {
			count++
		}
	}

	rem := limit - count
	if rem < 0 {
		rem = 0
	}
	return count, rem
}

func (l *SlidingWindowLimiter) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.windows = make(map[int]*rpmWindow)
	l.tokenWins = make(map[int]*tpmWindow)
}
