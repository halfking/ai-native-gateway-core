//go:build !integration

package main

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/db"
)

// last_outbound_final_full_realdb_test.go —— E1（final_full）读侧的**真库行为门**
// （§143.55，2026-10-21）。
//
// # 这道门补的是哪个盲点
//
// §143.55 修掉的是 `lastOutboundSessionSQL` 的一个**行为**缺陷：
// E1 门（`storage.session_final_full_enabled`）开着时，逐轮行的 outbound_body
// 是 SQL NULL、快照行的 request_id 是 `'final_full:<session>'`
// ⇒ 原式（`JOIN … ON sb.request_id = rl.request_id` + `WHERE sb.outbound_body IS NOT NULL`）
// **恒返 0 行**。
//
// 而门开之前**没有任何判据**能看见它：
//
//   - 形状门只查字符串（"JOIN 键必须是 request_id"、"必须有 outbound_body 过滤"），
//     而那两条断言**恰好把 bug 钉住了**（它们守的是 helper 合同「bodies 源不投影
//     ts ⇒ 只能按 request_id 关联」，而那条合同**只对逐轮正文成立**）；
//   - 没有任何门**真的跑一次**这条 SQL。
//
// ⇒ 与本仓反复记下的那条同族：**一条只被字符串断言过的 SQL，可以「看起来对」
// 很久**。本门照抄真库集成门的做法：**真跑一次**，而且**两种形态都跑**。
//
// # 五条判据，各钉一件会独立坏掉的事
//
//	① **门开形态**：逐轮 outbound_body = SQL NULL + 有 final_full 快照行
//	   ⇒ 旧式返 **0 行**（bug 现场）、新式返**快照正文**。
//	② **门关形态**：逐轮有正文、无快照行 ⇒ 新式与旧式**逐列相同**。
//	③ **快照腿是它答的**：把快照行删掉 ⇒ 新式对同一会话返 **0 行**
//	   ⇒ 证明 ① 的命中来自快照腿，不是逐轮行恰好也有正文。
//	④ **优先级**：逐轮有正文**且**有快照行（= 门开过又关掉的库形态）
//	   ⇒ 新式必须返**快照**（更新的完整 outbound），不是逐轮旧值。
//	⑤ **没有逐轮 bodies 行**：只有 turns+details 行 + 快照行
//	   ⇒ 新式仍须返**快照**。这一条是 **LEFT JOIN 存在的理由**：
//	   内连接要求「该轮必须有 bodies 行」，而 bodies 行会先于 turns 行被
//	   保留策略清掉 ⇒ 内连接会让快照腿**没机会答**。
//
// # 为什么旧式 SQL 是**抄**进来的，而新式是**引用**真常量
//
// 新式必须跑**生产代码本身**（`lastOutboundSessionSQL`）⇒ 直接引用常量；
// 旧式在 §143.55 之后已不存在于代码里 ⇒ 它只能作为**历史形态**被抄成字面量，
// 用来当「bug 的证人」。⚠ 抄的是**已经消失的形态**，不是被测对象 ⇒ 不存在
// 「副本漂移」风险（被测的那一份永远是常量本身）。
//
// # 无副作用
//
// 所有 INSERT/DELETE 都在**同一个事务**里，函数结尾无条件 `ROLLBACK`
// ⇒ 即使把 TEST_DATABASE_URL 误指到生产，代价也只是「一次事务往返」。
// ⚠ 但**仍建议指向一次性库**：生产有 RLS / 租户守卫，探针 INSERT 可能被拒
// ⇒ 那会是**假红**，不是真缺陷。
//
// # 需要什么样的库
//
// **新装库形态**（仓内 298 条 startup 迁移全链应用后的形态）。
// ⚠ 本机**没有任何现成的库**满足这个条件：
// 实测（2026-10-21，本机 PG 17.10 容器）逐库普查 40+ 个本地库：
//
//	`session_turns_hot` 带 `outbound_msg_count/outbound_token_est/outbound_msg_hashes`
//	三列的库 = **0 个**
//
// ⇒ 连**解析期**都过不去（那三列是 details 侧的列，见下）⇒ 必须新建。
// 造法（本门所在仓的标准配方，22.9 s）：
//
//	docker exec -i llm-gateway-pg psql -U llm_gateway -d postgres \
//	  -c "CREATE DATABASE itgate_14355;"
//	cd installer && TEST_INSTALLER_FRESH_DB_URL='postgres://…/itgate_14355?sslmode=disable' \
//	  go test -tags integration ./cmd/llm-gw-installer/ -run TestStartupChainGapsMatchReviewedSet
//
// # 三列元数据在**details 侧**（不是 turns 侧）——踩过才知道
//
// `outbound_msg_count` / `outbound_token_est` / `outbound_msg_hashes` 是
// **details 表**的列（`db.request_logs_view_schema.go` 的
// `detailsProjectionColumns`），`compression_meta` 才是 turns 侧的列
// ⇒ 造数据要**两张表都写**，只写 turns 会让四个元数据列全 NULL。
// e1GateBody 是门里用来对比正文的常量（jsonb 文本）。
// ⚠ 比较时**不能**直接比字节：PG 的 jsonb 输出会规范化（键序、空白）
// ⇒ 期望值也必须**经 PG 的 jsonb 归一化**后再比（见 jsonbNormalized）。
const (
	e1GateTenant      = "e1gate"
	e1GateSessionOn   = "e1gate_on"
	e1GateSessionOff  = "e1gate_off"
	e1GateSessionBoth = "e1gate_both"
	e1GateSessionNone = "e1gate_nobody"

	e1GateSnapshotJSON = `[{"role":"user","content":"snapshot-body"}]`
	e1GateLegacyJSON   = `[{"role":"user","content":"legacy-body"}]`
)

