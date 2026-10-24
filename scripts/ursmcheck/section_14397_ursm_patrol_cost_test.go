package ursm_test

// §143.97 的仓内门（R97 轮）：URSM 快照健康巡检链的成本修法。
//
// ── 缺陷本体（252 生产实测，2026-10-24）────────────────────────────────────
//
//	pg_stat_statements（按 dbid 拆库后）里 252 最贵的语句是巡检脚本的主判据：
//	    calls=79  total_exec=20,278.7 s  mean=256.7 s  max=591.6 s
//	⇒ 一条**监控**语句占了共享库最贵语句的榜首。
//
//	★ 该脚本其实**已经**在走 P11 的「汇总表快路」（base_source=daily，
//	  连续 20 轮 cov_days=7）⇒ 慢的**不是**「还在扫 7 天」这件事。
//
//	两个独立的成本源（EXPLAIN (ANALYZE, BUFFERS) 实测拆解）：
//
//	① ★★ 「今天」这条腿的谓词**不可下推** ——
//	   形态：AND date_trunc('hour', snapshot_ts) >= (今天零点)
//	   它是**列上的函数表达式**，PG 规划期不能把它换成 snapshot_ts 的范围条件
//	   ⇒ 只能当 **Filter** 挂在「snapshot_ts >= 7 天前」那个 Index Cond **之上**
//	   ⇒ 实测 Index Cond 仍然是 7 天 ⇒ 8 个分区全扫，其中 7 个分区的行扫完即丢。
//
//	② ★★ 每小时的 JIT 编译 ——
//	   巡检的每条查询都由**一次性 psql 会话**执行 ⇒ 后端一连就退
//	   ⇒ PG 的 JIT 产物缓存在后端内存里 ⇒ 下一轮全部重新编译
//	   ⇒ 实测 JIT: Functions: 181/259，Timing Total 6,550.9 / 7,160.9 ms
//	   ⇒ 这份时间**与判据结果一个字节都不相干**。
//
// ── 本门钉住什么 ──────────────────────────────────────────────────────────
//
//	① 结构门：巡检会话必须关 JIT（`-c jit=off`）—— 零语义影响（编译器不改变结果）。
//	② 结构门：「今天」这条腿必须写成**可下推的范围谓词**。
//	③ 真库语义门：新旧两种谓词在真库上**逐位等价**（边界点穷举）。
//	④ 真库端到端门：改造前后巡检的 **10 个判据字段逐字相同**（且基线非空）。
//	⑤ 真库计划门：改造后「今天」这条腿**只扫今天那一个分区**（旧形态扫 8 个）。
//
// ★ 为什么 ④⑤ 必须用**真库**：本地库规模比生产小 3 个数量级，
//   「本地跑得快」证明不了任何事；能证明的是**计划形态**与**结果口径**。
// ★ 为什么每条都有**阳性对照**：一个只会判「绿」的门没有牙。

import (
	"context"
	"fmt"
	"os"

	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/testdb"
)

const (
	sec14397PatrolScript = "../../scripts/252-monitor/ursm-snapshot-health.sh"
	// sec14397Frozen 是本轮所有真库门的时间锚点。
	//
	// ★★★ 为什么必须冻结（这是本轮自己踩出来的第二个「门」缺陷）：
	//   第一版的夹具与计划门都按 **墙钟的「今天」** 造数据/取窗口
	//   ⇒ 而 `date_trunc('hour', now())` 在 **00:00~01:00** 之间等于 **00:00**
	//   ⇒ 那个窗口是**空的** ⇒ 「今天」那条腿**一个分区都不会扫**
	//   ⇒ 计划门断言「必须扫 1 个分区」⇒ ★★ **每天 00:00~01:00 必红**（flaky 门）。
	//   ⇒ ⇒ ★★★ **通用式：★ 门不能依赖墙钟的「今天」** ——
	//     凡是用「今天 / 本周 / 最近 N 天」造夹具的门，都要在**固定时刻**上构造，
	//     否则它会在每天的某个时段变成随机红。
	sec14397Frozen = "timestamp '2026-10-24 23:00:00+08'"
	// sec14397FrozenToday 是 sec14397Frozen 所在的本地日零点（脚本里的「今天」）。
	sec14397FrozenToday = "timestamp '2026-10-24 00:00:00+08'"
)

// ── 脚本解析工具 ──────────────────────────────────────────────────────────

