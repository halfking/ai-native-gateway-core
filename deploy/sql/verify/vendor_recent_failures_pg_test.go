package verify

// vendor_recent_failures_pg_test.go — R75 P1（2026-10-01）真库门：
// 凭据详情页「最近失败」列表的连接语义。
//
// 缺陷：admin.VendorRecentFailuresSQL 曾把 (request_id, credential_id,
// attempt_index) 当作 candidate_failure_logs 的唯一键，与 supplier_errors_unified
// 做 LEFT JOIN。该三元组**不是**唯一键——同一次 dispatch 内，fp_slot_saturated
// 降级（degraded_continue，请求继续跑）与随后的上游失败各写一行、共用这三列
// （logDispatchPreflightRejection 是独立函数，不写 forwardForDispatch 闭包里的
// failureLogged）。后果有二，本门逐条复现：
//  1. 行数扇出：2 次尝试的 4 条失败渲染成 8 行；LIMIT 10 作用在扇出后的连接行
//     上，实际只给出 5 条不同失败；
//  2. 预览错配：fp_slot_saturated（网关侧准入事件、本无上游 body）被贴上了
//     network 失败的上游 body——运维按错误类型排查时看到的是别人的响应体。
//
// 为什么必须是真库门：这是纯 SQL 的连接语义问题。静态字符串断言只能证明
// 「写了 LATERAL」，证明不了「不扇出」「不错配」；而本仓库这条 unified 视图曾
// 因 `SELECT *` 触发 citus-columnar 的 "cache lookup failed for attribute source
// of relation"——第一版修法就是这样被真库当场否掉的。
//
// 运行方式（需要带 citus_columnar 扩展、且已应用 V359/V371 schema 的 PG）：
//
//	LLM_GATEWAY_SUPPLIER_PG_DSN='postgres://llm_gateway:pw@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./deploy/sql/verify/ -run TestVendorRecentFailuresSQL -v
//
// 本门不建任何对象：全部写操作在单个事务内完成并 ROLLBACK，对目标库零残留。

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/stretchr/testify/require"
)

const (
	fanoutTenant   = "r75-fanout-tenant"
	fanoutCredID   = 99001
	fanoutProvider = 9901
)

// vendorFanoutSeq 让每次运行的 request_id 互不相同：即便事务因故未回滚，
// 也不会与历史数据串味。
var vendorFanoutSeq atomic.Int64

// vendorFailureRow 是 VendorRecentFailuresSQL 的一行投影（只取本门断言用到的列）。
type vendorFailureRow struct {
	AttemptSeq int
	ErrorType  string
	Preview    *string
}

