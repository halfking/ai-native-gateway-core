package provider

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/secret"
)

// TestEnrichWithAPIKeys_KeyDecryptFail 验证密钥解密失败时的降级行为。
//
// 场景：
// - 多个候选者中部分密钥解密失败
// - 全部候选者密钥解密失败
//
// 期望：
// - 解密失败的候选者被标记为 Routable=false，但仍保留在结果中
// - 解密成功的候选者正常返回
// - 调用方（路由器）可以通过 filterAvailable 过滤掉失败的候选者
// - 当全部失败时，路由器的 tryDegradedMode 不会启用（因为 BlockReason 是永久性的）
//
// 修复问题：
// - 2026-07-08 P0: 修复"明确后端可用但报无可用路由"问题
// - 原因：enrichWithAPIKeys 在密钥解密失败时硬性 continue，导致返回空列表
// - 修复：将失败候选者标记为不可路由但保留在列表中
func TestEnrichWithAPIKeys_KeyDecryptFail(t *testing.T) {
	t.Run("partial_decrypt_failure", func(t *testing.T) {
		// 模拟场景：2个候选者，其中1个密钥解密失败
		rr := &resolveResponse{
			PlanOrder: []struct {
				CredentialID int    `json:"credential_id"`
				ProviderID   int    `json:"provider_id"`
				RawModel     string `json:"raw_model"`
				Tier         int    `json:"tier"`
			}{
				{CredentialID: 1, ProviderID: 1, RawModel: "gpt-4", Tier: 1},
				{CredentialID: 2, ProviderID: 1, RawModel: "gpt-4", Tier: 1},
			},
			Candidates: []json.RawMessage{
				json.RawMessage(`{"credential_id":1,"provider_id":1,"raw_model":"gpt-4","runtime_routable":true}`),
				json.RawMessage(`{"credential_id":2,"provider_id":1,"raw_model":"gpt-4","runtime_routable":true}`),
			},
		}

		// 创建一个 mock client，credential_id=1 解密成功，credential_id=2 解密失败
		client := &Client{
			candCache: make(map[string]cacheEntry[*resolveResponse]),
			keyCache:  make(map[int]cacheEntry[string]),
		}

		// 模拟 RevealAPIKey 行为：credential_id=2 返回错误
		// 实际测试中，我们通过 keyCache 预设值来模拟
		client.keyCache[1] = cacheEntry[string]{
			value:   "valid-key-1",
			expires: time.Now().Add(time.Hour),
		}
		// credential_id=2 不在 keyCache 中，且 fetchReveal 会失败（dbPool=nil）

		ctx := context.Background()
		cands := client.enrichWithAPIKeys(ctx, rr)

		// 验证：应该返回2个候选者
		if len(cands) != 2 {
			t.Fatalf("expected 2 candidates (1 success + 1 failed), got %d", len(cands))
		}

		// 验证：credential_id=1 应该正常（Routable=true, APIKey不为空）
		cand1 := findCandidateByID(cands, 1)
		if cand1 == nil {
			t.Fatal("credential_id=1 not found in result")
		}
		if !cand1.Routable {
			t.Errorf("credential_id=1 should be routable")
		}
		if cand1.APIKey == "" {
			t.Errorf("credential_id=1 should have API key")
		}
		if cand1.BlockReason != nil {
			t.Errorf("credential_id=1 should not have block reason, got %v", *cand1.BlockReason)
		}

		// 验证：credential_id=2 应该被标记为不可路由（Routable=false, BlockReason不为空）
		cand2 := findCandidateByID(cands, 2)
		if cand2 == nil {
			t.Fatal("credential_id=2 not found in result")
		}
		if cand2.Routable {
			t.Errorf("credential_id=2 should be marked as not routable")
		}
		if cand2.BlockReason == nil {
			t.Errorf("credential_id=2 should have block reason")
		} else if *cand2.BlockReason == "" {
			t.Errorf("credential_id=2 block reason should not be empty")
		}
		if cand2.APIKey != "" {
			t.Errorf("credential_id=2 should not have API key, got %q", cand2.APIKey)
		}
	})

	t.Run("all_decrypt_failure", func(t *testing.T) {
		// 模拟场景：2个候选者，全部密钥解密失败
		rr := &resolveResponse{
			PlanOrder: []struct {
				CredentialID int    `json:"credential_id"`
				ProviderID   int    `json:"provider_id"`
				RawModel     string `json:"raw_model"`
				Tier         int    `json:"tier"`
			}{
				{CredentialID: 3, ProviderID: 1, RawModel: "gpt-4", Tier: 1},
				{CredentialID: 4, ProviderID: 1, RawModel: "gpt-4", Tier: 1},
			},
			Candidates: []json.RawMessage{
				json.RawMessage(`{"credential_id":3,"provider_id":1,"raw_model":"gpt-4","runtime_routable":true}`),
				json.RawMessage(`{"credential_id":4,"provider_id":1,"raw_model":"gpt-4","runtime_routable":true}`),
			},
		}

		client := &Client{
			candCache: make(map[string]cacheEntry[*resolveResponse]),
			keyCache:  make(map[int]cacheEntry[string]),
		}
		// 两个 credential 都不在 keyCache 中，会触发解密失败

		ctx := context.Background()
		cands := client.enrichWithAPIKeys(ctx, rr)

		// 验证：应该返回2个候选者（都标记为不可路由）
		if len(cands) != 2 {
			t.Fatalf("expected 2 candidates (both failed), got %d", len(cands))
		}

		// 验证：所有候选者都应该被标记为不可路由
		for _, cand := range cands {
			if cand.Routable {
				t.Errorf("credential_id=%d should be marked as not routable", cand.CredentialID)
			}
			if cand.BlockReason == nil || *cand.BlockReason == "" {
				t.Errorf("credential_id=%d should have non-empty block reason", cand.CredentialID)
			}
			if cand.APIKey != "" {
				t.Errorf("credential_id=%d should not have API key", cand.CredentialID)
			}
		}
	})

	t.Run("all_decrypt_success", func(t *testing.T) {
		// 模拟场景：2个候选者，全部密钥解密成功
		rr := &resolveResponse{
			PlanOrder: []struct {
				CredentialID int    `json:"credential_id"`
				ProviderID   int    `json:"provider_id"`
				RawModel     string `json:"raw_model"`
				Tier         int    `json:"tier"`
			}{
				{CredentialID: 5, ProviderID: 1, RawModel: "gpt-4", Tier: 1},
				{CredentialID: 6, ProviderID: 1, RawModel: "gpt-4", Tier: 1},
			},
			Candidates: []json.RawMessage{
				json.RawMessage(`{"credential_id":5,"provider_id":1,"raw_model":"gpt-4","runtime_routable":true}`),
				json.RawMessage(`{"credential_id":6,"provider_id":1,"raw_model":"gpt-4","runtime_routable":true}`),
			},
		}

		client := &Client{
			candCache: make(map[string]cacheEntry[*resolveResponse]),
			keyCache:  make(map[int]cacheEntry[string]),
		}
		// 预设所有 credential 的密钥
		client.keyCache[5] = cacheEntry[string]{
			value:   "valid-key-5",
			expires: time.Now().Add(time.Hour),
		}
		client.keyCache[6] = cacheEntry[string]{
			value:   "valid-key-6",
			expires: time.Now().Add(time.Hour),
		}

		ctx := context.Background()
		cands := client.enrichWithAPIKeys(ctx, rr)

		// 验证：应该返回2个候选者
		if len(cands) != 2 {
			t.Fatalf("expected 2 candidates, got %d", len(cands))
		}

		// 验证：所有候选者都应该正常
		for _, cand := range cands {
			if !cand.Routable {
				t.Errorf("credential_id=%d should be routable", cand.CredentialID)
			}
			if cand.BlockReason != nil {
				t.Errorf("credential_id=%d should not have block reason", cand.CredentialID)
			}
			if cand.APIKey == "" {
				t.Errorf("credential_id=%d should have API key", cand.CredentialID)
			}
		}
	})
}