func sec14397ReadScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(sec14397PatrolScript)
	if err != nil {
		t.Fatalf("读巡检脚本 %s：%v", sec14397PatrolScript, err)
	}
	return string(b)
}

// sec14397ExtractBlock 取出 `NAME="` … `"\n` 之间的内容（脚本里的 SQL 变量块）。
func sec14397ExtractBlock(t *testing.T, body, name string) string {
	t.Helper()
	start := strings.Index(body, name+`="`)
	if start < 0 {
		t.Fatalf("脚本里找不到 %s 的定义块", name)
	}
	rest := body[start+len(name)+2:]
	end := strings.Index(rest, "\"\n")
	if end < 0 {
		t.Fatalf("脚本里 %s 的定义块没有闭合", name)
	}
	return rest[:end]
}

// sec14397ExtractTail 取出 `rows=$(de "$BUCKETS_SQL,` 之后、`FROM judged;"` 之前的聚合段。
func sec14397ExtractTail(t *testing.T, body string) string {
	t.Helper()
	const marker = `rows=$(de "$BUCKETS_SQL,`
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("脚本里找不到主判据的 de() 调用")
	}
	rest := body[idx+len(marker):]
	end := strings.Index(rest, "\nFROM judged;")
	if end < 0 {
		t.Fatalf("脚本里主判据的结尾 FROM judged; 找不到")
	}
	return rest[:end] + "\nFROM judged;"
}

// sec14397Render 复刻脚本的变量替换，把 SQL 渲染成可直接下发的形态。
//
// ★ 为什么在 Go 里复刻而不是直接跑脚本：
//
//	脚本要连生产库、跑 8 天基线；门要的是「同一段 SQL 的两种谓词形态」可对比。
//	⇒ 这里渲染出来的 SQL 与脚本生成的是**同一段文本**（同一个提取源），
//	  改动脚本就会立刻让这道门失配 ⇒ 这就是「语义耦合门」。
func sec14397Render(t *testing.T, body string, nowExpr string) string {
	t.Helper()
	sql := sec14397ExtractBlock(t, body, "BUCKETS_SQL_DAILY") + ",\n" + sec14397ExtractTail(t, body)
	subs := [][2]string{
		{"$G_UNIT", "hour"},
		{"$G_TRUNC", "date_trunc('hour',"},
		{"$G_STEP", "1 hour"},
		{"$G_IV", "interval '1 hour'"},
		{"$G_SLOT_TS", "extract(hour from date_trunc('hour', snapshot_ts))::int"},
		{"$G_SLOT_M", "extract(hour from s.m)::int"},
		{"$G_ACTIVE", "count(DISTINCT date_trunc('minute', snapshot_ts))"},
		{"$G_MAXA", "60"},
		{"$NOW_EXPR", nowExpr},
		{"$WINDOW_HOURS", "3"},
		{"$BASELINE_DAYS", "7"},
		{"$ACTIVE_DAYS", "3"},
		{"$DEGRADED_RATIO", "0.5"},
		{"$SPARSE_MIN_RATIO", "0.5"},
		{"$SPARSE_KEEP_RATIO", "0.5"},
	}
	for _, p := range subs {
		sql = strings.ReplaceAll(sql, p[0], p[1])
	}
	// ★ 变量必须被全部替换掉：漏一个就说明脚本新增了变量而门没跟上 ⇒ 直接失败。
	if left := strings.Contains(sql, "$G_") || strings.Contains(sql, "$NOW_EXPR") ||
		strings.Contains(sql, "$BASELINE") || strings.Contains(sql, "$ACTIVE") ||
		strings.Contains(sql, "$DEGRADED") || strings.Contains(sql, "$SPARSE") ||
		strings.Contains(sql, "$WINDOW"); left {
		t.Fatalf("渲染后仍有未替换的脚本变量 ⇒ 门与脚本已漂移：\n%s", sql)
	}
	return sql
}

