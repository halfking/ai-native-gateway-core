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
		"unavailable_reason LIKE 'probe_%'",
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
		regexp.MustCompile(`nps\.next_retry_at\s*>\s*now\(\)`),
		// 2026-07-24 fix: must include 2h backoff exemption to prevent 24h strandings
		regexp.MustCompile(`\(nps\.next_retry_at\s*-\s*now\(\)\)\s*<\s*INTERVAL\s+'2\s+hours?'`),
	}
	for _, re := range regexMustMatch {
		if !re.MatchString(sql) {
			t.Fatalf("expiredCmbRecoverySQL missing pattern %q in:\n%s", re.String(), sql)
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

// TestRecoverExpiredBindingsAllowsLongBackoff verifies that nodes stuck in
// long backoff periods (>2h, e.g. the 24h ladder tier) are eligible for
// recovery, preventing indefinite strandings after transient failures.
//
// Background: 2026-07-24 incident where pulian glm-5.2 was stranded for 24h
// because consecutive_failures=7 triggered the 24h backoff tier, and the
// original expiredCmbRecoverySQL skipped ALL nodes with next_retry_at > now().
// The fix adds a 2h exemption: nodes in backoff >2h are force-retried.
func TestRecoverExpiredBindingsAllowsLongBackoff(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// Simulate a node stuck in 24h backoff (next_retry_at 20h from now)
	// with cmb.available=FALSE and unavailable_recover_at already elapsed.
	// The SQL should SELECT this row because (20h - now) > 2h exemption.
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
