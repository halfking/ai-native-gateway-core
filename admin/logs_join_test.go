package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetLogDetail_TimestampMismatch pins the body-join key contract of the
// log detail read path.
//
// # 旧契约（v1 时代）
//
// bodies 的 JOIN 曾经写成
//
//	LEFT JOIN request_logs_bodies_with_current_month rb
//	  ON rb.request_id = rl.request_id AND rb.ts = rl.ts
//
// 而两侧的 ts 相等率**极低**（db/request_logs_view_schema.go 的文件头实测
// 99.85% 不等）⇒ ts 不等就**静默**丢正文（LEFT JOIN ⇒ NULL，不报错）。
//
// # R68 改瞄（2026-10-21，v1 族退役）
//
// 890 之后那条形态**不再可能**：
//
//	· v1 的 `request_logs_bodies_with_current_month` 视图随 v1 族一起消失；
//	· 会话族正文源 `db.SessionFamilyBodiesSourceSQL()` **不投影 `ts`**
//	  —— 那个文件头把理由写死了：「这个源不投影 ts…加上它会把一个**解析期
//	  错误**换成一个**静默的行数变化**。响亮失败优于静默改行」。
//
// ⇒ 本门改钉这条**更硬**的不变量：
//
//	① **ts 不等不再导致丢正文**（正文源只按 request_id 单键匹配）；
//	② 想按 ts 等值 JOIN 的写法会**响亮报错**（42703 undefined_column），
//	   而不是静默少算 —— 即「失败方式从静默变成响亮」这条改进被钉住。
//
// ⚠ 判据 ② 必须真的跑那条带 ts 的 SQL（而不是断言源文本里没有 "ts"）：
//
//	子串判据会被 request_id / request_delta 之类的词形骗过；
//	真正的不变量是「PG 对这条语句的裁决」。
func TestGetLogDetail_TimestampMismatch(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-ts-mismatch-" + time.Now().Format("20060102-150405.000")

	// Simulate the scenario where metadata and body are written with slightly different timestamps
	ts1 := time.Now().UTC()
	ts2 := ts1.Add(500 * time.Microsecond) // 500 microseconds difference

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// 元数据行（ts1）
	_, err = tx.Exec(ctx, `
		INSERT INTO session_turns_hot
			(session_id, turn_no, tenant_id, request_id, ts, model, success)
		VALUES ($1, 1, 'test-tenant', $2, $3, 'gpt-4', true)
	`, sessionIDFor(requestID), requestID, ts1)
	require.NoError(t, err)

	// 正文行（ts2 —— 与元数据行相差 500µs）
	_, err = tx.Exec(ctx, `
		INSERT INTO session_bodies_hot
			(session_id, turn_no, tenant_id, request_id, ts,
			 request_delta, response_delta)
		VALUES ($1, 1, 'test-tenant', $2, $3,
			'{"messages":[{"role":"user","content":"test"}]}'::jsonb,
			'{"choices":[{"message":{"role":"assistant","content":"response"}}]}'::jsonb)
	`, sessionIDFor(requestID), requestID, ts2)
	require.NoError(t, err)

	require.NoError(t, tx.Commit(ctx))

	// R65：本测试历史上提交后不清理（request_id 唯一故无冲突，但共享真库
	// 逐轮累积夹具行）——注册顺序在 pool.Close 之后，LIFO 保证先清理后关池。
	defer func() { cleanupTestRequestLog(t, pool, requestID) }()

	// ── 前提：两侧 ts 确实不等（否则本门证明不了任何东西）────────────
	var metaTS, bodyTS *time.Time
	err = pool.QueryRow(ctx, `
		SELECT rl.ts, (SELECT sb.ts FROM session_bodies_hot sb WHERE sb.request_id = $1 LIMIT 1)
		  FROM request_logs_with_current_month rl
		 WHERE rl.request_id = $1
		 LIMIT 1
	`, requestID).Scan(&metaTS, &bodyTS)
	require.NoError(t, err)
	require.NotNil(t, metaTS, "元数据行必须可见")
	require.NotNil(t, bodyTS, "正文行必须可见")
	require.NotEqual(t, *metaTS, *bodyTS,
		"前提破坏：两侧 ts 相等 ⇒ 本门证明不了「ts 不等不丢正文」")

	// ── 判据 1：按 request_id 单键 JOIN ⇒ 正文必须取得到 ─────────────
	var requestBody, responseBody *string
	err = pool.QueryRow(ctx, `
		SELECT rb.request_body::text, rb.response_body::text
		FROM request_logs_with_current_month rl
		LEFT JOIN (`+dbpkg.SessionFamilyBodiesSourceSQL()+`) rb
		  ON rb.request_id = rl.request_id
		WHERE rl.request_id = $1
		LIMIT 1
	`, requestID).Scan(&requestBody, &responseBody)
	require.NoError(t, err)
	require.NotNil(t, requestBody, "ts 不等不得导致丢正文（正文源只按 request_id 匹配）")
	require.NotNil(t, responseBody, "ts 不等不得导致丢正文")
	assert.Contains(t, *requestBody, "test", "Request body should contain test message")
	assert.Contains(t, *responseBody, "response", "Response body should contain response")

	// ── 判据 2：ts-等值 JOIN 的写法必须**响亮报错**，不得静默少算 ─────
	var tsJoinBody *string
	err = pool.QueryRow(ctx, `
		SELECT rb.request_body::text
		FROM request_logs_with_current_month rl
		LEFT JOIN (`+dbpkg.SessionFamilyBodiesSourceSQL()+`) rb
		  ON rb.request_id = rl.request_id AND rb.ts = rl.ts
		WHERE rl.request_id = $1
		LIMIT 1
	`, requestID).Scan(&tsJoinBody)
	require.Error(t, err,
		"会话族正文源不投影 ts ⇒ ts-等值 JOIN 必须响亮报错，不能静默丢行")
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr),
		"必须是 PG 错误码形态（实得 %T: %v）", err, err)
	assert.Equal(t, "42703", pgErr.Code,
		"必须是「列不存在」的响亮失败（undefined_column），而不是别的错误")
}
