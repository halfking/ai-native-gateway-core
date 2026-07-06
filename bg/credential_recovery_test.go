package bg

import (
	"strings"
	"testing"
)

func TestMnfCoolingRecoverySQLGuards(t *testing.T) {
	sql := mnfCoolingRecoverySQL()
	mustContain := []string{
		"cmb.unavailable_reason = 'mnf_cooling'",
		"cmb.unavailable_at <= NOW() - make_interval(mins => $1)",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
		"COALESCE(cmb.admin_protected, FALSE) = FALSE",
		"unavailable_reason = NULL",
		"unavailable_at = NULL",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("mnfCoolingRecoverySQL missing %q in:\n%s", want, sql)
		}
	}
}

func TestMnfCoolingRecoveryMinutes(t *testing.T) {
	t.Setenv("LLM_GATEWAY_MNF_COOL_MINUTES", "7")
	if got := mnfCoolingRecoveryMinutes(); got != 7 {
		t.Fatalf("mnfCoolingRecoveryMinutes = %d, want 7", got)
	}
	t.Setenv("LLM_GATEWAY_MNF_COOL_MINUTES", "bad")
	if got := mnfCoolingRecoveryMinutes(); got != 2 {
		t.Fatalf("mnfCoolingRecoveryMinutes invalid = %d, want 2", got)
	}
}

// TestStalePeriodicExhaustedCleanupSQLGuards 验证兜底恢复 SQL 包含所有
// 必需的安全条件（2026-07-06 P0 fix 审计）。
//
// 这个 SQL 解决凭据卡在 quota_state='periodic_exhausted' 无法自动恢复的问题。
// 关键不变量：
//  1. 只恢复 healthy 状态的凭据（避免误恢复正在失败中的凭据）
//  2. health_checked_at 必须在 2 小时内（确保探测数据是新鲜的）
//  3. 必须重置 quota_recover_at 和 state_reason_code（防止状态不一致）
//  4. 必须排除非 active 凭据（避免恢复 lifecycle 异常的凭据）
func TestStalePeriodicExhaustedCleanupSQLGuards(t *testing.T) {
	sql := stalePeriodicExhaustedCleanupSQL()
	mustContain := []string{
		// 必含的不变量
		"quota_state         = 'ok'",
		"quota_recover_at    = NULL",
		"state_reason_code   = NULL",
		"quota_state = 'periodic_exhausted'",
		"health_status = 'healthy'",
		"health_checked_at > now() - INTERVAL '2 hours'",
		"lifecycle_status = 'active'",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("stalePeriodicExhaustedCleanupSQL missing %q in:\n%s", want, sql)
		}
	}
}
