package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIsRetryableDBError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "ErrNoRows should not retry",
			err:      pgx.ErrNoRows,
			expected: false,
		},
		{
			name:     "context deadline exceeded should retry",
			err:      context.DeadlineExceeded,
			expected: true,
		},
		{
			name:     "connection refused should retry",
			err:      errors.New("connection refused"),
			expected: true,
		},
		{
			name:     "timeout should retry",
			err:      errors.New("i/o timeout"),
			expected: true,
		},
		{
			name:     "connection reset should retry",
			err:      errors.New("connection reset by peer"),
			expected: true,
		},
		{
			name:     "broken pipe should retry",
			err:      errors.New("broken pipe"),
			expected: true,
		},
		{
			name:     "SQL syntax error should not retry",
			err:      errors.New("syntax error at or near"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableDBError(tt.err)
			if got != tt.expected {
				t.Errorf("isRetryableDBError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestWaitForRetryReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := waitForRetry(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForRetry() error = %v, want context.Canceled", err)
	}
}

func BenchmarkIsRetryableDBError(b *testing.B) {
	err := errors.New("connection refused")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isRetryableDBError(err)
	}
}

func nonEmptyResolveResponse() *resolveResponse {
	return &resolveResponse{
		PlanOrder: []struct {
			CredentialID int    `json:"credential_id"`
			ProviderID   int    `json:"provider_id"`
			RawModel     string `json:"raw_model"`
			Tier         int    `json:"tier"`
		}{{CredentialID: 1, ProviderID: 1, RawModel: "gpt-4", Tier: 1}},
		Candidates: []json.RawMessage{json.RawMessage(`{"credential_id":1}`)},
	}
}

func TestCandidateCacheSuccessfulEmptyResponseUsesBoundedStaleEntry(t *testing.T) {
	existing := nonEmptyResolveResponse()
	empty := &resolveResponse{}
	client := &Client{candCache: map[string]cacheEntry[*resolveResponse]{
		"model": {value: existing, expires: time.Now().Add(-time.Second)},
	}}

	got, _, err := client.fetchCandidateGeneration("model", 0, func() (*resolveResponse, error) {
		return empty, nil
	})
	if err != nil {
		t.Fatalf("empty lookup error = %v", err)
	}
	if got != existing {
		t.Fatalf("successful transient empty lookup = %#v, want bounded stale response", got)
	}
	client.mu.RLock()
	cached := client.candCache["model"].value
	client.mu.RUnlock()
	if cached != existing {
		t.Fatalf("transient empty lookup overwrote old cache: %#v", cached)
	}
}

func TestCandidateCacheSuccessfulEmptyResponseReplacesExpiredStaleEntry(t *testing.T) {
	existing := nonEmptyResolveResponse()
	empty := &resolveResponse{}
	client := &Client{candCache: map[string]cacheEntry[*resolveResponse]{
		"model": {value: existing, expires: time.Now().Add(-candidateCacheStaleGrace)},
	}}

	got, _, err := client.fetchCandidateGeneration("model", 0, func() (*resolveResponse, error) {
		return empty, nil
	})
	if err != nil {
		t.Fatalf("empty lookup error = %v", err)
	}
	if got != empty {
		t.Fatalf("empty lookup after stale grace = %#v, want fresh empty response", got)
	}
	client.mu.RLock()
	cached := client.candCache["model"].value
	client.mu.RUnlock()
	if cached != empty || candidateResponseNonEmpty(cached) {
		t.Fatalf("empty lookup after stale grace did not replace old cache: %#v", cached)
	}
}

func TestCandidateCacheGenerationBlocksInFlightWriteback(t *testing.T) {
	client := &Client{candCache: make(map[string]cacheEntry[*resolveResponse]), candGeneration: 2}
	resp, _, err := client.fetchCandidateGeneration("model", 1, func() (*resolveResponse, error) {
		return nonEmptyResolveResponse(), nil
	})
	if resp != nil || !errors.Is(err, errCandidateCacheInvalidated) {
		t.Fatalf("stale generation = (%#v, %v), want nil invalidated error", resp, err)
	}
	if len(client.candCache) != 0 {
		t.Fatal("stale generation wrote into cache")
	}
}

func TestCandidateFlightKeyIncludesGeneration(t *testing.T) {
	oldKey := candidateFlightKey("gpt-4|default", 7)
	newKey := candidateFlightKey("gpt-4|default", 8)
	if oldKey == newKey {
		t.Fatalf("different generations share flight key %q", oldKey)
	}
	if oldKey != "cand:gpt-4|default:g7" {
		t.Fatalf("candidateFlightKey() = %q, want generation-qualified key", oldKey)
	}
}

func TestCandidateGenerationDoesNotShareOldFlight(t *testing.T) {
	client := &Client{candCache: make(map[string]cacheEntry[*resolveResponse])}
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	oldDone := make(chan error, 1)
	var oldCalls atomic.Int32
	var newCalls atomic.Int32

	go func() {
		_, _, err := client.fetchCandidateGeneration("model", 0, func() (*resolveResponse, error) {
			oldCalls.Add(1)
			close(oldStarted)
			<-releaseOld
			return nonEmptyResolveResponse(), nil
		})
		oldDone <- err
	}()
	<-oldStarted

	client.mu.Lock()
	client.candGeneration = 1
	client.mu.Unlock()

	newResp, shared, err := client.fetchCandidateGeneration("model", 1, func() (*resolveResponse, error) {
		newCalls.Add(1)
		return nonEmptyResolveResponse(), nil
	})
	if err != nil {
		t.Fatalf("new generation lookup error = %v", err)
	}
	if shared {
		t.Fatal("new generation lookup unexpectedly shared old flight")
	}
	if newResp == nil || newCalls.Load() != 1 {
		t.Fatalf("new generation fetch calls = %d, response = %#v", newCalls.Load(), newResp)
	}

	close(releaseOld)
	if err := <-oldDone; !errors.Is(err, errCandidateCacheInvalidated) {
		t.Fatalf("old generation error = %v, want invalidated error", err)
	}
	if oldCalls.Load() != 1 {
		t.Fatalf("old generation fetch calls = %d, want 1", oldCalls.Load())
	}
}

