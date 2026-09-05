package verify

// supplier_errors_pg_test.go — 2026-09-05 审计闭环5：hot+columnar 真实
// PostgreSQL 集成验证（门控，CI 离线绿）。
//
// 审计缺口：hot 仅保留 8 小时、批量迁移、历史分区不可更新/删除（repair
// 工具不得修改 columnar）此前只有文档与 SQL 静态约束，无运行态验证。
//
// 运行方式（需要带 citus_columnar 扩展的 PostgreSQL，如本仓库
// kx-citus-pg17 镜像）：
//
//	docker run -d --rm --name audit-pg -e POSTGRES_PASSWORD=audit \
//	  -p 55432:5432 kx-citus-pg17:offline-arm64
//	LLM_GATEWAY_SUPPLIER_PG_DSN='postgres://postgres:audit@127.0.0.1:55432/postgres?sslmode=disable' \
//	  go test ./deploy/sql/verify/ -run TestSupplierErrors -v
//
// 验证矩阵：
//   A. V371 迁移在真实 PG（含 columnar AM）上执行成功（含迁移内建验证块）；
//   B. hot 表可正常 INSERT/UPDATE/DELETE（heap 语义）；
//   C. 历史 columnar 分区拒绝 UPDATE/DELETE（不可变语义，repair 工具
//      不得修改 columnar 的依据）；
//   D. promote('8 hours') 只迁移超过保留窗口的行（8h 保留语义），
//      且批次上限生效（批量迁移语义）；
//   E. 迁移后 unified 视图跨 hot+historical 可读；
//   F. RLS：app.tenant_id 隔离生效；
//   G. supplier_error_stats 唯一键 UPSERT 幂等；
//   H. supplier_error_stats 分桶列（2026-09-05 审计 E-#6）：
//      retryable_count 计数 + stage_counts jsonb 分桶 + hour 二次
//      rollup 合并 + 重复聚合幂等。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const supplierPGDSNEnv = "LLM_GATEWAY_SUPPLIER_PG_DSN"

