//go:build !integration

package db

// `SessionFamilyBodiesSourceSQL` 的真库门（审计 §9.230）。
//
// # 为什么必须是真库门，而且必须真的 JOIN 一次
//
// 这个 helper 是一段**手搓的 SQL 字符串**。它的消费者
// （`admin/session_export.go` / `admin/session_compare.go`）的 SELECT 里
// 写的是 `COALESCE(rb.request_body, '{}'::jsonb)` ——
// **列名或类型不对时，报错发生在运行期**，而且报的是 42703（列不存在）
// 或 22P02（字面量类型不对），都不是编译期能发现的。
//
// ⚠ 与本项目反复记下的那条同族：一条只被 mock 断言过字符串的 SQL，
// 可以「看起来对」很久。两处致命缺陷（`rl.role` 42703、`”::jsonb` 22P02）
// 曾在 `sessionExportMessagesSQL` 里各潜伏数周（session_export.go 的注释记着），
// 靠的是把真库集成门钉在那句 SQL 上。⇒ 这里照抄那个做法：**真跑一次**。
//
// # 三条判据，各钉一件会独立坏掉的事
//
//	① 列名/类型对等：映射的**目的**是让两侧对调用点同形。
//	   两侧现在 bodies 列都是 jsonb、request_id 都是 text NOT NULL。
//	   任何一侧改名或改类型，这里红。
//	② 真实 JOIN 不放大行：session_bodies 父表**当前**有 57 行重复 request_id
//	   （37 个 id）。实测这 37 个都不在 session_turns 里，所以今天不放大。
//	   ⚠ **「今天不」不是保证**——一旦某个重复 id 落进 session_turns，
//	   导出就会多出重复消息且不报错。
//	③ v1 那一臂真的存在且同形：默认关闭时读的是它。
//	   它若被带外操作删掉，OFF 臂就是一个幻影，而门会绿。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func bodiesSourcePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— " +
			"⚠ 必须 skip 而不是当 0：否则这道门会在没连库的机器上「量到 0 列」而变绿。")
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