func TestCandidateGenerationMismatchDoesNotReturnOrWriteInFlightResult(t *testing.T) {
	client := &Client{candCache: make(map[string]cacheEntry[*resolveResponse])}
	started := make(chan struct{})
	release := make(chan struct{})
	staleResp := nonEmptyResolveResponse()
	result := make(chan struct {
		resp *resolveResponse
		err  error
	}, 1)

	go func() {
		resp, _, err := client.fetchCandidateGeneration("model", 0, func() (*resolveResponse, error) {
			close(started)
			<-release
			return staleResp, nil
		})
		result <- struct {
			resp *resolveResponse
			err  error
		}{resp: resp, err: err}
	}()
	<-started

	client.mu.Lock()
	client.candGeneration = 1
	client.mu.Unlock()
	close(release)

	got := <-result
	if got.resp != nil {
		t.Fatalf("generation-mismatched response returned = %#v, want nil", got.resp)
	}
	if !errors.Is(got.err, errCandidateCacheInvalidated) {
		t.Fatalf("generation-mismatched error = %v, want invalidated error", got.err)
	}
	client.mu.RLock()
	_, cached := client.candCache["model"]
	client.mu.RUnlock()
	if cached {
		t.Fatal("generation-mismatched response was written to cache")
	}
}

func TestCandidateGenerationRepeatedInvalidationReturnsBoundedError(t *testing.T) {
	client := &Client{candCache: make(map[string]cacheEntry[*resolveResponse])}
	var attempts int
	var invalidatedErr error

	for attempts = 0; attempts < candidateGenerationTries; attempts++ {
		client.mu.RLock()
		generation := client.candGeneration
		client.mu.RUnlock()

		_, _, err := client.fetchCandidateGeneration("model", generation, func() (*resolveResponse, error) {
			client.mu.Lock()
			client.candGeneration++
			client.mu.Unlock()
			return nonEmptyResolveResponse(), nil
		})
		if !errors.Is(err, errCandidateCacheInvalidated) {
			t.Fatalf("attempt %d error = %v, want invalidated error", attempts+1, err)
		}
		invalidatedErr = err
	}

	err := candidateGenerationRetryError(attempts, invalidatedErr)
	if !errors.Is(err, errCandidateCacheInvalidated) {
		t.Fatalf("bounded retry error = %v, want invalidated cause", err)
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("bounded retry error = %q, want explicit attempt limit", err)
	}
}

func TestCandidateCacheInvalidationAdvancesGeneration(t *testing.T) {
	old := defaultClient
	defer func() { defaultClient = old }()

	client := &Client{
		candCache: map[string]cacheEntry[*resolveResponse]{
			"model": {value: nonEmptyResolveResponse()},
		},
	}
	defaultClient = client

	InvalidateAllCandidateCache()
	if client.candGeneration != 1 || len(client.candCache) != 0 {
		t.Fatalf("all invalidation = generation %d, cache size %d; want generation 1 and empty cache", client.candGeneration, len(client.candCache))
	}
	client.candCache["model"] = cacheEntry[*resolveResponse]{value: nonEmptyResolveResponse()}
	InvalidateCandidateCacheForCredential(1)
	if client.candGeneration != 2 || len(client.candCache) != 0 {
		t.Fatalf("credential invalidation = generation %d, cache size %d; want generation 2 and empty cache", client.candGeneration, len(client.candCache))
	}
}

func TestStaleCandidateCacheAgeIsBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	entry := cacheEntry[*resolveResponse]{value: nonEmptyResolveResponse(), expires: now}

	if !staleCandidateCacheUsable(entry, now.Add(candidateCacheStaleGrace-time.Nanosecond)) {
		t.Fatal("entry inside stale grace should be usable")
	}
	if staleCandidateCacheUsable(entry, now.Add(candidateCacheStaleGrace)) {
		t.Fatal("entry at stale grace boundary must be rejected")
	}
}

func TestCanServeStaleCandidateCacheRequiresRetryableLiveContext(t *testing.T) {
	now := time.Unix(1000, 0)
	entry := cacheEntry[*resolveResponse]{value: nonEmptyResolveResponse(), expires: now}

	if !canServeStaleCandidateCache(context.Background(), errors.New("connection refused"), entry, now.Add(time.Second)) {
		t.Fatal("retryable error with live context should allow stale fallback")
	}
	if canServeStaleCandidateCache(context.Background(), errors.New("syntax error"), entry, now.Add(time.Second)) {
		t.Fatal("non-retryable error must not allow stale fallback")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if canServeStaleCandidateCache(ctx, errors.New("connection refused"), entry, now.Add(time.Second)) {
		t.Fatal("canceled context must not allow stale fallback")
	}
}
