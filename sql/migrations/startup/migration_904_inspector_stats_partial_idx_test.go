package startup

// migration_904_inspector_stats_partial_idx_test.go —— 904 的契约门（R89 inspector-stats 慢端点归因轮）。
//
// # 这道门守的是什么
//
// 904 给 public.session_dim / public.session_summaries 补两条**部分索引**，
// 修的是 GET /api/admin/sessions/inspector-stats 的两条「恒扫全表」聚合腿
// （252 生产只读实测，2026-10-27）：
//
//	L2  SELECT AVG(ss.health_score) FROM session_summaries ss
//	     WHERE ss.health_score IS NOT NULL
//	     ⇒ Parallel Seq Scan，Rows Removed by Filter: 184,361，389.373 ms
//	L3  SELECT COUNT(*) FROM session_dim sd
//	     WHERE sd.status = 'closed' AND sd.closed_at >= CURRENT_DATE
//	     ⇒ Parallel Seq Scan，Rows Removed by Filter: 301,024，195.432 ms
//
// 加索引后（事务内建索引 + EXPLAIN ANALYZE + ROLLBACK，生产零持久变更）：
//	L2 → Parallel Index Only Scan，119.706 ms；L3 → Index Only Scan，0.100 ms。
//
// 部分索引类迁移最容易出的错有五种，本门各钉一条：
//
//	① **谓词钉死**：部分索引的谓词必须与查询的谓词**逐字一致**。
//	   ★ 谓词写成 `health_score IS NULL`（抄了既有索引
//	   idx_session_summaries_health_pending 的那一侧）⇒ 索引在位、规划器
//	   用得上、但**覆盖的是相反的行集** ⇒ L2 仍要全表扫 ⇒ 端点不变慢，
//	   而**没有任何报错**。
//	   ★ 谓词漏掉 `status = 'closed'`（退化成全列索引）⇒ 体积暴涨、
//	   但正确性不受影响 ⇒ 所以还要钉住「**索引列**也必须是查询真正过滤的那列」
//	   （C1 必须落在 closed_at 上，不能落在 updated_at 上）。
//	② **语义耦合**：Go 侧 SQL 改了（谓词或过滤列），索引没跟着改 ⇒ 索引
//	   静默失效。⇒ 本门用 **go/ast** 抽出 `admin/session_health_api.go` 里
//	   的 SQL **字面量**（不剥注释也能拿到真实字面量 —— 这是为什么用 AST
//	   而不是正则：Go 源里到处是解释这两种谓词的注释，正则会命中注释）。
//	③ **只断言存在抓不到多一条**：必须**集合精确比对** —— up 体内
//	   `CREATE INDEX` 的条数与名字必须**恰好**是那两条，多一条（追加型变异）
//	   同样判红。
//	④ **幂等**：重跑必须跳过而不是报错（IF NOT EXISTS）。
//	⑤ **投递面（7 点）**：只登记在 canonical 目录而没登记在 installer /
//	   升级序列 / 清单镜像 / 台账里 ⇒ 生产库会静默跳过 ⇒「迁移写了但没生效」。
//
// # 判据纪律（本门自身）
//
// 每条**负向**断言都配一条**阳性对照**：先证明检测器能认得出「不合格」
// （喂一份合成样本，断言它被判红），再证明真文件被判绿。
// 合成样本只在本门内部构造，不碰仓库里的真实迁移文件。

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	mig904Name     = "904_inspector_stats_partial_indexes.sql"
	mig904DownName = "904_inspector_stats_partial_indexes.down.sql"

	// C1：L3（今日关闭数）—— 列 closed_at，谓词 status = 'closed'。
	mig904C1Index = "idx_session_dim_closed_at_closed"
	mig904C1Table = "public.session_dim"
	mig904C1Col   = "closed_at"
	mig904C1Pred  = "status = 'closed'"

	// C2：L2（平均健康分）—— 列 health_score，谓词 health_score IS NOT NULL。
	mig904C2Index = "idx_session_summaries_health_score_notnull"
	mig904C2Table = "public.session_summaries"
	mig904C2Col   = "health_score"
	mig904C2Pred  = "health_score IS NOT NULL"

	mig904UpSHA256   = "d2cd82dccf0ff6060ba11c3f593484e445e46ac44cba4b057edf12d45b25af20"
	mig904DownSHA256 = "3328f8c7368769c42641f7874dd38ba4e04138891abad3b8781abdc2ed2f5532"
)