func openSupplierPG(t *testing.T) (*pgx.Conn, func()) {
	t.Helper()
	dsn := os.Getenv(supplierPGDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set — offline mode; run with a citus_columnar-enabled PostgreSQL", supplierPGDSNEnv)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	cleanup := func() { _ = conn.Close(ctx) }
	return conn, cleanup
}

// bootstrapSupplierSchema 在干净库上建 columnar 扩展、23 号不变量脚本、
// V371 迁移。重复执行安全（测试内 DROP 全部对象后重建）。
func bootstrapSupplierSchema(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	root := repoRoot(t)

	_, err := conn.Exec(ctx, `DROP VIEW IF EXISTS supplier_errors_unified;
DROP TABLE IF EXISTS supplier_error_stats CASCADE;
DROP TABLE IF EXISTS supplier_errors CASCADE;
DROP TABLE IF EXISTS supplier_errors_hot CASCADE;
DROP FUNCTION IF EXISTS promote_supplier_errors_hot_to_partition(interval, int);
DROP FUNCTION IF EXISTS ensure_supplier_errors_partition(timestamp with time zone);`)
	require.NoError(t, err, "pre-clean")

	_, err = conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS citus_columnar;`)
	require.NoError(t, err, "citus_columnar extension must be available (use kx-citus-pg17 image)")

	// enforce_columnar_partition（phase-23 不变量脚本）——V371 的幂等依赖。
	invariantSQL, err := os.ReadFile(filepath.Join(root,
		"sql", "scripts", "phase-23-columnar-invariant", "02-event-trigger.sql"))
	require.NoError(t, err)
	_, err = conn.Exec(ctx, stripPSQLMeta(invariantSQL))
	require.NoError(t, err, "phase-23 invariant script")

	v371SQL, err := os.ReadFile(filepath.Join(root,
		"deploy", "sql", "migrations", "V371__supplier_errors_hot_and_stats.sql"))
	require.NoError(t, err)
	_, err = conn.Exec(ctx, stripPSQLMeta(v371SQL))
	require.NoError(t, err, "V371 migration must apply cleanly (includes its own validation block)")
}

// stripPSQLMeta 去掉 psql 元命令行（\set / \echo），pgx 只接受纯 SQL。
func stripPSQLMeta(sqlBytes []byte) string {
	var b strings.Builder
	for _, line := range strings.Split(string(sqlBytes), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "\\") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	// 本测试位于 deploy/sql/verify，仓库根在三级之上。
	return filepath.Join(wd, "..", "..", "..")
}

func insertSupplierError(t *testing.T, ctx context.Context, conn *pgx.Conn, occurredAt time.Time, reqID string) {
	t.Helper()
	_, err := conn.Exec(ctx, `
		INSERT INTO supplier_errors_hot (
			occurred_at, request_id, tenant_id, session_id, provider_id, supplier,
			credential_id, model, attempt_seq, error_type, error_code, http_status,
			error_message, is_retryable, stage, latency_ms, affected_users, request_metadata
		) VALUES ($1, $2, 'tenant-a', 'sess-1', 9, 'zhipu', 42, 'glm-5.2', 1,
		          'rate_limit', '1210', 429, 'too many requests', false, 'upstream', 812, 1, NULL)`,
		occurredAt, reqID)
	require.NoError(t, err)
}

// TestSupplierErrorsHotHeapSemantics B：hot 表支持 UPDATE/DELETE。
func TestSupplierErrorsHotHeapSemantics(t *testing.T) {
	conn, done := openSupplierPG(t)
	defer done()
	ctx := context.Background()
	bootstrapSupplierSchema(t, ctx, conn)

	now := time.Now()
	insertSupplierError(t, ctx, conn, now, "req-heap-1")

	_, err := conn.Exec(ctx, `UPDATE supplier_errors_hot SET error_message='updated' WHERE request_id='req-heap-1'`)
	require.NoError(t, err, "hot is heap: UPDATE must work")
	_, err = conn.Exec(ctx, `DELETE FROM supplier_errors_hot WHERE request_id='req-heap-1'`)
	require.NoError(t, err, "hot is heap: DELETE must work")
}

// TestSupplierErrorsColumnarPartitionImmutable C：历史分区拒绝 UPDATE/DELETE。
func TestSupplierErrorsColumnarPartitionImmutable(t *testing.T) {
	conn, done := openSupplierPG(t)
	defer done()
	ctx := context.Background()
	bootstrapSupplierSchema(t, ctx, conn)

	// 通过分区父表写入（路由到当月 columnar 分区）。
	_, err := conn.Exec(ctx, `
		INSERT INTO supplier_errors (occurred_at, request_id, tenant_id, provider_id, supplier,
			credential_id, model, attempt_seq, error_type, is_retryable)
		VALUES (NOW(), 'req-immutable-1', 'tenant-a', 9, 'zhipu', 42, 'glm-5.2', 1, 'rate_limit', false)`)
	require.NoError(t, err, "INSERT into columnar partition must work")

	// 确认当月子分区使用 columnar 访问方法（不可变语义的载体）。
	partition := "supplier_errors_" + time.Now().Format("2006_01")
	am := ""
	require.NoError(t, conn.QueryRow(ctx, `SELECT a.amname FROM pg_class c
		JOIN pg_am a ON a.oid = c.relam WHERE c.relname = $1`, partition).Scan(&am))
	assert.Equal(t, "columnar", am, "current-month partition must use the columnar access method")

	// 直接对 columnar 子分区 UPDATE/DELETE 必须失败（不可变语义）。
	_, err = conn.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET error_message='tampered' WHERE request_id='req-immutable-1'`, partition))
	require.Error(t, err, "UPDATE on columnar partition must fail (append-only history)")
	t.Logf("columnar partition UPDATE rejected with: %.140v", err)

	_, err = conn.Exec(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE request_id='req-immutable-1'`, partition))
	require.Error(t, err, "DELETE on columnar partition must fail (append-only history)")

	// 走 hot 的 TTL DELETE 语义不受影响（治理路径闭环）。
	_, err = conn.Exec(ctx, `DELETE FROM supplier_errors_hot WHERE request_id='nonexistent'`)
	require.NoError(t, err)
}

// TestSupplierErrorsPromoteRetentionAndBatch D：8h 保留 + 批量迁移。
func TestSupplierErrorsPromoteRetentionAndBatch(t *testing.T) {
	conn, done := openSupplierPG(t)
	defer done()
	ctx := context.Background()
	bootstrapSupplierSchema(t, ctx, conn)

	now := time.Now()
	// 1 行新鲜（保留窗口内）+ 3 行过期（9h 前）。
	insertSupplierError(t, ctx, conn, now.Add(-9*time.Hour), "req-cold-1")
	insertSupplierError(t, ctx, conn, now.Add(-9*time.Hour), "req-cold-2")
	insertSupplierError(t, ctx, conn, now.Add(-9*time.Hour), "req-cold-3")
	insertSupplierError(t, ctx, conn, now, "req-fresh-1")

	// 8h 保留 + 批次上限 2：只迁 2 行 cold，fresh 不动。
	var promoted int64
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT promote_supplier_errors_hot_to_partition('8 hours', 2)`).Scan(&promoted))
	assert.Equal(t, int64(2), promoted, "batch cap must limit promotion")

	var hotCount int
	require.NoError(t, conn.QueryRow(ctx, `SELECT COUNT(*) FROM supplier_errors_hot`).Scan(&hotCount))
	assert.Equal(t, 2, hotCount, "1 fresh + 1 remaining cold row stay in hot")

	// 迁完剩余 cold 行。
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT promote_supplier_errors_hot_to_partition('8 hours', 5000)`).Scan(&promoted))
	assert.Equal(t, int64(1), promoted)

	require.NoError(t, conn.QueryRow(ctx, `SELECT COUNT(*) FROM supplier_errors_hot`).Scan(&hotCount))
	assert.Equal(t, 1, hotCount, "only the fresh row remains within the 8h retention window")

	var historical int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT COUNT(*) FROM supplier_errors WHERE request_id LIKE 'req-cold-%'`).Scan(&historical))
	assert.Equal(t, 3, historical, "all cold rows promoted into columnar history")
}