const (
	// sec14397NewPredicate 是**修好后**的形态：可下推的范围谓词。
	sec14397NewPredicate = "AND snapshot_ts >= (now() AT TIME ZONE current_setting('TimeZone'))::date::timestamptz"
	// sec14397OldPredicate 是**修之前**的形态：列上的函数表达式（不可下推）。
	sec14397OldPredicate = "AND $G_TRUNC snapshot_ts) >= (now() AT TIME ZONE current_setting('TimeZone'))::date::timestamptz"
	// sec14397OldRendered 是【渲染后】的旧形态。
	// ★ 第一版直接把未渲染的旧形态（带 $G_TRUNC）塞回**已渲染**的 SQL
	//   ⇒ SQL 里留下一个没被替换的 `$G_TRUNC` ⇒ `syntax error at or near "$"`
	//   ⇒ 门自己红 ⇒ 教训：**变体替换必须发生在渲染之后，且两侧都要是渲染态**。
	sec14397OldRendered = "AND date_trunc('hour', snapshot_ts) >= (now() AT TIME ZONE current_setting('TimeZone'))::date::timestamptz"
)

// sec14397ToOld 把渲染好的 SQL 换回修之前的谓词形态（用于 A/B 与阳性对照）。
func sec14397ToOld(sql string) string {
	return strings.Replace(sql, sec14397NewPredicate, sec14397OldRendered, 1)
}

// ── 门 ①：巡检会话必须关 JIT ──────────────────────────────────────────────

func TestSection14397PatrolSessionDisablesJIT(t *testing.T) {
	body := sec14397ReadScript(t)

	// ★★★ 判据必须绑在【真正生效的那一处】上，不能用 `strings.Contains(整个文件, …)`。
	//   第一版就是用 Contains 判的 ⇒ ★★ **变异验证当场把它抓住**（M1）：
	//   脚本的说明注释里本来就有 `-c jit=off` 这几个字 ⇒
	//   ⇒ 把真正的设置删掉之后，Contains 照样命中**注释** ⇒ **门是绿的**。
	//   ⇒ ⇒ 通用式：**「文件里出现过某个字符串」≠「那个字符串在生效的位置出现过」**。
	opts := sec14397EffectivePGOptions(t, body)

	// ★ 阳性对照：判据本身必须能区分「带」与「不带」。
	if !sec14397JITPredicate("-c statement_timeout=600s -c jit=off") {
		t.Fatalf("阳性对照失效：带 -c jit=off 的样本被判成不合格")
	}
	if sec14397JITPredicate("-c statement_timeout=600s") {
		t.Fatalf("阳性对照失效：没带 -c jit=off 的样本被判成合格 ⇒ 这道门没有牙")
	}

	if !sec14397JITPredicate(opts) {
		t.Fatalf("★ 巡检会话没有关 JIT（生效的 PGOPTIONS = %q）⇒ 每小时要重新编译一遍全部表达式"+
			"（实测 181~259 个函数、6.5~7.2 s），而这份时间与判据结果毫无关系。"+
			"修法：PGOPTIONS 里加 `-c jit=off`（只影响本脚本自己的会话）", opts)
	}
}

// sec14397EffectivePGOptions 取出 de() 里**真正生效**的 PGOPTIONS 默认值。
func sec14397EffectivePGOptions(t *testing.T, body string) string {
	t.Helper()
	const marker = "PGOPTIONS="
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("脚本里找不到 PGOPTIONS")
	}
	rest := body[idx+len(marker):]
	// PGOPTIONS 的默认值被包在双引号里（`podman exec -e "PGOPTIONS=..."`）。
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("脚本里的 PGOPTIONS 默认值没有闭合的双引号")
	}
	return rest[:end]
}

func sec14397JITPredicate(pgoptions string) bool {
	return strings.Contains(pgoptions, "-c jit=off")
}

// sec14397TodayLegPredicate 从脚本的 DAILY 块里取出「今天」这条腿的**谓词**。
//
// ★ 为什么要「从脚本里抽」而不是在门里写死：
//
//	变异 M3（把谓词改成不等价的形态）第一版没被抓到 ——
//	因为等价门查的是**门自己写死的那段 SQL**，脚本改了它不知道。
//	⇒ ⇒ 判据必须绑在**被测对象**上，否则门与被测物可以各说各话。
func sec14397TodayLegPredicate(t *testing.T, body string) string {
	t.Helper()
	daily := sec14397ExtractBlock(t, body, "BUCKETS_SQL_DAILY")
	for _, ln := range strings.Split(daily, "\n") {
		s := strings.TrimSpace(ln)
		if s == "" || strings.HasPrefix(s, "--") {
			continue
		}
		// ★ 「今天」这条腿的谓词有两个特征：含 `::date::timestamptz`（今天零点），
		//   且以 AND 开头。汇总表那条腿的 `d.day < ...::date` 不含 `::timestamptz` ⇒ 不会误取。
		if strings.HasPrefix(s, "AND ") && strings.Contains(s, "::date::timestamptz") {
			return strings.TrimPrefix(s, "AND ")
		}
	}
	t.Fatalf("★ 脚本的 DAILY 块里找不到「今天」这条腿的谓词 ⇒ 门与脚本已漂移")
	return ""
}

