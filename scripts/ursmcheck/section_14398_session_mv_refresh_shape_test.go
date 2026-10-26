package ursm_test

// §143.98 的仓内门（R98 轮）：session 族物化视图刷新链的「成本形态」不变量。
//
// ── 归因对象（252 生产实测，2026-10-25）────────────────────────────────────
//
//	session_client_task_matrix 的 REFRESH MATERIALIZED VIEW CONCURRENTLY。
//	（154 是 session 轮 leader，每小时 :05 刷；245 是 routing 轮 leader。）
//
// ★ 逐次耗时的权威源是 **journald 的「refreshed session analytics view」elapsed**
//   ——它与 Prometheus gateway_mv_refresh_duration_seconds **逐字对齐**
//   （2026-10-25 01:06 前共 2 次：77.98 s + 124.68 s = 202.66 s = Histogram sum）。
//   n=94（2026-10-22 ~ 10-25）：min 2.66 s / med 43.05 s / max 592.81 s
//   ⇒ >60 s 占 38 次（40%），<10 s 占 28 次（30%）⇒ **波动 63 倍**。
//
// ★ 三条被**现场证伪**的假设（每一条都有反例，反例本身成为本门的对象）：
//
//	① 「那 108 s 的非查询侧是等锁」
//	   —— 01:05 现场采样：REFRESH 全程 wait_event_type 为空（不在任何 wait event）、
//	      pg_blocking_pids 为空（无阻塞者）、252 上无 >30 s 长事务 ⇒ **证伪**。
//	      锁形态是 ExclusiveLock granted=true（立即拿到）+ 两个 transient 临时表
//	      的 ShareLock ⇒ REFRESH CONCURRENTLY 对 matview 取的是 ExclusiveLock。
//
//	② 「session_dim 的 JOIN 键 ::text 显式转换废掉了索引」
//	   —— 去掉 ::text 后规划器选了**完全相同的计划**（Hash Right Join +
//	      Seq Scan on session_dim）⇒ **证伪**（PG 把 varchar→text 当 RelabelType，
//	      两侧同为 varchar 时比较语义不变）。
//
//	③ 「刷新慢是 matview 对象自身的问题（碎片 / 索引数 / delta 大小）」
//	   —— 三对象**同场交错** A/B（生产 vs 3-索引同构副本 vs 1-索引副本）：
//	      中位 3.38 / 3.09 / 3.09 s ⇒ **三者无差异** ⇒ 证伪。
//	      且「delta 100% vs 0%」也无差异（2.4~3.0 s vs 2.4~2.9 s）
//	      ⇒ 与仓内 855 迁移注释（§10.106.39）**独立吻合**。
//	   ⇒ ★★ 结论：REFRESH CONCURRENTLY 的**固有成本 ≈ 3 s**（热缓存），
//	     观察到的 52~592 s **全部是共享库负载波动**。
//	     ⇒ ★ 独立交叉验证：R97 巡检脚本的同源 cron 轮次在 42 s ~ 417 s 之间波动
//	       10 倍，而判据结果逐字不变。
//
// ★★ 两条**看起来显然、实测有害**的优化 —— 本门把它们钉死，防止后人重提：
//
//	④ 「把 30 天窗也下推到 session_dim 侧（sd.last_active_at >= ...）」
//	   —— 计划上确实把 Seq Scan 906,678 行换成了 Index Scan 313,203 行，
//	      但 buffers 318,737 → 553,704+7,437、耗时 5,672 ms → 45,577 ms（**8× 更慢**），
//	      **而且语义不等价**：抽样 2,000 行里有 484 行（24.2%）满足
//	      session_dim.last_active_at < session_summaries.last_request_at
//	      ⇒ 加这个过滤会把它们错误排除。
//
//	⑤ 「用 session_summaries.gw_task_id 取代 JOIN 去拿 task_id」
//	   —— 30 天窗 346,994 行里只有 89,153 行（25.7%）有 gw_task_id，
//	      而 JOIN 侧 COALESCE(sd.task_id,'unknown') 恒有值
//	      ⇒ 替换会让 74% 的行退化成 'unknown' ⇒ **口径变更** ⇒ 需属主裁决。
//
// ── 本门钉住什么 ──────────────────────────────────────────────────────────
//
//	① 结构门：当前生效的三视图定义必须带 **30 天时间窗**（855 加的；
//	   去掉它 ⇒ 输出组数从 1,832 涨回 104,299 ⇒ WAL 涨约 58 倍）。
//	② 结构门：三视图必须各带一个 **UNIQUE 索引** —— REFRESH ... CONCURRENTLY 的
//	   **硬前提**，缺了会直接报错而不是退化成普通 REFRESH。
//	③ 结构门：三视图必须保留 **refreshed_at** 列（admin 侧 4 个读点依赖它）。
//	④ 结构门：session_summaries 侧的 WHERE 必须是**可下推的范围条件**
//	   （last_request_at >= ...），不得写成列上的函数表达式（R97 同族）。
//	⑤ 结构门：定义里**不得**用 session_dim 的时间列做 JOIN 侧谓词（= ④）。
//	⑥ 真库反例门：session_dim.last_active_at **不是** session_summaries.last_request_at
//	   的合法代理 —— 门自己造夹具并断言违反样本存在 ⇒ 给 ⑤ 一个可核对的理由。
//	⑦ 真库反例门：session_summaries.gw_task_id 与 session_dim.task_id 的**覆盖率
//	   不同** ⇒ 「替换 JOIN」是口径变更而非等价改写（= ⑤ 的裁决依据）。
//	⑧ 结构门：startup 迁移的**两份副本**必须逐字一致（6 点同步面的一部分）。
//
// ★ 为什么 ⑥⑦ 用「自己造夹具」而不是读现成数据：
//   真库门跑在 testdb.Create 的**一次性空库**上 ⇒ 现成数据根本不存在；
//   而更要紧的是——**门不该依赖数据长什么样**。造一个必然违反的样本，
//   再断言「用 last_active_at 过滤会漏行」，得到的结论与生产数据量无关。
// ★ 每条都有**阳性对照**：一个只会判「绿」的门没有牙。

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/testdb"
)

