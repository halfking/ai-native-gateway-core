//go:build integration

package bg

// IT 集成测试（2026-09-16 balance-floor 审计修复的集成验证）：
// 用【真实 Postgres】+【mock 厂商控制面】驱动真实 BalanceFloorGuard 代码路径，
// 覆盖计划中单元测试盖不住的四项：汇总日志格式与内容、厂商 API 故障下的
// 重试/Warn 限流/退避戳、#4 逃生门释放、Stop() 优雅关闭，以及串行 vs 并发
// 探测耗时对比（E-B1：200 凭据 × 100ms —— 串行 20s+ vs 并发 10 ≈ 2s）。
//
// 运行方式（一次性 scratch 库 llm_guard_it，schema 由线上库
// pg_dump --schema-only -t credentials -t providers 导入；本测试绝不写线上
// llm_gateway 库）：
//
//	export GUARD_IT_DSN='postgres://llm_gateway:<pwd>@127.0.0.1:5432/llm_guard_it?sslmode=disable'
//	go test -tags integration ./bg/ -run 'TestITBalanceFloor' -v -count=1
//
// DSN 一律走环境变量 —— 凭据不进代码库（no-credential 规则）。清理：
// DROP DATABASE llm_guard_it。

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/secret"
)

// ---------------------------------------------------------------- harness --

func itPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("GUARD_IT_DSN")
	if dsn == "" {
		t.Skip("GUARD_IT_DSN 未设置：需要指向一次性 scratch 库（见文件头注释）")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect scratch db: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE credentials, providers CASCADE`); err != nil {
		t.Fatalf("truncate scratch tables: %v", err)
	}
	return pool
}

// itMockZhipu simulates the zhipu plan-quota control plane with configurable
// latency/failure and a request counter (for the G-O1 retry assertions).
type itMockZhipu struct {
	mu       sync.Mutex
	fail     bool
	latency  time.Duration
	attempts int64
	body     string
}

func (m *itMockZhipu) set(fail bool, latency time.Duration) {
	m.mu.Lock()
	m.fail, m.latency = fail, latency
	m.mu.Unlock()
}

func (m *itMockZhipu) count() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attempts
}

// itFixture starts the mock and registers a zhipu provider whose base_url
// carries an API prefix path (与线上一致 —— originQuotaURL 必须剥离它重建
// scheme://host + /api/monitor/usage/quota/limit)。
func itFixture(t *testing.T, pool *pgxpool.Pool, code, body string) (*pgxpool.Pool, int64, *itMockZhipu) {
	t.Helper()
	m := &itMockZhipu{body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.attempts++
		fail, lat, b := m.fail, m.latency, m.body
		m.mu.Unlock()
		if lat > 0 {
			time.Sleep(lat)
		}
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO providers (code, display_name, protocol, base_url, catalog_code, enabled)
		VALUES ($1, $1, 'openai-completions', $2, 'zhipu', TRUE)
		RETURNING id
	`, code, srv.URL+"/api/coding/paas/v4").Scan(&id); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	return pool, id, m
}

func itInsertCred(t *testing.T, pool *pgxpool.Pool, encKey []byte, providerID int64, label string, floorPercent *float64) int64 {
	t.Helper()
	ct, err := secret.EncryptFernet([]byte("it-test-api-key"), encKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO credentials (provider_id, label, fp_slot_limit, status, lifecycle_status,
		                         manual_disabled, secret_ciphertext, quota_floor_percent)
		VALUES ($1, $2, 1, 'active', 'active', FALSE, $3, $4)
		RETURNING id
	`, providerID, label, ct, floorPercent).Scan(&id); err != nil {
		t.Fatalf("insert credential: %v", err)
	}
	return id
}

// itSeedCredentials bulk-inserts n floor-configured credentials via one batch.
func itSeedCredentials(t *testing.T, pool *pgxpool.Pool, encKey []byte, providerID int64, n int, floorPercent *float64) {
	t.Helper()
	ct, err := secret.EncryptFernet([]byte("it-test-api-key"), encKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	batch := &pgx.Batch{}
	for i := 1; i <= n; i++ {
		batch.Queue(`
			INSERT INTO credentials (provider_id, label, fp_slot_limit, status, lifecycle_status,
			                         manual_disabled, secret_ciphertext, quota_floor_percent)
			VALUES ($1, $2, 1, 'active', 'active', FALSE, $3, $4)
		`, providerID, fmt.Sprintf("it-cred-%03d", i), ct, floorPercent)
	}
	if err := pool.SendBatch(context.Background(), batch).Close(); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
}

// itMarkStalePulled fakes a row THIS guard pulled long ago whose plan evidence
// went cold — the escape hatch's exact precondition.
func itMarkStalePulled(t *testing.T, pool *pgxpool.Pool, id int64, staleFor time.Duration) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE credentials
		SET quota_state = 'balance_exhausted',
		    availability_state = 'suspended',
		    state_reason_code = 'balance_floor',
		    state_reason_detail = 'balance_floor guard (zhipu_plan), used=96.0% (floor=95.0%)',
		    plan_quota_checked_at = now() - $2::interval
		WHERE id = $1
	`, id, staleFor); err != nil {
		t.Fatalf("mark stale pulled: %v", err)
	}
}