// ── 门 ②：「今天」这条腿必须写成可下推的范围谓词 ──────────────────────────

func TestSection14397TodayLegUsesPushableRangePredicate(t *testing.T) {
	body := sec14397ReadScript(t)
	pred := sec14397TodayLegPredicate(t, body)

	// ★ 阳性对照：判据必须真的能区分「旧形态」与「新形态」。
	//   （第一版把这个判据写反了 —— 判据函数返回「是否合格」，
	//     而断言却按「是否不合格」来读 ⇒ 对旧形态误报「被判成合格」⇒ 门自己红。
	//     ⇒ ★ 教训：阳性对照的**方向**也要自测，不能只测「样本能通过」。）
	oldForm := "date_trunc('hour', snapshot_ts) >= (now() AT TIME ZONE current_setting('TimeZone'))::date::timestamptz"
	newForm := "snapshot_ts >= (now() AT TIME ZONE current_setting('TimeZone'))::date::timestamptz"
	if sec14397PushdownPredicate(oldForm) {
		t.Fatalf("阳性对照失效：旧形态被判成合格 ⇒ 这道门没有牙")
	}
	if !sec14397PushdownPredicate(newForm) {
		t.Fatalf("阳性对照失效：新形态被判成不合格 ⇒ 判据写反了")
	}

	if !sec14397PushdownPredicate(pred) {
		t.Fatalf("★「今天」这条腿的谓词是 %q ⇒ 它是【列上函数表达式】的形态 ⇒ "+
			"规划器不能把它换成 snapshot_ts 的范围条件 ⇒ 只能挂在「7 天前」那个 Index Cond 之上当 Filter "+
			"⇒ 实测 8 个分区全扫、其中 7 个分区的行扫完即丢", pred)
	}
}

// sec14397PushdownPredicate 判据本体：谓词可下推 ⇔ 是纯范围谓词（不含列上函数表达式）。
func sec14397PushdownPredicate(pred string) bool {
	return strings.Contains(pred, "snapshot_ts >=") &&
		!strings.Contains(pred, "date_trunc('hour', snapshot_ts) >=") &&
		!strings.Contains(pred, "$G_TRUNC")
}

// ── 门 ⑦：双引号 SQL 块里不得混进 shell 风格的行注释 ──────────────────────
//
// ★ 这道门是被**本轮自己引入的一处真缺陷**逼出来的（R97 轮作者手滑）：
//
//	脚本的 SQL 变量是 bash **双引号字符串** ⇒ 块里的每一行都会原样进入 SQL。
//	而作者在注释里写了两行 `# …`（shell 的行注释）⇒
//	⇒ `# …` 进了 SQL ⇒ ★★ `syntax error at or near "等价性照旧成立（Asia"`。
//
// ★★ 后果形态极坏：脚本**照常执行**，但主判据整条语句语法错误
//
//	⇒ sql_failed() 判出来 ⇒ exit 3 ⇒ 巡检每小时都「量具不可用」⇒
//	⇒ 也就是说【巡检会安静地失明】，而外部看到的只是「脚本退出码 3」。
//
// ★ 既有门（monitor_shell_quoting_test.go）只守【反引号】，不守 `#` ⇒ 这一类是盲区。
func TestSection14397NoShellCommentInsideSQLBlocks(t *testing.T) {
	body := sec14397ReadScript(t)
	for _, name := range []string{"BUCKETS_SQL_SOURCE", "BUCKETS_SQL_DAILY"} {
		block := sec14397ExtractBlock(t, body, name)
		for i, ln := range strings.Split(block, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(ln), "#") {
				continue
			}
			t.Fatalf("★ %s 块里第 %d 行是 shell 风格的行注释（`#` 开头）⇒ "+
				"它会原样进入 SQL ⇒ 整条主判据语法错误 ⇒ 巡检每小时都判「量具不可用」"+
				"（★ 这比慢更坏：巡检会安静地失明）⇒ SQL 块里的注释必须用 `--`：\n%s",
				name, i+1, ln)
		}
	}
	// ★ 阳性对照：`--` 形态必须被判成合规。
	if strings.HasPrefix(strings.TrimSpace("-- 这是 SQL 注释"), "#") {
		t.Fatalf("阳性对照失效：`--` 形态被判成违规 ⇒ 判据写反了")
	}
}