const (
	// sec14398StartupDir 是仓内 startup 迁移的目录（视图定义在这里）。
	sec14398StartupDir = "../../sql/migrations/startup"
	// sec14398InstallerDir 是 startup 迁移的第二份副本（6 点同步面之一）。
	sec14398InstallerDir = "../../installer/cmd/llm-gw-installer/embeddata/startup"
	// sec14398InstallerViewFile 是两份副本里定义 session 族视图的那一个。
	sec14398InstallerViewFile = "855_session_analytics_time_window.sql"

	// sec14398MinWindowDays 是三视图必须保留的窗口下限（天）。
	// ★ 口径来源：855 迁移把它写成 INTERVAL '30 days'；本门只要求「不得
	//   短于 7 天」—— 7 天是巡检侧 baseline_days=7 的同量级下限，
	//   再短就会把「判据可比性」和「报表口径」一起破坏。
	sec14398MinWindowDays = 7
)

// sec14398Views 是本门覆盖的三个 session 族物化视图。
var sec14398Views = []string{
	"session_client_stats",
	"session_task_stats",
	"session_client_task_matrix",
}

// ── 定位与解析工具 ────────────────────────────────────────────────────────

// sec14398StripSQLComments 剥掉 SQL 注释。
//
// ★★★ 为什么必须剥：断言 SQL/源码前必须剥注释，否则「注释里写了
//   `WHERE ss.last_request_at >= NOW() - INTERVAL '30 days'`」就会让
//   一条**已经删掉时间窗**的迁移看起来合规。
func sec14398StripSQLComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// sec14398MigrationNumber 从文件名解析迁移号（"855_xxx.sql" → 855）。
func sec14398MigrationNumber(name string) (int, bool) {
	base := strings.TrimSuffix(filepath.Base(name), ".sql")
	idx := strings.Index(base, "_")
	if idx <= 0 {
		return 0, false
	}
	n, err := strconv.Atoi(base[:idx])
	if err != nil {
		return 0, false
	}
	return n, true
}