// TestSupplierErrorsUnifiedReadAndRLS E+F：unified 视图跨 hot+historical，RLS 隔离。
func TestSupplierErrorsUnifiedReadAndRLS(t *testing.T) {
	conn, done := openSupplierPG(t)
	defer done()
	ctx := context.Background()
	bootstrapSupplierSchema(t, ctx, conn)

	// hot 侧 tenant-a，historical 侧 tenant-b。
	_, err := conn.Exec(ctx, `
		INSERT INTO supplier_errors_hot (occurred_at, request_id, tenant_id, provider_id, supplier,
			credential_id, model, attempt_seq, error_type, is_retryable)
		VALUES (NOW(), 'req-uni-hot', 'tenant-a', 9, 'zhipu', 42, 'glm-5.2', 1, 'rate_limit', false)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `
		INSERT INTO supplier_errors (occurred_at, request_id, tenant_id, provider_id, supplier,
			credential_id, model, attempt_seq, error_type, is_retryable)
		VALUES (NOW(), 'req-uni-cold', 'tenant-b', 8, 'openai', 7, 'gpt-5', 2, 'timeout', true)`)
	require.NoError(t, err, "INSERT into historical partition must work")

	var unified int
	require.NoError(t, conn.QueryRow(ctx, `SELECT COUNT(*) FROM supplier_errors_unified`).Scan(&unified))
	assert.GreaterOrEqual(t, unified, 2, "unified view spans hot + historical")

	// RLS：超级用户绕过 RLS（FORCE 只约束属主），必须以普通角色探测。
	// 与生产对齐：网关应用角色是非 superuser。
	_, err = conn.Exec(ctx, `DO $do$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rls_probe') THEN
			CREATE ROLE rls_probe NOLOGIN;
		END IF;
	END $do$;`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `GRANT USAGE ON SCHEMA public TO rls_probe;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO rls_probe;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO rls_probe;`)
	require.NoError(t, err)

	_, err = conn.Exec(ctx, `SET ROLE rls_probe; SET app.tenant_id = 'tenant-a'`)
	require.NoError(t, err)
	var tenantRows int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COUNT(*) FROM supplier_errors_unified WHERE request_id IN ('req-uni-hot','req-uni-cold')`).Scan(&tenantRows))
	assert.Equal(t, 1, tenantRows, "RLS must isolate tenants in the unified view")

	_, err = conn.Exec(ctx, `RESET ROLE; RESET app.tenant_id`)
	require.NoError(t, err)
}