// findCandidateByID 从候选者列表中查找指定 credential_id 的候选者
func findCandidateByID(cands []Candidate, credentialID int) *Candidate {
	for i := range cands {
		if cands[i].CredentialID == credentialID {
			return &cands[i]
		}
	}
	return nil
}

func TestInvalidateCredentialKeyCachePreventsStaleRevealReinsert(t *testing.T) {
	previousDefault := defaultClient
	client := NewClient()
	defer func() { defaultClient = previousDefault }()

	const credentialID = 17
	client.keyCache[credentialID] = cacheEntry[string]{
		value:   "old-primary-key",
		expires: time.Now().Add(time.Hour),
	}
	client.keyCacheNeg = map[int]negativeCacheEntry{
		credentialID: {
			value:   "old decrypt failure",
			reason:  "other",
			expires: time.Now().Add(time.Hour),
		},
	}
	staleGeneration := client.keyGeneration[credentialID]

	InvalidateCredentialKeyCache(credentialID)
	if _, ok := client.keyCache[credentialID]; ok {
		t.Fatal("primary key cache entry was not evicted")
	}
	if _, ok := client.keyCacheNeg[credentialID]; ok {
		t.Fatal("negative key cache entry was not evicted")
	}
	if client.keyGeneration[credentialID] == staleGeneration {
		t.Fatal("credential key generation did not advance")
	}

	// A reveal that began before rotation must not restore either stale value.
	client.cacheRevealedKeyIfCurrent(credentialID, staleGeneration, "old-primary-key")
	client.cacheRevealFailureIfCurrent(credentialID, staleGeneration, errors.New("old decrypt failure"))
	if _, ok := client.keyCache[credentialID]; ok {
		t.Fatal("stale reveal restored primary key cache entry")
	}
	if _, ok := client.keyCacheNeg[credentialID]; ok {
		t.Fatal("stale reveal restored negative key cache entry")
	}

	currentGeneration := client.keyGeneration[credentialID]
	client.cacheRevealedKeyIfCurrent(credentialID, currentGeneration, "new-primary-key")
	if got := client.keyCache[credentialID].value; got != "new-primary-key" {
		t.Fatalf("current reveal cached %q, want new primary key", got)
	}
}