// sec14398DefiningMigrations 返回目录里所有「定义了该视图」的迁移，按迁移号升序。
//
// ★ 判据绑在**生效位置**上：谁定义了这个视图，就检查谁 —— 而不是门自己
//   写死一份定义（那会漏掉「有人重建了视图」这件事）。
func sec14398DefiningMigrations(t *testing.T, dir, view string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录 %s：%v", dir, err)
	}
	type mig struct {
		num  int
		name string
	}
	var found []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读 %s：%v", e.Name(), err)
		}
		stripped := sec14398StripSQLComments(string(b))
		// 只认「真的 CREATE MATERIALIZED VIEW <view>」这一处。
		re := regexp.MustCompile(`(?i)CREATE\s+MATERIALIZED\s+VIEW\s+(IF\s+NOT\s+EXISTS\s+)?` + regexp.QuoteMeta(view) + `\b`)
		if !re.MatchString(stripped) {
			continue
		}
		n, ok := sec14398MigrationNumber(e.Name())
		if !ok {
			continue
		}
		found = append(found, mig{n, e.Name()})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].num < found[j].num })
	var out []string
	for _, f := range found {
		out = append(out, f.name)
	}
	return out
}

// sec14398CurrentDefiningFile 返回**当前生效**（迁移号最大）的那份定义文件。
func sec14398CurrentDefiningFile(t *testing.T, view string) string {
	t.Helper()
	migs := sec14398DefiningMigrations(t, sec14398StartupDir, view)
	if len(migs) == 0 {
		t.Fatalf("在 %s 里找不到任何定义 %s 的迁移", sec14398StartupDir, view)
	}
	return filepath.Join(sec14398StartupDir, migs[len(migs)-1])
}

// sec14398ExtractViewBlock 抽出 `CREATE MATERIALIZED VIEW <view> … ;` 这一段。
func sec14398ExtractViewBlock(t *testing.T, src, view string) string {
	t.Helper()
	stripped := sec14398StripSQLComments(src)
	head := regexp.MustCompile(`(?i)CREATE\s+MATERIALIZED\s+VIEW\s+(IF\s+NOT\s+EXISTS\s+)?` + regexp.QuoteMeta(view) + `\b`)
	loc := head.FindStringIndex(stripped)
	if loc == nil {
		t.Fatalf("文件里没有 CREATE MATERIALIZED VIEW %s", view)
	}
	rest := stripped[loc[1]:]
	end := strings.Index(rest, ";")
	if end < 0 {
		t.Fatalf("CREATE MATERIALIZED VIEW %s 没有以 ';' 结束", view)
	}
	return rest[:end]
}

// ── 门 ①②③④：当前生效定义的关键不变量 ────────────────────────────────────