// itLogs swaps the default slog logger for a captured one (Debug+ so the
// rate-limited Debug lines are visible too).
type itLogs struct {
	mu  sync.Mutex
	buf strings.Builder
}

func itCaptureLogs(t *testing.T) *itLogs {
	t.Helper()
	old := slog.Default()
	l := &itLogs{}
	slog.SetDefault(slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return l
}

func (l *itLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *itLogs) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *itLogs) summaryLine() string {
	for _, line := range strings.Split(l.text(), "\n") {
		if strings.Contains(line, "balance_floor_guard: plan sweep completed") {
			return line
		}
	}
	return ""
}

// warnFailures counts non-rate-limited probe-failure Warn lines.
func (l *itLogs) warnFailures() int {
	n := 0
	for _, line := range strings.Split(l.text(), "\n") {
		if strings.Contains(line, "level=WARN") &&
			strings.Contains(line, "balance_floor_guard: plan probe failed") &&
			!strings.Contains(line, "rate-limited") {
			n++
		}
	}
	return n
}

// ----------------------------------------------------------------- tests --

// zhipuPlanITLowUsage: used=10% — safely above-water for a 95% floor.
const zhipuPlanITLowUsage = `{"success":true,"data":{"limits":[
	{"type":"TOKENS_LIMIT","unit":3,"percentage":10,"remaining":900000}]}}`

// TestITBalanceFloorSweepSummaryAndPerf 验证 F-L1 汇总日志格式与 E-B1 并发收益
// （计划预期形态：200 凭据 × 100ms mock 延迟 —— 串行 20s+，并发 10 ≈ 2s）。
func TestITBalanceFloorSweepSummaryAndPerf(t *testing.T) {
	pool := itPool(t)
	logs := itCaptureLogs(t)
	g := NewBalanceFloorGuard(pool, itEncKey())
	ctx := context.Background()

	// ---- Phase A: 并发 10（默认）----
	_, pid, mock := itFixture(t, pool, "it-zhipu-par", zhipuPlanSample)
	mock.set(false, 100*time.Millisecond)
	itSeedCredentials(t, pool, itEncKey(), pid, 200, float64p(99))
	start := time.Now()
	if err := g.CycleNow(ctx); err != nil {
		t.Fatalf("parallel cycle: %v", err)
	}
	parDur := time.Since(start)
	t.Logf("PERF parallel(10): 200 creds x 100ms -> %v", parDur)
	if parDur >= 8*time.Second {
		t.Fatalf("parallel sweep took %v, want < 8s (200/10 x 100ms = 2s)", parDur)
	}
	// F-L1: 汇总日志格式与内容。
	summary := logs.summaryLine()
	if summary == "" {
		t.Fatalf("plan sweep completed summary log missing")
	}
	for _, want := range []string{"probed=200", "success=200", "failed=0", "pulled=0", "restored=0", "duration="} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary log missing %q: %s", want, summary)
		}
	}
	// 探测结果落库抽查（sensing columns 真实写入）。
	var kind string
	var usedPct float64
	if err := pool.QueryRow(ctx, `SELECT plan_quota_kind, plan_quota_used_percent
			FROM credentials WHERE label = 'it-cred-001'`).Scan(&kind, &usedPct); err != nil {
		t.Fatalf("query probed row: %v", err)
	}
	if kind != "zhipu_plan" || usedPct != 23 {
		t.Fatalf("probed row kind=%s used=%v, want zhipu_plan/23", kind, usedPct)
	}

	// ---- Phase B: 并发 1（同构建下的串行基线，等价旧行为）----
	if _, err := pool.Exec(ctx, `TRUNCATE credentials, providers CASCADE`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	logs.mu.Lock()
	logs.buf.Reset()
	logs.mu.Unlock()
	t.Setenv("LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY", "1")
	g1 := NewBalanceFloorGuard(pool, itEncKey())
	_, pid2, mock2 := itFixture(t, pool, "it-zhipu-ser", zhipuPlanSample)
	mock2.set(false, 100*time.Millisecond)
	itSeedCredentials(t, pool, itEncKey(), pid2, 200, float64p(99))
	start = time.Now()
	if err := g1.CycleNow(ctx); err != nil {
		t.Fatalf("serial cycle: %v", err)
	}
	serDur := time.Since(start)
	t.Logf("PERF serial(1): 200 creds x 100ms -> %v", serDur)
	if serDur < 12*time.Second {
		t.Fatalf("serial baseline took only %v, want >= 12s (200 x 100ms serialized = 20s)", serDur)
	}
	t.Logf("PERF RESULT: serial(1)=%v parallel(10)=%v speedup=%.1fx",
		serDur, parDur, float64(serDur)/float64(parDur))
}

