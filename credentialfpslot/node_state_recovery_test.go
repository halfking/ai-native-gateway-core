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
	// P0-B (2026-10-28 两层路由审计 G2)：到期后的失败 = 恢复试探失败，
	// 属同一振荡循环的下一次禁用——计数 +1、时长按 cooldown×2^dc 指数递增
	//（dc 递增前 =1 ⇒ 300×2 = 600s），与 URSM record_request.lua 同公式。
	// 旧断言（"延长冷却期不增加计数"）钉的正是 P0-B 要改掉的恒定周期翻红。
	state.DisabledUntil = time.Now().Add(-time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))
	require.NoError(t, mgr.RecordNodeFailure(ctx, credID, model, "fail-after-cooldown", "timeout"))

	state, err := mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.Equal(t, 2, state.DisableCount, "P0-B：到期失败=恢复试探失败，计数 +1")
	assert.True(t, state.Disabled, "到期失败应再次禁用")
	assert.InDelta(t, state.DisabledUntil-state.LastFailureAt, int64(600), 2,
		"P0-B：第 2 次禁用时长 = 300×2^1 = 600s")

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

// TestNodeState_DisableBackoffExponential_URSMAligned 钉 P0-B
//（2026-10-28 两层路由审计 G2）的指数退避公式：第 N 次禁用时长 =
// base_cooldown × 2^(N−1) 封顶 1800——与 URSM v2 record_request.lua 的
// new_cool = cool × 2^disable_count → min(backoff_cap, 1800) 同公式同封顶。
// 反复「冷却到期 → 试探失败」的振荡节点冷却 300 → 600 → 1200 → 1800(cap)，
// 而非旧的恒定 300 无限翻红。成功恢复（到期 → 试探成功）计数归零、从
// 第一档重新开始，且 recovered_at（P0-A）落地。
func TestNodeState_DisableBackoffExponential_URSMAligned(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer client.Close()

	mgr := &Manager{client: client, cfg: Config{Enabled: true}}
	ctx := context.Background()
	credID := 19
	model := "p0b-backoff-model"

	// base cooldown 默认 300（settings.NodeDisabledCooldownSeconds），
	// cap 默认 1800（settings.NodeDisabledBackoffCapSeconds）。
	assertCoolingSeconds := func(t *testing.T, want int64, label string) {
		t.Helper()
		state, err := mgr.GetNodeState(ctx, credID, model)
		require.NoError(t, err)
		require.True(t, state.Disabled, label+": 节点应处于禁用态")
		require.NotZero(t, state.LastFailureAt, label+": 需有失败时间戳")
		assert.InDelta(t, want, state.DisabledUntil-state.LastFailureAt, 2,
			label+": 冷却时长应为 %ds（±2s 容错，Redis TIME 与本机钟差）", want)
	}

	// 第 1 次禁用（3 连败）：300 × 2^0 = 300s——与 P0-B 之前的固定值逐秒相等。
	for i := 0; i < 3; i++ {
		require.NoError(t, mgr.RecordNodeFailure(ctx, credID, model, "p0b-f1-"+string(rune('1'+i)), "timeout"))
	}
	assertCoolingSeconds(t, 300, "第 1 次禁用")

	// 振荡循环：冷却到期 → 恢复试探失败 → 再禁用。幂取递增前的计数。
	expireAndFail := func(label string) {
		t.Helper()
		state, err := mgr.GetNodeState(ctx, credID, model)
		require.NoError(t, err)
		state.DisabledUntil = time.Now().Add(-time.Second).Unix()
		require.NoError(t, mgr.SetNodeState(ctx, state))
		require.NoError(t, mgr.RecordNodeFailure(ctx, credID, model, "p0b-"+label, "timeout"))
	}

	expireAndFail("f2") // 第 2 次禁用：300 × 2^1 = 600s
	assertCoolingSeconds(t, 600, "第 2 次禁用（到期试探失败）")
	expireAndFail("f3") // 第 3 次禁用：300 × 2^2 = 1200s
	assertCoolingSeconds(t, 1200, "第 3 次禁用")
	expireAndFail("f4") // 第 4 次禁用：300 × 2^3 = 2400s → cap 1800
	assertCoolingSeconds(t, 1800, "第 4 次禁用（封顶）")
	expireAndFail("f5") // 第 5 次禁用：仍封顶 1800
	assertCoolingSeconds(t, 1800, "第 5 次禁用（封顶后不再增长）")

	// 计数对拍：5 次禁用后 disable_count = 5。
	state, err := mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.Equal(t, 5, state.DisableCount, "5 次禁用后计数应为 5")

	// 成功恢复：计数归零 + recovered_at 落地（P0-A：实际流量验证恢复的时刻）。
	state.DisabledUntil = time.Now().Add(-time.Second).Unix()
	require.NoError(t, mgr.SetNodeState(ctx, state))
	before := time.Now().Unix()
	require.NoError(t, mgr.RecordNodeSuccess(ctx, credID, model, "p0b-success"))
	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.False(t, state.Disabled, "成功恢复后应回池")
	assert.Equal(t, 0, state.DisableCount, "成功恢复后计数归零")
	assert.GreaterOrEqual(t, state.RecoveredAt, before, "P0-A：recovered_at 应写入恢复时刻")
	assert.LessOrEqual(t, state.RecoveredAt, time.Now().Unix(), "recovered_at 不应来自未来")

	// 恢复后再 3 连败：从第一档 300s 重新开始（清零生效，不是接着 2^5）。
	for i := 0; i < 3; i++ {
		require.NoError(t, mgr.RecordNodeFailure(ctx, credID, model, "p0b-r1-"+string(rune('1'+i)), "timeout"))
	}
	assertCoolingSeconds(t, 300, "成功恢复后的再次禁用从第一档重新开始")
	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.Equal(t, 1, state.DisableCount, "恢复后再次禁用计数从 1 重新计")
}

