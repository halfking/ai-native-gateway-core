package bg

import (
	"os"
	"regexp"
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

// TestAuthFailedRecoverySQLGuard pins BUG #3 fix (2026-07-22): the
// 60s recovery ticker's first UPDATE must include 'auth_failed' in
// the availability_state whitelist. Without 'auth_failed' in the
// whitelist, auth-failed credentials stay marked unavailable
// indefinitely even after their recover_at timestamp elapses —
// the production incident on 2026-07-22 lasted 15+ hours because
// of this gap (writer.go:218 wrote recover_at=NULL, recovery
// ticker only handled cooling/rate_limited/unreachable).
//
// We verify the contract by extracting the recovery SQL via a
// regex on the source (since the SQL is inline in recover()),
// so the test survives any future SQL body refactor as long as
// the whitelist still includes 'auth_failed'.
func TestAuthFailedRecoverySQLGuard(t *testing.T) {
	src, err := os.ReadFile("credential_recovery.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	// The first UPDATE in recover() must whitelist 'auth_failed'.
	// Use a tolerant pattern: any availability_state IN (...) clause
	// that includes the 'auth_failed' literal.
	pattern := regexp.MustCompile(`availability_state\s+IN\s*\([^)]*'auth_failed'[^)]*\)`)
	if !pattern.MatchString(body) {
		t.Fatalf("availability_state IN(...) clause does not include 'auth_failed' — BUG #3 regression:\n%s",
			extractSnippet(body, "availability_state"))
	}
	// The whitelist must still include the original 3 states to avoid
	// removing existing recovery paths.
	for _, s := range []string{"'cooling'", "'rate_limited'", "'unreachable'"} {
		if !strings.Contains(body, s) {
			t.Fatalf("recovery whitelist lost %s — regression of pre-existing recovery path", s)
		}
	}
}

// extractSnippet returns the 200-char window around the first occurrence
// of needle in body, for nicer test failure messages.
func extractSnippet(body, needle string) string {
	idx := strings.Index(body, needle)
	if idx < 0 {
		return "<needle not found>"
	}
	from := idx - 80
	to := idx + 200
	if from < 0 {
		from = 0
	}
	if to > len(body) {
		to = len(body)
	}
	return body[from:to]
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