func mig904Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig904Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// mig904StripComments 去行注释与块注释。
//
// ★ 必须剥：904 的文件头里有整段论证文字，其中就写着
// `WHERE status = 'closed'` 与 `WHERE health_score IS NOT NULL`
// ⇒ 不剥注释的话，「文件里出现过这个谓词」这条断言会被注释里的字面量
// 满足 ⇒ 恒绿门。
func mig904StripComments(s string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	s = block.ReplaceAllString(s, " ")
	line := regexp.MustCompile(`--[^\n]*`)
	return line.ReplaceAllString(s, " ")
}

// mig904NormalizePred 归一化谓词文本：去表别名前缀 + 折叠空白 + 小写。
//
// ★ 去别名是必需的：迁移里写 `status = 'closed'`（裸列），
//   Go 侧查询里写 `sd.status = 'closed'`（带别名）⇒ 归一化后必须相等。
func mig904NormalizePred(s string) string {
	for _, alias := range []string{"sd.", "ss.", "th.", "tp.", "ft.", "rl."} {
		s = strings.ReplaceAll(s, alias, "")
	}
	s = strings.ToLower(s)
	return strings.Join(strings.Fields(s), " ")
}

// mig904CheckUpBody 校验 up 体形态，返回问题描述列表（空 = 合格）。
// ★ 抽成纯函数 ⇒ 阳性对照喂坏样本走同一把尺子。
func mig904CheckUpBody(clean string) []string {
	var problems []string

	// ① 两条索引各自的「名字 + 表 + 列 + 谓词」逐字钉死。
	type want struct {
		index string
		table string
		col   string
		pred  string
	}
	for _, w := range []want{
		{mig904C1Index, mig904C1Table, mig904C1Col, mig904C1Pred},
		{mig904C2Index, mig904C2Table, mig904C2Col, mig904C2Pred},
	} {
		idxRE := regexp.MustCompile(`(?is)CREATE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+` +
			regexp.QuoteMeta(w.index) + `\s+ON\s+` + regexp.QuoteMeta(w.table) +
			`\s*\(\s*` + regexp.QuoteMeta(w.col) + `\s*\)\s*WHERE\s+` +
			regexp.QuoteMeta(w.pred))
		if !idxRE.MatchString(clean) {
			problems = append(problems, "缺索引定义（CREATE INDEX IF NOT EXISTS "+w.index+
				" ON "+w.table+" ("+w.col+") WHERE "+w.pred+"）—— 谓词必须与查询逐字一致，"+
				"写反了索引会覆盖相反的行集，端点不会变慢且不报任何错")
		}
	}

	// ② 幂等：不得出现不带 IF NOT EXISTS 的同名创建。
	for _, name := range []string{mig904C1Index, mig904C2Index} {
		nonIdemRE := regexp.MustCompile(`(?is)CREATE\s+INDEX\s+` + regexp.QuoteMeta(name))
		if nonIdemRE.MatchString(clean) {
			problems = append(problems, "存在不带 IF NOT EXISTS 的同名 CREATE INDEX（"+name+
				"）—— 重跑会直接报错")
		}
	}

	// ③ 不得出现 CONCURRENTLY（startup 迁移默认在事务里跑）。
	if regexp.MustCompile(`(?is)CREATE\s+INDEX\s+CONCURRENTLY`).MatchString(clean) {
		problems = append(problems, "含 CREATE INDEX CONCURRENTLY —— startup 迁移默认在事务里跑，"+
			"CONCURRENTLY 不能在事务块内执行（且必须同步改单事务通道的豁免表）")
	}

	// ④ 事务边界：必须有 BEGIN/COMMIT。
	if !regexp.MustCompile(`(?is)\bBEGIN\s*;`).MatchString(clean) {
		problems = append(problems, "缺 BEGIN —— 索引创建与锁超时的设置应在一个显式事务里")
	}
	if !regexp.MustCompile(`(?is)\bCOMMIT\s*;`).MatchString(clean) {
		problems = append(problems, "缺 COMMIT")
	}

	// ⑤ ★ 集合精确比对：up 体内的 CREATE INDEX **条数**必须恰好是 2。
	//
	// ★ 只断言「那两条存在」抓不到**多出来的一条** ⇒ 追加型变异
	//   （往 up 体里塞第三条索引）会绿灯 ⇒ 这是 LESSONS §346 记的
	//   同一个坑：断言存在 ≠ 断言集合相等。
	allCreate := regexp.MustCompile(`(?is)CREATE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?` +
		regexp.QuoteMeta(mig904C1Index) + `|` +
		`CREATE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?` + regexp.QuoteMeta(mig904C2Index) +
		`|` +
		`CREATE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?[A-Za-z_][A-Za-z0-9_]*\s+ON\s+public\.`)
	if n := len(allCreate.FindAllString(clean, -1)); n != 2 {
		problems = append(problems, "up 体里的 CREATE INDEX 条数 = "+strconv.Itoa(n)+
			" ≠ 2 —— 必须恰好是本迁移的两条；多出来的（追加型变异）同样是缺陷")
	}

	// ⑥ 不得改动查询：本迁移是「只加索引」的迁移，出现 UPDATE/DELETE/
	//    TRUNCATE/ALTER TABLE 一律判红（那属于口径变更，必须另立迁移）。
	for _, kw := range []string{`(?is)\bUPDATE\b`, `(?is)\bDELETE\b`, `(?is)\bTRUNCATE\b`, `(?is)\bALTER\s+TABLE\b`} {
		if regexp.MustCompile(kw).MatchString(clean) {
			problems = append(problems, "up 体出现了写/改表语句（"+kw+"）—— 本迁移只允许建索引，"+
				"任何数据或结构变更都属于口径变更，必须另立迁移")
		}
	}

	return problems
}

