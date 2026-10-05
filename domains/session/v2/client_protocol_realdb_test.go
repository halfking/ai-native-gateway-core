//go:build !integration

package v2

// client_protocol_realdb_test.go — 2026-10-05（审计 §9.208）。
//
// # 这道门在守什么
//
// `session_turns.client_protocol` 这一列**从来没有被写方写过**：
// 实测 30 天 0 / 1,659,271 行非空，而它的两个同族列
// `agent_name` / `agent_type` 是 98.8%（1,455 / 1,473）。
//
// 后果不是「少一列数据」，而是**用户可见的**：本机
// `storage.admin_logs_native_turns_read = true`，于是
// `admin/logs.go` 的请求日志列表直读 session 族原生投影
// （`admin/logs.go:204` 选 `rl.client_protocol`），而该投影来自这批行
// ⇒ **列表里这个字段恒为空**。接口 200、页面正常、无任何日志。
//
// # 为什么必须是真库门
//
// 这次改动动的是一条 **98 → 99 参数的位置参数列表**。
// 少一个或多一个 `$N`，PostgreSQL 报
// `bind message supplies N parameters, but prepared statement requires M`
// —— 但那**只在真的执行时**才发生。pgxmock 的桩只要参数个数对就能过，
// 单测也照样绿。
//
// 而它**曾经真的错过一次**：第一版把列加进列清单、实参也加了，
// **SELECT 段里忘了加 `$99`** —— 是本轮自己写的一次性参数计数检查抓到的，
// 不是任何既有门。⇒ 这道门的作用是让「真跑一次」成为常规动作。
//
// # 阴性对照：空串必须落 NULL，而不是常量
//
// 如果映射写成常量或忘了 nilIfEmpty，这道门仍然可能对「有值」的用例报绿。
// 所以第二个用例用**空** ClientProtocol，断言落库是 NULL：
// ⇒ 证明这一列是**真的从记录里取来的**，不是写死的。

import (
	"context"

	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTurnWriterWritesClientProtocol_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database client_protocol proof")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}

	// 前提自证：列必须真的在。缺列时下面每一步都会以「读回 0」通过，
	// 那是一条恒真的门。
	var hasCol bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		                 WHERE table_name='session_turns_hot' AND column_name='client_protocol')`).
		Scan(&hasCol); err != nil {
		t.Fatalf("probe column: %v", err)
	}
	if !hasCol {
		t.Fatalf("session_turns_hot.client_protocol 不存在 —— 本门前提不成立，" +
			"它会在缺列时恒绿。**不要**把恒绿当通过。")
	}

	stamp := time.Now().UTC().Format("20060102150405.000000000")
	ids := []string{
		"probe-cp-value-" + strings.ReplaceAll(stamp, ".", ""),
		"probe-cp-empty-" + strings.ReplaceAll(stamp, ".", ""),
	}
	// 清理必须覆盖两张脸：session_turns_hot 与 session_turns（AppendTurn
	// 走的是 hot，但分区提升后同一行会出现在母表）。
	defer func() {
		for _, id := range ids {
			for _, tbl := range []string{"public.session_turns", "public.session_turns_hot"} {
				if _, err := pool.Exec(ctx,
					`DELETE FROM `+tbl+` WHERE request_id = $1`, id); err != nil {
					t.Errorf("cleanup %s from %s: %v", id, tbl, err)
				}
			}
		}
		var leftover int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM (
			  SELECT request_id FROM session_turns_hot WHERE request_id LIKE 'probe-cp-%'
			  UNION ALL SELECT request_id FROM session_turns  WHERE request_id LIKE 'probe-cp-%'
			) x`).Scan(&leftover); err != nil {
			t.Errorf("post-cleanup probe: %v", err)
			return
		}
		if leftover != 0 {
			t.Errorf("本门在库里留了 %d 行探针数据（request_id LIKE 'probe-cp-%%'）", leftover)
		}
	}()

	w := newTurnWriter(pool)

	// 阳性：带值的记录必须把值落进去。
	if _, err := w.AppendTurn(ctx, TurnRecord{
		SessionID:      "probe-cp-session",
		TenantID:       "probe-cp-tenant",
		RequestID:      ids[0],
		Ts:             time.Now().UTC().Truncate(time.Microsecond),
		ClientProtocol: "anthropic-messages",
		// ⚠ DigestJSON 必须自带合法 JSON：`AppendTurn` 把它按
		// `string(rec.DigestJSON)` 传给 jsonb 列，零值 → `''::jsonb` → 22P02。
		// 生产侧不踩是因为 digestJSON 总来自 sessiondigest.Marshal（出错即 return）。
		DigestJSON: []byte(`{}`),
	}); err != nil {
		// 参数个数错就是在这里炸 —— 这正是这道门要抓的。
		t.Fatalf("AppendTurn (value case): %v — 若报 bind message supplies N parameters，"+
			"则是列清单 / $N / 实参三处没对齐", err)
	}
	var got *string
	if err := pool.QueryRow(ctx,
		`SELECT client_protocol FROM session_turns_hot WHERE request_id = $1`, ids[0]).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got == nil || *got != "anthropic-messages" {
		t.Fatalf("client_protocol = %v, want %q —— 列仍然没被写（§9.203 之后的同一个形态）", got, "anthropic-messages")
	}

	// 阴性对照：空值必须落 NULL，而不是被写死成某个常量。
	if _, err := w.AppendTurn(ctx, TurnRecord{
		SessionID:  "probe-cp-session",
		TenantID:   "probe-cp-tenant",
		RequestID:  ids[1],
		Ts:         time.Now().UTC().Truncate(time.Microsecond),
		DigestJSON: []byte(`{}`),
		// ClientProtocol 故意留空
	}); err != nil {
		t.Fatalf("AppendTurn (empty case): %v", err)
	}
	var gotEmpty *string
	if err := pool.QueryRow(ctx,
		`SELECT client_protocol FROM session_turns_hot WHERE request_id = $1`, ids[1]).Scan(&gotEmpty); err != nil {
		t.Fatalf("read back (empty case): %v", err)
	}
	if gotEmpty != nil {
		t.Fatalf("空 ClientProtocol 落库为 %q，期望 NULL —— nilIfEmpty 没生效，"+
			"或这一列被写成了常量", *gotEmpty)
	}

	// 投影腿（admin 列表读的那条）**不在本门考核**。
	// `db.SessionFamilyTurnsSourceSQL()` 里的投影是纯列引用
	// （`t.client_protocol::character varying(50)`，db/request_logs_view_schema.go:457），
	// 而那个函数住在 db 包 —— 从 v2 引入要穿过 db→v2 的依赖方向。
	// 本门只保证**写侧**成立：值会落库、空值落 NULL、参数个数对齐。
	// 「admin 列表因此不再为空」是读侧的事，由 admin 侧的门负责。
	t.Logf("写侧成立：%s 的 client_protocol=%q、%s 的 client_protocol 为 NULL", ids[0], "anthropic-messages", ids[1])
}