// lastOutboundSessionSQLPreE1 是 §143.55 **之前**的形态（历史文本，仅作证人）。
//
// ⚠ 抄它的理由写在文件头：「被测对象永远是 `lastOutboundSessionSQL` 常量本身」。
// ⚠ 一旦有人把新式改坏，这段历史文本**不会**跟着变 ⇒ 判据 ① 的「旧式返 0 行」
// 那一半仍然成立（它证明的是「旧形态确实不行」，与新形态无关）。
var lastOutboundSessionSQLPreE1 = `
		SELECT sb.outbound_body, rl.outbound_msg_count, rl.outbound_token_est,
		       rl.outbound_msg_hashes, rl.compression_meta
		FROM ` + db.SessionFamilyTurnsForSessionSQL() + ` rl
		JOIN ` + db.SessionFamilyBodiesSourceSQL() + ` sb
		  ON sb.request_id = rl.request_id
		WHERE rl.tenant_id = $2 AND sb.outbound_body IS NOT NULL
		ORDER BY rl.ts DESC
		LIMIT 1
	`

// e1GatePool 取得真库连接；没配 DSN 就 skip。
//
// ⚠ **skip 而不是当 0**：否则这道门会在没连库的机器上「量到 0 行」而变绿
// （与 `db/session_bodies_source_test.go` 同款纪律）。
func e1GatePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— " +
			"⚠ 必须 skip 而不是当 0：否则这道门会在没连库的机器上「量到 0 行」而变绿。" +
			"（造库配方见本文件头）")
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

// e1GateRequireSessionFamily 断言目标库是「新装库形态」。
//
// ⚠ 这一步是**量具自检**：会话族关系不存在 ⇒ 不是「新装库形态」⇒ skip 并说明
// 缺哪一个关系（**不静默通过**）。⚠ 它检查的是**库的形状**，与被测代码无关
// ⇒ 摘掉修复时它照样通过、照样红（不会变成「用被测对象的产物当 oracle」）。
func e1GateRequireSessionFamily(t *testing.T, pool *pgxpool.Pool) {
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
			t.Skipf("目标库缺关系 public.%s ⇒ 不是「新装库形态」⇒ 跳过。"+
				"（造库配方见本文件头：installer 298 条 startup 迁移全链应用后的库）", rel)
		}
	}
	// ★ 额外要求：三列元数据必须在（它们是 details 侧的列）。
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

// e1GateRow 是本门比对的行形态（与 scanLastOutboundRow 的 5 列一一对应）。
type e1GateRow struct {
	body   []byte
	count  *int
	tokens *int
	hashes []byte
	meta   []byte
}

