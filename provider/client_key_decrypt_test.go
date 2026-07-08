package provider

import (
	"context"
	"encoding/json"
	"testing"
	"time"
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
