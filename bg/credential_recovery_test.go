package bg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// TestExpiredCmbRecoverySQLGuards pins the safety guards of the
// expired-binding probe SQL introduced for the 2026-07-24 incident
// where /api/routing/resolve showed routable nodes while real chat
// requests returned "no available nodes" because:
//   - cmb.available was flipped to FALSE by either continuous_failure
//     (credentialhealth/checker.go) or a probe_* reason (bg/node_probe.go)
//   - cmb.unavailable_recover_at elapsed (5-minute cooldown done)
//   - nothing re-checked whether the upstream had recovered
//
// The recovery must SELECT only — it must NEVER write cmb.available=TRUE
// blindly. Instead it hands the (cred, model) pair to NodeProbeWorker,
// whose runOne success path is the authoritative writer of cmb.available.
//
// Required safety guards:
//  1. Only cmb rows that are currently available=FALSE with a past
//     unavailable_recover_at are picked.
//  2. Only the two transient reasons are targeted: 'continuous_failure'
//     and probe_* (anything written by node_probe.go:1496-1510).
//  3. NEVER touch manual* / admin_protected rows (operators chose those).
//  4. Credential / provider must be active, not manually disabled, and
//     availability_state='ready' (otherwise the upstream itself is bad).
//  5. Skip rows whose node_probe_state is paused OR still has a future
//     next_retry_at (operator paused OR ladder mid-cycle).
func TestExpiredCmbRecoverySQLGuards(t *testing.T) {
	sql := expiredCmbRecoverySQL()
	mustContain := []string{
		// ── 必须的 cmb 谓词 ──
		"cmb.available = FALSE",
		"cmb.unavailable_recover_at IS NOT NULL",
		"cmb.unavailable_recover_at <= now()",
		// ── 只挑两个 transient 原因 ──
		"unavailable_reason IN ('continuous_failure'",
		"unavailable_reason LIKE 'probe!_%' ESCAPE '!'",
		"unavailable_reason LIKE 'auto!_%' ESCAPE '!'",
		// ── 硬保护：manual/admin_protected 一律不动 ──
		"COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'",
		"COALESCE(cmb.admin_protected, FALSE) = FALSE",
		// ── credential / provider 必为可路由 ──
		"COALESCE(c.status, 'active') = 'active'",
		"COALESCE(c.lifecycle_status, 'active') = 'active'",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"c.availability_state = 'ready'",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
		"p.enabled = TRUE",
		// ── 输出列：必须包含 credential_id + raw_model_name ──
		"cmb.credential_id",
		"pm.raw_model_name",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("expiredCmbRecoverySQL missing %q in:\n%s", want, sql)
		}
	}
	// Tolerant regex checks for SQL clauses where column-alignment
	// whitespace varies between writes (mirrors TestAuthFailedRecoverySQLGuard).
	regexMustMatch := []*regexp.Regexp{
		regexp.MustCompile(`nps\.paused\s*=\s*TRUE`),
	}
	for _, re := range regexMustMatch {
		if !re.MatchString(sql) {
			t.Fatalf("expiredCmbRecoverySQL missing pattern %q in:\n%s", re.String(), sql)
		}
	}
	// 2026-07-24 fix: removed next_retry_at > now() check entirely.
	// Now we recover ALL expired cmb rows regardless of backoff state,
	// except those manually paused. This prevents nodes from being stranded.
	mustNotContain := []string{
		"next_retry_at > now()",
		"next_retry_at  > now()",
	}
	for _, want := range mustNotContain {
		if strings.Contains(sql, want) {
			t.Fatalf("expiredCmbRecoverySQL must NOT contain %q (removed in 2026-07-24 fix) in:\n%s", want, sql)
		}
	}
}