func TestVendorRecentFailuresSQL(t *testing.T) {
	conn, closeConn := openSupplierPG(t)
	defer closeConn()
	ctx := context.Background()

	// 前置：四张表/视图必须来自本仓库迁移，不能是同名但形状不同的对象。
	// 缺任一即跳过，而不是让门假绿。
	for _, obj := range []string{
		"candidate_failure_logs_hot", "candidate_failure_logs_unified",
		"supplier_errors_hot", "supplier_errors_unified",
	} {
		var exists bool
		require.NoError(t, conn.QueryRow(ctx,
			`SELECT to_regclass('public.'||$1) IS NOT NULL`, obj).Scan(&exists))
		if !exists {
			t.Skipf("public.%s missing — apply V359/V371 before running this gate", obj)
		}
	}

	// run 在单个事务内播种 n 次尝试并跑真身 SQL，返回结果。
	run := func(t *testing.T, nAttempts int) []vendorFailureRow {
		t.Helper()
		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback(ctx) })
		_, err = tx.Exec(ctx, `SELECT set_config('app.bypass_rls','true',true)`)
		require.NoError(t, err)

		reqID := fmt.Sprintf("r75-fanout-%d", vendorFanoutSeq.Add(1))
		for attempt := 0; attempt < nAttempts; attempt++ {
			seedDispatchAttempt(t, ctx, tx, reqID, attempt)
		}

		rows, err := tx.Query(ctx, admin.VendorRecentFailuresSQL,
			int64(fanoutCredID), fanoutTenant, time.Now().Add(-time.Hour))
		require.NoError(t, err, "VendorRecentFailuresSQL must execute on real PG")
		defer rows.Close()

		var out []vendorFailureRow
		for rows.Next() {
			var occurredAt time.Time
			var requestID, model string
			var attemptSeq int
			var errorType string
			var errMsg *string
			var httpStatus, latency *int
			var retryable *bool
			var stage, supplier, errorCode, preview *string
			require.NoError(t, rows.Scan(&occurredAt, &requestID, &model, &attemptSeq, &errorType,
				&errMsg, &httpStatus, &retryable, &stage, &supplier, &errorCode, &latency, &preview))
			out = append(out, vendorFailureRow{attemptSeq, errorType, preview})
		}
		require.NoError(t, rows.Err())
		return out
	}

	t.Run("同一次 dispatch 的多条失败不得扇出、不得错配", func(t *testing.T) {
		rows := run(t, 2)

		// 2 次尝试 × 2 条失败 = 4 条不同失败。旧 SQL 返回 8 行。
		require.Len(t, rows, 4, "one failure must not fan out into several rows")

		byKey := make(map[string]*string, len(rows))
		for _, r := range rows {
			key := fmt.Sprintf("%s/attempt=%d", r.ErrorType, r.AttemptSeq)
			_, dup := byKey[key]
			require.False(t, dup, "duplicate (attempt, kind) row: %s", key)
			byKey[key] = r.Preview
		}
		for attempt := 0; attempt < 2; attempt++ {
			// 准入降级行本没有上游 body：绝不能被贴上别的错误的 body。
			require.Nil(t, byKey[fmt.Sprintf("fp_slot_saturated/attempt=%d", attempt)],
				"attempt %d: fp_slot_saturated must not carry an upstream body preview", attempt)
			// 上游失败行必须仍能拿到**它自己那次尝试**的 body。
			want := "preview-for-attempt-" + fmt.Sprint(attempt)
			got := byKey[fmt.Sprintf("network/attempt=%d", attempt)]
			require.NotNil(t, got, "attempt %d: network row lost its preview", attempt)
			require.Equal(t, want, *got,
				"attempt %d: preview bled across attempts — panel shows another error's body", attempt)
		}
	})

	t.Run("LIMIT 10 限的是失败条数而非连接行数", func(t *testing.T) {
		rows := run(t, 12) // 24 条不同失败

		// 断言必须数**不同**失败（attempt, kind），不能只数行数：旧 SQL 扇出后
		// LIMIT 10 同样返回 10 行，但那 10 行只对应 5 条不同失败——只数行数的
		// 断言在缺陷形态下也会绿（变异验证当场抓到这一点）。
		require.Len(t, rows, 10, "LIMIT must cap the result set at 10 rows")
		distinct := make(map[string]struct{}, len(rows))
		for _, r := range rows {
			distinct[fmt.Sprintf("%s/attempt=%d", r.ErrorType, r.AttemptSeq)] = struct{}{}
		}
		require.Len(t, distinct, 10,
			"the 10 returned rows must be 10 DISTINCT failures; fan-out means fewer")
	})
}

// seedDispatchAttempt 在事务内写入一次 dispatch 尝试产生的两条失败：
// fp_slot_saturated 准入降级（无上游 body）+ network 上游失败（有 body）。
// 两行共用 (request_id, credential_id, attempt_index)，只有 error_kind 与
// upstream_response_preview 不同——这正是旧 JOIN 键不唯一的根源。
func seedDispatchAttempt(t *testing.T, ctx context.Context, tx pgx.Tx, reqID string, attempt int) {
	t.Helper()
	preview := "preview-for-attempt-" + fmt.Sprint(attempt)
	_, err := tx.Exec(ctx, `
		INSERT INTO candidate_failure_logs_hot
		 (request_id, tenant_id, session_id, credential_id, provider_id,
		  raw_model_name, attempt_index, error_kind, error_message,
		  upstream_response_preview, ts, context)
		VALUES ($1,$2,'sess',$3,$4,'glm-5.2',$5,
		        'fp_slot_saturated','fp slot saturated',NULL,NOW(),'{}'::jsonb),
		       ($1,$2,'sess',$3,$4,'glm-5.2',$5,
		        'network','dial tcp refused',$6,NOW(),'{}'::jsonb)`,
		reqID, fanoutTenant, fanoutCredID, fanoutProvider, attempt, preview)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		INSERT INTO supplier_errors_hot
		 (occurred_at, request_id, tenant_id, session_id, provider_id, supplier,
		  credential_id, model, attempt_seq, error_type, http_status, error_message,
		  is_retryable, stage, request_metadata)
		VALUES (NOW(),$1,$2,'sess',$3,'r75',$4,'glm-5.2',$5,
		        'fp_slot_saturated',NULL,'fp slot saturated',TRUE,'preflight','{}'::jsonb),
		       (NOW(),$1,$2,'sess',$3,'r75',$4,'glm-5.2',$5,
		        'network',502,'dial tcp refused',TRUE,'upstream','{}'::jsonb)`,
		reqID, fanoutTenant, fanoutProvider, fanoutCredID, attempt)
	require.NoError(t, err)
}