// TestSupplierErrorStatsUpsertIdempotent G：唯一键 UPSERT 幂等（防重复聚合）。
func TestSupplierErrorStatsUpsertIdempotent(t *testing.T) {
	conn, done := openSupplierPG(t)
	defer done()
	ctx := context.Background()
	bootstrapSupplierSchema(t, ctx, conn)

	bucket := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	upsert := func(count int) {
		t.Helper()
		_, err := conn.Exec(ctx, `
			INSERT INTO supplier_error_stats (
				stat_time, granularity, supplier, credential_id, error_type, model,
				error_count, unique_requests, affected_users, success_count, total_requests
			) VALUES ($1, 'minute', 'zhipu', 42, 'rate_limit', 'glm-5.2', $2, 3, 3, 0, $2)
			ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
			DO UPDATE SET error_count = EXCLUDED.error_count,
			              total_requests = EXCLUDED.total_requests,
			              aggregated_at = NOW()`, bucket, count)
		require.NoError(t, err)
	}
	upsert(4)
	upsert(6) // 重复聚合同一窗口：覆盖而非新增

	var n int
	var errCount int
	var rate float64
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COUNT(*), SUM(error_count), MAX(error_rate) FROM supplier_error_stats
		WHERE supplier='zhipu' AND error_type='rate_limit'`).Scan(&n, &errCount, &rate))
	assert.Equal(t, 1, n, "unique bucket constraint must collapse repeated aggregation")
	assert.Equal(t, 6, errCount, "last aggregation wins")
	assert.InDelta(t, 100.0, rate, 0.001, "error_rate generated column = count/total*100")

	// '' 维度与 NULL 语义：supplier='' 行可去重（UNIQUE 生效）。
	emptyUpsert := func() {
		t.Helper()
		_, err := conn.Exec(ctx, `
			INSERT INTO supplier_error_stats (stat_time, granularity, supplier, credential_id,
				error_type, model, error_count, unique_requests, affected_users, total_requests)
			VALUES ($1, 'minute', '', 0, '', '', 1, 1, 1, 1)
			ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
			DO NOTHING`, bucket)
		require.NoError(t, err)
	}
	emptyUpsert()
	emptyUpsert()
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COUNT(*) FROM supplier_error_stats WHERE supplier=''`).Scan(&n))
	assert.Equal(t, 1, n, "unknown-dimension rows must dedupe via '' (not NULL)")
}

// TestSupplierErrorsMigrationIdempotent 幂等：V371 重放不炸（幂等部署要求）。
func TestSupplierErrorsMigrationIdempotent(t *testing.T) {
	conn, done := openSupplierPG(t)
	defer done()
	ctx := context.Background()
	bootstrapSupplierSchema(t, ctx, conn)

	v371, err := os.ReadFile(filepath.Join(repoRoot(t),
		"deploy", "sql", "migrations", "V371__supplier_errors_hot_and_stats.sql"))
	require.NoError(t, err)
	_, err = conn.Exec(ctx, stripPSQLMeta(v371))
	require.NoError(t, err, "V371 replay must be idempotent")
	require.False(t, strings.Contains(string(v371), "ON_ERROR_STOP 2"), "sanity")
}

// insertSupplierErrorBucketed 是带 is_retryable/stage 的 hot 行写入 helper
// （E-#6 分桶语义验证用）。
func insertSupplierErrorBucketed(t *testing.T, ctx context.Context, conn *pgx.Conn, occurredAt time.Time, reqID string, retryable bool, stage string) {
	t.Helper()
	_, err := conn.Exec(ctx, `
		INSERT INTO supplier_errors_hot (
			occurred_at, request_id, tenant_id, provider_id, supplier,
			credential_id, model, attempt_seq, error_type, is_retryable, stage
		) VALUES ($1, $2, 'tenant-a', 9, 'zhipu', 42, 'glm-5.2', 1, 'rate_limit', $3, $4)`,
		occurredAt, reqID, retryable, stage)
	require.NoError(t, err)
}