// ── 门 ③：新旧谓词在真库上逐位等价 ────────────────────────────────────────

func TestSection14397TodayLegPredicateIsEquivalentOnRealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SKIP：没有 TEST_DATABASE_URL ⇒ 无法在真库上验证谓词等价（★ 这道腿在 CI 里恒 SKIP ⇒ 其余各腿必须独立有效）")
	}
	scratch := testdb.Create(t, dsn)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, scratch)
	if err != nil {
		t.Fatalf("连一次性库失败：%v", err)
	}
	defer pool.Close()
	// ★★★ 全程用**同一条**连接：`SET TIME ZONE` 只对**当前会话**生效，
	//   而 pgxpool 的后续查询可能换一条连接 ⇒ 那样「今天零点」就不是同一个点了。
	//   ⇒ ⇒ 这道门必须是**单会话**的（第一次写的时候漏了 ⇒ 变异验证与整包测试各暴露一次）。
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("取连接失败：%v", err)
	}
	defer conn.Release()

	// ★ 生产会话时区就是 Asia/Shanghai（252 实测）⇒ 这里显式钉住，
	//   否则「今天零点」在不同会话里不是同一个点 ⇒ 等价性无从谈起。
	if _, err := conn.Exec(ctx, "SET TIME ZONE 'Asia/Shanghai'"); err != nil {
		t.Fatalf("设置会话时区失败：%v", err)
	}

	// ★★★ 探针必须查**脚本里那条真实的谓词**，不能查门自己写死的 SQL
	//   （第一版写死了 ⇒ 变异 M3 把脚本改成不等价的谓词，门照样绿）。
	pred := sec14397TodayLegPredicate(t, sec14397ReadScript(t))
	// 把脚本里的占位符渲染成 hour 粒度的真实表达式（$G_TRUNC = "date_trunc('hour',"）。
	expr := strings.ReplaceAll(pred, "$G_TRUNC ", "date_trunc('hour', ")
	expr = strings.ReplaceAll(expr, "snapshot_ts", "g.ts")
	// ★ 把表达式里的 now() 换成**冻结值** ⇒ 探针与墙钟无关。
	expr = strings.ReplaceAll(expr, "now()", sec14397Frozen)

	// 穷举边界：今天零点前后各若干分钟/小时，以及零点整。
	q := fmt.Sprintf(`
		WITH t AS (
		  SELECT %s AS today
		)
		SELECT count(*) FILTER (WHERE ((%s) IS DISTINCT FROM (g.ts >= t.today))) AS diff,
		       count(*) AS total
		  FROM t, LATERAL (
		    SELECT generate_series(t.today - interval '2 days', t.today + interval '2 days', interval '1 minute') AS ts
		  ) g`, sec14397FrozenToday, expr)

	var diff, total int64
	if err := conn.QueryRow(ctx, q).Scan(&diff, &total); err != nil {
		t.Fatalf("等价性探针失败：%v", err)
	}
	if total < 1000 {
		t.Fatalf("等价性探针只覆盖了 %d 个时间点 ⇒ 量具本身失效", total)
	}
	if diff != 0 {
		t.Errorf("★ 脚本里的谓词 %q 在 %d/%d 个时间点上与「ts >= 今天零点」不等 ⇒ 「今天」这条腿的改写不是等价改写",
			pred, diff, total)
	}
	t.Logf("§143.97 等价门：谓词 = %s；穷举 %d 个时间点 ⇒ 差异 %d", pred, total, diff)
}

// ── 真库夹具 ──────────────────────────────────────────────────────────────