// ── ① up 体形态 ────────────────────────────────────────────────────────────

func TestMigration904_UpBodyShape(t *testing.T) {
	up := mig904Read(t, mig904Tree(t), filepath.Join("sql", "migrations", "startup", mig904Name))
	if problems := mig904CheckUpBody(mig904StripComments(up)); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("904 up 体：%s", p)
		}
	}
}

// TestMigration904_DetectorRejectsBadShapes 阳性对照：五份坏样本都必须被判红。
//
// ★ 五份坏样本对应五种「索引在位但不起作用」的形态 —— 这正是本门要防的。
// ★ 第 ⑤ 份是**追加型**变异：往合格文本里**多塞一条索引**。
//   只断言「那两条存在」的门会放它过去 ⇒ 这一条专治那个洞。
func TestMigration904_DetectorRejectsBadShapes(t *testing.T) {
	good := mig904StripComments(mig904Read(t, mig904Tree(t), filepath.Join("sql", "migrations", "startup", mig904Name)))
	if len(mig904CheckUpBody(good)) != 0 {
		t.Fatal("阳性对照失败：真文件被判为不合格 —— 检测器坏了")
	}

	// 坏样本①：C1 谓词被摘成全列索引（丢掉 status = 'closed'）。
	//
	// ★ 用正则而不是字面量替换：第一版写的是 `strings.Replace(good,
	//   ") WHERE status = 'closed'", ")", 1)` ⇒ 而真文件里 `)` 与 `WHERE`
	//   之间**是换行** ⇒ 替换没发生 ⇒ 坏样本等于合格样本 ⇒ 门自曝
	//   「没有鉴别力」（这正是阳性对照的作用：它把量具的洞当场逼出来）。
	bad1 := regexp.MustCompile(`(?is)\s*WHERE\s+status\s*=\s*'closed'`).ReplaceAllString(good, "")
	if len(mig904CheckUpBody(bad1)) == 0 {
		t.Error("坏样本①（C1 谓词缺失）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本②：C1 的索引列被换成 updated_at（查询并不按它过滤）。
	bad2 := strings.Replace(good,
		"ON public.session_dim (closed_at)", "ON public.session_dim (updated_at)", 1)
	if len(mig904CheckUpBody(bad2)) == 0 {
		t.Error("坏样本②（C1 索引列换成 updated_at）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本③：C2 谓词抄成相反的一侧（IS NULL）⇒ 覆盖的是互补行集。
	bad3 := regexp.MustCompile(`(?is)WHERE\s+health_score\s+IS\s+NOT\s+NULL`).
		ReplaceAllString(good, "WHERE health_score IS NULL")
	if len(mig904CheckUpBody(bad3)) == 0 {
		t.Error("坏样本③（C2 谓词写成 IS NULL）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本④：幂等守卫被摘。
	bad4 := strings.Replace(good, "CREATE INDEX IF NOT EXISTS "+mig904C2Index,
		"CREATE INDEX "+mig904C2Index, 1)
	if len(mig904CheckUpBody(bad4)) == 0 {
		t.Error("坏样本④（幂等守卫缺失）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本⑤：换成 CONCURRENTLY。
	bad5 := strings.Replace(good, "CREATE INDEX IF NOT EXISTS "+mig904C1Index,
		"CREATE INDEX CONCURRENTLY "+mig904C1Index, 1)
	if len(mig904CheckUpBody(bad5)) == 0 {
		t.Error("坏样本⑤（CONCURRENTLY）被判合格 —— 检测器没有鉴别力")
	}

	// ★ 坏样本⑥（追加型）：在合格文本末尾**多塞一条**索引。
	//   「只断言那两条存在」的门会放它过去 ⇒ 必须判红。
	bad6 := good + "\nCREATE INDEX IF NOT EXISTS idx_session_dim_extra_dummy " +
		"ON public.session_dim (tenant_id);\n"
	if len(mig904CheckUpBody(bad6)) == 0 {
		t.Error("坏样本⑥（追加型：多一条索引）被判合格 —— 检测器没有鉴别力")
	}
}

// ── ② lock_timeout / statement_timeout（仓库规矩） ─────────────────────────

func TestMigration904_CarriesLockAndStatementTimeout(t *testing.T) {
	up := mig904Read(t, mig904Tree(t), filepath.Join("sql", "migrations", "startup", mig904Name))
	clean := strings.ToLower(mig904StripComments(up))

	if !strings.Contains(clean, "lock_timeout") {
		t.Error("904 的 up 体没有 lock_timeout —— 编号 >870 的启动迁移必须自带（startup_ddl_lock_guard_gate 规矩）")
	}
	lt := regexp.MustCompile(`(?i)lock_timeout\s*=\s*'(\d+)s'`)
	m := lt.FindStringSubmatch(up)
	if m == nil {
		t.Error("904 的 lock_timeout 不是 '<秒>s' 形态")
	} else if m[1] == "0" {
		t.Error("904 的 lock_timeout = 0s ⇒ 无限等待，与「快速失败」的语义相反")
	}

	// statement_timeout：共享 PG 服务端 statement_timeout 30s 会掐断索引
	// 构建 ⇒ 迁移永不记账 ⇒ 永久卡点（891/901/903 同款理由）。
	if !strings.Contains(clean, "statement_timeout") {
		t.Error("904 的 up 体没有 statement_timeout —— 长索引构建会被共享 PG 的服务端超时掐断后永不记账")
	}
	st := regexp.MustCompile(`(?i)statement_timeout\s*=\s*'(\d+)min'`)
	sm := st.FindStringSubmatch(up)
	if sm == nil {
		t.Error("904 的 statement_timeout 不是 '<分钟>min' 形态")
	} else if sm[1] == "0" {
		t.Error("904 的 statement_timeout = 0min ⇒ 无限等待")
	}
}

// ── ③ down 把两条索引都删回去 ──────────────────────────────────────────────

func TestMigration904_DownDropsBothIndexes(t *testing.T) {
	down := mig904StripComments(mig904Read(t, mig904Tree(t), filepath.Join("sql", "migrations", "startup", mig904DownName)))
	for _, name := range []string{mig904C1Index, mig904C2Index} {
		// ★ 形态要同时接受带/不带 schema 前缀的两种写法。
		dropRE := regexp.MustCompile(`(?is)DROP\s+INDEX\s+IF\s+EXISTS\s+(?:public\.)?` +
			regexp.QuoteMeta(name))
		if !dropRE.MatchString(down) {
			t.Error("904 down 未删 " + name + " —— 回滚后索引还在，慢查询形态不会回来")
		}
	}
}

// ── ④ 两份副本逐字节一致 + 台账 sha 一致 ──────────────────────────────────

func TestMigration904_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	root := mig904Tree(t)
	for _, name := range []string{mig904Name, mig904DownName} {
		canonical := mig904Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embedded := mig904Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != embedded {
			t.Errorf("%s 与 installer embeddata 副本不一致 —— 只改一份会让装出来的库与仓库的库分叉", name)
		}
		sum := sha256.Sum256([]byte(canonical))
		got := hex.EncodeToString(sum[:])
		want := mig904UpSHA256
		if name == mig904DownName {
			want = mig904DownSHA256
		}
		if got != want {
			t.Errorf("%s 的 sha256 = %s 与台账登记的 %s 不符 —— 文件改动后必须同步 db-changelog", name, got, want)
		}
	}
}

// ── ⑤ 登记面（7 点） ──────────────────────────────────────────────────────

func TestMigration904_RegisteredAtAllDeliveryPoints(t *testing.T) {
	root := mig904Tree(t)

	runner := mig904Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"`+mig904Name+`"`) {
		t.Error("runner.go StartupFiles 未登记 904 —— 新装链会跳过它")
	}

	main := mig904Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig904Name) {
		t.Error("main.go 缺 904 的 go:embed —— embeddata 副本成死文件")
	}
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig904DownName) {
		t.Error("main.go 缺 904 down 的 go:embed")
	}
	if !strings.Contains(main, `"startup/`+mig904Name+`"`) {
		t.Error("main.go 的 embed map 未登记 904")
	}
	if !strings.Contains(main, `"startup/`+mig904DownName+`"`) {
		t.Error("main.go 的 embed map 未登记 904 down")
	}

	seq := mig904Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, "sql/migrations/startup/"+mig904Name) {
		t.Error("生产升级序列 apply-db-revision-sequence.sh 未登记 904 —— 升级库会被静默跳过（投递通道缺口）")
	}

	tsv := mig904Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, mig904Name) {
		t.Error("installed_startup_migrations.tsv 镜像未登记 904")
	}

	changelog := mig904Read(t, root, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, "| 904 | `"+mig904Name+"`") {
		t.Error("docs/db-changelog.md 未登记 904")
	}
	// ★ 必须拿**文件实测**的 sha 去比对台账，而不是只查「台账里含某个 sha 串」。
	upBytes := mig904Read(t, root, filepath.Join("sql", "migrations", "startup", mig904Name))
	downBytes := mig904Read(t, root, filepath.Join("sql", "migrations", "startup", mig904DownName))
	upSum := sha256.Sum256([]byte(upBytes))
	downSum := sha256.Sum256([]byte(downBytes))
	if got := hex.EncodeToString(upSum[:]); !strings.Contains(changelog, got) {
		t.Errorf("docs/db-changelog.md 里 904 的 sha256 与文件实测不符（实测 %s）—— 台账与文件已分叉", got)
	}
	if got := hex.EncodeToString(downSum[:]); !strings.Contains(changelog, got) {
		t.Errorf("docs/db-changelog.md 里 904 down 的 sha256 与文件实测不符（实测 %s）—— 台账与文件已分叉", got)
	}
}

// ── ⑥ ★ 语义耦合门：索引谓词必须与 Go 侧 SQL 的谓词逐字一致 ───────────────
//
// ★ 这是本门**最有价值**的一条：它把「数据库里的索引形态」与「Go 侧查询的
//   谓词」绑在一起。
//
// 事故形态（正是它要防的）：有人改了 admin/session_health_api.go 的
// 谓词（比如把 `status = 'closed'` 改成 `status <> 'closed'`），**没有同步改
// 索引** ⇒ 索引仍在位、迁移仍在台账里、端点却悄悄回到 2.7s ⇒ 而**没有任何
// 门会红**。
//
// ★ 为什么用 go/ast 而不是正则：Go 源里到处是**解释**这两个谓词的注释
//   （`// status = 'closed' AND closed_at >= CURRENT_DATE` 就在注释里），
//   正则会命中注释 ⇒ 门恒绿。AST 只取真实的字符串字面量。
func TestMigration904_IndexPredicatesMatchGoQueries(t *testing.T) {
	root := mig904Tree(t)

	// 抽 Go 侧的真实 SQL 字面量。
	literals := mig904GoStringLiterals(t, filepath.Join(root, "admin", "session_health_api.go"))

	// L3 的 SQL 字面量：含 session_dim + closed_at。
	var l3 string
	for _, s := range literals {
		if strings.Contains(s, "session_dim sd") && strings.Contains(s, "closed_at") {
			l3 = s
			break
		}
	}
	if l3 == "" {
		t.Fatal("在 admin/session_health_api.go 的字符串字面量里找不到 L3 的 SQL " +
			"（含 session_dim sd 且含 closed_at）—— 若查询已改写，本门前提失效，请人工复核索引形态")
	}

	// L2 的 SQL 字面量：含 AVG(ss.health_score)。
	var l2 string
	for _, s := range literals {
		if strings.Contains(s, "AVG(ss.health_score)") {
			l2 = s
			break
		}
	}
	if l2 == "" {
		t.Fatal("在 admin/session_health_api.go 的字符串字面量里找不到 L2 的 SQL " +
			"（含 AVG(ss.health_score)）—— 若查询已改写，本门前提失效，请人工复核索引形态")
	}

	// C1：查询谓词里的 status 等值 + 日期列必须与索引的列/谓词对上。
	//
	// ★ 只取**谓词本身**（捕获组），不带 `WHERE ` 前缀 —— 否则两侧粒度
	//   不一致（迁移侧没有 WHERE，查询侧有）⇒ 归一化后仍然不等 ⇒
	//   门会对着**正确**的实现报红（量具坏了，不是被测坏了）。
	qC1 := regexp.MustCompile(`(?is)WHERE\s+(sd\.status\s*=\s*'closed')`).FindStringSubmatch(l3)
	if qC1 == nil {
		t.Fatal("L3 的 SQL 里找不到 `WHERE sd.status = 'closed'` —— 索引谓词已与查询脱节")
	}
	if !strings.Contains(l3, "sd.closed_at") {
		t.Error("L3 的 SQL 里没有 sd.closed_at —— C1 索引的列（closed_at）已不是查询真正过滤的列")
	}

	// C2：查询谓词里的 health_score IS NOT NULL 必须与索引谓词对上。
	qC2 := regexp.MustCompile(`(?is)WHERE\s+(ss\.health_score\s+IS\s+NOT\s+NULL)`).FindStringSubmatch(l2)
	if qC2 == nil {
		t.Fatal("L2 的 SQL 里找不到 `WHERE ss.health_score IS NOT NULL` —— 索引谓词已与查询脱节")
	}

	// 归一化后，Go 侧谓词必须与迁移里的索引谓词**逐字相等**。
	up := mig904StripComments(mig904Read(t, root, filepath.Join("sql", "migrations", "startup", mig904Name)))

	mC1 := regexp.MustCompile(`(?is)CREATE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+` +
		regexp.QuoteMeta(mig904C1Index) + `\s+ON\s+` + regexp.QuoteMeta(mig904C1Table) +
		`\s*\(\s*` + regexp.QuoteMeta(mig904C1Col) + `\s*\)\s*WHERE\s+(.+?);`).FindStringSubmatch(up)
	if mC1 == nil {
		t.Fatal("904 up 体里解析不出 C1 的索引谓词")
	}
	if got, want := mig904NormalizePred(mC1[1]), mig904NormalizePred(qC1[1]); got != want {
		t.Errorf("C1 的索引谓词（%q）与 Go 侧 L3 谓词（%q）不一致 —— 索引会覆盖不同的行集，"+
			"端点会无声地退回全表扫", got, want)
	}

	mC2 := regexp.MustCompile(`(?is)CREATE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+` +
		regexp.QuoteMeta(mig904C2Index) + `\s+ON\s+` + regexp.QuoteMeta(mig904C2Table) +
		`\s*\(\s*` + regexp.QuoteMeta(mig904C2Col) + `\s*\)\s*WHERE\s+(.+?);`).FindStringSubmatch(up)
	if mC2 == nil {
		t.Fatal("904 up 体里解析不出 C2 的索引谓词")
	}
	if got, want := mig904NormalizePred(mC2[1]), mig904NormalizePred(qC2[1]); got != want {
		t.Errorf("C2 的索引谓词（%q）与 Go 侧 L2 谓词（%q）不一致 —— 索引会覆盖不同的行集，"+
			"端点会无声地退回全表扫", got, want)
	}
}

// mig904GoStringLiterals 用 go/ast 抽出 Go 文件里**全部**字符串字面量。
//
// ★ 刻意不用正则扫源码：注释里也会出现 SQL 片段 ⇒ 正则会把注释当代码。
func mig904GoStringLiterals(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		out = append(out, v)
		return true
	})
	return out
}

// ── ⑦ 不得出现在单事务通道的豁免表里 ─────────────────────────────────────
//
// ★ 904 不含 CONCURRENTLY ⇒ 它跑在 installer's --single-transaction 通道里
//   ⇒ **不需要**在 startup_concurrency_gate_test.go 的豁免表登记。
//   若有人后来给它加了 CONCURRENTLY 却忘了改豁免表 ⇒ 全新安装会死在
//   「CREATE INDEX CONCURRENTLY cannot run inside a transaction block」。
func TestMigration904_NotInConcurrencyExceptionTable(t *testing.T) {
	root := mig904Tree(t)

	// ★ 判定必须**剥掉注释**：904 的文件头散文里就写着「仍不用 CONCURRENTLY」
	//   —— 不剥的话会把这句**说明**当成「文件里用了 CONCURRENTLY」。
	up := mig904StripComments(mig904Read(t, root, filepath.Join("sql", "migrations", "startup", mig904Name)))
	if strings.Contains(strings.ToUpper(up), "CONCURRENTLY") {
		t.Error("904 的可执行体出现了 CONCURRENTLY —— startup 迁移默认在事务里跑，" +
			"且必须同步改 startup_concurrency_gate_test.go 的豁免表")
	}

	conc := mig904Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "startup_concurrency_gate_test.go"))
	if strings.Contains(conc, `"`+mig904Name+`"`) {
		t.Error("904 不含 CONCURRENTLY，却登记在单事务通道的豁免表里 —— 豁免表只收真正需要非事务通道的文件")
	}
}