// TestITBalanceFloorVendorFailureRetryWarnBackoff 验证厂商 500 下的 G-O1 重试
// 次数（1+2）、F-L2 Warn 升级与 15 分钟限流、#12a 退避戳与 fail-open。
func TestITBalanceFloorVendorFailureRetryWarnBackoff(t *testing.T) {
	pool := itPool(t)
	logs := itCaptureLogs(t)
	g := NewBalanceFloorGuard(pool, itEncKey())
	ctx := context.Background()

	_, pid, mock := itFixture(t, pool, "it-zhipu-fail", zhipuPlanSample)
	id := itInsertCred(t, pool, itEncKey(), pid, "it-cred-fail", float64p(95))

	// Cycle 1：500 —— 3 次 HTTP 尝试，1 条 Warn，退避戳落库，fail-open。
	mock.set(true, 0)
	if err := g.CycleNow(ctx); err != nil {
		t.Fatalf("cycle with failing vendor must fail-open, got %v", err)
	}
	if got := mock.count(); got != 3 {
		t.Fatalf("HTTP attempts = %d, want exactly 3 (1 initial + 2 retries, G-O1)", got)
	}
	if logs.warnFailures() != 1 {
		t.Fatalf("warn failures = %d, want 1 (F-L2 upgrade to Warn)", logs.warnFailures())
	}
	var failedAtSet, checkedAtNull bool
	var quotaState string
	if err := pool.QueryRow(ctx, `SELECT plan_quota_probe_failed_at IS NOT NULL,
			plan_quota_checked_at IS NULL, COALESCE(quota_state,'ok')
			FROM credentials WHERE id = $1`, id).Scan(&failedAtSet, &checkedAtNull, &quotaState); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !failedAtSet || !checkedAtNull {
		t.Fatalf("backoff stamp contract broken: failed_at_set=%v checked_at_null=%v", failedAtSet, checkedAtNull)
	}
	if quotaState != "ok" {
		t.Fatalf("fail-open violated: quota_state=%q after probe failure", quotaState)
	}

	// Cycle 2：清退避戳强制重探 —— 不出新 Warn（15 分钟窗口限流→Debug），仍 3 次尝试。
	if _, err := pool.Exec(ctx,
		`UPDATE credentials SET plan_quota_probe_failed_at = to_timestamp(0) WHERE id = $1`, id); err != nil {
		t.Fatalf("reset backoff: %v", err)
	}
	if err := g.CycleNow(ctx); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if got := mock.count(); got != 6 {
		t.Fatalf("total HTTP attempts = %d, want 6 (3 per cycle)", got)
	}
	if logs.warnFailures() != 1 {
		t.Fatalf("second cycle must be rate-limited to Debug, warn failures = %d", logs.warnFailures())
	}
	if !strings.Contains(logs.text(), "rate-limited") {
		t.Fatalf("rate-limited Debug line missing in logs")
	}
}