// sec14397Fixture 在一次性库里造出与生产同形的两件东西：
//
//	① 按天分区的 ursm_node_snapshot_min（9 天数据）；
//	② 由**生产函数** refresh_ursm_snapshot_slot_daily 填出来的汇总表。
func sec14397Fixture(t *testing.T, ctx context.Context, conn *pgxpool.Conn) {
	t.Helper()
	// ★ 用**生产函数**填汇总表（迁移 892 的原文）⇒ 汇总表与源表的对应关系
	//   不是被手写出来的假数据。
	stmts := []string{
		`SET TIME ZONE 'Asia/Shanghai'`,
		`CREATE TABLE public.ursm_node_snapshot_min (
		     snapshot_ts     timestamptz NOT NULL,
		     tenant_id      text NOT NULL,
		     credential_id  text NOT NULL,
		     raw_model_name text NOT NULL,
		     payload        jsonb
		 ) PARTITION BY RANGE (snapshot_ts)`,
		`ALTER TABLE public.ursm_node_snapshot_min
		     ADD CONSTRAINT ursm_node_snapshot_min_pkey
		     PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name)`,
		// ★ 造分区：区间取 [冻结日-9, max(墙钟今天, 冻结日)+1]
		//   ⇒ 冻结窗口有数据、**墙钟的今天**也有数据
		//   ⇒ 两道真库门（判定等价比对 / 计划形态）都与墙钟无关。
		`DO $$
		 DECLARE d date; n text; lo date; hi date;
		 BEGIN
		   lo := LEAST((now() AT TIME ZONE current_setting('TimeZone'))::date, DATE '2026-10-24') - 9;
		   hi := GREATEST((now() AT TIME ZONE current_setting('TimeZone'))::date, DATE '2026-10-24') + 1;
		   FOR i IN 0..((hi - lo)) LOOP
		     d := lo + i;
		     n := 'ursm_node_snapshot_min_' || to_char(d, 'YYYYMMDD');
		     EXECUTE format(
		       'CREATE TABLE public.%I PARTITION OF public.ursm_node_snapshot_min
		          FOR VALUES FROM (%L) TO (%L)',
		       n, (d::text || ' 00:00:00+08')::timestamptz,
		              ((d + 1)::text || ' 00:00:00+08')::timestamptz);
		   END LOOP;
		 END $$;`,
		// ★ 每小时若干行，分钟位置散开 ⇒ count(DISTINCT date_trunc('minute')) 有意义。
		`INSERT INTO public.ursm_node_snapshot_min
		 SELECT g.ts, 'tenant-1', 'cred-' || (i % 7), 'model-' || (i % 3), '{}'::jsonb
		   FROM generate_series(
		          LEAST((now() AT TIME ZONE current_setting('TimeZone'))::date, DATE '2026-10-24')::timestamptz - interval '9 days',
		          GREATEST((now() AT TIME ZONE current_setting('TimeZone'))::date, DATE '2026-10-24')::timestamptz + interval '1 day',
		          interval '7 minutes') AS g(ts),
		        LATERAL generate_series(1, 8 + (EXTRACT(hour FROM g.ts)::int % 6)) AS i`,
		`CREATE TABLE IF NOT EXISTS public.ursm_snapshot_slot_daily (
		     day         date        NOT NULL,
		     granularity text        NOT NULL,
		     slot        int         NOT NULL,
		     bucket_ts   timestamptz NOT NULL,
		     rows        bigint      NOT NULL,
		     CONSTRAINT ursm_snapshot_slot_daily_pkey PRIMARY KEY (day, granularity, slot, bucket_ts)
		 )`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("造夹具失败：%v\nSQL: %s", err, s)
		}
	}
	// 套用迁移 892 的原文（建函数 + 首次回填）——**逐字**用仓内文件。
	mig, err := os.ReadFile("../../sql/migrations/startup/892_ursm_snapshot_slot_daily.sql")
	if err != nil {
		t.Fatalf("读迁移 892 失败：%v", err)
	}
	if _, err := conn.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("应用迁移 892 失败：%v", err)
	}
}

// ── 门 ④：改造前后，巡检的 10 个判据字段逐字相同 ───────────────────────────