// TestRecoverExpiredBindingsEnqueuesProbes pins the 2026-07-24
// root cause: previously the 60s recovery tick only restored
// availability_state / quota_state / circuit_state / health_status
// and NEVER re-checked cmb.available=FALSE bindings. When business
// traffic succeeded through OTHER working nodes, the previously-failed
// (cred, model) pair was never re-probed and stayed "available=FALSE"
// until an operator manually cleared and re-fetched the model list.
//
// This test wires a fake probe submitter + cache invalidator into
// CredentialRecovery, feeds pgxmock with two expired cmb rows, and
// asserts:
//   - The new SQL is run (the SELECT ... cmb ... query).
//   - The probe submitter is called once per row with the right
//     (credID, raw_model_name) pair.
//   - The candidate cache invalidator is called for each unique
//     credential ID so the next chat request re-plans with the
//     recovered binding visible without waiting for the 30s candCache
//     TTL.
func TestRecoverExpiredBindingsEnqueuesProbes(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	rows := pgxmock.NewRows([]string{"credential_id", "raw_model_name"}).
		AddRow(11, "glm-5.2").
		AddRow(11, "glm-5.3")
	mock.ExpectQuery("FROM credential_model_bindings cmb").
		WillReturnRows(rows)

	r := &CredentialRecovery{db: mock, done: make(chan struct{})}

	var (
		mu          sync.Mutex
		submitted   []string
		invalidated = make(map[int]int)
	)
	r.SetProbeSubmitter(func(credID int, model string) {
		mu.Lock()
		defer mu.Unlock()
		submitted = append(submitted, fmt.Sprintf("%d|%s", credID, model))
	})
	r.SetInvalidateCandidateCache(func(credID int) {
		mu.Lock()
		defer mu.Unlock()
		invalidated[credID]++
	})

	if err := r.recoverExpiredBindings(context.Background()); err != nil {
		t.Fatalf("recoverExpiredBindings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	wantSubmitted := []string{"11|glm-5.2", "11|glm-5.3"}
	if len(submitted) != len(wantSubmitted) {
		t.Fatalf("submitted count = %d, want %d (got %v)", len(submitted), len(wantSubmitted), submitted)
	}
	for _, w := range wantSubmitted {
		found := false
		for _, s := range submitted {
			if s == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing submit for %q (got %v)", w, submitted)
		}
	}
	if invalidated[11] < 1 {
		t.Errorf("expected invalidateCandidateCache to be called for cred 11, got %d", invalidated[11])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestRecoverExpiredBindingsSkipsWhenNoRows verifies the no-op path
// when nothing is eligible: no submit, no invalidate, no error.
func TestRecoverExpiredBindingsSkipsWhenNoRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("FROM credential_model_bindings cmb").
		WillReturnRows(pgxmock.NewRows([]string{"credential_id", "raw_model_name"}))

	r := &CredentialRecovery{db: mock, done: make(chan struct{})}
	calls := 0
	r.SetProbeSubmitter(func(int, string) { calls++ })
	r.SetInvalidateCandidateCache(func(int) { calls++ })

	if err := r.recoverExpiredBindings(context.Background()); err != nil {
		t.Fatalf("recoverExpiredBindings: %v", err)
	}
	if calls != 0 {
		t.Errorf("expected 0 callbacks for empty result, got %d", calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestRecoverExpiredBindingsReturnsErrorOnQueryFailure ensures the
// caller (the recover() tick loop) sees a non-nil error rather than
// silently swallowing it. Without this, a DB outage would silently
// suspend the recovery loop and the 2026-07-24 incident recurs.
func TestRecoverExpiredBindingsReturnsErrorOnQueryFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("FROM credential_model_bindings cmb").
		WillReturnError(errors.New("simulated db outage"))

	r := &CredentialRecovery{db: mock, done: make(chan struct{})}
	calls := 0
	r.SetProbeSubmitter(func(int, string) { calls++ })
	r.SetInvalidateCandidateCache(func(int) { calls++ })

	if err := r.recoverExpiredBindings(context.Background()); err == nil {
		t.Fatalf("expected error from recoverExpiredBindings on DB failure")
	}
	if calls != 0 {
		t.Errorf("submitter must not be called when query fails, got %d calls", calls)
	}
}

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
//  5. 2026-08-08 P0: writer 路径设置了 quota_recover_at（5 小时窗口），
//     到期前必须拒绝清除 —— 否则 periodic_quota_probe 用 probe_model
//     探测健康被当成业务模型配额恢复，导致 429→suspended→cleared→
//     re-route→429 死循环（cred 35 zhima-max 生产现场）。
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
		// 2026-08-08 P0 guard: 未到期的 quota_recover_at 不得清除
		"quota_recover_at IS NULL OR quota_recover_at <= now()",
		"lifecycle_status = 'active'",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("stalePeriodicExhaustedCleanupSQL missing %q in:\n%s", want, sql)
		}
	}
}

// TestSuspendedRecoverySQLGuard pins the 2026-08-07 P0 fix for
// the credential-state deadlock. Production evidence:
//
//	cred 22 (zhipu-roocode-v2): quota_state='ok' but
//	  availability_state='suspended' (status self-contradiction).
//	cred 34 (zhima-1): quota_state='permanently_exhausted' +
//	  availability_state='suspended', probe returns 200 healthy but
//	  state cannot recover.
//
// Without 'suspended' in the recover() IN list AND the hard-quota
// guard, the 60-second ticker is the only recovery path and it
// silently skips these credentials — the loop is:
//  1. Quota event writes suspended
//  2. stale-cleanup clears quota → availability hangs suspended
//  3. Admin force-enables → re-enters on next quota → loop
//
// The guard is mandatory (suspended can mean balance/permanent/
// auth_revoked too) so the test enforces both:
//
//	availability_state IN (..., 'suspended')
//	AND (
//	    availability_state <> 'suspended'
//	    OR COALESCE(quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
//	)
//
// Without the guard, a true balance_exhausted credential would
// auto-recover just because recover_at elapsed — that's the
// permanently_exhausted cred-34 looping on its own. The guard
// ensures balance/permanent must be flipped to 'ok' first (via
// balance_quota_probe success path) before availability flips.
func TestSuspendedRecoverySQLGuard(t *testing.T) {
	src, err := os.ReadFile("credential_recovery.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	// (a) 'suspended' must be in the recover() availability_state whitelist.
	pattern := regexp.MustCompile(`availability_state\s+IN\s*\([^)]*'suspended'[^)]*\)`)
	if !pattern.MatchString(body) {
		t.Fatalf("availability_state IN(...) clause does NOT include 'suspended' — 2026-08-07 P0 regression:\n%s",
			extractSnippet(body, "availability_state"))
	}
	// (b) The hard-quota guard must be present (otherwise permanently_
	// exhausted creds would auto-flip on availability_recover_at, the
	// exact deadlock we are guarding against).
	guardPattern := regexp.MustCompile(
		`availability_state\s*<>\s*'suspended'\s*` +
			`OR\s+COALESCE\(quota_state,\s*'ok'\)\s*NOT\s+IN\s*\(\s*'permanently_exhausted',\s*'balance_exhausted'\s*\)`,
	)
	if !guardPattern.MatchString(body) {
		t.Fatalf("suspended recovery hard-quota guard missing — balance/permanent creds would auto-recover on availability_recover_at alone:\n%s",
			extractSnippet(body, "suspended"))
	}
	// (c) Original whitelist states must still be present (no regression).
	for _, s := range []string{"'cooling'", "'rate_limited'", "'unreachable'", "'auth_failed'"} {
		if !strings.Contains(body, s) {
			t.Fatalf("recovery whitelist lost %s — regression of pre-existing recovery path", s)
		}
	}
}

// TestStalePeriodicSyncAvailabilitySQLGuard pins the 2026-08-07 P0 fix
// to stalePeriodicExhaustedCleanupSQL — when probe confirms healthy,
// the SQL must clear availability_state='suspended' together with
// quota_state. Otherwise credentials enter the
// "quota='ok' but availability='suspended'" self-contradiction (cred 22
// production snapshot), which is unroutable AND unrecoverable via any
// auto path.
//
// Contract:
//   - availability_state='suspended' is flipped to 'ready'
//   - availability_recover_at is cleared (NULL)
//   - non-suspended availability_state is left untouched
func TestStalePeriodicSyncAvailabilitySQLGuard(t *testing.T) {
	sql := stalePeriodicExhaustedCleanupSQL()
	mustContain := []string{
		// availability_state='suspended' → 'ready'
		"availability_state      = CASE",
		"WHEN availability_state = 'suspended' THEN 'ready'",
		// availability_recover_at cleared when previously suspended
		"availability_recover_at = CASE",
		"WHEN availability_state = 'suspended' THEN NULL",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("stalePeriodicExhaustedCleanupSQL missing %q — 2026-08-07 P0 regression:\n%s", want, sql)
		}
	}
}

// TestRecoverOrdering_AvailabilityBeforeQuota pins the 2026-08-07 P0
// execution-order constraint: the 60s recover() tick must run the
// availability recovery SQL BEFORE the quota recovery SQL.
//
// Why this matters: the new suspended-recovery guard checks
// quota_state to refuse auto-flip while quota is still hard-failed.
// If the quota SQL runs first and clears periodic_exhausted → 'ok',
// the subsequent availability SQL can no longer distinguish
// "periodic whose recover_at just elapsed" from "originally ok" —
// suspended credentials would lose their guard context and the
// ticker could mis-fire on rows that should stay suspended.
//
// We assert by reading the source and checking that
// `availability_state = 'ready'` (the SET target of the first UPDATE)
// appears earlier in recover() than the string `quota_state = 'ok'`
// (the SET target of the second UPDATE).
func TestRecoverOrdering_AvailabilityBeforeQuota(t *testing.T) {
	src, err := os.ReadFile("credential_recovery.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	availIdx := strings.Index(body, "availability_state = 'ready'")
	quotaIdx := strings.Index(body, "quota_state = 'ok'")
	if availIdx < 0 || quotaIdx < 0 {
		t.Fatalf("could not locate both availability and quota SET targets in credential_recovery.go")
	}
	if availIdx >= quotaIdx {
		t.Fatalf("availability recovery must run BEFORE quota recovery in recover(); got availability@%d quota@%d",
			availIdx, quotaIdx)
	}
}

// TestRecoverExpiredBindingsIgnoresBackoffState verifies that expired cmb rows
// are eligible for recovery regardless of their node_probe_state.next_retry_at,
// preventing indefinite strandings after transient failures.
//
// Background: 2026-07-24 incident where pulian glm-5.2 was stranded for 24h
// because consecutive_failures=7 triggered the 24h backoff tier, and the
// original expiredCmbRecoverySQL skipped ALL nodes with next_retry_at > now().
// The fix removes the next_retry_at check entirely: if cmb.unavailable_recover_at
// has elapsed, we force-retry immediately regardless of backoff state (unless
// manually paused).
func TestRecoverExpiredBindingsIgnoresBackoffState(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// Simulate a node stuck in 6h backoff (next_retry_at 5h from now)
	// with cmb.available=FALSE and unavailable_recover_at already elapsed.
	// The SQL should SELECT this row because we now ignore next_retry_at entirely.
	mock.ExpectQuery(`SELECT cmb\.credential_id, pm\.raw_model_name`).
		WillReturnRows(
			pgxmock.NewRows([]string{"credential_id", "raw_model_name"}).
				AddRow(42, "glm-5.2"),
		)

	var submitted []string
	submitter := func(credID int, model string) {
		submitted = append(submitted, fmt.Sprintf("cred=%d,model=%s", credID, model))
	}

	r := &CredentialRecovery{
		db:                       mock,
		probeSubmitter:           submitter,
		invalidateCandidateCache: func(credID int) {},
	}

	if err := r.recoverExpiredBindings(context.Background()); err != nil {
		t.Fatalf("recoverExpiredBindings failed: %v", err)
	}

	if len(submitted) != 1 {
		t.Fatalf("expected 1 submitted probe, got %d: %v", len(submitted), submitted)
	}
	if submitted[0] != "cred=42,model=glm-5.2" {
		t.Fatalf("expected cred=42,model=glm-5.2, got %s", submitted[0])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 2026-08-17 P0 fix: fresh-degraded (in-cooldown) self-check tests
// -----------------------------------------------------------------------------

// TestFreshDegradedCmbSQLGuards pins the safety guards of the new
// fresh-degraded SELECT. See freshDegradedCmbSQL() for the full contract.
func TestFreshDegradedCmbSQLGuards(t *testing.T) {
	sql := freshDegradedCmbSQL()
	mustContain := []string{
		// cmb 谓词：只挑 continuous_failure + cooldown 内 + 已被踢至少 60s
		"cmb.available = FALSE",
		"cmb.unavailable_reason = 'continuous_failure'",
		"cmb.unavailable_at <= now() - INTERVAL '60 seconds'",
		"cmb.unavailable_recover_at IS NOT NULL",
		"cmb.unavailable_recover_at > now()",
		// 硬保护：manual / admin_protected / lifecycle 一律不动
		"COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'",
		"COALESCE(cmb.admin_protected, FALSE) = FALSE",
		"COALESCE(c.status, 'active') = 'active'",
		"COALESCE(c.lifecycle_status, 'active') = 'active'",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"c.availability_state = 'ready'",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
		"p.enabled = TRUE",
		// 跳过 node_probe_state paused 或 mid-cycle
		"nps.paused = TRUE OR nps.next_retry_at > now()",
		// 输出列：必须包含 credential_id + raw_model_name
		"cmb.credential_id",
		"pm.raw_model_name",
		// ORDER BY 最旧 → 最新的，方便已恢复的供应商先被探活
		"ORDER BY cmb.unavailable_at ASC",
		// LIMIT 防止惊群
		"LIMIT 30",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("freshDegradedCmbSQL missing %q in:\n%s", want, sql)
		}
	}
	// 该 SELECT 必须不触碰 cmb.available（只读路径，由 probe 路径写回）。
	mustNotContain := []string{
		"UPDATE credential_model_bindings",
		"SET cmb.available = TRUE",
		"SET    available = TRUE",
	}
	for _, want := range mustNotContain {
		if strings.Contains(sql, want) {
			t.Fatalf("freshDegradedCmbSQL must NOT mutate cmb.available (got %q) in:\n%s", want, sql)
		}
	}
}

// TestRecoverFreshDegradedBindingsEnqueuesProbes mirrors the
// TestRecoverExpiredBindingsEnqueuesProbes test but for the new
// in-cooldown self-check branch: rows returned by the SELECT are handed to
// the probeSubmitter, the candidate cache is invalidated for unique cred
// IDs, and the function returns nil error when pgxmock provides two rows.
func TestRecoverFreshDegradedBindingsEnqueuesProbes(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	rows := pgxmock.NewRows([]string{"credential_id", "raw_model_name"}).
		AddRow(22, "glm-5.2").   // 智码 zhipu glm-5.2
		AddRow(33, "minimax-m3") // 智码 minimax-m3
	mock.ExpectQuery("FROM credential_model_bindings cmb").
		WillReturnRows(rows)

	r := &CredentialRecovery{db: mock, done: make(chan struct{})}

	var (
		mu          sync.Mutex
		submitted   []string
		invalidated = make(map[int]int)
	)
	r.SetProbeSubmitter(func(credID int, model string) {
		mu.Lock()
		defer mu.Unlock()
		submitted = append(submitted, fmt.Sprintf("%d|%s", credID, model))
	})
	r.SetInvalidateCandidateCache(func(credID int) {
		mu.Lock()
		defer mu.Unlock()
		invalidated[credID]++
	})

	if err := r.recoverFreshDegradedBindings(context.Background()); err != nil {
		t.Fatalf("recoverFreshDegradedBindings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	wantSubmitted := []string{"22|glm-5.2", "33|minimax-m3"}
	if len(submitted) != len(wantSubmitted) {
		t.Fatalf("submitted count = %d, want %d (got %v)", len(submitted), len(wantSubmitted), submitted)
	}
	for _, w := range wantSubmitted {
		found := false
		for _, s := range submitted {
			if s == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing submit for %q (got %v)", w, submitted)
		}
	}
	if invalidated[22] < 1 || invalidated[33] < 1 {
		t.Errorf("expected invalidateCandidateCache to be called for cred 22 & 33, got %v", invalidated)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestRecoverFreshDegradedBindingsNoOpWhenNoSubmitter mirrors the
// safety pattern from the expired-binding tests: when no NodeProbeWorker
// is wired yet, the new branch must be a silent no-op (no DB query,
// no error).
func TestRecoverFreshDegradedBindingsNoOpWhenNoSubmitter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	// No ExpectQuery: if the code actually runs the SELECT, pgxmock will
	// panic with "unexpected call".

	r := &CredentialRecovery{db: mock, done: make(chan struct{})}
	if err := r.recoverFreshDegradedBindings(context.Background()); err != nil {
		t.Fatalf("expected nil error when probeSubmitter is nil, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestCredentialRecoverySetTickInterval pins the env-overridable interval
// added for the 2026-08-17 P0 fix. Verifies:
//   - new instance uses defaultCredentialRecoveryInterval until overridden
//   - SetTickInterval(<positive>) is picked up by tickIntervalLocked()
//   - Very-short positive intervals are clamped to 1s.
//   - SetTickInterval(0) marks the loop "disabled": tickIntervalLocked()
//     returns 0 so run() can switch to the long heart-beat (see run()).
//   - SetTickInterval(-1) is ignored (no panic, no overwrite).
func TestCredentialRecoverySetTickInterval(t *testing.T) {
	r := &CredentialRecovery{}

	if got := r.tickIntervalLocked(); got != defaultCredentialRecoveryInterval {
		t.Fatalf("default tickIntervalLocked() = %v, want %v (no override yet → boot default)", got, defaultCredentialRecoveryInterval)
	}

	r.SetTickInterval(20 * time.Second)
	if got := r.tickIntervalLocked(); got != 20*time.Second {
		t.Fatalf("after SetTickInterval(20s) tickIntervalLocked() = %v, want 20s", got)
	}

	// Very short intervals get clamped to 1s so a typo in env doesn't
	// DDoS the database.
	r.SetTickInterval(100 * time.Millisecond)
	if got := r.tickIntervalLocked(); got != time.Second {
		t.Fatalf("after SetTickInterval(100ms) tickIntervalLocked() = %v, want 1s clamp", got)
	}

	// Negative values are ignored (defensive — env parsing may yield -1).
	r.SetTickInterval(-1 * time.Second)
	if got := r.tickIntervalLocked(); got != time.Second {
		t.Fatalf("after SetTickInterval(-1s) tickIntervalLocked() = %v, want previous 1s (no overwrite)", got)
	}

	// 0 = disabled sentinel: tickIntervalLocked() returns 0 so that run()
	// can resolve it to a long heart-beat (disabledProbeInterval) instead
	// of crashing NewTicker(0). The key invariant: tickIntervalLocked() never
	// silently resurrects the boot default once the operator has explicitly
	// disabled us.
	r.SetTickInterval(0)
	if got := r.tickIntervalLocked(); got != 0 {
		t.Fatalf("after SetTickInterval(0) tickIntervalLocked() = %v, want 0 (disabled sentinel, not boot default)", got)
	}

	// Re-arming with a positive value must put us back in the enabled state
	// — the disabled state is reversible.
	r.SetTickInterval(45 * time.Second)
	if got := r.tickIntervalLocked(); got != 45*time.Second {
		t.Fatalf("after SetTickInterval(45s) re-arm tickIntervalLocked() = %v, want 45s", got)
	}
}

// TestCredentialRecoveryRunDoesNotPanicOnDisabled is the regression pin for
// the 2026-08-17 audit finding that SetTickInterval(0) used to flow through
// to time.NewTicker(0), which panics with "non-positive interval for
// NewTicker". The run() loop must resolve a 0 interval to
// disabledProbeInterval before constructing the ticker.
func TestCredentialRecoveryRunDoesNotPanicOnDisabled(t *testing.T) {
	r := &CredentialRecovery{done: make(chan struct{})}
	r.SetTickInterval(0) // would have panicked before the fix

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Errorf("run() panicked on disabled interval: %v", rec)
			}
			cancel()
		}()
		// Give the loop a slice of a second to arm the ticker; if NewTicker
		// were called with 0 it would panic before this sleep finishes.
		time.Sleep(150 * time.Millisecond)
	}()
	r.run(ctx)
}

// TestHealthAutoRecoverSetTickIntervalDisabled mirrors the credential
// recovery test for the secondary recovery worker: SetTickInterval(0) must
// drive the loop into the disabled state (return 0 from currentInterval),
// not silently revert to the boot-time default.
func TestHealthAutoRecoverSetTickIntervalDisabled(t *testing.T) {
	w := NewHealthAutoRecover(nil, time.Minute)

	if got := w.currentInterval(); got != time.Minute {
		t.Fatalf("default currentInterval() = %v, want 1m", got)
	}

	w.SetTickInterval(0)
	if got := w.currentInterval(); got != 0 {
		t.Fatalf("after SetTickInterval(0) currentInterval() = %v, want 0 (disabled)", got)
	}

	w.SetTickInterval(30 * time.Second)
	if got := w.currentInterval(); got != 30*time.Second {
		t.Fatalf("after re-arm SetTickInterval(30s) currentInterval() = %v, want 30s", got)
	}
}

// -----------------------------------------------------------------------------
// 会话优化 v4 T5 — 36h 成功回看恢复扫描 (FR-4 R4.4 / UT-CR-09)
// -----------------------------------------------------------------------------

// TestLookbackCandidateSQLGuards pins the UT-CR-09 eligibility contract:
// only degraded/offline bindings with a SUCCESS inside the lookback window
// are candidates; the SELECT never mutates cmb.available.
func TestLookbackCandidateSQLGuards(t *testing.T) {
	sql := lookbackCandidateSQL()
	mustContain := []string{
		// degraded/offline predicate with cooldown elapsed-or-unscheduled
		"cmb.available = FALSE",
		"cmb.unavailable_recover_at IS NULL OR cmb.unavailable_recover_at <= now()",
		// hard guards (mirror expiredCmbRecoverySQL)
		"COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'",
		"COALESCE(cmb.admin_protected, FALSE) = FALSE",
		"COALESCE(c.status, 'active') = 'active'",
		"COALESCE(c.lifecycle_status, 'active') = 'active'",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"c.availability_state = 'ready'",
		// the 36h success window — on BOTH log tables
		"FROM request_logs_hot rl",
		"FROM request_logs rl",
		"rl.success = TRUE",
		"rl.ts > now() - make_interval(hours => $1)",
		"COALESCE(rl.outbound_model, rl.client_model) = pm.raw_model_name",
		// bounded fan-out
		"LIMIT $2",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("lookbackCandidateSQL missing %q in:\n%s", want, sql)
		}
	}
	// SELECT-only: the authoritative cmb.available flip belongs to the
	// node_probe runOne success branch, never to the scan.
	for _, banned := range []string{"UPDATE credential_model_bindings", "SET available = TRUE"} {
		if strings.Contains(sql, banned) {
			t.Fatalf("lookbackCandidateSQL must NOT contain %q (select-only contract):\n%s", banned, sql)
		}
	}
}

// mapHotStub satisfies LookbackHotConfig for the resolution tests.
type mapHotStub map[string]int

func (m mapHotStub) GetInt(key string, defaultValue int) int {
	if v, ok := m[key]; ok {
		return v
	}
	return defaultValue
}

// TestLookbackIntervalAndWindowResolution pins the config precedence:
// hotconfig → env → default (15min / 36h). Non-positive window values fall
// back to the default so a bad settings row cannot disable the predicate.
func TestLookbackIntervalAndWindowResolution(t *testing.T) {
	r := &CredentialRecovery{}
	if got := r.lookbackScanIntervalLocked(); got != defaultLookbackScanInterval {
		t.Fatalf("default lookback interval = %v, want %v", got, defaultLookbackScanInterval)
	}
	if got := r.lookbackWindowHoursLocked(); got != defaultLookbackWindowHours {
		t.Fatalf("default lookback window = %dh, want %dh", got, defaultLookbackWindowHours)
	}

	t.Setenv("LLM_GATEWAY_RECOVERY_LOOKBACK_INTERVAL_SECONDS", "120")
	if got := r.lookbackScanIntervalLocked(); got != 2*time.Minute {
		t.Fatalf("env interval = %v, want 2m", got)
	}
	t.Setenv("LLM_GATEWAY_RECOVERY_LOOKBACK_WINDOW_HOURS", "12")
	if got := r.lookbackWindowHoursLocked(); got != 12 {
		t.Fatalf("env window = %dh, want 12h", got)
	}

	r.SetLookbackHotConfig(mapHotStub{
		HotKeyRecoveryLookbackIntervalSeconds: 30,
		HotKeyRecoveryLookbackWindowHours:     48,
	})
	if got := r.lookbackScanIntervalLocked(); got != 30*time.Second {
		t.Fatalf("hotconfig interval = %v, want 30s (hotconfig wins over env)", got)
	}
	if got := r.lookbackWindowHoursLocked(); got != 48 {
		t.Fatalf("hotconfig window = %dh, want 48h (hotconfig wins over env)", got)
	}

	// Non-positive window from hotconfig falls back to env, then default.
	r.SetLookbackHotConfig(mapHotStub{HotKeyRecoveryLookbackWindowHours: 0})
	if got := r.lookbackWindowHoursLocked(); got != 12 {
		t.Fatalf("hotconfig window 0 must fall back to env 12h, got %dh", got)
	}

	// Very small intervals clamp to 1s.
	r.SetLookbackHotConfig(mapHotStub{HotKeyRecoveryLookbackIntervalSeconds: 1})
	if got := r.lookbackScanIntervalLocked(); got != time.Second {
		t.Fatalf("1s hotconfig interval must stay 1s, got %v", got)
	}

	// An explicit 0 from hotconfig is the disabled sentinel (same semantics
	// as SetTickInterval(0) on the 30s loop) — NOT a fall-through to default.
	r.SetLookbackHotConfig(mapHotStub{HotKeyRecoveryLookbackIntervalSeconds: 0})
	if got := r.lookbackScanIntervalLocked(); got != 0 {
		t.Fatalf("explicit hotconfig 0 must disable the scan (0 sentinel), got %v", got)
	}
}

// TestScanLookbackTriggersRecoveryAndProbe pins the positive UT-CR-09
// semantics at the unit level: a candidate row (already filtered by the SQL
// success predicate) is claimed via the SKIP LOCKED lease tx, receives the
// Recover(30) success write, and is handed to the dual-round probe entry.
func TestScanLookbackTriggersRecoveryAndProbe(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// Candidate query: one degraded binding with in-window success.
	mock.ExpectQuery("FROM credential_model_bindings cmb").
		WithArgs(36, lookbackBatchLimit).
		WillReturnRows(pgxmock.NewRows([]string{"credential_id", "raw_model_name", "tenant_id"}).
			AddRow(22, "glm-5.2", "default"))

	// Claim: BEGIN → SELECT ... FOR UPDATE SKIP LOCKED (row exists) →
	// UPDATE lease → COMMIT (the node_probe.go:1140-1209 shape).
	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE SKIP LOCKED").
		WithArgs(22, "glm-5.2").
		WillReturnRows(pgxmock.NewRows([]string{"one"}).AddRow(1))
	mock.ExpectExec("UPDATE node_probe_state").
		WithArgs(pgxmock.AnyArg(), 22, "glm-5.2").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	r := &CredentialRecovery{db: mock, lookbackDB: mock, done: make(chan struct{})}
	var (
		mu        sync.Mutex
		recovers  []string
		submitted []string
	)
	r.SetURSMRecoverSink(func(ctx context.Context, tenantID string, credID int, model string, success bool, latencyMs int) error {
		mu.Lock()
		defer mu.Unlock()
		recovers = append(recovers, fmt.Sprintf("%s|%d|%s|%v", tenantID, credID, model, success))
		return nil
	})
	r.SetProbeSubmitter(func(credID int, model string) {
		mu.Lock()
		defer mu.Unlock()
		submitted = append(submitted, fmt.Sprintf("%d|%s", credID, model))
	})
	r.SetInvalidateCandidateCache(func(int) {})

	r.scanLookbackRecoveries(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(recovers) != 1 || recovers[0] != "default|22|glm-5.2|true" {
		t.Fatalf("recover writes = %v, want one evidence-backed success=true write", recovers)
	}
	if len(submitted) != 1 || submitted[0] != "22|glm-5.2" {
		t.Fatalf("probe submissions = %v, want the dual-round probe for 22|glm-5.2", submitted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestScanLookbackNoRowsNoTrigger pins the negative half of UT-CR-09 at the
// unit level: no candidate rows (SQL excluded them — no in-window success)
// → zero recover writes, zero probe submissions, no claim transactions.
func TestScanLookbackNoRowsNoTrigger(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("FROM credential_model_bindings cmb").
		WithArgs(36, lookbackBatchLimit).
		WillReturnRows(pgxmock.NewRows([]string{"credential_id", "raw_model_name", "tenant_id"}))

	r := &CredentialRecovery{db: mock, lookbackDB: mock, done: make(chan struct{})}
	calls := 0
	r.SetURSMRecoverSink(func(context.Context, string, int, string, bool, int) error {
		calls++
		return nil
	})
	r.SetProbeSubmitter(func(int, string) { calls++ })

	r.scanLookbackRecoveries(context.Background())
	if calls != 0 {
		t.Fatalf("no candidates must trigger nothing, got %d hook calls", calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestScanLookbackSkipsLeasedOrPausedRows pins the cross-instance lease: a
// claim whose SELECT finds no lease-free row and whose INSERT loses the ON
// CONFLICT race (row exists, leased/paused elsewhere) reports claimed=false
// and the scan skips both the recover write and the probe submission.
func TestScanLookbackSkipsLeasedOrPausedRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("FROM credential_model_bindings cmb").
		WithArgs(36, lookbackBatchLimit).
		WillReturnRows(pgxmock.NewRows([]string{"credential_id", "raw_model_name", "tenant_id"}).
			AddRow(33, "minimax-m3", "default"))

	// SELECT FOR UPDATE SKIP LOCKED → no rows (leased by another instance);
	// INSERT ... ON CONFLICT DO NOTHING → 0 rows affected (row existed).
	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE SKIP LOCKED").
		WithArgs(33, "minimax-m3").
		WillReturnRows(pgxmock.NewRows([]string{"one"}))
	mock.ExpectExec("INSERT INTO node_probe_state").
		WithArgs(33, "minimax-m3", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectRollback()

	r := &CredentialRecovery{db: mock, lookbackDB: mock, done: make(chan struct{})}
	calls := 0
	r.SetURSMRecoverSink(func(context.Context, string, int, string, bool, int) error {
		calls++
		return nil
	})
	r.SetProbeSubmitter(func(int, string) { calls++ })

	r.scanLookbackRecoveries(context.Background())
	if calls != 0 {
		t.Fatalf("leased candidate must be skipped entirely, got %d hook calls", calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestClaimLookbackInsertsMissingProbeRow pins the never-probed branch: a
// candidate degraded purely by continuous_failure (no node_probe_state row)
// gets one INSERTed with a 5s-armed next_retry_at plus the lease so the
// worker can pick it up — the scan does not silently drop it.
func TestClaimLookbackInsertsMissingProbeRow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE SKIP LOCKED").
		WithArgs(44, "never-probed-model").
		WillReturnRows(pgxmock.NewRows([]string{"one"}))
	mock.ExpectExec("INSERT INTO node_probe_state").
		WithArgs(44, "never-probed-model", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	r := &CredentialRecovery{lookbackDB: mock}
	claimed, err := r.claimLookbackCandidate(context.Background(), 44, "never-probed-model")
	if err != nil || !claimed {
		t.Fatalf("claim = (%v, %v), want (true, nil) for a never-probed candidate", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestScanLookbackNoOpWithoutHooks pins the wiring safety: with neither the
// URSM recover sink nor the probe submitter wired, the scan is a silent
// no-op (no SQL issued) — mirroring recoverFreshDegradedBindings.
func TestScanLookbackNoOpWithoutHooks(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	// No expectations: any query would fail the test.

	r := &CredentialRecovery{db: mock, lookbackDB: mock, done: make(chan struct{})}
	r.scanLookbackRecoveries(context.Background())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}