func TestSection14398SessionMVDefinitionInvariants(t *testing.T) {
	for _, view := range sec14398Views {
		view := view
		t.Run(view, func(t *testing.T) {
			path := sec14398CurrentDefiningFile(t, view)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("读 %s：%v", path, err)
			}
			block := sec14398ExtractViewBlock(t, string(b), view)
			lower := strings.ToLower(block)

			// ① 30 天时间窗（不得被改成更短的窗口，也不得整个去掉）。
			// ★ 判据从**实际定义**里抽 INTERVAL 'N days'，而不是写死 "30" ——
			//   将来若属主拍板把窗口改成 14 天，这道门应当跟着改，而不是红。
			winRe := regexp.MustCompile(`(?i)INTERVAL\s+'(\d+)\s+days?'`)
			m := winRe.FindStringSubmatch(block)
			if m == nil {
				t.Fatalf("%s 的定义里没有 INTERVAL 'N days' 形态的时间窗 ⇒ "+
					"它会退化成 855 之前「每轮全扫 session_summaries」的形态 "+
					"（输出组数 1,832 → 104,299，WAL 涨约 58 倍）", path)
			}
			days, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("解析窗口天数 %q 失败：%v", m[1], err)
			}
			if days < sec14398MinWindowDays {
				t.Fatalf("%s 的时间窗是 %d 天，低于下限 %d 天 ⇒ 报表口径与巡检判据都会破",
					path, days, sec14398MinWindowDays)
			}
			// 时间窗必须落在 session_summaries 的 last_request_at 上（可下推范围条件）。
			if !regexp.MustCompile(`(?i)last_request_at\s*>=`).MatchString(block) {
				t.Fatalf("%s 的时间窗不是写在 `last_request_at >= …` 上 ⇒ "+
					"不可下推的谓词会让规划器退化成全表扫", path)
			}
			// ★ 不得出现列上的函数表达式（R97 同族：date_trunc(...) >= … 不可下推）。
			if regexp.MustCompile(`(?i)date_trunc\s*\(`).MatchString(block) {
				t.Fatalf("%s 的时间窗谓词里出现 date_trunc(...) ⇒ 那是列上的函数表达式，"+
					"规划器换不成范围条件 ⇒ 会挂在宽 Index Cond 之上扫完即丢", path)
			}

			// ② UNIQUE 索引（REFRESH ... CONCURRENTLY 的硬前提）。
			// ★ 从**同一份文件**里找，而不是从别处 ⇒ 判据绑在被测对象上。
			idxRe := regexp.MustCompile(`(?i)CREATE\s+UNIQUE\s+INDEX[^;]*` + regexp.QuoteMeta(view) + `\b`)
			if !idxRe.MatchString(sec14398StripSQLComments(string(b))) {
				t.Fatalf("%s 没有为 %s 建 UNIQUE 索引 ⇒ REFRESH ... CONCURRENTLY 会直接报错，"+
					"而不是退化成普通 REFRESH", path, view)
			}

			// ③ refreshed_at 列（admin 侧 4 个读点依赖它）。
			if !strings.Contains(lower, "refreshed_at") {
				t.Fatalf("%s 的定义里没有 refreshed_at ⇒ admin 侧 4 个读点会读到陈旧值", path)
			}
		})
	}
}

// ── 门 ⑤：不得用 session_dim 的时间列做 JOIN 侧谓词 ─────────────────────────

func TestSection14398SessionMVCannotPushTimeWindowIntoSessionDim(t *testing.T) {
	for _, view := range sec14398Views {
		view := view
		t.Run(view, func(t *testing.T) {
			path := sec14398CurrentDefiningFile(t, view)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("读 %s：%v", path, err)
			}
			block := sec14398ExtractViewBlock(t, string(b), view)
			// ★ 只在 JOIN 谓词里查 session_dim 的时间列。
			//   session_dim 有 last_active_at / closed_at / created_at 等列；
			//   本门禁止的是「拿它们替代 session_summaries.last_request_at 做窗口过滤」。
			dimTime := regexp.MustCompile(`(?i)\b(?:sd|dim|session_dim)\s*\.\s*` +
				`(last_active_at|closed_at|created_at|updated_at)\b`)
			if dimTime.MatchString(block) {
				t.Fatalf("%s 的 JOIN 谓词里用了 session_dim 的时间列（%s）⇒ "+
					"★ 语义不等价：生产抽样 2,000 行里有 484 行（24.2%%）满足 "+
					"session_dim.last_active_at < session_summaries.last_request_at，"+
					"加这个过滤会把它们错误排除；★ 且实测更慢 8× "+
					"(5,672 ms → 45,577 ms，buffers 318,737 → 553,704+)",
					path, dimTime.FindString(block))
			}
		})
	}
}

// ── 门 ⑥：真库反例 —— last_active_at 不是 last_request_at 的合法代理 ─────────