// TestNodeState_RecoveredAtNotWrittenOnNormalSuccess 钉 P0-A 的 recovered_at
// 语义：只有「冷却到期后的第一个成功」（恢复被验证）才写 recovered_at；
// 常规成功（节点本就健康）不产生恢复证据。
func TestNodeState_RecoveredAtNotWrittenOnNormalSuccess(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer client.Close()

	mgr := &Manager{client: client, cfg: Config{Enabled: true}}
	ctx := context.Background()
	credID := 20
	model := "p0a-recovered-at-model"

	// 从未禁用过的节点：成功不写 recovered_at。
	require.NoError(t, mgr.RecordNodeSuccess(ctx, credID, model, "normal-ok"))
	state, err := mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.Zero(t, state.RecoveredAt, "常规成功不产生恢复证据")
}

// TestNodeState_ResetWritesRecoveredAt 钉 P0-A 批判审计轮修正：admin 手动
// reset 节点健康（ResetNodeHealthState，admin/routing.go 的手动恢复路径）
// 是「未经流量验证的恢复」——恰是 re-flapping 风险最高、最需要 ramp 的
// 场景，必须**写** recovered_at=now（与 breaker.Reset 对称），而不是清零。
// 首版误写清零 ⇒ 同一场景两面语义相反（breaker 进 ramp / NodeState 不进）。
func TestNodeState_ResetWritesRecoveredAt(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer client.Close()

	mgr := &Manager{client: client, cfg: Config{Enabled: true}}
	ctx := context.Background()
	credID := 21
	model := "p0a-reset-ramp-model"

	// 先制造一次禁用（保证 key 存在且处于健康字段非零态）。
	for i := 0; i < 3; i++ {
		require.NoError(t, mgr.RecordNodeFailure(ctx, credID, model, "rst-"+string(rune('1'+i)), "timeout"))
	}
	state, err := mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	require.True(t, state.Disabled)
	require.Zero(t, state.RecoveredAt, "禁用中不应有恢复证据")

	// admin reset：健康字段清零 + recovered_at 写当前时刻（进 ramp）。
	before := time.Now().Unix()
	require.NoError(t, mgr.ResetNodeHealthState(ctx, credID, model))
	state, err = mgr.GetNodeState(ctx, credID, model)
	require.NoError(t, err)
	assert.False(t, state.Disabled, "reset 后应回池")
	assert.GreaterOrEqual(t, state.RecoveredAt, before, "reset 应写 recovered_at（手动恢复进 ramp）")
	assert.LessOrEqual(t, state.RecoveredAt, time.Now().Unix(), "recovered_at 不应来自未来")
}