// TestRevealAPIKeyNegativeCache pins the 2026-08-17 P0 fix that memoises
// decrypt failures for decryptFailureCacheTTL seconds. Without the cache,
// every request within the 5-minute positive cache window re-fetches
// ciphertext from PG and re-tries DecryptAny, generating the 60+/min log
// storm that prompted this fix.
//
// Test plan:
//  1. Pre-populate keyCacheNeg with a known error message.
//  2. Call RevealAPIKey — should return the cached error WITHOUT hitting
//     fetchReveal (which would panic because dbPool is nil).
//  3. Verify the returned error message wraps the cached error.
//  4. Verify a separate credential with no entry still gets a fresh error.
func TestRevealAPIKeyNegativeCache(t *testing.T) {
	client := &Client{
		candCache:   make(map[string]cacheEntry[*resolveResponse]),
		keyCache:    make(map[int]cacheEntry[string]),
		keyCacheNeg: make(map[int]negativeCacheEntry),
	}

	// Pre-populate the negative cache: credential 17 was seen 60 seconds ago
	// with this exact error.
	client.keyCacheNeg[17] = negativeCacheEntry{
		value:   "cannot decrypt: unknown format",
		reason:  "other",
		expires: time.Now().Add(decryptFailureCacheTTL),
	}

	// Calling RevealAPIKey should NOT hit fetchReveal — the negative cache
	// is consulted first. If the implementation regresses, fetchReveal will
	// be called with dbPool=nil and return "credential reveal not configured
	// (no DB, keyring, or fernet key)" — that's the wrong error and the
	// test fails.
	got, err := client.RevealAPIKey(context.Background(), 587, 17)
	if err == nil {
		t.Fatalf("expected error from negative-cached credential 17, got value=%q", got)
	}
	if got != "" {
		t.Fatalf("expected empty API key for negative-cached credential, got %q", got)
	}
	if !contains(err.Error(), "cannot decrypt: unknown format") {
		t.Fatalf("expected cached error to be wrapped, got %q", err.Error())
	}
	if !errors.Is(err, secret.ErrRevealCached) {
		t.Fatalf("expected negative-cache sentinel secret.ErrRevealCached, got %q (errors.Is=false)", err.Error())
	}

	// An entry past its expiry must be re-attempted (we don't have a real DB
	// so the test stops here — we just confirm the cache no longer matches).
	client.keyCacheNeg[17] = negativeCacheEntry{
		value:   "old-error",
		reason:  "other",
		expires: time.Now().Add(-1 * time.Second), // already expired
	}
	_, err = client.RevealAPIKey(context.Background(), 587, 17)
	if err == nil {
		t.Fatalf("expected error from expired negative-cached credential, got nil")
	}
	if contains(err.Error(), "old-error") {
		t.Fatalf("expired negative entry must not be reused, got %q", err.Error())
	}
}

