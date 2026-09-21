package provider

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestCanServeCandidateCacheDuringOutage pins the DB-outage window contract
// (2026-09-04): retryable infrastructure errors may serve expired non-empty
// entries up to candidateOutageGrace, but dead contexts, non-retryable
// errors, empty entries, and entries past the window may not.
func TestCanServeCandidateCacheDuringOutage(t *testing.T) {
	nonEmptyValue := &resolveResponse{
		PlanOrder: []struct {
			CredentialID int    `json:"credential_id"`
			ProviderID   int    `json:"provider_id"`
			RawModel     string `json:"raw_model"`
			Tier         int    `json:"tier"`
		}{{CredentialID: 7}},
		Candidates: []json.RawMessage{{}},
	}
	nonEmpty := cacheEntry[*resolveResponse]{
		value:   nonEmptyValue,
		expires: time.Now().Add(-10 * time.Minute),
	}
	retryable := errors.New("dial tcp 10.0.0.1:5432: connection refused")
	nonRetryable := errors.New("syntax error in SQL statement")

	cases := []struct {
		name  string
		ctx   context.Context
		err   error
		entry cacheEntry[*resolveResponse]
		want  bool
	}{
		{"expired entry inside outage window", context.Background(), retryable, nonEmpty, true},
		{"entry just past outage window", context.Background(), retryable, cacheEntry[*resolveResponse]{value: nonEmpty.value, expires: time.Now().Add(-(candidateOutageGrace + time.Minute))}, false},
		{"non-retryable error", context.Background(), nonRetryable, nonEmpty, false},
		{"cancelled context", canceledCtx(), retryable, nonEmpty, false},
		{"empty entry", context.Background(), retryable, cacheEntry[*resolveResponse]{expires: time.Now()}, false},
		{"zero expiry", context.Background(), retryable, cacheEntry[*resolveResponse]{value: nonEmpty.value}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canServeCandidateCacheDuringOutage(tc.ctx, tc.err, tc.entry, time.Now()); got != tc.want {
				t.Fatalf("canServeCandidateCacheDuringOutage = %v, want %v", got, tc.want)
			}
		})
	}
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// TestRevealStaleServeAndNegativeCacheSkip pins the reveal-side outage gear:
// an expired positive keyCache entry serves within revealOutageGrace on a
// retryable DB error, and retryable DB errors never poison the negative
// cache (which would otherwise block both recovery and stale serving).
func TestRevealStaleServeAndNegativeCacheSkip(t *testing.T) {
	c := NewClient()
	c.mu.Lock()
	c.keyCache[42] = cacheEntry[string]{
		value:   "sk-upstream-secret",
		expires: time.Now().Add(-2 * time.Minute), // positive TTL (5m) elapsed
	}
	c.mu.Unlock()

	key, ok := c.getStaleRevealedKey(42)
	if !ok || key != "sk-upstream-secret" {
		t.Fatalf("getStaleRevealedKey = (%q, %v), want stale serve", key, ok)
	}

	// Past the outage window: refuse.
	c.mu.Lock()
	c.keyCache[43] = cacheEntry[string]{
		value:   "sk-too-old",
		expires: time.Now().Add(-(revealOutageGrace + time.Minute)),
	}
	c.mu.Unlock()
	if _, ok := c.getStaleRevealedKey(43); ok {
		t.Fatal("entry past revealOutageGrace must not serve")
	}

	// Retryable DB error must NOT be negative-cached…
	retryable := errors.New("dial tcp 10.0.0.1:5432: connection refused")
	c.cacheRevealFailureIfCurrent(42, c.keyGeneration[42], retryable)
	c.mu.RLock()
	_, negCached := c.keyCacheNeg[42]
	c.mu.RUnlock()
	if negCached {
		t.Fatal("retryable DB error must not poison the negative cache")
	}

	// …while genuine decrypt failures still are.
	decryptErr := errors.New("reveal decrypt: cipher: message authentication failed")
	c.cacheRevealFailureIfCurrent(42, c.keyGeneration[42], decryptErr)
	c.mu.RLock()
	_, negCached = c.keyCacheNeg[42]
	c.mu.RUnlock()
	if !negCached {
		t.Fatal("decrypt failure must still be negative-cached")
	}
}
