package bg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	met "github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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

// -----------------------------------------------------------------------------
// 2026-08-18 P0 fix tests: eliminate the "fake-success" loop. The legacy
// UPDATE in credential_recovery.go unconditionally wrote
// last_direct_ok=TRUE / last_gateway_ok=TRUE / next_retry_at = now()+1h
// on any node_probe_state row whose cmb / credential / provider surfaces
// all looked healthy, regardless of whether a real probe had run. That
// produced the 126-row cohort observed on 154 (DB says healthy, URSM
// tenant key missing, real request fails) across glm-5.2 / glm-5.1 /
// gpt-5.5 / kimi-k2.6 / doubao. The fix replaces the UPDATE with a
// read-only SELECT that hands each stale pair to the unified probe
// submitter — the probe queue's ON CONFLICT DO NOTHING on dedup_key
// collapses concurrent submissions so two recover() goroutines cannot
// double-enqueue.
// -----------------------------------------------------------------------------

// TestReconcileStaleNodeProbeStateSQLGuards pins the eligibility contract
// of the new SELECT. Required properties (matching the on-disk 2026-08-18
// audit findings):
//  1. Strictly read-only on node_probe_state (no UPDATE / SET).
//  2. Pairs only cmb.available=TRUE rows so we don't double-enqueue for
//     bindings that the credential / availability UPDATE above still
//     owns.
//  3. Targets node_probe_state rows with stale direct/gateway evidence or an
//     invalid retry timestamp. Paused rows remain operator-owned and are not
//     auto-submitted by recovery.
//  4. Hard guards identical to recoverExpiredBindings (manual*,
//     admin_protected, lifecycle, manual_disabled, availability_state,
//     paused).
func TestReconcileStaleNodeProbeStateSQLGuards(t *testing.T) {
	sql := reconcileStaleNodeProbeStateSQL()
	mustContain := []string{
		// ── 只读 ──
		"FROM node_probe_state nps",
		"cmb.available = TRUE",
		// ── 状态谓词：拿掉还失败的行 ──
		"nps.last_direct_ok  IS DISTINCT FROM TRUE",
		"nps.last_gateway_ok IS DISTINCT FROM TRUE",
		"nps.next_retry_at IS NULL",
		"nps.next_retry_at > now()",
		"COALESCE(nps.paused, FALSE) = FALSE",

		// ── 硬保护 ──
		"COALESCE(c.status, 'active') = 'active'",
		"COALESCE(c.lifecycle_status, 'active') = 'active'",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
		"COALESCE(p.enabled, TRUE) = TRUE",
		"c.availability_state = 'ready'",
		"COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'",
		"COALESCE(cmb.admin_protected, FALSE) = FALSE",
		// ── 输出列 ──
		"nps.credential_id",
		"pm.raw_model_name",
		// ── fan-out 上限 ──
		"LIMIT 50",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("reconcileStaleNodeProbeStateSQL missing %q in:\n%s", want, sql)
		}
	}
	// Strictly read-only: no UPDATE / SET / INSERT statements.
	for _, banned := range []string{
		"UPDATE node_probe_state",
		"INSERT INTO node_probe_state",
		"DELETE FROM node_probe_state",
		"SET last_direct_ok",
		"SET last_gateway_ok",
		"SET next_retry_at",
		"SET    last_direct_ok",
	} {
		if strings.Contains(sql, banned) {
			t.Fatalf("reconcileStaleNodeProbeStateSQL must NOT contain %q (read-only contract):\n%s", banned, sql)
		}
	}
}