// TestSupplierErrorStatsRetryableStageBuckets H（审计 E-#6）：分桶列在真实
// PG 上的语义。minute rollup 逐字镜像 bg/supplier_error_stats_aggregator.go
// 的 supplierErrorStatsRollupSQL（未导出；字面漂移由 bg 包的
// TestSupplierErrorStatsRollupSQLContract 契约测试钳制）。
func TestSupplierErrorStatsRetryableStageBuckets(t *testing.T) {
	conn, done := openSupplierPG(t)
	defer done()
	ctx := context.Background()
	bootstrapSupplierSchema(t, ctx, conn)

	now := time.Now()
	occurred := now.Add(-5 * time.Minute)
	insertSupplierErrorBucketed(t, ctx, conn, occurred, "req-bkt-1", true, "upstream")
	insertSupplierErrorBucketed(t, ctx, conn, occurred, "req-bkt-2", true, "upstream")
	insertSupplierErrorBucketed(t, ctx, conn, occurred, "req-bkt-3", true, "connect")
	insertSupplierErrorBucketed(t, ctx, conn, occurred, "req-bkt-4", false, "upstream")
	insertSupplierErrorBucketed(t, ctx, conn, occurred, "req-bkt-5", false, "")

	// 与聚合器相同的分钟 rollup（minute 级，pgx 传 interval 文本）。
	minuteRollup := `
WITH base AS (
    SELECT date_bin($1::interval, occurred_at, '2000-01-01'::timestamptz) AS stat_time,
           supplier, credential_id, error_type, model,
           request_id, affected_users, is_retryable, stage
    FROM supplier_errors_hot
    WHERE occurred_at >= $3 AND occurred_at < $4
), bucket_totals AS (
    SELECT stat_time, supplier, credential_id, error_type, model,
           COUNT(*)::int AS error_count,
           COUNT(DISTINCT request_id)::int AS unique_requests,
           SUM(affected_users)::int AS affected_users,
           SUM(CASE WHEN is_retryable THEN 1 ELSE 0 END)::int AS retryable_count
    FROM base
    GROUP BY 1, 2, 3, 4, 5
), bucket_stages AS (
    SELECT stat_time, supplier, credential_id, error_type, model,
           jsonb_object_agg(stage, n) AS stage_counts
    FROM (
        SELECT stat_time, supplier, credential_id, error_type, model, stage,
               COUNT(*)::int AS n
        FROM base
        GROUP BY 1, 2, 3, 4, 5, 6
    ) per_stage
    GROUP BY 1, 2, 3, 4, 5
)
INSERT INTO supplier_error_stats (
    stat_time, granularity, supplier, credential_id, error_type, model,
    error_count, unique_requests, affected_users, success_count, total_requests,
    retryable_count, stage_counts
)
SELECT
    b.stat_time, $2, b.supplier, b.credential_id, b.error_type, b.model,
    b.error_count, b.unique_requests, b.affected_users, 0, b.error_count,
    b.retryable_count, COALESCE(s.stage_counts, '{}'::jsonb)
FROM bucket_totals b
LEFT JOIN bucket_stages s
       ON b.stat_time = s.stat_time
      AND b.supplier = s.supplier
      AND b.credential_id = s.credential_id
      AND b.error_type = s.error_type
      AND b.model = s.model
ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
DO UPDATE SET
    error_count     = EXCLUDED.error_count,
    unique_requests = EXCLUDED.unique_requests,
    affected_users  = EXCLUDED.affected_users,
    total_requests  = EXCLUDED.total_requests,
    retryable_count = EXCLUDED.retryable_count,
    stage_counts    = EXCLUDED.stage_counts,
    aggregated_at   = NOW()`

	from := now.Add(-time.Hour)
	to := now.Add(time.Minute)
	rollupArgs := []any{"1 minute", "minute", from, to}
	_, err := conn.Exec(ctx, minuteRollup, rollupArgs...)
	require.NoError(t, err, "minute rollup with bucket columns must execute on real PG")

	var errCount, retryableCount int
	var stageCounts map[string]int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT error_count, retryable_count, stage_counts::jsonb FROM supplier_error_stats
		WHERE granularity = 'minute' AND supplier = 'zhipu' AND credential_id = 42`,
	).Scan(&errCount, &retryableCount, &stageCounts))
	assert.Equal(t, 5, errCount)
	assert.Equal(t, 3, retryableCount, "retryable_count must count is_retryable=true rows")
	assert.Equal(t, map[string]int{"upstream": 3, "connect": 1, "": 1}, stageCounts,
		"stage_counts must bucket per stage (empty stage kept losslessly)")

	// 重复聚合（覆盖式 UPSERT）幂等：计数不翻倍。
	_, err = conn.Exec(ctx, minuteRollup, rollupArgs...)
	require.NoError(t, err)
	var buckets int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT COUNT(*) FROM supplier_error_stats
		WHERE granularity = 'minute' AND supplier = 'zhipu'`).Scan(&buckets))
	assert.Equal(t, 1, buckets, "repeat aggregation must upsert, not duplicate")

	// hour 二次 rollup 合并分桶（镜像 supplierErrorStatsHourRollupSQL）。
	hourRollup := `
WITH bucket_totals AS (
    SELECT date_trunc('hour', stat_time) AS stat_time,
           supplier, credential_id, error_type, model,
           SUM(error_count)::int AS error_count,
           SUM(unique_requests)::int AS unique_requests,
           SUM(affected_users)::int AS affected_users,
           SUM(success_count)::int AS success_count,
           SUM(total_requests)::int AS total_requests,
           SUM(retryable_count)::int AS retryable_count
    FROM supplier_error_stats
    WHERE granularity = 'minute'
      AND stat_time >= date_trunc('hour', $1::timestamptz)
      AND stat_time < $2
    GROUP BY 1, 2, 3, 4, 5
), bucket_stages AS (
    SELECT stat_time, supplier, credential_id, error_type, model,
           jsonb_object_agg(stage, n) AS stage_counts
    FROM (
        SELECT date_trunc('hour', stat_time) AS stat_time,
               supplier, credential_id, error_type, model,
               e.key AS stage, SUM((e.value)::bigint)::int AS n
        FROM supplier_error_stats src
        CROSS JOIN LATERAL jsonb_each(src.stage_counts) e(key, value)
        WHERE src.granularity = 'minute'
          AND src.stat_time >= date_trunc('hour', $1::timestamptz)
          AND src.stat_time < $2
        GROUP BY 1, 2, 3, 4, 5, 6
    ) per_stage
    GROUP BY 1, 2, 3, 4, 5
)
INSERT INTO supplier_error_stats (
    stat_time, granularity, supplier, credential_id, error_type, model,
    error_count, unique_requests, affected_users, success_count, total_requests,
    retryable_count, stage_counts
)
SELECT
    b.stat_time, 'hour', b.supplier, b.credential_id, b.error_type, b.model,
    b.error_count, b.unique_requests, b.affected_users, b.success_count, b.total_requests,
    b.retryable_count, COALESCE(s.stage_counts, '{}'::jsonb)
FROM bucket_totals b
LEFT JOIN bucket_stages s
       ON b.stat_time = s.stat_time
      AND b.supplier = s.supplier
      AND b.credential_id = s.credential_id
      AND b.error_type = s.error_type
      AND b.model = s.model
ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
DO UPDATE SET
    error_count     = EXCLUDED.error_count,
    retryable_count = EXCLUDED.retryable_count,
    stage_counts    = EXCLUDED.stage_counts,
    aggregated_at   = NOW()`
	_, err = conn.Exec(ctx, hourRollup, from, to)
	require.NoError(t, err, "hour rollup must merge bucket columns")

	var hourRetryable int
	var hourStages map[string]int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT retryable_count, stage_counts::jsonb FROM supplier_error_stats
		WHERE granularity = 'hour' AND supplier = 'zhipu' AND credential_id = 42`,
	).Scan(&hourRetryable, &hourStages))
	assert.Equal(t, 3, hourRetryable)
	assert.Equal(t, map[string]int{"upstream": 3, "connect": 1, "": 1}, hourStages,
		"hour bucket must merge minute stage_counts via jsonb_each")
}
