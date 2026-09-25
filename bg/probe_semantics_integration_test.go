//go:build integration

package bg

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// R50 F20（2026-09-21 审计轮）：探针 v3 的行为级真库钉桩。R49 发现
// "探针 v3 测试全为 SQL 字符串钉桩"（INV-2 ON CONFLICT 行为、INV-4 门数据
// 语义零运行时验证）——字符串钉桩只是格式锁，锁不住 SQL 语义。本文件对
// 真 PG 执行真实语句并断言行级结果（-tags integration 运行，testcontainers）。

func startProbeSemanticsPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("db_test"),
		postgres.WithUsername("db_test"),
		postgres.WithPassword("db_test"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() {
		termCtx, termCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer termCancel()
		_ = container.Terminate(termCtx)
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	var pool *pgxpool.Pool
	for attempt := 0; attempt < 30; attempt++ {
		pool, err = pgxpool.New(ctx, dsn)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				break
			}
			pool.Close()
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// minimalProbeStateSchema 复刻 node_probe_state 中 Submit upsert 触碰的列集
// （与 712 真表同名同约束键；其余列与探测调度无关，最小表形即可）。
const minimalProbeStateSchema = `
	CREATE TABLE node_probe_state (
		credential_id        bigint NOT NULL,
		raw_model_name       text   NOT NULL,
		consecutive_failures integer NOT NULL DEFAULT 0,
		next_retry_at        timestamptz NOT NULL DEFAULT now(),
		next_retry_seconds   integer NOT NULL DEFAULT 5,
		paused               boolean NOT NULL DEFAULT false,
		last_direct_ok       boolean,
		last_err_code        text,
		in_flight_until      timestamptz,
		updated_at           timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (credential_id, raw_model_name)
	)
`

const minimalProbeRunsSchema = `
	CREATE TABLE node_probe_runs (
		id            bigserial PRIMARY KEY,
		credential_id bigint NOT NULL,
		raw_model_name text NOT NULL,
		success       boolean NOT NULL,
		started_at    timestamptz NOT NULL DEFAULT now(),
		completed_at  timestamptz
	)
`

// TestNodeProbeSubmitUpsert_ReArmSemantics —— INV-2 行为钉桩：对四类存量行
// 形态执行真实 Submit upsert，断言"成功即停放、新失败立即重武装、梯子行
// 不塌缩"的行级结果。
func TestNodeProbeSubmitUpsert_ReArmSemantics(t *testing.T) {
	pool := startProbeSemanticsPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := pool.Exec(ctx, minimalProbeStateSchema); err != nil {
		t.Fatalf("create node_probe_state: %v", err)
	}

	// 四类存量行：paused 7 连败 / 梯子中段（未来 next_retry）/
	// 已过期梯子行 / 健康停放（30 天外）。
	seeds := []string{
		`INSERT INTO node_probe_state (credential_id, raw_model_name, paused, consecutive_failures, last_err_code, next_retry_at, next_retry_seconds, last_direct_ok)
		 VALUES (1, 'm', TRUE, 7, '429', now() + interval '10 minutes', 600, FALSE)`,
		`INSERT INTO node_probe_state (credential_id, raw_model_name, paused, consecutive_failures, last_err_code, next_retry_at, next_retry_seconds, last_direct_ok, in_flight_until)
		 VALUES (2, 'm', FALSE, 3, '429', now() + interval '1 hour', 3600, FALSE, now() + interval '2 minutes')`,
		`INSERT INTO node_probe_state (credential_id, raw_model_name, paused, consecutive_failures, last_err_code, next_retry_at, next_retry_seconds, last_direct_ok)
		 VALUES (3, 'm', FALSE, 2, '500', now() - interval '1 minute', 5, FALSE)`,
		`INSERT INTO node_probe_state (credential_id, raw_model_name, paused, consecutive_failures, last_err_code, next_retry_at, next_retry_seconds, last_direct_ok, in_flight_until)
		 VALUES (4, 'm', FALSE, 0, NULL, now() + interval '30 days', 2592000, TRUE, NULL)`,
	}
	for _, s := range seeds {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	upsert := nodeProbeSubmitUpsertSQL()
	for cred := int64(1); cred <= 4; cred++ {
		if _, err := pool.Exec(ctx, upsert, cred, "m"); err != nil {
			t.Fatalf("submit upsert cred %d: %v", cred, err)
		}
	}

	type row struct {
		paused      bool
		cf          int
		lastErr     *string
		nextRetry   time.Time
		inFlightNil bool
	}
	for cred, want := range map[int64]struct {
		paused      bool
		cf          int
		wantErrCode *string
		reArmed     bool // next_retry_at 拉回 now+5s 窗口
		inFlightNil bool
	}{
		1: {paused: false, cf: 0, wantErrCode: nil, reArmed: true, inFlightNil: true},           // paused → 完全重武装
		2: {paused: false, cf: 3, wantErrCode: strp("429"), reArmed: false, inFlightNil: false}, // 梯子中段 → 保留（链路推进）
		3: {paused: false, cf: 2, wantErrCode: strp("500"), reArmed: true, inFlightNil: true},   // 过期梯子行 → 重排期、计数保留
		4: {paused: false, cf: 0, wantErrCode: nil, reArmed: true, inFlightNil: true},           // 健康停放 → 新失败立即重启（INV-2 分支）
	} {
		var r row
		var errCode *string
		var inFlight *time.Time
		err := pool.QueryRow(ctx, `
			SELECT paused, consecutive_failures, last_err_code, next_retry_at, in_flight_until
			FROM node_probe_state WHERE credential_id = $1 AND raw_model_name = 'm'`, cred).
			Scan(&r.paused, &r.cf, &errCode, &r.nextRetry, &inFlight)
		if err != nil {
			t.Fatalf("cred %d: read back: %v", cred, err)
		}
		if r.paused != want.paused {
			t.Errorf("cred %d: paused = %v, want %v", cred, r.paused, want.paused)
		}
		if r.cf != want.cf {
			t.Errorf("cred %d: consecutive_failures = %d, want %d", cred, r.cf, want.cf)
		}
		if errCodeStr(errCode) != errCodeStr(want.wantErrCode) {
			t.Errorf("cred %d: last_err_code = %v, want %v", cred, errCode, want.wantErrCode)
		}
		if want.reArmed {
			// now()+5s 窗口：断言落点在 (now, now+10s] 内（原值是 +1h/+10m/+30d，不可能混入）。
			if r.nextRetry.Before(time.Now()) || r.nextRetry.After(time.Now().Add(10*time.Second)) {
				t.Errorf("cred %d: next_retry_at %v not in re-arm window (now, now+10s]", cred, r.nextRetry)
			}
		} else {
			if r.nextRetry.Before(time.Now().Add(30 * time.Minute)) {
				t.Errorf("cred %d: ladder schedule collapsed to %v (< now+30m)", cred, r.nextRetry)
			}
		}
		if (inFlight == nil) != want.inFlightNil {
			t.Errorf("cred %d: in_flight_until nil=%v, want nil=%v", cred, inFlight == nil, want.inFlightNil)
		}
	}
}

// TestCredentialTwoProbeSuccessGate_DataSemantics —— INV-4 行为钉桩：
// "≥2 个不同模型的最新一轮探测在 24h 内成功"——关键语义是 DISTINCT ON
// 先取最新一轮、后滤 success：某模型窗口内先败后成（最新=成）计入，
// 先成后败（最新=败）即使窗口内有早前成功也不计入。
func TestCredentialTwoProbeSuccessGate_DataSemantics(t *testing.T) {
	pool := startProbeSemanticsPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := pool.Exec(ctx, minimalProbeRunsSchema); err != nil {
		t.Fatalf("create node_probe_runs: %v", err)
	}
	seeds := []string{
		// 凭据 1：两个模型最新轮都成功（1h/2h 前）→ 门命中（排除）。
		`INSERT INTO node_probe_runs (credential_id, raw_model_name, success, started_at, completed_at)
		 VALUES (1, 'model-a', TRUE, now() - interval '1 hour', now() - interval '59 minutes'),
		        (1, 'model-b', TRUE, now() - interval '2 hours', now() - interval '119 minutes')`,
		// 凭据 2：model-a 最新成功；model-b 窗口内先成后败（最新=败）→ 不命中。
		`INSERT INTO node_probe_runs (credential_id, raw_model_name, success, started_at, completed_at)
		 VALUES (2, 'model-a', TRUE, now() - interval '1 hour', now() - interval '59 minutes'),
		        (2, 'model-b', TRUE, now() - interval '2 hours', now() - interval '119 minutes'),
		        (2, 'model-b', FALSE, now() - interval '30 minutes', now() - interval '29 minutes')`,
		// 凭据 3：两模型最新轮都成功但已出 24h 窗口 → 不命中。
		`INSERT INTO node_probe_runs (credential_id, raw_model_name, success, started_at, completed_at)
		 VALUES (3, 'model-a', TRUE, now() - interval '25 hours', now() - interval '25 hours'),
		        (3, 'model-b', TRUE, now() - interval '26 hours', now() - interval '26 hours')`,
	}
	for _, s := range seeds {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cases := []struct {
		cred int64
		excl bool // true = 凭据被 INV-4 排除（门命中）
	}{
		{1, true},
		{2, false},
		{3, false},
	}
	for _, tc := range cases {
		var gated bool
		err := pool.QueryRow(ctx,
			`SELECT `+credentialTwoProbeSuccessGateSQL("$1"), tc.cred).Scan(&gated)
		if err != nil {
			t.Fatalf("cred %d: gate query: %v", tc.cred, err)
		}
		if gated != tc.excl {
			t.Errorf("cred %d: INV-4 gate = %v, want %v", tc.cred, gated, tc.excl)
		}
	}
}

func strp(s string) *string { return &s }

func errCodeStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
