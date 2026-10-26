// 判据纪律：cache-economics 的**压缩计数**查询（873 上线后的残余项）。
//
// 缺陷：压缩计数读 canonical 视图 request_logs_with_current_month，
// 30 天窗口 **>300s 跑不完**，3s 子预算 100% 超时 ⇒ 154 journalctl 每次调用
// 都记 `compressed request count unavailable / timeout` ⇒ compressed_requests
// 恒为 0 是**降级产物而不是真实值**，且每次白烧 3s。
//
// 实测（同一 30 天窗口）：
//
//	request_logs_with_current_month   >300s（跑不完）
//	_request_logs（母表单腿）            648 ms → 10,590 行
//	_request_logs_hot                   3 ms  → 0 行
//
// 本文件钉两条互相制约的源码不变量：**必须读母表**（否则跑不完），
// 且**必须是单腿**（否则 promote 会把重复行数两次）。只钉前者会放任
// 有人「顺手补成 UNION ALL」；只钉后者则等于放任它继续读那个跑不完的视图。
package admin

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// stripSQLCommentsForCompressed 去掉 `--` 行注释。
//
// ★ 必需：usage_enhanced.go 里那条压缩计数的注释**通篇都在讨论**
//
//	request_logs_with_current_month / _request_logs_hot / promote 去重，
//	注释里出现这些名字恰恰是在解释「为什么不再用它们」。
//	不剥离的话，下面两条断言一律恒真——「不得出现某视图」会因为注释里
//	提到它而误报，「必须出现某表」更会因为注释里提到而恒真。
//	这是 AGENTS.md「别把注释当代码导致假绿」的又一次现身。
func stripSQLCommentsForCompressed(sql string) string {
	var out []string
	for _, line := range strings.Split(sql, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// extractCompressedCountSQL 逐字取出 compressedQuery 的 SQL 字面量。
func extractCompressedCountSQL(t *testing.T) string {
	t.Helper()
	body := cacheEconomicsSourceText(t)

	const fnOpen = "func (h *Handler) usageCacheEconomics("
	fnStart := strings.Index(body, fnOpen)
	if fnStart < 0 {
		t.Fatal("cannot locate usageCacheEconomics")
	}
	fnEnd := strings.Index(body[fnStart+len(fnOpen):], "\nfunc ")
	fnBody := body[fnStart:]
	if fnEnd >= 0 {
		fnBody = body[fnStart : fnStart+len(fnOpen)+fnEnd]
	}

	const open = "compressedQuery := `"
	if n := strings.Count(fnBody, open); n != 1 {
		t.Fatalf("usageCacheEconomics 里应有且仅有 1 处 %q，实为 %d 处", open, n)
	}
	i := strings.Index(fnBody, open)
	rest := fnBody[i+len(open):]

	// 结束标记不能写成 "`, compressedArgs" —— 实际形态是闭合反引号换行后接
	// `compressedArgs := []any{...}`。从后者往回找最近的 "`" 更稳，
	// 对缩进（tab/空格）不敏感。
	anchor := strings.Index(rest, "compressedArgs := []any")
	if anchor < 0 {
		t.Fatal("cannot locate the compressedArgs declaration that terminates the SQL literal")
	}
	tick := strings.LastIndex(rest[:anchor], "`")
	if tick < 0 {
		t.Fatal("cannot locate the closing backtick of the compressedQuery SQL literal")
	}
	return stripSQLCommentsForCompressed(rest[:tick])
}

// TestCompressedCountReadsParentNotCanonicalView —— 正判据。
//
// 读 canonical 视图在 30 天窗口跑不完（>300s），子预算必然超时，
// 端点于是报一个恒为 0 的假值。
func TestCompressedCountReadsParentNotCanonicalView(t *testing.T) {
	sql := extractCompressedCountSQL(t)

	if strings.Contains(sql, "request_logs_with_current_month") {
		t.Error("压缩计数仍在读 request_logs_with_current_month：该视图在 30 天窗口 >300s 跑不完，" +
			"子预算必然超时，端点会报一个恒为 0 的假值")
	}
	// ★ 2026-10-20（迁移 890，v1 族真删）：压缩计数的取数源从 v1 母表
	// `_request_logs` 换成**会话族母表** `session_turns`（hot 不查 ——
	// promote 会把 hot 搬进母表，两腿同时存在时朴素 UNION ALL 会把同一行
	// 数两次，见 TestCompressedCountStaysSingleLeg）。
	// ⇒ 判据从「必须读 _request_logs 母表」**反转**为「必须读会话族母表，
	// 且不得再出现 v1 族关系名」。
	if !strings.Contains(sql, "FROM session_turns ") {
		t.Errorf("压缩计数没有读 session_turns 母表，实际 SQL：%s", strings.TrimSpace(sql))
	}
	if hits := retiredV1RelationsInSQL(sql); len(hits) > 0 {
		t.Errorf("压缩计数仍引用已退役 v1 族关系名 %v —— v1 族已随 890 被 DROP，读它 = 42P01：%s",
			hits, strings.TrimSpace(sql))
	}
	// 就地阳性对照：同一把尺子必须能认出 v1 形态（否则上面那条恒绿）。
	if hits := retiredV1RelationsInSQL("FROM _request_logs"); len(hits) == 0 {
		t.Error("retiredV1RelationsInSQL 认不出 v1 形态 —— 负向断言已恒绿，判据坏了")
	}
	// 谓词本身不能被改掉：这两条是「压缩过」的唯一定义。
	for _, frag := range []string{
		"compression_strategy IS NOT NULL",
		"compression_strategy <> ''",
		"rl.ts >= $1",
		"rl.ts < $2",
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("压缩计数 SQL 缺 %q：口径被改了，而实测真值（10,590 行）依赖它", frag)
		}
	}
}

// TestCompressedCountStaysSingleLeg —— 反判据，钉住 promote 去重语义。
//
// promote 会把旧行从 _request_logs_hot 搬到母表；两腿同时存在时，
// 朴素 UNION ALL 会把同一行数两次。对**计数**指标那是系统性虚高，
// 比 ≤8h 的低估严重得多（低估方向保守、可解释；虚高无法自证）。
// ⇒ 本查询必须保持单腿母表读。
func TestCompressedCountStaysSingleLeg(t *testing.T) {
	sql := extractCompressedCountSQL(t)

	if strings.Contains(sql, "_request_logs_hot") {
		t.Error("压缩计数并上了 _request_logs_hot 腿：promote 期间同一行会在两腿各计一次，" +
			"对计数指标就是系统性虚高。要双腿必须走族视图并核对 promote 去重语义")
	}
	if strings.Contains(strings.ToUpper(sql), "UNION") {
		t.Error("压缩计数 SQL 出现了 UNION：单腿计数不应有 UNION 形态，" +
			"否则 promote 重复行会被数两次")
	}
}

// TestCompressedCountRealDBCompletesWithinSubBudget —— 真库层：
// 用被测源码逐字取出的 SQL，带与 handler **同款**的 3s 子预算跑，
// 必须**跑完**，且算出的值与直接对物理表独立重算的一致。
//
// 「跑完」才是这条端点真正的回归点；一致性是防止「查出来了但口径错了」
// （本文件头注的 10,590 就是在防这个：旧实现返回的 0 也是「查出来了」
// 的形态 —— 它压根没查成）。
func TestCompressedCountRealDBCompletesWithinSubBudget(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	sql := extractCompressedCountSQL(t)
	// handler 传的是具体的 start/end；判据用相对形态以便自证窗口。
	sql = strings.Replace(sql, "rl.ts >= $1 AND rl.ts < $2",
		"rl.ts >= (now() - interval '30 days') AND rl.ts < now()", 1)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// 与 handler 同款的子预算。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var got int64
	if err := pool.QueryRow(ctx, sql).Scan(&got); err != nil {
		t.Fatalf("压缩计数在 3s 子预算内没跑完：%v"+
			"（旧实现读 canonical 视图 30 天窗口 >300s，端点因此报恒为 0 的假值）", err)
	}

	// 独立重算：不经 handler 的字面量，直接打物理表。
	//
	// ★ R68 改瞄（2026-10-21）：这里原来查 v1 母表 `_request_logs` ⇒ 890 之后
	// 必然 42P01（relation does not exist）。而**被测 SQL 本身**在 890 那轮
	// 已经改读会话族母表 `session_turns`（见上面 TestCompressedCountReadsParentNotCanonicalView
	// 的判据：「必须读 session_turns 母表」）。
	// ⇒ 独立重算必须打**同一张物理表**，否则就是「拿 v1 的数字对会话族的查询」
	//   —— 两边口径不同源，比对本身没有意义。
	var want int64
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM session_turns
		 WHERE ts >= (now() - interval '30 days') AND ts < now()
		   AND compression_strategy IS NOT NULL AND compression_strategy <> ''
	`).Scan(&want); err != nil {
		t.Fatalf("独立重算失败：%v", err)
	}
	if got != want {
		t.Errorf("压缩计数与独立重算不一致：handler 路径 %d，独立重算 %d", got, want)
	}
	t.Logf("压缩请求数（30 天）：handler 路径 %d，独立重算 %d", got, want)
}