// TestSessionFamilyBodiesSourceColumnParity 钉住两侧列名与类型同形。
func TestSessionFamilyBodiesSourceColumnParity(t *testing.T) {
	pool := bodiesSourcePool(t)
	ctx := context.Background()

	// ⚠ **第一版这里用 `pg_attribute` + `pg_temp.z` 取列名，失败了**
	// （`relation "pg_temp.z" does not exist`）——子查询别名不是能在
	// pg_attribute 里查到的关系。⇒ 量具错了，不是被测物错了。
	//
	// 改用**消费点的真实访问方式**：`SELECT pg_typeof(rb.<col>)`。
	// 这一版更强也更简单：
	//   - 列不存在 ⇒ **解析期**就 42P01/42703，错误信息直接指名那个列名；
	//   - 类型不对 ⇒ pg_typeof 返回的不是期望值。
	// 不需要猜怎么从 catalog 里挖子查询的列。
	got := map[string]string{}
	for _, c := range []string{"request_body", "response_body", "request_id"} {
		var typ string
		if err := pool.QueryRow(ctx,
			`SELECT pg_typeof(rb.`+c+`)::text FROM `+SessionFamilyBodiesSourceSQL()+
				` rb LIMIT 1`).Scan(&typ); err != nil {
			t.Errorf("★ 会话 bodies 源取不到列 %q（%v）—— "+
				"调用点写的是 COALESCE(rb.%s, '{}'::jsonb) 与 ON rb.request_id = rl.request_id，"+
				"列名一变就是运行期 42703。", c, err, c)
			continue
		}
		got[c] = typ
	}
	if len(got) == 0 {
		t.Fatal("一个列名都取不到 —— " +
			"⚠ 这里不能用「什么都没取到」当作通过：那是量具坏了，不是源坏了。")
	}
	t.Logf("会话 bodies 源列类型：%v", got)

	// ① 调用点投影的两个列名必须在，且是 jsonb。
	for _, c := range []string{"request_body", "response_body"} {
		typ, ok := got[c]
		if !ok {
			continue // 上面已报
		}
		if typ != "jsonb" {
			t.Errorf("session bodies 源的 %q 类型是 %s，期望 jsonb —— "+
				"v1 侧是 jsonb，不一致时 `COALESCE(…, '{}'::jsonb)` 会 22P02 或静默变文本", c, typ)
		}
	}

	// ② v1 那一臂（默认关闭时读的那一侧）必须同形。
	//
	// ⚠ **第一版这里只查了 request_body 一列**，却在注释里写「必须同形」。
	// 调用点投影的是 request_body **和** response_body，ON 子句用的是 request_id ——
	// 少查两列，v1 侧若把 response_body 改名，这道门照样绿，而默认支在运行期 42703。
	// v1 是**真实关系**（不是子查询），所以 catalog 查法在这里成立且不怕空表。
	v1Rel := "'request_logs_bodies_with_current_month'::regclass"
	for _, c := range []string{"request_body", "response_body", "request_id"} {
		var typ string
		err := pool.QueryRow(ctx, `
			SELECT format_type(a.atttypid, a.atttypmod)
			FROM pg_attribute a
			WHERE a.attrelid = `+v1Rel+`
			  AND a.attname = $1 AND NOT a.attisdropped`, c).Scan(&typ)
		if err != nil {
			t.Errorf("★ v1 bodies 关系没有 %q 列（%v）—— "+
				"默认关闭时读的就是它；它若被改名或删掉，OFF 臂是幻影而门会绿。", c, err)
			continue
		}
		// 会话侧同样查一次，两边都要能查到才有资格比类型。
		sessionTyp, ok := got[c]
		if !ok {
			continue // 上面已报
		}
		if typ != sessionTyp {
			t.Errorf("v1 侧 %q 是 %s，会话侧是 %s —— 两侧不同形，"+
				"开关打开后 COALESCE(rb.%s, …) / ON rb.%s 会 22P02 或 42703",
				c, typ, sessionTyp, c, c)
		}
	}
}

// TestSessionFamilyBodiesSourceDoesNotDuplicateTurns 钉住真实 JOIN 不放大行。
//
// 这条对应一个**已存在但今天不发作**的隐患：`session_bodies` 父表有 57 行
// 重复 request_id（37 个 id）。实测它们都不在 session_turns 里。
// 本门让「不放大」变成**被检查的事实**，而不是一句「今天恰好没事」。
func TestSessionFamilyBodiesSourceDoesNotDuplicateTurns(t *testing.T) {
	pool := bodiesSourcePool(t)
	ctx := context.Background()

	var turns, resultRows, extra int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::bigint,
		       count(*)::bigint,
		       count(*)::bigint - count(DISTINCT rl.request_id)
		FROM (SELECT request_id FROM session_turns ORDER BY ts DESC LIMIT 5000) rl
		LEFT JOIN `+SessionFamilyBodiesSourceSQL()+` rb ON rb.request_id = rl.request_id
	`).Scan(&turns, &resultRows, &extra); err != nil {
		t.Fatalf("real join shape: %v", err)
	}
	t.Logf("真实 JOIN 形状：turns=%d → 结果 %d 行，放大 %d", turns, resultRows, extra)
	if extra != 0 {
		t.Errorf("session bodies 源让 %d 个 turn 被放大成多行（多出 %d 行）—— "+
			"会话导出会**多出重复消息且不报错**。\n"+
			"已知隐患：`session_bodies` 父表当前有重复 request_id（实测 37 个 id / 57 行）。\n"+
			"若某个重复 id 落进 session_turns 就会发作。处置：源里加 DISTINCT ON，"+
			"或给 session_bodies 加 (request_id) 唯一约束。", extra, extra)
	}
	if resultRows != turns {
		t.Errorf("结果行数 %d ≠ turn 数 %d —— LEFT JOIN 不该改变行数，"+
			"变了说明源里出现了 NULL request_id 行", resultRows, turns)
	}
}
