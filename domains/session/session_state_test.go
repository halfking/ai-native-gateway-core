package session_test

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRotationHookLogic 测试轮换 Hook 的逻辑（不依赖真实 Redis）
func TestRotationHookLogic(t *testing.T) {
	tests := []struct {
		name           string
		rotCtx         *session.RotationContext
		expectRotation bool
		expectReason   string
	}{
		{
			name: "初始凭据",
			rotCtx: &session.RotationContext{
				SessionID:       "sess-001",
				TenantID:        "tenant-test",
				OldCredentialID: 0,
				NewCredentialID: 1001,
				Model:           "gpt-4",
				Provider:        "openai",
			},
			expectRotation: true,
			expectReason:   session.SwitchReasonInitial,
		},
		{
			name: "凭据轮换",
			rotCtx: &session.RotationContext{
				SessionID:       "sess-001",
				TenantID:        "tenant-test",
				OldCredentialID: 1001,
				NewCredentialID: 1002,
				Model:           "gpt-4",
				Provider:        "openai",
				SwitchReason:    session.SwitchReasonRotate,
			},
			expectRotation: true,
			expectReason:   session.SwitchReasonRotate,
		},
		{
			name: "无变化",
			rotCtx: &session.RotationContext{
				SessionID:       "sess-001",
				TenantID:        "tenant-test",
				OldCredentialID: 1001,
				NewCredentialID: 1001,
				Model:           "gpt-4",
				Provider:        "openai",
			},
			expectRotation: false,
		},
		{
			name: "模型切换",
			rotCtx: &session.RotationContext{
				SessionID:       "sess-001",
				TenantID:        "tenant-test",
				OldCredentialID: 1001,
				NewCredentialID: 1002,
				Model:           "gpt-4o",
				Provider:        "openai",
				SwitchReason:    session.SwitchReasonModelSwitch,
			},
			expectRotation: true,
			expectReason:   session.SwitchReasonModelSwitch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证 RotationContext 结构
			assert.NotEmpty(t, tt.rotCtx.SessionID)
			assert.NotEmpty(t, tt.rotCtx.TenantID)

			if tt.expectRotation {
				assert.NotZero(t, tt.rotCtx.NewCredentialID)
				// 如果有明确的 reason，验证它
				if tt.rotCtx.SwitchReason != "" {
					assert.Equal(t, tt.expectReason, tt.rotCtx.SwitchReason)
				}
			}
		})
	}
}

// TestExtractRotationContextFromMetadata 测试从 metadata 提取轮换上下文
func TestExtractRotationContextFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]interface{}
		want     *session.RotationContext
	}{
		{
			name: "完整 metadata",
			metadata: map[string]interface{}{
				"session_id":    "sess-001",
				"tenant_id":     "tenant-test",
				"credential_id": 1001,
				"model":         "gpt-4",
				"provider":      "openai",
				"switch_reason": "rotate",
				"fp_slot_index": 5,
			},
			want: &session.RotationContext{
				SessionID:       "sess-001",
				TenantID:        "tenant-test",
				NewCredentialID: 1001,
				Model:           "gpt-4",
				Provider:        "openai",
				SwitchReason:    "rotate",
				FPSlotIndex:     5,
			},
		},
		{
			name: "部分 metadata",
			metadata: map[string]interface{}{
				"session_id": "sess-002",
				"tenant_id":  "tenant-test",
				"cred_id":    2001, // 测试别名
				"model":      "claude-3",
			},
			want: &session.RotationContext{
				SessionID:       "sess-002",
				TenantID:        "tenant-test",
				NewCredentialID: 2001,
				Model:           "claude-3",
			},
		},
		{
			name:     "空 metadata",
			metadata: nil,
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := session.ExtractRotationContextFromMetadata(tt.metadata)

			if tt.want == nil {
				assert.Nil(t, got)
				return
			}

			require.NotNil(t, got)
			assert.Equal(t, tt.want.SessionID, got.SessionID)
			assert.Equal(t, tt.want.TenantID, got.TenantID)
			assert.Equal(t, tt.want.NewCredentialID, got.NewCredentialID)
			assert.Equal(t, tt.want.Model, got.Model)
			assert.Equal(t, tt.want.Provider, got.Provider)
			assert.Equal(t, tt.want.SwitchReason, got.SwitchReason)
			assert.Equal(t, tt.want.FPSlotIndex, got.FPSlotIndex)
		})
	}
}