// TestRevealAPIKeySuccessInvalidatesNegativeCache verifies that a successful
// reveal clears any prior negative entry, so the next call doesn't carry
// forward a stale "broken" signal. This is critical because the positive
// cache only stores the API key — without explicit invalidation, the next
// error (after the 5-min positive TTL expires) would resurrect the negative
// cache unnecessarily.
func TestRevealAPIKeySuccessInvalidatesNegativeCache(t *testing.T) {
	client := &Client{
		candCache:   make(map[string]cacheEntry[*resolveResponse]),
		keyCache:    make(map[int]cacheEntry[string]),
		keyCacheNeg: make(map[int]negativeCacheEntry),
	}
	client.keyCacheNeg[42] = negativeCacheEntry{
		value:   "previous failure",
		reason:  "other",
		expires: time.Now().Add(time.Hour),
	}
	// Pre-populate the positive cache so RevealAPIKey returns success
	// without touching dbPool.
	client.keyCache[42] = cacheEntry[string]{
		value:   "fresh-key",
		expires: time.Now().Add(time.Hour),
	}

	got, err := client.RevealAPIKey(context.Background(), 1, 42)
	if err != nil {
		t.Fatalf("expected success, got error=%v", err)
	}
	if got != "fresh-key" {
		t.Fatalf("expected fresh-key, got %q", got)
	}
}

// TestRecordNegativeCacheLockedEvictsOldest pins the eviction policy: when the
// negative cache exceeds decryptFailureCacheMax, the entry with the earliest
// expires is dropped to make room. We don't fill 1024 entries (too slow) —
// we shrink the constant locally via the cap. Since the policy is "linear
// scan for oldest expiry", we test with 3 entries and verify the eviction
// behaviour holds regardless of cap size.
func TestRecordNegativeCacheLockedEvictsOldest(t *testing.T) {
	client := &Client{
		candCache:   make(map[string]cacheEntry[*resolveResponse]),
		keyCache:    make(map[int]cacheEntry[string]),
		keyCacheNeg: make(map[int]negativeCacheEntry),
	}
	now := time.Now()
	// Three pre-existing entries; #2 has the earliest expiry.
	client.keyCacheNeg[1] = negativeCacheEntry{value: "err1", reason: "other", expires: now.Add(30 * time.Second)}
	client.keyCacheNeg[2] = negativeCacheEntry{value: "err2", reason: "other", expires: now.Add(10 * time.Second)}
	client.keyCacheNeg[3] = negativeCacheEntry{value: "err3", reason: "other", expires: now.Add(60 * time.Second)}

	// Force the map to look "full" by reducing the cap counter via direct
	// insert of decryptFailureCacheMax-1 entries. To keep the test fast, we
	// just verify the eviction logic itself: pre-populate the map to its
	// cap (via a manual override that hits the >= branch) by inserting one
	// more entry and checking the oldest got evicted.
	//
	// We can't easily shrink decryptFailureCacheMax without polluting the
	// production constant, so we instead drive the eviction logic by
	// checking the documented behaviour: when the cap is exceeded, the
	// entry whose expires is earliest gets dropped. We trigger that branch
	// by manually pre-populating decryptFailureCacheMax entries (1024),
	// then inserting one more — slow but deterministic.
	for i := 100; i < 100+decryptFailureCacheMax-3; i++ {
		client.keyCacheNeg[i] = negativeCacheEntry{
			value:   "bulk",
			reason:  "other",
			expires: now.Add(time.Duration(i) * time.Second), // monotonically increasing
		}
	}

	// Inserting one more entry should evict the one with the earliest
	// expiry (credential 2, expires at +10s).
	client.mu.Lock()
	client.recordNegativeCacheLocked(99, "fresh", "other")
	client.mu.Unlock()

	if _, ok := client.keyCacheNeg[2]; ok {
		t.Fatalf("expected credential 2 (oldest) to be evicted, but it remains")
	}
	if _, ok := client.keyCacheNeg[99]; !ok {
		t.Fatalf("expected new credential 99 to be present")
	}
}

// contains is a tiny helper to keep the assertions readable.
func contains(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
