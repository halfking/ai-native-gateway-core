package credentialfpslot

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNodeState_RecoveryAfterCooldown verifies that a cooldown-expired node
// returns to the routing pool before its next real request.
func TestNodeState_RecoveryAfterCooldown(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer client.Close()

	mgr := &Manager{
		client: client,
		cfg:    Config{Enabled: true}, // 必须启用才能记录节点状态
	}
	ctx := context.Background()

	credID := 15 // 商汤
	model := "sense-5.5-large"
	// 1. 记录连续 3 次失败，触发禁用
	for i := 0; i < 3; i++ {
		err := mgr.RecordNodeFailure(ctx, credID, model, "req-fail-"+string(rune('1'+i)), "rate_limit")
		require.NoError(t, err)
	}

	// 验证节点已被禁用
	state, err := mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.True(t, state.Disabled, "节点应该被禁用")
	assert.Greater(t, state.DisabledUntil, time.Now().Unix(), "应该设置冷却期")
	assert.Equal(t, 1, state.DisableCount, "禁用次数应为 1")

	// 2. Mark the cooldown expired.
	state.DisabledUntil = time.Now().Add(-time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))

	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.True(t, state.IsUsable(time.Now()), "冷却期到期后应重新进入路由池")
	assert.False(t, state.Disabled, "读取状态时应自动恢复")

	// 3. A real request after recovery can fail and extend the cooldown.
	err = mgr.RecordNodeFailure(ctx, credID, model, "req-fail-after-cooldown", "timeout")
	require.NoError(t, err)

	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.True(t, state.Disabled, "冷却期到期但失败请求不应恢复节点")
	assert.Equal(t, "cooldown_extended_due_to_failure", state.DisabledReason)

	// 4. Expire the extended cooldown before the next successful request.
	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	state.DisabledUntil = time.Now().Add(-time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))

	// 5. Record a successful request after the node is routable again.
	err = mgr.RecordNodeSuccess(ctx, credID, model, "req-success-1")
	require.NoError(t, err)

	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.False(t, state.Disabled, "成功请求应恢复节点")
	assert.Equal(t, "recovered_with_actual_success", state.DisabledReason)
	assert.Equal(t, int64(0), state.DisabledUntil, "冷却期应清零")
	assert.Equal(t, 0, state.DisableCount, "禁用计数应重置")
}

// TestNodeState_ReDisableAfterRecovery 验证 P0 修复：
// 恢复后如果再次连续失败 3 次，立即重新禁用。
func TestNodeState_ReDisableAfterRecovery(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer client.Close()

	mgr := &Manager{
		client: client,
		cfg:    Config{Enabled: true}, // 必须启用才能记录节点状态
	}
	ctx := context.Background()

	credID := 18 // NVIDIA NIM
	model := "nvidia/llama-3.1-nemotron-ultra-253b-instruct"
	// 1. 记录连续 3 次失败 → 禁用
	for i := 0; i < 3; i++ {
		err := mgr.RecordNodeFailure(ctx, credID, model, "req-fail-"+string(rune('1'+i)), "empty_response")
		require.NoError(t, err)
	}

	state, err := mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.True(t, state.Disabled)
	assert.Equal(t, 1, state.DisableCount)

	// 2. Expire the cooldown before the actual success request.
	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	state.DisabledUntil = time.Now().Add(-time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))
	err = mgr.RecordNodeSuccess(ctx, credID, model, "req-success-recovery")
	require.NoError(t, err)

	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.False(t, state.Disabled, "应该已恢复")

	// 3. 恢复后记录一次成功（保持健康）
	err = mgr.RecordNodeSuccess(ctx, credID, model, "req-success-2")
	require.NoError(t, err)

	// 4. 再次连续失败 3 次
	// 预期：立即重新禁用，不需要等待冷却期
	for i := 0; i < 3; i++ {
		err = mgr.RecordNodeFailure(ctx, credID, model, "req-fail-again-"+string(rune('1'+i)), "timeout")
		require.NoError(t, err)
	}

	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.True(t, state.Disabled, "再次连续失败应立即禁用")
	assert.Contains(t, state.DisabledReason, "consecutive_3_failures_after_recovery")
	assert.Equal(t, 1, state.DisableCount, "禁用计数应为 1（成功恢复后重置过）")
}

// TestNodeState_IsUsableRespectsCooldown 验证 IsUsable 在冷却期内返回 false
func TestNodeState_IsUsableRespectsCooldown(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer client.Close()

	mgr := &Manager{
		client: client,
		cfg:    Config{Enabled: true}, // 必须启用才能记录节点状态
	}
	ctx := context.Background()

	credID := 15
	model := "sense-5.5-medium"
	// 1. 连续失败 3 次
	for i := 0; i < 3; i++ {
		err := mgr.RecordNodeFailure(ctx, credID, model, "req-"+string(rune('1'+i)), "rate_limit")
		require.NoError(t, err)
	}

	// 2. 验证 IsUsable 返回 false（冷却期内）
	state, err := mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.False(t, state.IsUsable(time.Now()), "冷却期内应不可用")

	// 3. 冷却期到期后，节点应重新进入路由池
	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	state.DisabledUntil = time.Now().Add(-time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))
	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.True(t, state.IsUsable(time.Now()), "冷却期到期后应可路由")
	assert.False(t, state.Disabled, "冷却期到期后应自动恢复")

	// 4. Record a real successful request.
	state.DisabledUntil = time.Now().Add(-time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))

	// 5. 记录成功请求
	err = mgr.RecordNodeSuccess(ctx, credID, model, "req-success")
	require.NoError(t, err)

	// 6. 现在 IsUsable 应该返回 true
	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.True(t, state.IsUsable(time.Now()), "成功恢复后应可用")
}

// TestNodeState_DisableCountTracking 验证禁用计数追踪
func TestNodeState_DisableCountTracking(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer client.Close()

	mgr := &Manager{
		client: client,
		cfg:    Config{Enabled: true}, // 必须启用才能记录节点状态
	}
	ctx := context.Background()

	credID := 18
	model := "nvidia/llama-3.1-nemotron-ultra-253b-instruct"
	// 第一次禁用
	for i := 0; i < 3; i++ {
		_ = mgr.RecordNodeFailure(ctx, credID, model, "fail-1-"+string(rune('1'+i)), "empty_response")
	}

	state, _ := mgr.GetNodeState(ctx, credID, model)
	assert.Equal(t, 1, state.DisableCount, "第一次禁用，计数应为 1")

	// 冷却期到期，失败恢复（延长冷却期）
	state.DisabledUntil = time.Now().Add(-time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))
	require.NoError(t, mgr.RecordNodeFailure(ctx, credID, model, "fail-after-cooldown", "timeout"))

	state, err := mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.Equal(t, 1, state.DisableCount, "延长冷却期不增加计数")

	// 再次冷却期到期，成功恢复
	state.DisabledUntil = time.Now().Add(-time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))
	require.NoError(t, mgr.RecordNodeSuccess(ctx, credID, model, "success-recovery"))

	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.Equal(t, 0, state.DisableCount, "成功恢复后计数重置")
	assert.False(t, state.Disabled)

	// 再次连续失败（第二次禁用）
	for i := 0; i < 3; i++ {
		require.NoError(t, mgr.RecordNodeFailure(ctx, credID, model, "fail-2-"+string(rune('1'+i)), "timeout"))
	}

	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.Equal(t, 1, state.DisableCount, "第二次禁用，计数应为 1（已重置过）")
}