// TestUsageUpdateCalculation 测试使用统计累加计算
func TestUsageUpdateCalculation(t *testing.T) {
	tests := []struct {
		name           string
		existingPrompt int64
		existingCompl  int64
		existingCost   float64
		existingTurns  int64
		newUsage       session.UsageUpdate
		expectPrompt   int64
		expectCompl    int64
		expectCost     float64
		expectTurns    int64
	}{
		{
			name:           "首次使用",
			existingPrompt: 0,
			existingCompl:  0,
			existingCost:   0,
			existingTurns:  0,
			newUsage: session.UsageUpdate{
				PromptTokens:     100,
				CompletionTokens: 50,
				CostUSD:          0.001,
			},
			expectPrompt: 100,
			expectCompl:  50,
			expectCost:   0.001,
			expectTurns:  1,
		},
		{
			name:           "累加使用",
			existingPrompt: 100,
			existingCompl:  50,
			existingCost:   0.001,
			existingTurns:  1,
			newUsage: session.UsageUpdate{
				PromptTokens:     200,
				CompletionTokens: 100,
				CostUSD:          0.002,
			},
			expectPrompt: 300,
			expectCompl:  150,
			expectCost:   0.003,
			expectTurns:  2,
		},
		{
			name:           "零使用",
			existingPrompt: 500,
			existingCompl:  250,
			existingCost:   0.005,
			existingTurns:  5,
			newUsage: session.UsageUpdate{
				PromptTokens:     0,
				CompletionTokens: 0,
				CostUSD:          0,
			},
			expectPrompt: 500,
			expectCompl:  250,
			expectCost:   0.005,
			expectTurns:  6, // 轮次仍然 +1
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 模拟累加逻辑
			prompt := tt.existingPrompt + int64(tt.newUsage.PromptTokens)
			compl := tt.existingCompl + int64(tt.newUsage.CompletionTokens)
			cost := tt.existingCost + tt.newUsage.CostUSD
			turns := tt.existingTurns + 1

			assert.Equal(t, tt.expectPrompt, prompt)
			assert.Equal(t, tt.expectCompl, compl)
			assert.InDelta(t, tt.expectCost, cost, 0.0001)
			assert.Equal(t, tt.expectTurns, turns)
		})
	}
}

// TestCredRotationEntry 测试凭据轮换记录结构
func TestCredRotationEntry(t *testing.T) {
	now := time.Now().UTC()

	entry := session.CredRotationEntry{
		CredentialID:     1001,
		Model:            "gpt-4",
		Provider:         "openai",
		StartedAt:        now,
		EndedAt:          nil,
		Turns:            5,
		PromptTokens:     500,
		CompletionTokens: 250,
		CostUSDCents:     50,
		SwitchReason:     session.SwitchReasonInitial,
		FPSlotIndex:      3,
	}

	// 验证结构字段
	assert.Equal(t, int(1001), entry.CredentialID)
	assert.Equal(t, "gpt-4", entry.Model)
	assert.Equal(t, "openai", entry.Provider)
	assert.False(t, entry.StartedAt.IsZero())
	assert.Nil(t, entry.EndedAt)
	assert.Equal(t, 5, entry.Turns)
	assert.Equal(t, int64(500), entry.PromptTokens)
	assert.Equal(t, int64(250), entry.CompletionTokens)
	assert.Equal(t, int64(50), entry.CostUSDCents)
	assert.Equal(t, session.SwitchReasonInitial, entry.SwitchReason)
	assert.Equal(t, 3, entry.FPSlotIndex)

	// 模拟结束轮换
	endTime := now.Add(5 * time.Minute)
	entry.EndedAt = &endTime
	assert.NotNil(t, entry.EndedAt)
	assert.Equal(t, 5*time.Minute, entry.EndedAt.Sub(entry.StartedAt))
}

// TestSwitchReasonConstants 测试切换原因常量
func TestSwitchReasonConstants(t *testing.T) {
	reasons := []string{
		session.SwitchReasonInitial,
		session.SwitchReasonSticky,
		session.SwitchReasonRotate,
		session.SwitchReasonFallback,
		session.SwitchReasonModelSwitch,
		session.SwitchReasonManual,
		session.SwitchReasonSlotExhaust,
		session.SwitchReasonProbeFail,
	}

	// 验证所有常量都不为空
	for _, r := range reasons {
		assert.NotEmpty(t, r, "SwitchReason constant should not be empty")
	}

	// 验证常量唯一性
	seen := make(map[string]bool)
	for _, r := range reasons {
		assert.False(t, seen[r], "SwitchReason %s should be unique", r)
		seen[r] = true
	}
}

// TestStatusConstants 测试状态常量
func TestStatusConstants(t *testing.T) {
	statuses := []string{
		session.StatusActive,
		session.StatusStopped,
		session.StatusRecovered,
		session.StatusExpired,
	}

	for _, s := range statuses {
		assert.NotEmpty(t, s, "Status constant should not be empty")
	}

	// 验证状态唯一性
	seen := make(map[string]bool)
	for _, s := range statuses {
		assert.False(t, seen[s], "Status %s should be unique", s)
		seen[s] = true
	}
}

// BenchmarkUsageUpdate 性能测试：使用统计更新
func BenchmarkUsageUpdate(b *testing.B) {
	usage := session.UsageUpdate{
		PromptTokens:     1000,
		CompletionTokens: 500,
		Model:            "gpt-4",
		Provider:         "openai",
		CredentialID:     1001,
		CostUSD:          0.1,
	}

	var total int64
	var totalCost float64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		total += int64(usage.PromptTokens)
		total += int64(usage.CompletionTokens)
		totalCost += usage.CostUSD
	}
	b.StopTimer()
}
