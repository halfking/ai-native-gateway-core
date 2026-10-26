//go:build !integration

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// last_outbound_no_rows_realdb_test.go —— 「**0 行不是错误**」这条收口的**真库行为门**
// （§143.57，2026-10-21）。
//
// # 这道门钉的是哪个生产缺陷
//
// ★★ **生产上已存在 7 天的 WARN 风暴**（154 主机实测）：
//
//	session_compressor: cache load failed, treating as new session
//	  session="gt_gw_f6438c77-…"  error="no rows in result set"
//
// 读数：154 上 18:00/19:00/20:00/21:00–21:27 各 **23 / 109 / 120 / 58** 次，
// 最早出现 **2026-10-14 05:43**（⇒ 已存在 7 天）。
//
// # 机制（2026-10-21 在 252 共享库上逐步实测闭合，非推理）
//
//	① auto_title / auto_summary 的 **loopback 请求**带 `X-Gw-Session-Id: gt_<原id>`
//	   （`admin/auto_title_generator.go:1083`）⇒ 于是压缩 hook 拿到的是
//	   **`gt_` 前缀**的 session id（实测 154 日志里的 WARN session **全部**是
//	   `gt_gw_` 前缀，**没有一个**是裸 `gw_`）。
//	② `gt_` 前缀的 id 在共享库里**一行都没有**（实测：`session_turns_hot` /
//	   `session_turns` / `session_bodies_hot` / `session_bodies` /
//	   `session_bodies_unified` / `sessions` **各 0 行**）。
//	③ L3 兜底腿 `lastOutboundSessionSQL` 以 **`session_turns` 为驱动表**
//	   （`FROM <turns> rl LEFT JOIN <bodies> sb`）⇒ ⇒ `gt_` id 恒无行
//	   ⇒ ⇒ **该腿恒返 0 行**（实测：同一批 `gt_gw_` id 返 0 行，而它们的真身
//	   `gw_` id 返 **1 行**）。
//	④ 0 行 ⇒ `pgx.ErrNoRows` ⇒ ⇒ 在 §143.55 的二进制上**被当成「数据库出错」**
//	   ⇒ ⇒ `domains/hooks/compression/session_cache.go` 的 `getLocal` 吞掉它、
//	   `session_cache_shared.go` 的 `loadSharedLocked` **直接往上抛**
//	   ⇒ ⇒ **每一次 loopback 必然产生一条 WARN**。
//
// # ⇒ 所以本门钉的就是那条收口
//
// `cmd/gateway/main_v3_wiring.go` 的 `scanLastOutboundRow` 必须把
// **`pgx.ErrNoRows` 映射成 `(nil, nil)`**（而不是往上抛）——
// §143.56 已落地，★ 本门用**真库**证明它在「0 行」这个真实形态上确实成立。
//
// # 为什么这道门不能只靠 §143.56 那道门
//
// §143.56 的真库门钉的是「**有正文的会话**在门开/门关两种形态下的行数与正文」。
// ⇒ 它**不覆盖**「**这个 session 在库里一行都没有**」这个形态
// ⇒ ⇒ 而**生产上的 WARN 风暴恰恰只发生在这个形态上**（`gt_` id 永无行）
// ⇒ ⇒ ⇒ **缺了这道门，那条收口可以在「有正文」的形态上全绿，而生产上仍在刷 WARN**。
//
// # 判据清单（各钉一件会独立坏掉的事）
//
//	① **0 行形态**：库里**完全没有**这个 session（`gt_` 形态）⇒ 该腿必须返 **0 行**
//	   （⇒ 不是「查询出错」，也不是「返 1 行 jsonb null」）。
//	② **收口生效**：0 行经 `scanLastOutboundRow` 必须得到 **`(nil, nil)`**
//	   ⇒ ★ **错误必须为 nil**（这是「0 行 vs 数据库出错」的分界线）。
//	③ **真身对照**：同一个库里的「**有** turns 行」的 session ⇒ 该腿必须返 **≥1 行**
//	   ⇒ ⇒ ★ 这条防的是「把腿写坏了导致永远 0 行」——**判据 ① 单独存在时是恒真的**
//	   （一条恒返 0 行的腿也能让 ① 变绿）⇒ ⇒ **必须有一条「必须非 0」的对照**。
//	④ **ErrNoRows 之外的真错误必须照旧上报**：构造一条**必然出错**的 SQL
//	   ⇒ ⇒ `scanLastOutboundRow` 必须返回**非 nil 的 error**
//	   ⇒ ⇒ ★ 防的是「把收口写成『吞掉一切错误』」——那是更糟的缺陷。
//
// # 造库配方
//
//	TEST_DATABASE_URL=<一次性库 DSN>  go test ./cmd/gateway/ -run TestNoRowsIsNotAnError
//
//	⚠ 一次性库须是「新装库形态」（installer 的 startup 迁移全链应用后的库）。
//	⚠ 本门**只读**：所有写操作都在**回滚事务**里（见下），可安全指向任何库。