// e1GateQuery 跑一条 outbound 查询并把 5 列取回来。
//
// ⚠ 用 `rows` 而不是 `QueryRow`：**0 行**是本门要断言的合法结果
// （判据 ① 的「旧式返 0 行」）⇒ `QueryRow` 会把 0 行变成 `pgx.ErrNoRows`，
// 那样就分不清「0 行」与「查询出错」了。
func e1GateQuery(t *testing.T, tx pgx.Tx, sqlText, sessionID, tenantID string) ([]e1GateRow, error) {
	t.Helper()
	rows, err := tx.Query(context.Background(), sqlText, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []e1GateRow
	for rows.Next() {
		var r e1GateRow
		if err := rows.Scan(&r.body, &r.count, &r.tokens, &r.hashes, &r.meta); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// e1GateJSONText 用 **PG 自己的 jsonb 归一化**渲染期望值。
//
// ⚠ 必要：PG 的 jsonb 输出会重排键序、压空白 ⇒ 直接比字节会假红。
func e1GateJSONText(t *testing.T, tx pgx.Tx, raw string) string {
	t.Helper()
	var out string
	if err := tx.QueryRow(context.Background(),
		`SELECT ($1::jsonb)::text`, raw).Scan(&out); err != nil {
		t.Fatalf("jsonb 归一化 %q: %v", raw, err)
	}
	return out
}

// e1GateInsertTurn 造一轮「turns + details + bodies 逐轮行」。
//
// ⚠ outboundBody == nil ⇒ 写成 **SQL NULL**（这正是 E1 门开时逐轮行的形态：
// `bodiesRec.OutboundBody = nil` ⇒ `jsonTextOrNull(nil)`）。
// ⚠ kind 固定 'turn_delta'；final_full 行由 e1GateInsertFinalFull 单独造。
func e1GateInsertTurn(t *testing.T, tx pgx.Tx, sessionID, requestID string, turnNo int, outboundBody []byte) {
	t.Helper()
	e1GateInsertTurnMeta(t, tx, sessionID, requestID, turnNo)
	ctx := context.Background()
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.session_bodies_hot
			(session_id, turn_no, tenant_id, request_id, ts, outbound_body, kind, partition_date)
		VALUES ($1, $2, $3, $4, now(), $5::text::jsonb, 'turn_delta', CURRENT_DATE)`,
		sessionID, turnNo, e1GateTenant, requestID, jsonTextOrNullBody(outboundBody)); err != nil {
		t.Fatalf("insert session_bodies_hot %s: %v", requestID, err)
	}
}

// e1GateInsertTurnMeta 只造 turns + details 两行（**不造 bodies 行**）。
//
// ⚠ 判据 ⑤ 用的就是这个形状：**该轮没有 bodies 行**（保留策略清掉 bodies
// 之后 turns 元数据仍在的形态）⇒ 内连接会让快照腿没机会答 ⇒ 必须左连接。
func e1GateInsertTurnMeta(t *testing.T, tx pgx.Tx, sessionID, requestID string, turnNo int) {
	t.Helper()
	ctx := context.Background()
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.session_turns_hot
			(session_id, turn_no, tenant_id, request_id, ts, compression_meta, partition_date)
		VALUES ($1, $2, $3, $4, now(), NULL, CURRENT_DATE)`,
		sessionID, turnNo, e1GateTenant, requestID); err != nil {
		t.Fatalf("insert session_turns_hot %s: %v", requestID, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.session_turn_details_hot
			(session_id, turn_no, tenant_id, request_id, ts,
			 outbound_msg_count, outbound_token_est, outbound_msg_hashes, partition_date)
		VALUES ($1, $2, $3, $4, now(), $5, $6, $7::jsonb, CURRENT_DATE)`,
		sessionID, turnNo, e1GateTenant, requestID, 2, 11, `["h1","h2"]`); err != nil {
		t.Fatalf("insert session_turn_details_hot %s: %v", requestID, err)
	}
}

// e1GateInsertFinalFull 造 E1 的快照行（turn_no=0、request_id='final_full:<session>'）。
func e1GateInsertFinalFull(t *testing.T, tx pgx.Tx, sessionID, outboundJSON string) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), `
		INSERT INTO public.session_bodies_hot
			(session_id, turn_no, tenant_id, request_id, ts, outbound_body, kind, partition_date)
		VALUES ($1, 0, $2, $3, now(), $4::text::jsonb, 'final_full', CURRENT_DATE)`,
		sessionID, e1GateTenant, "final_full:"+sessionID, outboundJSON); err != nil {
		t.Fatalf("insert final_full %s: %v", sessionID, err)
	}
}

// jsonTextOrNullBody 复刻写侧的 `jsonTextOrNull`：空 ⇒ SQL NULL。
//
// ⚠ 复刻而不是引用 domains/session/v2 的那个函数：那个是**未导出**的，
// 而本门必须在 package main 里自足（cmd/gateway 不能 import domains/session/v2
// 的私有符号）。⚠ 语义必须一致：E1 门开时逐轮行就是 **SQL NULL**。
func jsonTextOrNullBody(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// TestE1FinalFullReadSide 钉住 E1 读侧的四条行为判据（见文件头）。
func TestE1FinalFullReadSide(t *testing.T) {
	pool := e1GatePool(t)
	e1GateRequireSessionFamily(t, pool)

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// ⚠ 无条件回滚：这道门**不得**在目标库留下任何一行。
	defer func() {
		if err := tx.Rollback(context.Background()); err != nil {
			t.Logf("rollback: %v", err)
		}
	}()

	// ── 造数据 ────────────────────────────────────────────────────────────
	//
	// ① e1gate_on：门开形态（逐轮 outbound_body = SQL NULL + 有快照行）
	e1GateInsertTurn(t, tx, e1GateSessionOn, "e1gate_on_r1", 1, nil)
	e1GateInsertFinalFull(t, tx, e1GateSessionOn, e1GateSnapshotJSON)

	// ② e1gate_off：门关形态（逐轮有正文、无快照行）
	e1GateInsertTurn(t, tx, e1GateSessionOff, "e1gate_off_r1", 1, []byte(e1GateLegacyJSON))

	// ④ e1gate_both：门开过又关掉的库形态（逐轮有正文 + 有快照行）
	e1GateInsertTurn(t, tx, e1GateSessionBoth, "e1gate_both_r1", 1, []byte(e1GateLegacyJSON))
	e1GateInsertFinalFull(t, tx, e1GateSessionBoth, e1GateSnapshotJSON)

	// ⑤ e1gate_nobody：**没有逐轮 bodies 行**（保留策略清掉 bodies 后的形态）
	//    + 有快照行 ⇒ 左连接才能让快照腿答上。
	e1GateInsertTurnMeta(t, tx, e1GateSessionNone, "e1gate_nobody_r1", 1)
	e1GateInsertFinalFull(t, tx, e1GateSessionNone, e1GateSnapshotJSON)

	// ── 判据 ①：门开形态下，旧式返 0 行、新式返快照正文 ──────────────────
	oldRows, err := e1GateQuery(t, tx, lastOutboundSessionSQLPreE1, e1GateSessionOn, e1GateTenant)
	if err != nil {
		t.Fatalf("旧式查询（门开形态）: %v", err)
	}
	if len(oldRows) != 0 {
		t.Errorf("★ 旧式 SQL 在「门开形态」下返了 %d 行（期望 0）—— "+
			"这说明本门造的数据**没有复现** E1 的现场（逐轮 outbound_body 必须是 SQL NULL），"+
			"门开形态这一半的判据失去意义", len(oldRows))
	}

	newRows, err := e1GateQuery(t, tx, lastOutboundSessionSQL, e1GateSessionOn, e1GateTenant)
	if err != nil {
		t.Fatalf("新式查询（门开形态）: %v", err)
	}
	if len(newRows) != 1 {
		t.Fatalf("★ 新式 SQL 在「门开形态」下返了 %d 行（期望 1）—— "+
			"这正是 §143.55 要修的缺陷（final_full 快照读不到 ⇒ L3 冷启动拿不到 outbound）",
			len(newRows))
	}
	wantSnapshot := e1GateJSONText(t, tx, e1GateSnapshotJSON)
	if got := string(newRows[0].body); got != wantSnapshot {
		t.Errorf("★ 新式 SQL 在「门开形态」下返的正文不是快照正文：\n got = %s\nwant = %s",
			got, wantSnapshot)
	}
	// 元数据必须来自 details 侧（证明整条查询真的走通了 turns+details join）
	if newRows[0].count == nil || *newRows[0].count != 2 {
		t.Errorf("★ outbound_msg_count 不等于 details 里写的 2（got %v）—— "+
			"说明 details 侧没 join 上", newRows[0].count)
	}
	if newRows[0].tokens == nil || *newRows[0].tokens != 11 {
		t.Errorf("★ outbound_token_est 不等于 details 里写的 11（got %v）", newRows[0].tokens)
	}

	// ── 判据 ②：门关形态下，新式与旧式逐列相同 ───────────────────────────
	oldOff, err := e1GateQuery(t, tx, lastOutboundSessionSQLPreE1, e1GateSessionOff, e1GateTenant)
	if err != nil {
		t.Fatalf("旧式查询（门关形态）: %v", err)
	}
	newOff, err := e1GateQuery(t, tx, lastOutboundSessionSQL, e1GateSessionOff, e1GateTenant)
	if err != nil {
		t.Fatalf("新式查询（门关形态）: %v", err)
	}
	if len(oldOff) != 1 || len(newOff) != 1 {
		t.Fatalf("门关形态下旧式返 %d 行、新式返 %d 行（期望各 1 行）—— "+
			"「门关时语义不变」的前提不成立", len(oldOff), len(newOff))
	}
	wantLegacy := e1GateJSONText(t, tx, e1GateLegacyJSON)
	if got := string(newOff[0].body); got != wantLegacy {
		t.Errorf("★ 门关形态下新式返的正文不是逐轮正文：\n got = %s\nwant = %s", got, wantLegacy)
	}
	if !bytes.Equal(newOff[0].hashes, oldOff[0].hashes) {
		t.Errorf("★ 门关形态下 outbound_msg_hashes 变了：旧 %s / 新 %s",
			oldOff[0].hashes, newOff[0].hashes)
	}
	if (newOff[0].count == nil) != (oldOff[0].count == nil) ||
		(newOff[0].count != nil && *newOff[0].count != *oldOff[0].count) {
		t.Errorf("★ 门关形态下 outbound_msg_count 变了：旧 %v / 新 %v",
			oldOff[0].count, newOff[0].count)
	}

	// ── 判据 ④：逐轮有正文 + 有快照行 ⇒ 必须取快照（更新的完整 outbound）────
	bothRows, err := e1GateQuery(t, tx, lastOutboundSessionSQL, e1GateSessionBoth, e1GateTenant)
	if err != nil {
		t.Fatalf("新式查询（两者都有）: %v", err)
	}
	if len(bothRows) != 1 {
		t.Fatalf("两者都有的形态下新式返 %d 行（期望 1）", len(bothRows))
	}
	if got := string(bothRows[0].body); got != wantSnapshot {
		t.Errorf("★ 逐轮与快照同时存在时，新式没有优先取快照：\n got = %s\nwant = %s",
			got, wantSnapshot)
	}

	// ── 判据 ⑤：没有逐轮 bodies 行时，左连接仍须让快照腿答上 ──────────────
	noneRows, err := e1GateQuery(t, tx, lastOutboundSessionSQL, e1GateSessionNone, e1GateTenant)
	if err != nil {
		t.Fatalf("新式查询（没有逐轮 bodies 行）: %v", err)
	}
	if len(noneRows) != 1 {
		t.Fatalf("★ 该轮没有 bodies 行时新式返了 %d 行（期望 1）—— "+
			"这正是 bodies JOIN 必须是 **LEFT JOIN** 的理由："+
			"内连接要求「该轮有 bodies 行」，而 bodies 行会先于 turns 元数据被保留策略清掉 "+
			"⇒ 内连接会让 final_full 快照腿**永远没机会答**", len(noneRows))
	}
	if got := string(noneRows[0].body); got != wantSnapshot {
		t.Errorf("★ 该轮没有 bodies 行时，新式返的正文不是快照正文：\n got = %s\nwant = %s",
			got, wantSnapshot)
	}

	// ── 判据 ③：删掉快照行 ⇒ 新式对同一会话返 0 行 ────────────────────────
	// （证明 ① 的命中确实来自快照腿）
	if _, err := tx.Exec(ctx, `
		DELETE FROM public.session_bodies_hot
		WHERE tenant_id = $1 AND request_id = $2`,
		e1GateTenant, "final_full:"+e1GateSessionOn); err != nil {
		t.Fatalf("delete final_full: %v", err)
	}
	afterDelete, err := e1GateQuery(t, tx, lastOutboundSessionSQL, e1GateSessionOn, e1GateTenant)
	if err != nil {
		t.Fatalf("新式查询（快照删掉后）: %v", err)
	}
	if len(afterDelete) != 0 {
		t.Errorf("★ 删掉快照行后新式仍返 %d 行（期望 0）—— "+
			"说明 ① 的命中**不是**来自快照腿（逐轮行自己答了）", len(afterDelete))
	}
}