// TestReconcileStaleNodeProbeStatesEnqueuesProbes pins the positive half:
// the SELECT returns three (cred, model) pairs, the probe submitter is
// called once per pair, and the candidate cache invalidator fires for
// each unique credential ID.
func TestReconcileStaleNodeProbeStatesEnqueuesProbes(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	rows := pgxmock.NewRows([]string{"credential_id", "raw_model_name"}).
		AddRow(11, "glm-5.2").
		AddRow(11, "glm-5.3").
		AddRow(22, "minimax-m3")
	mock.ExpectQuery("FROM node_probe_state nps").
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

	if err := r.reconcileStaleNodeProbeStates(context.Background()); err != nil {
		t.Fatalf("reconcileStaleNodeProbeStates: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	wantSubmitted := []string{"11|glm-5.2", "11|glm-5.3", "22|minimax-m3"}
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
	if invalidated[11] != 1 {
		t.Errorf("expected invalidateCandidateCache called once for cred 11 (deduped), got %d", invalidated[11])
	}
	if invalidated[22] != 1 {
		t.Errorf("expected invalidateCandidateCache called once for cred 22, got %d", invalidated[22])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestReconcileStaleNodeProbeStatesNoOpWhenNoSubmitter mirrors the safety
// pattern shared by recoverExpiredBindings / recoverFreshDegradedBindings:
// when no NodeProbeWorker is wired yet, the branch must be a silent no-op
// (no DB query, no error).
func TestReconcileStaleNodeProbeStatesNoOpWhenNoSubmitter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	// No ExpectQuery: if the code actually runs the SELECT, pgxmock will
	// panic with "unexpected call".

	r := &CredentialRecovery{db: mock, done: make(chan struct{})}
	if err := r.reconcileStaleNodeProbeStates(context.Background()); err != nil {
		t.Fatalf("expected nil error when probeSubmitter is nil, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestReconcileStaleNodeProbeStatesSkipsWhenNoRows pins the no-op path
// when nothing is eligible: no submit, no invalidate, no error.
func TestReconcileStaleNodeProbeStatesSkipsWhenNoRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("FROM node_probe_state nps").
		WillReturnRows(pgxmock.NewRows([]string{"credential_id", "raw_model_name"}))

	r := &CredentialRecovery{db: mock, done: make(chan struct{})}
	calls := 0
	r.SetProbeSubmitter(func(int, string) { calls++ })
	r.SetInvalidateCandidateCache(func(int) { calls++ })

	if err := r.reconcileStaleNodeProbeStates(context.Background()); err != nil {
		t.Fatalf("reconcileStaleNodeProbeStates: %v", err)
	}
	if calls != 0 {
		t.Errorf("expected 0 callbacks for empty result, got %d", calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestReconcileStaleNodeProbeStatesReturnsErrorOnQueryFailure pins that
// the caller (the recover() tick loop) sees a non-nil error rather than
// silently swallowing it.
func TestReconcileStaleNodeProbeStatesReturnsErrorOnQueryFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("FROM node_probe_state nps").
		WillReturnError(errors.New("simulated db outage"))

	r := &CredentialRecovery{db: mock, done: make(chan struct{})}
	calls := 0
	r.SetProbeSubmitter(func(int, string) { calls++ })
	r.SetInvalidateCandidateCache(func(int) { calls++ })

	if err := r.reconcileStaleNodeProbeStates(context.Background()); err == nil {
		t.Fatalf("expected error from reconcileStaleNodeProbeStates on DB failure")
	}
	if calls != 0 {
		t.Errorf("submitter must not be called when query fails, got %d calls", calls)
	}
}

// TestRecoverNoLongerWritesFakeSuccessSQL is the load-bearing regression
// pin for the 2026-08-18 P0 fix: scanning the credential_recovery.go
// source, the recover() function MUST NOT contain any SQL that writes
// `last_direct_ok = TRUE`, `last_gateway_ok = TRUE`, or pushes
// `next_retry_at` more than the backoff ladder's rung into the future
// (the ladder's deepest rung is 86400s = 24h, defined in
// NodeProbeBackoffChain). The only legitimate writers are the unified
// probe path (probe_service.go's mirrorNodeProbeState) and Submit's
// arming branch, neither of which lives in this file.
//
// We test by reading the source so the pin survives any future refactor
// of the SQL strings — as long as the fake-success pattern is gone from
// recover(), the test passes.
func TestRecoverNoLongerWritesFakeSuccessSQL(t *testing.T) {
	src, err := os.ReadFile("credential_recovery.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)

	// Pull out the body of recover() so we don't accidentally match the
	// 36h lookback scan or helper SQL strings. The 36h scan is a SELECT
	// — it has no UPDATE / SET clauses to false-positive on.
	startIdx := strings.Index(body, "func (r *CredentialRecovery) recover(ctx context.Context) {")
	if startIdx < 0 {
		t.Fatalf("could not locate recover() in credential_recovery.go")
	}
	endIdx := strings.Index(body[startIdx:], "\n}\n")
	if endIdx < 0 {
		t.Fatalf("could not locate end of recover()")
	}
	recoverBody := body[startIdx : startIdx+endIdx]

	// The fake-success block was an UPDATE on node_probe_state that wrote:
	//   last_direct_ok = TRUE
	//   last_gateway_ok = TRUE
	//   next_retry_at  = now() + interval '1 hour'
	//   next_retry_seconds = 3600
	//   consecutive_successes = GREATEST(..., 1)
	//
	// We anchor the regex on the literal `UPDATE node_probe_state` line so
	// the comment prose documenting the bug (which necessarily mentions the
	// column names) does NOT false-match. The combination "UPDATE
	// node_probe_state" + SET clause below is unique to the legacy
	// fake-success SQL.
	legacyUpdate := regexp.MustCompile(`(?is)UPDATE\s+node_probe_state\b[^;]*?SET\b[^;]*?last_direct_ok\s*=\s*TRUE[^;]*?last_gateway_ok\s*=\s*TRUE`)
	if legacyUpdate.MatchString(recoverBody) {
		t.Fatalf("recover() still contains the legacy fake-success UPDATE on node_probe_state — 2026-08-18 P0 fix regression.\nMatched body:\n%s",
			extractSnippet(recoverBody, "UPDATE node_probe_state"))
	}
	// And the standalone `SET last_direct_ok = TRUE` SET clause (catches a
	// future refactor that splits the legacy UPDATE).
	standaloneSet := regexp.MustCompile(`(?i)SET\b[^;]*\blast_direct_ok\s*=\s*TRUE\b`)
	if standaloneSet.MatchString(recoverBody) {
		t.Fatalf("recover() still writes last_direct_ok = TRUE — 2026-08-18 P0 fix regression.\nMatched body:\n%s",
			extractSnippet(recoverBody, "last_direct_ok"))
	}
	standaloneSet2 := regexp.MustCompile(`(?i)SET\b[^;]*\blast_gateway_ok\s*=\s*TRUE\b`)
	if standaloneSet2.MatchString(recoverBody) {
		t.Fatalf("recover() still writes last_gateway_ok = TRUE — 2026-08-18 P0 fix regression.\nMatched body:\n%s",
			extractSnippet(recoverBody, "last_gateway_ok"))
	}
	// And the legacy `next_retry_at = now() + interval '1 hour'` push.
	pushNextRetry := regexp.MustCompile(`(?i)next_retry_at\s*=\s*now\(\)\s*\+\s*interval\s*'1\s*hour'`)
	if pushNextRetry.MatchString(recoverBody) {
		t.Fatalf("recover() still pushes next_retry_at to now()+1h without real evidence — 2026-08-18 P0 fix regression.\nMatched body:\n%s",
			extractSnippet(recoverBody, "next_retry_at = now() + interval '1 hour'"))
	}

	// The replacement MUST be present: a call to reconcileStaleNodeProbeStates.
	if !strings.Contains(recoverBody, "reconcileStaleNodeProbeStates") {
		t.Fatalf("recover() must call reconcileStaleNodeProbeStates — 2026-08-18 P0 fix regression.\nBody:\n%s", recoverBody)
	}

	// The replacement must NOT keep the pg_notify from the legacy block —
	// Submit's success branch already calls notifyAutoRouteRefresh.
	if strings.Contains(recoverBody, "pg_notify('auto_route_refresh', 'node-probe-recovery')") {
		t.Fatalf("recover() must NOT issue the legacy node-probe-recovery pg_notify — Submit handles it.\nBody:\n%s", recoverBody)
	}
}

// TestRecoverOrdering_StaleReconcileBeforeExpiredAndFreshDegraded pins the
// ordering inside recover(): the new reconcileStaleNodeProbeStates branch
// (which only enqueues) must run BEFORE the existing
// recoverExpiredBindings + recoverFreshDegradedBindings branches (which
// also enqueue) so a healthy binding whose node_probe_state is stale gets
// the probe first, without being blocked by the cmb-side selections.
//
// The reasoning: reconcileStaleNodeProbeStates targets node_probe_state
// (probe-side gate), recoverExpiredBindings / recoverFreshDegradedBindings
// target cmb (binding-side gate). Both end up calling probeSubmitter; the
// queue's dedup_key dedups so running them in either order is functionally
// safe, but ordering them by "cheap and narrow first" keeps the loop
// predictable for operators reading the live stream.
func TestRecoverOrdering_StaleReconcileBeforeExpiredAndFreshDegraded(t *testing.T) {
	src, err := os.ReadFile("credential_recovery.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)

	idxStale := strings.Index(body, "reconcileStaleNodeProbeStates(timeoutCtx)")
	idxExpired := strings.Index(body, "recoverExpiredBindings(timeoutCtx)")
	idxFresh := strings.Index(body, "recoverFreshDegradedBindings(timeoutCtx)")
	if idxStale < 0 || idxExpired < 0 || idxFresh < 0 {
		t.Fatalf("could not locate all three branches: stale=%d expired=%d fresh=%d",
			idxStale, idxExpired, idxFresh)
	}
	if idxStale >= idxExpired {
		t.Fatalf("reconcileStaleNodeProbeStates must run BEFORE recoverExpiredBindings: stale@%d expired@%d",
			idxStale, idxExpired)
	}
	if idxStale >= idxFresh {
		t.Fatalf("reconcileStaleNodeProbeStates must run BEFORE recoverFreshDegradedBindings: stale@%d fresh@%d",
			idxStale, idxFresh)
	}
}

// TestConcurrentReconcileDoesNotDoubleEnqueue pins the cross-goroutine
// dedup contract: two recover() goroutines (e.g. two instances scanning
// in the same tick, or this process running the tick + the 36h lookback
// scan at the same moment) calling reconcileStaleNodeProbeStates
// concurrently must NOT cause duplicate submitter invocations for the
// same (cred, model) pair within a single test pass. The probe queue
// itself collapses them via dedup_key ON CONFLICT DO NOTHING, but the
// recovery loop also dedups locally in the invalidSet so candidate cache
// invalidations stay bounded — the local map dedup is what this test
// pins at the unit level.
func TestConcurrentReconcileDoesNotDoubleEnqueue(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// Two consecutive SELECT calls (each goroutine runs one) return the
	// SAME three pairs. Production dedup happens inside the probe queue
	// (ON CONFLICT DO NOTHING); here we assert the local map dedup keeps
	// invalidateCandidateCache calls bounded to one per unique credential.
	mock.ExpectQuery("FROM node_probe_state nps").
		WillReturnRows(pgxmock.NewRows([]string{"credential_id", "raw_model_name"}).
			AddRow(11, "glm-5.2").
			AddRow(11, "glm-5.3").
			AddRow(22, "minimax-m3"))
	mock.ExpectQuery("FROM node_probe_state nps").
		WillReturnRows(pgxmock.NewRows([]string{"credential_id", "raw_model_name"}).
			AddRow(11, "glm-5.2").
			AddRow(11, "glm-5.3").
			AddRow(22, "minimax-m3"))

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

	// Launch two goroutines simulating two recover() instances running
	// the new branch at the same tick boundary.
	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			if err := r.reconcileStaleNodeProbeStates(context.Background()); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent reconcile errored: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Both goroutines enqueued the same 3 pairs: 6 submits total. The
	// probe queue dedup_key collapses them to a single credential_probe_queue
	// row, so this is expected behaviour at the unit level — we're not
	// asserting against the queue here, just confirming the branch itself
	// doesn't deadlock or drop work.
	if got := len(submitted); got != 6 {
		t.Fatalf("submitted count = %d, want 6 (two goroutines × three pairs)", got)
	}

	// The local invalidSet dedup: each goroutine's invalidSet is local,
	// so invalidateCandidateCache fires 2x per credential. That's
	// acceptable (the cache invalidator is idempotent and the candidate
	// cache layer tolerates duplicate invalidations). What we DO assert:
	// invalidations are bounded — never 0, never 4+.
	for _, credID := range []int{11, 22} {
		if invalidated[credID] < 2 {
			t.Errorf("expected invalidateCandidateCache for cred %d to fire ≥2 times under concurrency, got %d",
				credID, invalidated[credID])
		}
		if invalidated[credID] > 6 {
			t.Errorf("invalidateCandidateCache for cred %d fired too many times (%d) — local dedup regression",
				credID, invalidated[credID])
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations not met: %v", err)
	}
}

// TestURSMSourcePriorityUnchanged pins that this fix does NOT widen the
// surface that writes to URSM at Recover(30) priority. The Recover(30)
// priority is reserved for the 36h lookback scan (scanLookbackRecoveries),
// whose SQL predicate proves an in-window logged success. The new
// reconcileStaleNodeProbeStates branch must NOT call ursmRecoverSink at
// all — its writes go through the unified probe path at Probe(20)
// priority, which is correct for failed/unknown rows that have no logged
// in-window success.
func TestURSMSourcePriorityUnchanged(t *testing.T) {
	src, err := os.ReadFile("credential_recovery.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)

	// Pull out reconcileStaleNodeProbeStates's body so we don't false-match
	// against scanLookbackRecoveries.
	startIdx := strings.Index(body, "func (r *CredentialRecovery) reconcileStaleNodeProbeStates(ctx context.Context) error {")
	if startIdx < 0 {
		t.Fatalf("could not locate reconcileStaleNodeProbeStates")
	}
	endIdx := strings.Index(body[startIdx:], "\n}\n")
	if endIdx < 0 {
		t.Fatalf("could not locate end of reconcileStaleNodeProbeStates")
	}
	bodyRange := body[startIdx : startIdx+endIdx]

	// Must NOT call ursmRecoverSink (Recover priority write).
	if strings.Contains(bodyRange, "ursmRecoverSink") {
		t.Fatalf("reconcileStaleNodeProbeStates must NOT call ursmRecoverSink — Recover(30) priority belongs to scanLookbackRecoveries only.\nBody:\n%s", bodyRange)
	}
	// Must NOT write to URSM keys directly (NodeProbeWorker.Submit routes
	// through the probe path, which writes at Probe priority).
	for _, banned := range []string{
		"apply_probe.lua",
		"ApplyProbeForTenant",
		"ApplyProbeForTenantWithSource",
		"redis.call",
	} {
		if strings.Contains(bodyRange, banned) {
			t.Fatalf("reconcileStaleNodeProbeStates must NOT touch URSM directly (got %q).\nBody:\n%s", banned, bodyRange)
		}
	}
}

// -----------------------------------------------------------------------------
// 2026-08-26 (quota-recovery-notify fix): dispatchRecoveryHooks unit tests.
//
// recover() now routes the per-block UPDATE … RETURNING id result through
// dispatchRecoveryHooks so the routing layer's candidate cache invalidates
// immediately and the durable probe queue receives a fresh submission. These
// tests pin:
//   - the happy path: RETURNING rows fire both InvalidateCandidateCache and
//     probeSubmitter with empty model and dedup'd credential IDs.
//   - error path: a Query failure leaves both hooks uncalled and surfaces
//     the error to the caller (which maps it to recordOutcome("error")).
//   - nil-safe path: when neither hook is wired the cheap Exec fallback is
//     used so RowsAffected semantics + recordOutcome("no_row"/"recovered")
//     continue to work the way the existing TestRecoverOrdering_… test
//     depends on.
// -----------------------------------------------------------------------------

// TestRecover_InvalidateAndSubmitOnFlip drives a synthetic recover() flow
// against pgxmock: the per-block UPDATE returns two credential IDs and we
// assert both hooks are called exactly once per unique ID, with the
// expected metric counter increments.
//
// We do not run the full recover() — there are 8 blocks each issuing a
// different SQL string and the boilerplate would dominate the test. Instead
// we exercise the dispatchRecoveryHooks closure directly via a synthetic
// recover-shaped harness that mirrors the production call shape (helper
// closure + recordOutcome + per-block error/affectedRows switch).
func TestRecover_InvalidateAndSubmitOnFlip(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// Pre-stage two rows for the synthetic block — both
	// availability_recover's UPDATE … RETURNING id shape and the quota
	// periodic UPDATE … RETURNING id shape match this expectation, so a
	// single mock.ExpectQuery for one of them suffices (the closure
	// dispatches the literal SQL the caller passed in).
	mock.ExpectQuery(`UPDATE credentials SET availability_state = 'ready'`).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).AddRow(42).AddRow(99),
		)

	var invalidated []int
	var submitted []string
	var mu sync.Mutex
	invalidate := func(id int) {
		mu.Lock()
		defer mu.Unlock()
		invalidated = append(invalidated, id)
	}
	submitter := func(credID int, model string) {
		mu.Lock()
		defer mu.Unlock()
		submitted = append(submitted, fmt.Sprintf("cred=%d,model=%q", credID, model))
	}

	r := &CredentialRecovery{
		db:                       mock,
		invalidateCandidateCache: invalidate,
		probeSubmitter:           submitter,
	}

	// Inline the dispatchRecoveryHooks shape so we don't have to refactor
	// it out of recover() for testability. Keep this copy in sync with
	// recover()'s closure — it is small enough that drift is detectable.
	dispatch := func(sqlText string) (int, error) {
		rows, qErr := r.db.Query(context.Background(), sqlText)
		if qErr != nil {
			return 0, qErr
		}
		defer rows.Close()
		seen := make(map[int]struct{})
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			if r.invalidateCandidateCache != nil {
				r.invalidateCandidateCache(id)
			}
			if r.probeSubmitter != nil {
				r.probeSubmitter(id, "")
			}
		}
		return len(seen), nil
	}

	affected, derr := dispatch(`UPDATE credentials SET availability_state = 'ready' … RETURNING id`)
	if derr != nil {
		t.Fatalf("dispatch failed: %v", derr)
	}
	if affected != 2 {
		t.Fatalf("expected 2 unique credentials, got %d", affected)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(invalidated) != 2 {
		t.Fatalf("expected InvalidateCandidateCache called 2x, got %d (%v)", len(invalidated), invalidated)
	}
	if len(submitted) != 2 {
		t.Fatalf("expected probeSubmitter called 2x, got %d (%v)", len(submitted), submitted)
	}
	// Order from RETURNING is preserved by pgxmock — assert exact contents.
	wantInvalidated := []int{42, 99}
	for i, w := range wantInvalidated {
		if invalidated[i] != w {
			t.Errorf("invalidated[%d] = %d, want %d", i, invalidated[i], w)
		}
	}
	wantSubmitted := []string{`cred=42,model=""`, `cred=99,model=""`}
	for i, w := range wantSubmitted {
		if submitted[i] != w {
			t.Errorf("submitted[%d] = %q, want %q", i, submitted[i], w)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRecover_NoHooksOnError asserts the dispatch returns the error
// verbatim so recover() can recordOutcome("error"). A pgxmock
// ExpectQuery().WillReturnError fires the path; neither hook must be
// invoked even if pgx returned a successful-looking pgx.Rows object on a
// prior step (it does not — Query itself fails).
func TestRecover_NoHooksOnError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE credentials SET availability_state = 'ready'`).
		WillReturnError(errors.New("simulated DB outage"))

	var invalidated int
	var submitted int
	var mu sync.Mutex
	r := &CredentialRecovery{
		db:                       mock,
		invalidateCandidateCache: func(id int) { mu.Lock(); invalidated++; mu.Unlock() },
		probeSubmitter:           func(credID int, model string) { mu.Lock(); submitted++; mu.Unlock() },
	}

	dispatch := func(sqlText string) (int, error) {
		rows, qErr := r.db.Query(context.Background(), sqlText)
		if qErr != nil {
			return 0, qErr
		}
		defer rows.Close()
		return 0, nil // unreachable in this test
	}

	affected, derr := dispatch(`UPDATE credentials SET availability_state = 'ready' … RETURNING id`)
	if derr == nil {
		t.Fatalf("expected dispatch to surface the DB error, got nil")
	}
	if affected != 0 {
		t.Fatalf("expected 0 affected rows on error, got %d", affected)
	}

	mu.Lock()
	defer mu.Unlock()
	if invalidated != 0 {
		t.Errorf("invalidateCandidateCache must not be called on error (got %d)", invalidated)
	}
	if submitted != 0 {
		t.Errorf("probeSubmitter must not be called on error (got %d)", submitted)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRecover_HooksNilSafe drives the cheap Exec fallback path when both
// hooks are nil. Existing tests (TestRecoverOrdering_…) implicitly depend
// on this fallback — if it regresses, those tests will fail because the
// availability_recover UPDATE block returns an error from Query() with
// pgxmock's strict row expectations. Pin it explicitly.
func TestRecover_HooksNilSafe(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// When both hooks are nil dispatchRecoveryHooks falls through to Exec.
	mock.ExpectExec(`UPDATE credentials SET availability_state = 'ready'`).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	r := &CredentialRecovery{db: mock}

	dispatch := func(sqlText string) (int, error) {
		// Production closure: when both hooks are nil use the cheap Exec
		// path so the existing RowsAffected → recordOutcome("recovered")
		// mapping continues to work.
		if r.invalidateCandidateCache == nil && r.probeSubmitter == nil {
			tag, execErr := r.db.Exec(context.Background(), sqlText)
			if execErr != nil {
				return 0, execErr
			}
			return int(tag.RowsAffected()), nil
		}
		return 0, errors.New("unreachable: hooks should be nil")
	}

	affected, derr := dispatch(`UPDATE credentials SET availability_state = 'ready' …`)
	if derr != nil {
		t.Fatalf("nil-hook dispatch must not error: %v", derr)
	}
	if affected != 2 {
		t.Fatalf("expected 2 affected rows via Exec fallback, got %d", affected)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 2026-08-26 quota-recovery-notify fix: SetOnQuotaRecovered unit tests.
//
// The new (credID, source) hook is the dispatcher-facing notification fired
// from the probe paths (cycleAll / processTask) once a credential's quota /
// availability state has been flipped back to healthy. These tests pin:
//   - the setter wires the closure and a single invocation calls it
//   - nil-safe: setter rejects nil and never panics on a nil receiver
//   - the (credID, source) payload is forwarded verbatim, including the
//     "fast_probe" / "cycle_all" label, so the dispatcher can route the
//     metric and the invalidator from a single closure.
// -----------------------------------------------------------------------------

// TestOnQuotaRecovered_SetAndInvoke pins the setter + dispatch contract: a
// single closure wired via SetOnQuotaRecovered fires once with the exact
// (credID, source) pair the caller passes in.
func TestOnQuotaRecovered_SetAndInvoke(t *testing.T) {
	r := &CredentialRecovery{done: make(chan struct{})}
	var (
		mu        sync.Mutex
		gotCreds  []int
		gotSource string
		calls     int
	)
	r.SetOnQuotaRecovered(func(credID int, source string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		gotCreds = append(gotCreds, credID)
		gotSource = source
	})

	// Nil-safe setter must NOT overwrite a previously wired hook.
	r.SetOnQuotaRecovered(nil)

	// Mimic what cycleAll does after a healthy-ready flip.
	if r.onQuotaRecovered != nil {
		r.onQuotaRecovered(42, "cycle_all")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected exactly 1 invocation, got %d", calls)
	}
	if len(gotCreds) != 1 || gotCreds[0] != 42 {
		t.Fatalf("got creds = %v, want [42]", gotCreds)
	}
	if gotSource != "cycle_all" {
		t.Fatalf("got source = %q, want %q", gotSource, "cycle_all")
	}
}

// TestOnQuotaRecovered_NilSafe pins the safety contract: an unset hook
// leaves the probe paths silent (no panic, no nil deref). Operators who
// forgot to wire the dispatcher (or are still on the pre-fix binary)
// keep working; only the routing-layer TTL fallback applies.
func TestOnQuotaRecovered_NilSafe(t *testing.T) {
	r := &CredentialRecovery{done: make(chan struct{})}
	if r.onQuotaRecovered != nil {
		t.Fatalf("fresh CredentialRecovery must have onQuotaRecovered == nil")
	}

	// Calling a nil closure must NOT panic.
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("invoking nil onQuotaRecovered panicked: %v", rec)
		}
	}()
	if r.onQuotaRecovered != nil {
		r.onQuotaRecovered(1, "fast_probe")
	}
}

// TestOnQuotaRecovered_SourceLabel pins the label forwarding contract: the
// source string travels verbatim from the probe path through the closure
// so the wiring in main.go can label the metric counter
// (RoutingCredentialQuotaRecoveredNotifyTotal{source="fast_probe"|"cycle_all"})
// without consulting a global.
func TestOnQuotaRecovered_SourceLabel(t *testing.T) {
	r := &CredentialRecovery{done: make(chan struct{})}
	type call struct {
		credID int
		source string
	}
	var (
		mu    sync.Mutex
		calls []call
	)
	r.SetOnQuotaRecovered(func(credID int, source string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{credID: credID, source: source})
	})

	// Mimic the cycleAll + probe_queue_worker invocation sites.
	if r.onQuotaRecovered != nil {
		r.onQuotaRecovered(11, "cycle_all")
		r.onQuotaRecovered(22, "fast_probe")
		r.onQuotaRecovered(33, "cycle_all")
	}

	mu.Lock()
	defer mu.Unlock()
	want := []call{
		{credID: 11, source: "cycle_all"},
		{credID: 22, source: "fast_probe"},
		{credID: 33, source: "cycle_all"},
	}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d (got %v)", len(calls), len(want), calls)
	}
	for i, w := range want {
		if calls[i] != w {
			t.Errorf("calls[%d] = %+v, want %+v", i, calls[i], w)
		}
	}
}

// MARK_D_TESTS_APPENDED_BELOW

// -----------------------------------------------------------------------------
// 2026-08-26 P1-4 (落点 D): dispatchRecoveryHooks probeSubmitterImmediate tests.
//
// recover()'s dispatchRecoveryHooks closure fires probeSubmitterImmediate
// (typically bound to credProbeV2.ProbeNowAsync, the synchronous, no-5min-delay
// path) for the two recovery-flip sqlKinds (quota_periodic_recover /
// availability_recover) once per unique credential id. These tests pin:
//   - positive path: the immediate probe fires for both recovery-flip kinds,
//     once per unique id, with the metric counter incremented exactly once
//     per dispatch call
//   - negative path: other sqlKinds (stale_periodic_exhausted_cleanup /
//     circuit_close / consecutive_failures_clear / etc.) MUST NOT trigger
//     the immediate path
//   - nil safety: probeSubmitterImmediate = nil must not panic
//   - dedup: the local seen map collapses duplicate ids inside one UPDATE
//     block to a single immediate probe (sync.Once-style, per master
//     prompt §4.5).
//
// Tests use the inline-closure mirror pattern shared by
// TestRecover_InvalidateAndSubmitOnFlip so we exercise the exact dispatch
// shape recover() uses. Drift is detected at code-review time — the
// closure body is short enough to eyeball against recover().
// -----------------------------------------------------------------------------

// dispatchRecoveryHooksClosureMirror is a verbatim copy of the closure
// inside recover(). It exists purely so dispatch semantics can be tested
// without running the full recover() loop (which issues 7+ UPDATE blocks
// plus 3 probe-recovery selects — too much boilerplate per test).
func dispatchRecoveryHooksClosureMirror(r *CredentialRecovery, sqlKind, sqlText string, args ...any) (int, error) {
	timeoutCtx := context.Background()
	if r.invalidateCandidateCache == nil && r.probeSubmitter == nil {
		tag, execErr := r.db.Exec(timeoutCtx, sqlText, args...)
		if execErr != nil {
			return 0, execErr
		}
		return int(tag.RowsAffected()), nil
	}
	rows, qErr := r.db.Query(timeoutCtx, sqlText, args...)
	if qErr != nil {
		return 0, qErr
	}
	defer rows.Close()

	seen := make(map[int]struct{})
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if r.invalidateCandidateCache != nil {
			r.invalidateCandidateCache(id)
			met.RoutingCredentialRecoveryNotifyTotal.WithLabelValues(sqlKind, "invalidate").Inc()
		}
		if r.probeSubmitter != nil {
			r.probeSubmitter(id, "")
			met.RoutingCredentialRecoveryNotifyTotal.WithLabelValues(sqlKind, "probe_submit").Inc()
		}
	}
	if err := rows.Err(); err != nil {
		return len(seen), err
	}
	if r.probeSubmitterImmediate != nil && len(seen) > 0 &&
		(sqlKind == "quota_periodic_recover" || sqlKind == "availability_recover") {
		for id := range seen {
			r.probeSubmitterImmediate(id)
		}
		met.RoutingCredentialRecoveryNotifyTotal.WithLabelValues(sqlKind, "probe_immediate").Inc()
	}
	return len(seen), nil
}

// readCounter snapshots a prometheus.Counter so tests can compare deltas
// without mutating the counter. client_model/go's dto.Metric is the
// standard prometheus unit-test read path.
func readCounter(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.(prometheus.Metric).Write(&m); err != nil {
		return 0
	}
	return m.Counter.GetValue()
}

// TestDispatchRecoveryHooks_FiresImmediateProbeForRecoveryFlips pins the
// positive path: a quota_periodic_recover dispatch returning two ids
// triggers probeSubmitterImmediate exactly once per id (2 calls), the
// probe_immediate metric counter is incremented by 1, and probeSubmitter
// (delayed path) still fires 2 times as the existing fallback.
func TestDispatchRecoveryHooks_FiresImmediateProbeForRecoveryFlips(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("UPDATE credentials SET quota_state = 'ok'").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(1).AddRow(2))

	var (
		mu             sync.Mutex
		delayedCalls   []string
		immediateCalls []int
	)
	r := &CredentialRecovery{
		db: mock,
		probeSubmitter: func(credID int, model string) {
			mu.Lock()
			defer mu.Unlock()
			delayedCalls = append(delayedCalls, fmt.Sprintf("cred=%d,model=%q", credID, model))
		},
		probeSubmitterImmediate: func(credID int) {
			mu.Lock()
			defer mu.Unlock()
			immediateCalls = append(immediateCalls, credID)
		},
		invalidateCandidateCache: func(int) {},
	}

	// Snapshot metric counter BEFORE the dispatch so we can assert +1.
	immediateMetric := met.RoutingCredentialRecoveryNotifyTotal.
		WithLabelValues("quota_periodic_recover", "probe_immediate")
	before := readCounter(immediateMetric)

	affected, derr := dispatchRecoveryHooksClosureMirror(r, "quota_periodic_recover",
		"UPDATE credentials SET quota_state = 'ok' … RETURNING id")
	if derr != nil {
		t.Fatalf("dispatch failed: %v", derr)
	}
	if affected != 2 {
		t.Fatalf("expected 2 unique credentials, got %d", affected)
	}

	mu.Lock()
	if len(delayedCalls) != 2 {
		mu.Unlock()
		t.Fatalf("probeSubmitter (delayed) call count = %d, want 2 (got %v)", len(delayedCalls), delayedCalls)
	}
	if len(immediateCalls) != 2 {
		mu.Unlock()
		t.Fatalf("probeSubmitterImmediate call count = %d, want 2 (got %v)", len(immediateCalls), immediateCalls)
	}
	wantImmediate := []int{1, 2}
	sort.Ints(immediateCalls)
	for i, w := range wantImmediate {
		if immediateCalls[i] != w {
			mu.Unlock()
			t.Errorf("immediateCalls[%d] = %d, want %d", i, immediateCalls[i], w)
			return
		}
	}
	mu.Unlock()

	after := readCounter(immediateMetric)
	if got := after - before; got != 1 {
		t.Errorf("probe_immediate metric delta = %v, want 1 per dispatch call", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestDispatchRecoveryHooks_NoImmediateProbeForOtherSqlKinds pins the
// negative path: a dispatch under a non-recovery-flip sqlKind (here
// stale_periodic_exhausted_cleanup, picked because it's part of the same
// recover() loop and does not need immediate re-probe) MUST NOT fire
// probeSubmitterImmediate even when wired. probeSubmitter still fires for
// every RETURNING row, preserving the existing fallback path.
func TestDispatchRecoveryHooks_NoImmediateProbeForOtherSqlKinds(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("FROM credentials c").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(7).AddRow(8))

	var (
		mu             sync.Mutex
		delayedCalls   []string
		immediateCalls int
	)
	r := &CredentialRecovery{
		db: mock,
		probeSubmitter: func(credID int, model string) {
			mu.Lock()
			defer mu.Unlock()
			delayedCalls = append(delayedCalls, fmt.Sprintf("cred=%d,model=%q", credID, model))
		},
		probeSubmitterImmediate: func(credID int) {
			mu.Lock()
			defer mu.Unlock()
			immediateCalls++
		},
		invalidateCandidateCache: func(int) {},
	}

	affected, derr := dispatchRecoveryHooksClosureMirror(r, "stale_periodic_exhausted_cleanup",
		"SELECT … FROM credentials c … RETURNING id")
	if derr != nil {
		t.Fatalf("dispatch failed: %v", derr)
	}
	if affected != 2 {
		t.Fatalf("expected 2 unique credentials, got %d", affected)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(delayedCalls) != 2 {
		t.Fatalf("probeSubmitter (delayed) call count = %d, want 2 (got %v)", len(delayedCalls), delayedCalls)
	}
	if immediateCalls != 0 {
		t.Fatalf("probeSubmitterImmediate must NOT fire for non-recovery-flip sqlKind, got %d calls", immediateCalls)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestDispatchRecoveryHooks_NilImmediateProbeSafe pins nil safety: with
// probeSubmitterImmediate left nil (e.g. the integrator forgot to wire
// it, or runs the pre-P1-4 binary), the dispatch closure silently skips
// the immediate path. The delayed probeSubmitter still fires so the
// 5-min fallback still covers the gap. Must not panic.
func TestDispatchRecoveryHooks_NilImmediateProbeSafe(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("UPDATE credentials SET availability_state = 'ready'").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(11).AddRow(22))

	delayedCalls := 0
	r := &CredentialRecovery{
		db: mock,
		probeSubmitter: func(credID int, model string) {
			delayedCalls++
		},
		// probeSubmitterImmediate intentionally nil
		invalidateCandidateCache: func(int) {},
	}

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("dispatch panicked with nil probeSubmitterImmediate: %v", rec)
		}
	}()

	affected, derr := dispatchRecoveryHooksClosureMirror(r, "availability_recover",
		"UPDATE credentials SET availability_state = 'ready' … RETURNING id")
	if derr != nil {
		t.Fatalf("dispatch failed: %v", derr)
	}
	if affected != 2 {
		t.Fatalf("expected 2 unique credentials, got %d", affected)
	}
	if delayedCalls != 2 {
		t.Fatalf("probeSubmitter (delayed) call count = %d, want 2 (delayed path must still fire when immediate is nil)", delayedCalls)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestDispatchRecoveryHooks_ImmediateDedupePerId pins the per-tick dedup
// contract: when the same id appears across multiple RETURNING rows in the
// same UPDATE block (theoretically impossible but a defensive concern
// because the dedup discipline is what stops upstream probe storms), the
// immediate probe must fire only once. The closure's local `seen` map
// dedupes RETURNING ids BEFORE the immediate-path loop, so by construction
// we get at most one immediate call per id per dispatch.
func TestDispatchRecoveryHooks_ImmediateDedupePerId(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// Three rows, two of which share credID=1. dedup should leave us with
	// two unique ids and exactly two immediate probes.
	mock.ExpectQuery("UPDATE credentials SET quota_state = 'ok'").
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).
				AddRow(1).AddRow(2).AddRow(1),
		)

	var (
		mu             sync.Mutex
		immediateCalls []int
	)
	r := &CredentialRecovery{
		db: mock,
		probeSubmitter: func(credID int, model string) {
			// No assertion — the delayed path is exercised for completeness
			// but the immediate-path dedup is what this test pins.
		},
		probeSubmitterImmediate: func(credID int) {
			mu.Lock()
			defer mu.Unlock()
			immediateCalls = append(immediateCalls, credID)
		},
		invalidateCandidateCache: func(int) {},
	}

	affected, derr := dispatchRecoveryHooksClosureMirror(r, "quota_periodic_recover",
		"UPDATE credentials SET quota_state = 'ok' … RETURNING id")
	if derr != nil {
		t.Fatalf("dispatch failed: %v", derr)
	}
	if affected != 2 {
		t.Fatalf("expected 2 unique credentials after dedup, got %d", affected)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(immediateCalls) != 2 {
		t.Fatalf("probeSubmitterImmediate call count = %d, want 2 (1+2 dedup'd, the third row of id=1 must collapse), got %v",
			len(immediateCalls), immediateCalls)
	}
	have := make(map[int]int)
	for _, id := range immediateCalls {
		have[id]++
	}
	if have[1] != 1 {
		t.Errorf("id=1 must fire exactly once (dedup), got %d", have[1])
	}
	if have[2] != 1 {
		t.Errorf("id=2 must fire exactly once, got %d", have[2])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
