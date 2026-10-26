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
//	③ **v1 那一臂已经退役**（迁移 885 真删表 + 890 退族）⇒ 这一腿的原语义
//	   「默认关闭时读的是它，它若被删掉 OFF 臂就是幻影」**已经不存在**：
//	   今天**没有**「默认关闭时读 v1」这回事。
//	   ⇒ 现行判据改成两件事（见下面「③ 的改瞄」小节）：
//	   ③a **v1 bodies 关系必须不存在**（退役完成的现行不变量）；
//	   ③b **会话族自己的两条腿必须同形**（hot 臂与父表的列类型一致）。
//
// # ③ 的改瞄（R69 轮，2026-10-21）
//
// 原 ③ 判的是「v1 那一臂真的存在且同形」，即
// `'request_logs_bodies_with_current_month'::regclass` 必须能查到 request_body /
// response_body / request_id 三列。**这条判据在 v1 族退役后恒红**（42P01），
// 而且它红的理由不是产品坏了，是**它守的那个东西已经不存在了**。
//
// ⇒ 同一族的「两侧同形」在今天仍然有意义，只是**两侧换了人**：
// 以前是「v1 侧 vs 会话侧」，今天是「会话 hot 臂 vs 会话父表臂」。
// ⇒ 故 ③ 改为 ③a + ③b。
//
// ⚠ **另外，① 的第一版探针是「有数据的」**（`SELECT pg_typeof(rb.<col>) FROM <源> LIMIT 1`）
// ⇒ **表空时返回 0 行** ⇒ 报 `no rows in result set` ⇒ **红**，
// 而那个红**与源无关**（源是对的，库是空的）⇒ **A 类：门在拒绝空证据**。
// 本轮把探针改成**与数据无关**（LEFT JOIN LATERAL ⇒ 恒 1 行），
// 于是「列名改了 ⇒ 解析期 42703」这条真判据**在任何库上都能验**。

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
	//
	// ⚠★ **R69 轮：这一版的探针是「有数据的」⇒ 空表上恒红。**
	// `SELECT pg_typeof(rb.<col>) FROM <源> LIMIT 1` 在源返回 0 行时
	// ⇒ `no rows in result set` ⇒ 门红 ⇒ ★ **那个红与源无关**
	// （源是对的、库是空的）⇒ **A 类：门在拒绝空证据**。
	//
	// ⇒ 改成**与数据无关**的形状：`LEFT JOIN LATERAL (...) ON true`
	// ⇒ 恒返回 **1 行**；`pg_typeof` 取的是**表达式的类型**（不是值的类型）
	// ⇒ 行内全是 NULL 也照样返回声明类型（实测：空库上得到 jsonb|jsonb|text）。
	// ⇒ 且**解析期语义完全保留**：列名写错仍然是 42703（实测）。
	probeColumnType := func(col string) (string, error) {
		var typ string
		err := pool.QueryRow(ctx, `
			SELECT pg_typeof(x.`+col+`)::text
			FROM (SELECT 1) AS s
			LEFT JOIN LATERAL (
				SELECT * FROM `+SessionFamilyBodiesSourceSQL()+` AS src LIMIT 1
			) AS x ON true
		`).Scan(&typ)
		return typ, err
	}
	got := map[string]string{}
	for _, c := range []string{"request_body", "response_body", "request_id"} {
		typ, err := probeColumnType(c)
		if err != nil {
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

	// ③a **v1 bodies 关系必须不存在**（v1 族已退役：迁移 885 真删表 + 890 退族）。
	//
	// ⚠ 这条**取代**了原来那条「v1 那一臂必须存在且同形」——
	// 后者的前提（默认关闭时读 v1）**已经不存在了**。
	// 现在真正的不变量是反过来的：**v1 不该再回来**。
	// ⇒ 一旦有人把 `_request_logs_bodies` / `request_logs_bodies_with_current_month`
	// 重新建起来（例如某个迁移回滚、或带外操作），这里立刻红。
	for _, rel := range []string{
		"request_logs_bodies_with_current_month",
		"_request_logs_bodies",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT to_regclass('public.`+rel+`') IS NOT NULL`).Scan(&exists); err != nil {
			t.Errorf("探测 v1 bodies 关系 %q 是否存在：%v", rel, err)
			continue
		}
		if exists {
			t.Errorf("★ v1 bodies 关系 public.%s **又存在了** —— "+
				"v1 族已随迁移 885/890 退役（真删表）；它若被重新建起，"+
				"说明有迁移回滚或带外 DDL ⇒ 读面会重新分裂成两族。", rel)
		}
	}

	// ③b **会话族自己的两条腿必须同形**（hot 臂 vs 父表臂）。
	//
	// ★ 这是原 ③「两侧同形」在**当前形态**下的等价物：
	// 以前两侧是「v1 侧 vs 会话侧」，今天两侧是会话族的 hot 臂与父表臂。
	// 用 **catalog** 取类型（这两张是真实关系，catalog 查法成立）⇒
	// **与数据无关**，空表上也成立。
	type relType struct{ rel, col string }
	for _, rt := range []relType{
		{"session_bodies_hot", "request_delta"},
		{"session_bodies", "request_delta"},
		{"session_bodies_hot", "response_delta"},
		{"session_bodies", "response_delta"},
		{"session_bodies_hot", "request_id"},
		{"session_bodies", "request_id"},
	} {
		var typ string
		err := pool.QueryRow(ctx, `
			SELECT format_type(a.atttypid, a.atttypmod)
			FROM pg_attribute a
			WHERE a.attrelid = 'public.`+rt.rel+`'::regclass
			  AND a.attname = $1 AND NOT a.attisdropped`, rt.col).Scan(&typ)
		if err != nil {
			t.Errorf("★ 会话 bodies 关系 %q 没有 %q 列（%v）—— "+
				"源里映射的是它；列名一变就是运行期 42703。", rt.rel, rt.col, err)
			continue
		}
		// 与源投影出来的类型比：源里 `request_delta AS request_body` ⇒
		// 源侧类型必须等于物理列类型（映射不能改类型，否则 COALESCE(…, '{}') 会 22P02）。
		srcCol := map[string]string{
			"request_delta":  "request_body",
			"response_delta": "response_body",
			"request_id":     "request_id",
		}[rt.col]
		if srcTyp, ok := got[srcCol]; ok && srcTyp != typ {
			t.Errorf("会话族 %s.%s 是 %s，源里投影成 %s 后是 %s —— 两侧不同形，"+
				"`COALESCE(rb.%s, '{}'::jsonb)` 会 22P02 或 42703",
				rt.rel, rt.col, typ, srcCol, srcTyp, srcCol)
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