func TestSection14397PatrolVerdictUnchangedAfterPushdown(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SKIP：没有 TEST_DATABASE_URL ⇒ 无法在真库上验证判定等价")
	}
	scratch := testdb.Create(t, dsn)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, scratch)
	if err != nil {
		t.Fatalf("连一次性库失败：%v", err)
	}
	defer pool.Close()
	// ★★★ 全程用**同一条**连接：`SET TIME ZONE` 只对**当前会话**生效，
	//   而 pgxpool 的后续查询可能换一条连接 ⇒ 那样「今天零点」就不是同一个点了。
	//   ⇒ ⇒ 这道门必须是**单会话**的（第一次写的时候漏了 ⇒ 变异验证与整包测试各暴露一次）。
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("取连接失败：%v", err)
	}
	defer conn.Release()

	sec14397Fixture(t, ctx, conn)

	body := sec14397ReadScript(t)
	// ★ 冻结 NOW_EXPR ⇒ 两种形态算的是**同一段数据** ⇒ 字段必须逐字相同。
	nowExpr := `timestamp '2026-10-24 23:00:00+08'`
	newSQL := sec14397Render(t, body, nowExpr)
	oldSQL := sec14397ToOld(newSQL)
	if oldSQL == newSQL {
		t.Fatalf("★ 改造前的形态没能被构造出来 ⇒ 这道门没有在比「改造前 vs 改造后」")
	}

	newRes := sec14397RunVerdict(t, ctx, conn, newSQL)
	oldRes := sec14397RunVerdict(t, ctx, conn, oldSQL)

	t.Logf("§143.97 端到端门：新形态 => %s", newRes)
	t.Logf("§143.97 端到端门：旧形态 => %s", oldRes)

	if newRes == "" || oldRes == "" {
		t.Fatalf("★ 有一侧没跑出结果（新=%q 旧=%q）⇒ 量具失效", newRes, oldRes)
	}
	// ★ 基线必须非空：空基线会让所有桶判成 NO-BASELINE ⇒ 两边都「一样」也没意义。
	fields := strings.Split(newRes, "|")
	if len(fields) != 10 {
		t.Fatalf("★ 判据结果不是 10 个字段：%q", newRes)
	}
	if fields[7] == "0" || fields[7] == "" {
		t.Fatalf("★ 基线中位数为空（ref_p50=%q）⇒ 没有参照系，两边相同毫无意义", fields[7])
	}
	if newRes != oldRes {
		t.Fatalf("★ 改造前后判据字段不同 ⇒ 这不是等价改写：\n  新: %s\n  旧: %s", newRes, oldRes)
	}
}

// sec14397RunVerdict 跑一次主判据，返回 10 个字段的拼接结果。
func sec14397RunVerdict(t *testing.T, ctx context.Context, conn *pgxpool.Conn, sql string) string {
	t.Helper()
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		t.Fatalf("主判据失败：%v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatalf("读结果失败：%v", err)
		}
		for _, v := range vals {
			out = append(out, fmt.Sprintf("%v", v))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("读结果失败：%v", err)
	}
	return strings.Join(out, "|")
}

// ── 门 ⑤：改造后「今天」这条腿只扫今天那一个分区 ──────────────────────────

func TestSection14397TodayLegScansOnlyTodaysPartition(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SKIP：没有 TEST_DATABASE_URL ⇒ 无法在真库上验证计划形态")
	}
	scratch := testdb.Create(t, dsn)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, scratch)
	if err != nil {
		t.Fatalf("连一次性库失败：%v", err)
	}
	defer pool.Close()
	// ★★★ 全程用**同一条**连接：`SET TIME ZONE` 只对**当前会话**生效，
	//   而 pgxpool 的后续查询可能换一条连接 ⇒ 那样「今天零点」就不是同一个点了。
	//   ⇒ ⇒ 这道门必须是**单会话**的（第一次写的时候漏了 ⇒ 变异验证与整包测试各暴露一次）。
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("取连接失败：%v", err)
	}
	defer conn.Release()

	sec14397Fixture(t, ctx, conn)

	// ★ 只取「今天」这条腿（基线腿的 UNION 分支），单独看它的计划。
	// ★ 窗口与「今天」都取**冻结值** ⇒ 这道门与墙钟无关（见 sec14397Frozen 的说明）。
	const legTemplate = `
		SELECT extract(hour from date_trunc('hour', snapshot_ts))::int AS slot, count(*) AS c
		  FROM public.ursm_node_snapshot_min
		 WHERE snapshot_ts >= date_trunc('hour', %[1]s - interval '7 days')
		   AND snapshot_ts <  date_trunc('hour', %[1]s)
		   AND %[2]s
		 GROUP BY 1, date_trunc('hour', snapshot_ts)`

	newLeg := fmt.Sprintf(legTemplate, sec14397Frozen,
		"snapshot_ts >= "+sec14397FrozenToday)
	oldLeg := fmt.Sprintf(legTemplate, sec14397Frozen,
		"date_trunc('hour', snapshot_ts) >= "+sec14397FrozenToday)

	newPlan := sec14397Explain(t, ctx, conn, newLeg)
	oldPlan := sec14397Explain(t, ctx, conn, oldLeg)

	t.Logf("§143.97 计划门（新形态）：\n%s", newPlan)
	t.Logf("§143.97 计划门（旧形态）：\n%s", oldPlan)

	// ★ 阳性对照：旧形态必须真的扫很多分区，否则这道门证明不了什么。
	oldScanned := sec14397CountScannedPartitions(oldPlan)
	if oldScanned < 2 {
		t.Fatalf("★ 阳性对照失效：旧形态只扫了 %d 个分区 ⇒ 本地夹具造不出「扫 7 天」这个形态 ⇒ 这道门证明不了下推的效果", oldScanned)
	}

	// ★ 新形态：只能是「今天那一个分区」被真正扫到。
	if strings.Contains(newPlan, "Filter: (date_trunc") {
		t.Errorf("★ 新形态里还有 date_trunc 的 Filter ⇒ 谓词仍不可下推：\n%s", newPlan)
	}
	newScanned := sec14397CountScannedPartitions(newPlan)
	if newScanned != 1 {
		t.Errorf("★ 新形态扫了 %d 个分区（期望 1）⇒ 「今天」这条腿仍在扫历史分区：\n%s", newScanned, newPlan)
	}
}

