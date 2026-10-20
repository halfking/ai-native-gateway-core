package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ★★★ R68 改瞄（2026-10-21，v1 族退役后的 42P01 收口）：
//
// 本文件原来守的是「详情页用 `request_logs_with_current_month` LEFT JOIN
// `request_logs_bodies_with_current_month` 取正文」这个形状。890 把 v1 族整体
// DROP ⇒ 这两张表/视图的形态都变了：
//
//	· `request_logs_bodies_with_current_month` 随 v1 族一起消失 ⇒ 旧查询直接
//	  42P01（relation does not exist）；
//	· canonical 视图 `request_logs_with_current_month` **还在**，但内容已换成
//	  会话族（`session_turns_hot ∪ session_turns`）；
//	· 产品侧的详情读法**本来就是两步**（admin/logs.go 的注释自证）：
//	  「主查询只读 request_logs_with_current_month（metadata + 主表内嵌 body）
//	   …若主表内嵌 body 为空，单独查 body」—— 890 之后第二步只剩**会话族正文源**
//	  （`db.SessionFamilyBodiesSourceSQL()` = `session_bodies_hot ∪ session_bodies`）。
//
// ⇒ 所以这三条门**不是**「把表名换掉」，而是**照抄产品现在的两步读法**：
//   元数据腿 = `logsSourceFromSQL()`（默认 canonical 视图，打开灰度开关则是
//   会话族原生源），正文腿 = `h.fetchRequestBodies()`（产品函数本身）。
//
// ⚠ 会话族的正文列**不是** v1 的 `request_body`/`response_body`，而是
//   `request_delta`/`response_delta`（见 db/request_logs_view_schema.go 的
//   sessionBodiesSourceProjection）—— 这正是「机械换表名」会踩的坑。

// TestGetLogDetail_WithBodies verifies that the log detail read path retrieves
// complete request_body and response_body from the **session family**.
func TestGetLogDetail_WithBodies(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-get-detail-" + time.Now().Format("20060102-150405.000")

	// 会话族夹具：元数据行 + 正文行（两腿都插，否则 provider/正文维度会塌）
	insertSessionTurnWithBodies(t, pool, requestID)

	// ── 元数据腿：与产品同源（logsSourceFromSQL 默认是 canonical 视图，
	//    灰度开关打开时是会话族原生源；两者都是会话族）。
	var metaRequestID string
	err := pool.QueryRow(ctx, `
		SELECT rl.request_id
		  FROM `+logsSourceFromSQL()+`
		 WHERE rl.request_id = $1
		 LIMIT 1
	`, requestID).Scan(&metaRequestID)
	require.NoError(t, err, "元数据腿必须能从会话族读到该 request_id")
	assert.Equal(t, requestID, metaRequestID)

	// ── 正文腿：产品函数本身（admin/logs.go::fetchRequestBodies）
	h := &Handler{db: pool}
	reqBody, respBody, err := h.fetchRequestBodies(ctx, requestID)
	require.NoError(t, err, "正文腿必须能从会话族取到完整正文")

	rbJSON, ok := reqBody.(map[string]any)
	require.True(t, ok, "request_body should decode to a JSON object, got %T", reqBody)
	assert.Contains(t, rbJSON, "model", "request_body should carry the inserted model field")

	obJSON, ok := respBody.(map[string]any)
	require.True(t, ok, "response_body should decode to a JSON object, got %T", respBody)
	assert.Contains(t, obJSON, "id", "response_body should carry the inserted id field")

	// Cleanup
	cleanupTestRequestLog(t, pool, requestID)
}