// noRowsGateTenant / noRowsGateSessionGhost 是「库里一行都没有」的 session 形态。
//
// ★ 用 `gt_` 前缀不是为了「模拟」——**生产上它就是那个形态**
// （auto_title 的 loopback id）。所以夹具形态与生产**同形**。
const (
	noRowsGateTenant       = "e1gate"
	noRowsGateSessionGhost = "e1gate_gt_ghost_session"
	noRowsGateSessionReal  = "e1gate_real_session"
)

// noRowsGatePool 取得真库连接；没配 DSN 就 skip。
//
// ⚠ **skip 而不是当 0**（与 §143.56 那道门同款纪律）：
// 否则这道门会在没连库的机器上「量到 0 行」而变绿。
func noRowsGatePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— " +
			"⚠ 必须 skip 而不是当 0：否则这道门会在没连库的机器上「量到 0 行」而变绿。")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// noRowsGateRequireSessionFamily 断言目标库是「新装库形态」——**量具自检**。
//
// ⚠ 它检查的是**库的形状**，与被测代码无关 ⇒ 摘掉修复时它照样通过（不会变成
// 「用被测对象的产物当 oracle」）。
func noRowsGateRequireSessionFamily(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, rel := range []string{
		"session_turns_hot", "session_turns", "session_turn_details_hot",
		"session_turn_details", "session_bodies_hot", "session_bodies",
		"session_bodies_unified",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT to_regclass('public.`+rel+`') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatalf("探测关系 %q: %v", rel, err)
		}
		if !exists {
			t.Skipf("目标库缺关系 public.%s ⇒ 不是「新装库形态」⇒ 跳过。", rel)
		}
	}
	for _, probe := range []struct{ rel, col string }{
		{"session_turn_details_hot", "outbound_msg_count"},
		{"session_turn_details_hot", "outbound_token_est"},
		{"session_turn_details_hot", "outbound_msg_hashes"},
		{"session_turns_hot", "compression_meta"},
	} {
		var n int
		err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_attribute
			WHERE attrelid = $1::regclass AND attname = $2 AND NOT attisdropped`,
			"public."+probe.rel, probe.col).Scan(&n)
		if err != nil {
			t.Fatalf("探测 %s.%s: %v", probe.rel, probe.col, err)
		}
		if n == 0 {
			t.Skipf("目标库 public.%s 缺列 %s ⇒ 不是「新装库形态」⇒ 跳过。",
				probe.rel, probe.col)
		}
	}
}

// noRowsGateCountRows 用 `rows`（不是 `QueryRow`）数一条 outbound 查询返几行。
//
// ⚠ 用 `rows` 的理由与 §143.56 那道门相同：**0 行是本门要断言的合法结果**
// ⇒ ⇒ `QueryRow` 会把 0 行变成 `pgx.ErrNoRows` ⇒ ⇒ 那就分不清
// 「0 行」与「查询出错」了 ⇒ ⇒ **本门要断言的正是这个区别**。
func noRowsGateCountRows(t *testing.T, tx pgx.Tx, sqlText, sessionID, tenantID string) (int, error) {
	t.Helper()
	rows, err := tx.Query(context.Background(), sqlText, sessionID, tenantID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// TestNoRowsIsNotAnError 断言「**这个 session 在库里一行都没有**」时，
// L3 兜底腿返 0 行、且 `scanLastOutboundRow` 把 0 行映射成 `(nil, nil)`。
//
// ★ 这就是生产上那条 WARN 风暴（154，7 天，~100+/小时）的形态。
func TestNoRowsIsNotAnError(t *testing.T) {
	pool := noRowsGatePool(t)
	noRowsGateRequireSessionFamily(t, pool)

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// ★ 回滚 ⇒ 本门对目标库**零写入**，可安全指向任何库。
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	// ── 准备「真身」session：turns + bodies 各一行（⇒ 该腿必须返 ≥1 行）──
	//
	// ⚠ `turn_no` 是 **NOT NULL 且无默认值**（实测 information_schema）⇒ 必须显式给。
	// ⚠ `partition_date` 有默认 `CURRENT_DATE` ⇒ 不用给。
	const realReqID = "e1gate_real_req_0001"
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.session_turns_hot
			(tenant_id, session_id, request_id, turn_no, ts, compression_meta)
		VALUES ($1, $2, $3, 1, now(), NULL)`,
		noRowsGateTenant, noRowsGateSessionReal, realReqID); err != nil {
		t.Skipf("无法写入 session_turns_hot（列形状可能不同）⇒ 跳过：%v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.session_bodies_hot
			(tenant_id, session_id, request_id, turn_no, ts, kind, outbound_body)
		VALUES ($1, $2, $3, 1, now(), 'turn_delta', $4::jsonb)`,
		noRowsGateTenant, noRowsGateSessionReal, realReqID,
		`[{"role":"user","content":"real-body"}]`); err != nil {
		t.Skipf("无法写入 session_bodies_hot（列形状可能不同）⇒ 跳过：%v", err)
	}

	// ── 判据 ①：「库里一行都没有」的 session ⇒ 该腿必须返 **0 行** ──
	//
	// ★ 这正是生产形态：`gt_` 前缀的 loopback id 在库里一行都没有。
	ghostRows, err := noRowsGateCountRows(t, tx, lastOutboundSessionSQL,
		noRowsGateSessionGhost, noRowsGateTenant)
	if err != nil {
		t.Fatalf("判据 ①：兜底腿对「无行 session」查询本身出错：%v", err)
	}
	if ghostRows != 0 {
		t.Fatalf("判据 ①：兜底腿对「库里一行都没有」的 session 返了 %d 行，"+
			"期望 0 行 ⇒ ★ 这意味着该腿把「无行」当成了「有正文」"+
			"（生产上 `gt_` 前缀的 loopback session 永无行 ⇒ 恒 0 行）。", ghostRows)
	}

	// ── 判据 ②：0 行经 scanLastOutboundRow ⇒ **(nil, nil)**，错误必须为 nil ──
	//
	// ★ 这是「0 行 vs 数据库出错」的分界线。
	// ★ 生产影响：`session_cache_shared.go` 的 `loadSharedLocked` 会把错误
	//   **直接往上抛** ⇒ 不收口 ⇒ 「没有历史」变成硬错误。
	row, err := scanLastOutboundRow(pool.QueryRow(ctx, lastOutboundSessionSQL,
		noRowsGateSessionGhost, noRowsGateTenant))
	if err != nil {
		t.Fatalf("判据 ②：兜底腿返 0 行时 scanLastOutboundRow 返回了错误 %v；"+
			"期望 (nil, nil) ⇒ ★ 「0 行」必须与「数据库出错」分开表达"+
			"（生产上这正是 154 那条 `no rows in result set` WARN 风暴的来源，"+
			"已持续 7 天、~100+/小时）。", err)
	}
	if row != nil {
		t.Fatalf("判据 ②：兜底腿返 0 行时 scanLastOutboundRow 却返回了一个非 nil 的 row：%+v；"+
			"期望 (nil, nil)。", row)
	}

	// ── 判据 ③：★ 对照组 —— 「有 turns 行」的 session 必须返 **≥1 行** ──
	//
	// ★★ **判据 ① 单独存在时是恒真的**：一条「永远返 0 行」的腿也能让 ① 变绿。
	// ⇒ ⇒ 必须有一条「必须非 0」的对照 ⇒ ★ 这就是它存在的理由。
	realRows, err := noRowsGateCountRows(t, tx, lastOutboundSessionSQL,
		noRowsGateSessionReal, noRowsGateTenant)
	if err != nil {
		t.Fatalf("判据 ③：兜底腿对「有行 session」查询出错：%v", err)
	}
	if realRows < 1 {
		t.Fatalf("判据 ③：兜底腿对「库里明明有 turns 行」的 session 返了 %d 行，"+
			"期望 ≥1 行 ⇒ ★ 这条腿被写坏了（⇒ 判据 ① 会假绿）。", realRows)
	}

	// ── 判据 ④：★ 真错误必须照旧上报（收口不能写成「吞掉一切错误」）──
	//
	// ★ 这是收口的**反向边界**：只该放过 `pgx.ErrNoRows`，不该放过别的。
	brokenSQL := "SELECT outbound_body FROM public.session_bodies_unified WHERE no_such_column = $1"
	_, err = scanLastOutboundRow(pool.QueryRow(ctx, brokenSQL, noRowsGateSessionReal))
	if err == nil {
		t.Fatalf("判据 ④：一条必然出错的 SQL（不存在的列）却返回了 nil error；" +
			"⇒ 收口写成了「吞掉一切错误」⇒ ★ 那比原缺陷更糟。")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("判据 ④：一条必然出错的 SQL 却被判成 ErrNoRows（⇒ 那说明库里真有那个列，" +
			"判据失效）。")
	}
	if !strings.Contains(err.Error(), "no_such_column") {
		t.Logf("判据 ④：真实错误已上报（%v）；※ 错误文本未含列名 ⇒ 仅记录，不作判据。", err)
	}
}