// sec14397CountScannedPartitions 数计划里**真正被扫到**的分区数
// （`never executed` 的不算；★ 同一个分区的「Bitmap Heap Scan」与
// 「Bitmap Index Scan」两行是**同一个分区**，不能数两次 —— 第一版就这么数错了，
// 把「只扫 1 个分区」误报成「扫了 2 个分区」⇒ 门自己红 ⇒ 修正后重验）。
func sec14397CountScannedPartitions(plan string) int {
	n := 0
	for _, ln := range strings.Split(plan, "\n") {
		if !strings.Contains(ln, "ursm_node_snapshot_min_") {
			continue
		}
		if !strings.Contains(ln, "Scan") {
			continue
		}
		// ★ 索引上的扫描行（`..._pkey`）与分区表上的扫描行指的是同一个分区。
		if strings.Contains(ln, "_pkey") {
			continue
		}
		if strings.Contains(ln, "never executed") {
			continue
		}
		n++
	}
	return n
}

func sec14397Explain(t *testing.T, ctx context.Context, conn *pgxpool.Conn, sql string) string {
	t.Helper()
	rows, err := conn.Query(ctx, "EXPLAIN (COSTS ON) "+sql)
	if err != nil {
		t.Fatalf("EXPLAIN 失败：%v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var ln string
		if err := rows.Scan(&ln); err != nil {
			t.Fatalf("读计划失败：%v", err)
		}
		lines = append(lines, ln)
	}
	return strings.Join(lines, "\n")
}

// ── 门 ⑥：台账必须跟着脚本走 ──────────────────────────────────────────────

func TestSection14397DeployedLedgerCoversTheChangedScript(t *testing.T) {
	b, err := os.ReadFile("../252-monitor/deployed-252.tsv")
	if err != nil {
		t.Fatalf("读部署台账失败：%v", err)
	}
	raw := string(b)
	if !strings.Contains(raw, "ursm-snapshot-health.sh") {
		t.Fatalf("★ 部署台账里没有 ursm-snapshot-health.sh 这一行 ⇒ 脚本改了却没登记 ⇒ 线上与仓内会漂移")
	}
	// ★ 台账的「仓内 md5」由同包的 deployed_script_drift_test.go 负责核对；
	//   这里只钉住「这一行存在且状态字段是 in-sync 或 drift」——
	//   因为 R97 改了脚本内容 ⇒ 这一行**必须**被更新过（改脚本不改台账 = 漂移）。
	for _, ln := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(ln, "ursm-snapshot-health.sh\t") {
			continue
		}
		parts := strings.Split(ln, "\t")
		if len(parts) < 4 {
			t.Fatalf("★ 台账行字段不足：%q", ln)
		}
		if parts[3] != "in-sync" && parts[3] != "drift" {
			t.Fatalf("★ 台账状态字段非法：%q", parts[3])
		}
		return
	}
	t.Fatalf("★ 台账里找不到 ursm-snapshot-health.sh 的整行")
}