// TestGetLogDetail_BackwardsCompatible pins the storage-tier compatibility
// contract of the detail read path: rows that live **only in the parent tier**
// (i.e. the "cold" side that was promoted out of the hot table) must still be
// reachable.
//
// R68 改瞄：旧契约是「v1 bodies 视图必须 UNION `_request_logs_bodies_hot` 与
// `_request_logs_bodies`（父表晋升行）」。890 之后没有 v1 族了 ⇒ 会话族的等价
// 契约是**父表腿可达**：`session_bodies`（父表）里的行必须仍然能取到。
//
// ⚠ 这里用**行为**证明两腿都活着，而不是 introspection（
//
//	information_schema.view_table_usage 只对视图有效，而会话族正文源是
//	手工拼的 UNION 子查询 ⇒ introspection 对它无效 ⇒ 只能跑）。
//	前提断言：hot 腿**确实没有**该 request_id（否则证明不了父表腿被读到）。
func TestGetLogDetail_BackwardsCompatible(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-backwards-compat-" + time.Now().Format("20060102-150405.000")

	// 只插**父表**一行（session_bodies），不插 session_bodies_hot。
	insertSessionTurnParentBodiesOnly(t, pool, requestID)

	// 前提：hot 腿确实没有这一行 ⇒ 命中只可能来自父表腿。
	var hotRows int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM session_bodies_hot WHERE request_id = $1
	`, requestID).Scan(&hotRows)
	require.NoError(t, err)
	require.Equal(t, 0, hotRows, "前提破坏：hot 腿竟然有这一行 ⇒ 父表腿可达性没被证明")

	// 行为证明：产品的正文读路径能取到只有父表腿才有的那一行。
	h := &Handler{db: pool}
	reqBody, respBody, err := h.fetchRequestBodies(ctx, requestID)
	require.NoError(t, err,
		"父表腿（session_bodies）必须仍然可达 —— 它是「历史侧」的唯一载体")
	require.NotNil(t, reqBody)
	require.NotNil(t, respBody)

	rbJSON, ok := reqBody.(map[string]any)
	require.True(t, ok, "request_body should decode to a JSON object, got %T", reqBody)
	assert.Contains(t, rbJSON, "model", "父表腿的正文内容必须完整")

	obJSON, ok := respBody.(map[string]any)
	require.True(t, ok, "response_body should decode to a JSON object, got %T", respBody)
	assert.Contains(t, obJSON, "id", "父表腿的正文内容必须完整")

	// Cleanup（注册晚于 pool.Close 的 defer，LIFO 先清理后关池）
	defer func() { cleanupTestRequestLog(t, pool, requestID) }()
}

// TestGetLogDetail_MissingBodies verifies that the detail read path handles
// the case where there is **no body row anywhere** gracefully: the body leg
// reports sql.ErrNoRows (which getLog treats as non-fatal) while the metadata
// leg still returns the record.
func TestGetLogDetail_MissingBodies(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-missing-bodies-" + time.Now().Format("20060102-150405.000")

	// 只插元数据（无正文 anywhere）
	insertSessionTurnMetadataOnly(t, pool, requestID)

	// 元数据腿照常返回这一行
	var metaRequestID string
	err := pool.QueryRow(ctx, `
		SELECT rl.request_id
		  FROM `+logsSourceFromSQL()+`
		 WHERE rl.request_id = $1
		 LIMIT 1
	`, requestID).Scan(&metaRequestID)
	require.NoError(t, err, "元数据腿不依赖正文 ⇒ 必须照常返回")
	assert.Equal(t, requestID, metaRequestID)

	// 正文腿：报 ErrNoRows，且**不得**是别的错误（caller 据此判「非致命」）
	h := &Handler{db: pool}
	reqBody, respBody, err := h.fetchRequestBodies(ctx, requestID)
	require.Error(t, err)
	assert.ErrorIs(t, err, sql.ErrNoRows,
		"两边都没正文时必须是 sql.ErrNoRows，caller 才能把它当非致命")
	assert.Nil(t, reqBody)
	assert.Nil(t, respBody)

	// Cleanup
	cleanupTestRequestLog(t, pool, requestID)
}

// ─────────────────────────────────────────────────────────────────────────────
// 会话族夹具
//
// ⚠ 换夹具不是换表名：**列名与唯一键都换了**
//   · 正文列：v1 `request_body`/`response_body` ⇒ 会话族 `request_delta`/`response_delta`；
//   · 唯一键：会话族 `session_turns_hot` 上是 (tenant_id, session_id, turn_no)
//     ⇒ 同一 session 下多行必须给不同 turn_no（这里固定用 session_id 派生 +
//     turn_no=1，因 session_id 由 request_id 派生 ⇒ 天然不撞）。
// ─────────────────────────────────────────────────────────────────────────────

// insertSessionTurnWithBodies writes one metadata row (session_turns_hot) and
// one body row (session_bodies_hot) — the session family's hot tier.
func insertSessionTurnWithBodies(t *testing.T, pool *pgxpool.Pool, requestID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO session_turns_hot
			(session_id, turn_no, tenant_id, request_id, ts,
			 model, success, latency_ms)
		VALUES ($1, 1, 'test-tenant', $2, now(),
			'test-model', true, 1234)
	`, sessionIDFor(requestID), requestID)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		INSERT INTO session_bodies_hot
			(session_id, turn_no, tenant_id, request_id,
			 request_delta, response_delta)
		VALUES ($1, 1, 'test-tenant', $2,
			'{"model":"test-model","messages":[{"role":"user","content":"test"}]}'::jsonb,
			'{"id":"test-response","content":"result"}'::jsonb)
	`, sessionIDFor(requestID), requestID)
	require.NoError(t, err)

	require.NoError(t, tx.Commit(ctx))
}

// insertSessionTurnParentBodiesOnly writes the **parent tier** body row only
// (session_bodies), leaving session_bodies_hot untouched. Used to prove the
// cold/parent leg of the session family's body source is still reachable.
func insertSessionTurnParentBodiesOnly(t *testing.T, pool *pgxpool.Pool, requestID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO session_turns_hot
			(session_id, turn_no, tenant_id, request_id, ts,
			 model, success, latency_ms)
		VALUES ($1, 1, 'test-tenant', $2, now(),
			'test-model', true, 1234)
	`, sessionIDFor(requestID), requestID)
	require.NoError(t, err)

	// ⚠ session_bodies 是**分区父表**：partition_date 默认 CURRENT_DATE ⇒
	//   路由到当月分区（实测为 heap ⇒ INSERT/DELETE 都支持）。
	_, err = tx.Exec(ctx, `
		INSERT INTO session_bodies
			(session_id, turn_no, tenant_id, request_id,
			 request_delta, response_delta)
		VALUES ($1, 1, 'test-tenant', $2,
			'{"model":"test-model","messages":[{"role":"user","content":"test"}]}'::jsonb,
			'{"id":"test-response","content":"result"}'::jsonb)
	`, sessionIDFor(requestID), requestID)
	require.NoError(t, err)

	require.NoError(t, tx.Commit(ctx))
}

// insertSessionTurnMetadataOnly writes only the metadata row (no body row in
// either tier).
func insertSessionTurnMetadataOnly(t *testing.T, pool *pgxpool.Pool, requestID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO session_turns_hot
			(session_id, turn_no, tenant_id, request_id, ts,
			 model, success, latency_ms)
		VALUES ($1, 1, 'test-tenant', $2, now(),
			'test-model', true, 1234)
	`, sessionIDFor(requestID), requestID)
	require.NoError(t, err)

	require.NoError(t, tx.Commit(ctx))
}

// sessionIDFor derives a session_id from a request_id. The session family's
// unique key is (tenant_id, session_id, turn_no) ⇒ the session_id must be
// unique per fixture row, otherwise two fixtures would collide on turn_no=1.
func sessionIDFor(requestID string) string {
	return "sess-" + requestID
}