// TestITBalanceFloorEscapeHatchStaleRelease 验证 #4 逃生门：plan 证据陈旧超
// 2h 的 floor 摘出行被释放；同 tick 重探测成功则复核回正常，失败则保持释放。
func TestITBalanceFloorEscapeHatchStaleRelease(t *testing.T) {
	pool := itPool(t)
	_ = itCaptureLogs(t)
	g := NewBalanceFloorGuard(pool, itEncKey())
	ctx := context.Background()

	// A：健康端点 —— 释放后同 tick 重探测成功（used=10 << floor 95）→ 保持 ok，
	// 且 checked_at 刷新（2h 内逃生门不再触发）。
	_, pidA, _ := itFixture(t, pool, "it-zhipu-esc-ok", zhipuPlanITLowUsage)
	idA := itInsertCred(t, pool, itEncKey(), pidA, "it-cred-esc-ok", float64p(95))
	itMarkStalePulled(t, pool, idA, 3*time.Hour)

	// B：故障端点 —— 释放后重探测失败 → 保持释放态（fail-open，不重摘）。
	_, pidB, mockB := itFixture(t, pool, "it-zhipu-esc-bad", zhipuPlanSample)
	idB := itInsertCred(t, pool, itEncKey(), pidB, "it-cred-esc-bad", float64p(95))
	itMarkStalePulled(t, pool, idB, 3*time.Hour)
	mockB.set(true, 0)

	if err := g.CycleNow(ctx); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	var stateA, detailA string
	var freshA bool
	if err := pool.QueryRow(ctx, `SELECT COALESCE(quota_state,'ok'), COALESCE(state_reason_detail,''),
			COALESCE(plan_quota_checked_at, to_timestamp(0)) > now() - interval '1 minute'
			FROM credentials WHERE id = $1`, idA).Scan(&stateA, &detailA, &freshA); err != nil {
		t.Fatalf("query A: %v", err)
	}
	if stateA != "ok" {
		t.Fatalf("A: released+recovered row must stay ok, got %q (%s)", stateA, detailA)
	}
	if !freshA {
		t.Fatalf("A: re-probe must refresh plan_quota_checked_at")
	}

	var stateB, detailB string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(quota_state,'ok'), COALESCE(state_reason_detail,'')
			FROM credentials WHERE id = $1`, idB).Scan(&stateB, &detailB); err != nil {
		t.Fatalf("query B: %v", err)
	}
	if stateB != "ok" || !strings.Contains(detailB, "escape hatch") {
		t.Fatalf("B: stale row must be released with escape-hatch detail, got state=%q detail=%q", stateB, detailB)
	}
}

// TestITBalanceFloorStopGraceful 验证 D-L1：Stop() 等 worker 真正退出且幂等。
func TestITBalanceFloorStopGraceful(t *testing.T) {
	pool := itPool(t)
	base := runtime.NumGoroutine()
	t.Setenv("LLM_GATEWAY_BALANCE_FLOOR_INTERVAL", "30s")
	g := NewBalanceFloorGuard(pool, itEncKey())
	g.Start(context.Background())
	g.lifecycleMu.Lock()
	done := g.workerDone
	g.lifecycleMu.Unlock()
	if done == nil {
		t.Fatalf("worker not armed")
	}
	time.Sleep(100 * time.Millisecond) // 让 worker 进入 select 稳态
	start := time.Now()
	g.Stop()
	stopDur := time.Since(start)
	t.Logf("Stop() joined worker in %v", stopDur)
	if stopDur >= 2*time.Second {
		t.Fatalf("Stop took %v, want < 2s (idle worker exit is immediate)", stopDur)
	}
	select {
	case <-done:
	default:
		t.Fatalf("workerDone must be closed after Stop returns")
	}
	g.Stop() // 幂等，不得 hang
	time.Sleep(200 * time.Millisecond)
	if n := runtime.NumGoroutine(); n > base+2 {
		t.Fatalf("goroutine leak: base=%d now=%d", base, n)
	}
}

// ------------------------------------------------------- shared fixtures --

var (
	itKeyOnce  sync.Once
	itKeyBytes []byte
)

// itEncKey: deterministic 32-byte Fernet key shared by all IT scenarios.
func itEncKey() []byte {
	itKeyOnce.Do(func() {
		itKeyBytes = make([]byte, 32)
		for i := range itKeyBytes {
			itKeyBytes[i] = byte(i*7 + 3)
		}
	})
	return itKeyBytes
}