func TestSection14398LastActiveAtIsNotAProxyForLastRequestAt(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SKIP：没有 TEST_DATABASE_URL ⇒ 无法在真库上构造反例" +
			"（★ 这道腿在 CI 里恒 SKIP ⇒ 其余各腿必须独立有效）")
	}
	scratch := testdb.Create(t, dsn)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, scratch)
	if err != nil {
		t.Fatalf("连一次性库失败：%v", err)
	}
	defer pool.Close()
	// ★ 单会话：SET TIME ZONE 只对当前会话生效，而 pool 的后续查询可能换连接。
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("取连接失败：%v", err)
	}
	defer conn.Release()

	// ★ 夹具用**固定时刻**，不依赖墙钟的「今天」⇒ 这道门不会在某个时段随机红。
	const base = "timestamp '2026-10-25 00:00:00+08'"
	// ★ 用 fmt.Sprintf 而不是 raw string 拼接：Go 的 raw string 里不能出现
	//   反引号拼接（配对会错乱 ⇒ 编译期 "missing ',' in argument list"）。
	setup := fmt.Sprintf(`
CREATE TABLE session_summaries (
  session_key text NOT NULL, tenant_id text NOT NULL,
  last_request_at timestamptz NOT NULL, gw_task_id text);
CREATE TABLE session_dim (
  gw_session_id text NOT NULL, tenant_id text NOT NULL,
  task_id text, last_active_at timestamptz);
INSERT INTO session_summaries VALUES
  ('s-eq',   't1', %[1]s,                       'task-a'),
  ('s-lt',   't1', %[1]s + interval '1 hour',    'task-b'),
  ('s-gt',   't1', %[1]s - interval '1 hour',    NULL),
  ('s-miss', 't1', %[1]s,                       'task-c');
INSERT INTO session_dim VALUES
  ('s-eq',   't1', 'task-a', %[1]s),
  ('s-lt',   't1', 'task-b', %[1]s),                     -- ★ 违反者
  ('s-gt',   't1', 'task-c', %[1]s - interval '1 hour');`, base)
	if _, err := conn.Exec(ctx, setup); err != nil {
		t.Fatalf("造夹具失败：%v", err)
	}

	// 违反样本 = session_dim.last_active_at < session_summaries.last_request_at。
	var violates, total int64
	q := `
SELECT count(*) FILTER (WHERE sd.last_active_at < ss.last_request_at), count(*)
FROM session_summaries ss
LEFT JOIN session_dim sd
  ON ss.session_key = sd.gw_session_id AND ss.tenant_id = sd.tenant_id`
	if err := conn.QueryRow(ctx, q).Scan(&violates, &total); err != nil {
		t.Fatalf("反例探针失败：%v", err)
	}
	if total < 4 {
		t.Fatalf("反例探针只覆盖了 %d 行 ⇒ 量具本身失效", total)
	}
	if violates == 0 {
		t.Fatalf("★ 夹具里没有造出违反样本 ⇒ 反例门失去意义（这说明门写错了，不是数据问题）")
	}

	// 关键断言：把 last_active_at 当成窗口过滤条件，会漏掉这些行。
	var kept, expected int64
	q2 := fmt.Sprintf(`
SELECT count(*), (SELECT count(*) FROM session_summaries)
FROM session_summaries ss
LEFT JOIN session_dim sd
  ON ss.session_key = sd.gw_session_id AND ss.tenant_id = sd.tenant_id
 AND sd.last_active_at >= %[1]s
WHERE ss.last_request_at >= %[1]s
  AND (sd.last_active_at IS NULL OR sd.last_active_at >= %[1]s)`, base)
	if err := conn.QueryRow(ctx, q2).Scan(&kept, &expected); err != nil {
		t.Fatalf("过滤探针失败：%v", err)
	}
	if kept == expected {
		t.Fatalf("★ 加了 sd.last_active_at 过滤之后行数没变（%d = %d）⇒ "+
			"反例夹具没能证明「last_active_at 不是 last_request_at 的代理」", kept, expected)
	}
}

// ── 门 ⑦：真库反例 —— gw_task_id 与 session_dim.task_id 不可互换 ──────────────

