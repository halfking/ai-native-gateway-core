//go:build integration

package handlers

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUsageLedgerViewHasRawModelName_ViewFreezeGuard 是 rule 49 §9-2 view-freeze
// 补强的 schema-probe 测试：方案 C（commit 66ffab81d）后，
// `usage_ledger_with_current_month` view 必须保留 `raw_model_name` 列；
// loadProviderRequestStats（?model= 过滤）依赖此列存在，列不在则查询静默
// 失败导致按模型统计接口返回 0。
//
// 运行方式:
//
//	export LLM_GATEWAY_PG_URL="postgres://llm_gateway:<pass>@172.16.2.210:5432/llm_gateway?sslmode=disable"
//	go test -tags=integration ./internal/handlers -v -run TestUsageLedgerViewHasRawModelName_ViewFreezeGuard
//
// 触发条件: 任何 ALTER TABLE usage_ledger[_hot] ADD/DROP COLUMN raw_model_name* 或
// CREATE OR REPLACE VIEW usage_ledger_with_current_month 忘记包含 raw_model_name 时
// （例如 rule 49 §9.2 强制条款未走全流程），本测试会 FAIL，CI 拦截。
func TestUsageLedgerViewHasRawModelName_ViewFreezeGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping view-freeze probe in short mode")
	}

	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set, skipping view-freeze probe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err, "connect to PG")
	defer pool.Close()

	// 1) Probe: view 存在
	var viewExists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.views
			WHERE table_schema = 'public'
			  AND table_name = 'usage_ledger_with_current_month'
		)
	`).Scan(&viewExists)
	require.NoError(t, err, "probe view existence")
	require.True(t, viewExists,
		"view public.usage_ledger_with_current_month 不存在；"+
			"按 rule 49 §9.2 任何 ALTER TABLE ADD COLUMN 后必须重建该 view")

	// 2) Probe: raw_model_name 列存在
	var columnCount int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'usage_ledger_with_current_month'
		  AND column_name = 'raw_model_name'
	`).Scan(&columnCount)
	require.NoError(t, err, "probe raw_model_name column")
	assert.Equal(t, 1, columnCount,
		"view 缺 raw_model_name 列；loadProviderRequestStats(?model=) 静默返回 0，"+
			"是 2026-08-18 方案 C 引入的 view-freeze 风险点")

	// 3) 端到端验证：loadProviderRequestStats 实际查询的列全部存在
	// 提取查询中所有引用的列名（与 quality_handler.go::loadProviderRequestStats 一致）
	requiredColumns := []string{
		"ts",
		"provider_id",
		"raw_model_name",
		"success",
		"total_tokens",
	}
	for _, col := range requiredColumns {
		var n int
		err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'usage_ledger_with_current_month'
			  AND column_name = $1
		`, col).Scan(&n)
		require.NoError(t, err, "probe column %s", col)
		assert.Equal(t, 1, n, "view 缺必需列 %s（loadProviderRequestStats 依赖）", col)
	}

	// 4) 类型对齐：raw_model_name 应为 TEXT/VARCHAR（与 usage_ledger_hot/usage_ledger 一致）
	var dataType string
	err = pool.QueryRow(ctx, `
		SELECT data_type FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'usage_ledger_with_current_month'
		  AND column_name = 'raw_model_name'
	`).Scan(&dataType)
	require.NoError(t, err, "probe column type")
	assert.True(t,
		dataType == "text" || dataType == "character varying",
		"raw_model_name data_type=%q 异常（期望 text 或 character varying）", dataType)

	// 5) Smoke：实际执行 loadProviderRequestStats 的查询骨架，
	// 确认 ?model= 子句不会触发 "column does not exist"。
	// 用一个不存在的 provider_id + 极小窗口，预期 0 行但 0 错误。
	rows, err := pool.Query(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE ts >= NOW() - INTERVAL '30 days'),
			COALESCE(SUM(total_tokens), 0)
		FROM usage_ledger_with_current_month
		WHERE provider_id = $1
		  AND ts >= NOW() - INTERVAL '30 days'
		  AND raw_model_name = $2
	`, int64(-1), "schema-probe-test-no-such-model")
	require.NoError(t, err, "实际查询（?model= 子句）必须成功；"+
		"如失败说明 view 缺 raw_model_name 或列对齐失败")
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("查询返回 0 行；可能是 view 解析错误而非 0 结果")
	}
	var dummyCount, dummySum int64
	require.NoError(t, rows.Scan(&dummyCount, &dummySum),
		"query must scan successfully even with 0 matching rows")
	if errors.Is(rows.Err(), context.DeadlineExceeded) {
		t.Fatal("查询超时；view 可能挂起（罕见，但应避免）")
	}

	t.Logf("view-freeze probe passed: usage_ledger_with_current_month.raw_model_name present, "+
		"data_type=%s, loadProviderRequestStats smoke OK (count=%d, sum=%d)",
		dataType, dummyCount, dummySum)
}