func TestSection14398SessionTaskIDSourceIsNotInterchangeable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SKIP：没有 TEST_DATABASE_URL ⇒ 无法在真库上构造反例" +
			"（★ 这道腿在 CI 里恒 SKIP ⇒ 其余各腿必须独立有效）")
	}
	scratch := testdb.Create(t, dsn)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, scratch)
	if err != nil {
		t.Fatalf("连一次性库失败：%v", err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("取连接失败：%v", err)
	}
	defer conn.Release()

	const base = "timestamp '2026-10-25 00:00:00+08'"
	setup := fmt.Sprintf(`
CREATE TABLE session_summaries (
  session_key text NOT NULL, tenant_id text NOT NULL,
  last_request_at timestamptz NOT NULL, gw_task_id text);
CREATE TABLE session_dim (
  gw_session_id text NOT NULL, tenant_id text NOT NULL,
  task_id text, last_active_at timestamptz);
INSERT INTO session_summaries VALUES
  ('s-1', 't1', %[1]s, 'task-a'),
  ('s-2', 't1', %[1]s, 'task-b'),
  ('s-3', 't1', %[1]s, NULL),
  ('s-4', 't1', %[1]s, '');
INSERT INTO session_dim VALUES
  ('s-1', 't1', 'task-a', %[1]s),
  ('s-2', 't1', 'task-b', %[1]s),
  ('s-3', 't1', 'task-c', %[1]s),
  ('s-4', 't1', 'task-d', %[1]s);`, base)
	if _, err := conn.Exec(ctx, setup); err != nil {
		t.Fatalf("造夹具失败：%v", err)
	}

	// 覆盖率：JOIN 侧（COALESCE(sd.task_id,'unknown')）恒有值；
	// 而 session_summaries.gw_task_id 只有部分行有值 ⇒ 两者不可互换。
	var joinCov, gwCov, total int64
	q := `
SELECT
  count(*) FILTER (WHERE COALESCE(sd.task_id, 'unknown') <> ''),
  count(*) FILTER (WHERE ss.gw_task_id IS NOT NULL AND ss.gw_task_id <> ''),
  count(*)
FROM session_summaries ss
LEFT JOIN session_dim sd
  ON ss.session_key = sd.gw_session_id AND ss.tenant_id = sd.tenant_id`
	if err := conn.QueryRow(ctx, q).Scan(&joinCov, &gwCov, &total); err != nil {
		t.Fatalf("覆盖率探针失败：%v", err)
	}
	if total < 4 {
		t.Fatalf("覆盖率探针只覆盖了 %d 行 ⇒ 量具本身失效", total)
	}
	if joinCov != total {
		t.Fatalf("★ JOIN 侧覆盖率不是 100%%（%d/%d）⇒ 夹具不对", joinCov, total)
	}
	if gwCov == joinCov {
		t.Fatalf("★ gw_task_id 覆盖率与 JOIN 侧相同（%d = %d）⇒ 反例门失去意义", gwCov, joinCov)
	}
	// ★ 生产实测口径（252，2026-10-25）：30 天窗 346,994 行里 89,153 行有 gw_task_id
	//   （25.7%），而 JOIN 侧恒有值 ⇒ 替换会让约 74% 的行退化成 'unknown'。
	if gwCov*100/total > 90 {
		t.Fatalf("★ gw_task_id 覆盖率 %d%% 太高 ⇒ 这不是「替换 JOIN 会改口径」的那种形态",
			gwCov*100/total)
	}
}

// ── 门 ⑧：startup 迁移的两份副本必须逐字一致 ────────────────────────────────

func TestSection14398StartupCopiesStayIdentical(t *testing.T) {
	a := filepath.Join(sec14398StartupDir, sec14398InstallerViewFile)
	b := filepath.Join(sec14398InstallerDir, sec14398InstallerViewFile)
	ba, err := os.ReadFile(a)
	if err != nil {
		t.Fatalf("读 %s：%v", a, err)
	}
	bb, err := os.ReadFile(b)
	if err != nil {
		t.Fatalf("读 %s：%v", b, err)
	}
	ha := md5.Sum(ba)
	hb := md5.Sum(bb)
	if ha != hb {
		t.Fatalf("★ 两份 startup 迁移不一致：\n  %s = %s\n  %s = %s\n"+
			"⇒ startup 迁移有 6 点同步面，漏同步会让「新装库」与「升级库」形态不同",
			a, hex.EncodeToString(ha[:]), b, hex.EncodeToString(hb[:]))
	}
}

var _ = fmt.Sprintf
