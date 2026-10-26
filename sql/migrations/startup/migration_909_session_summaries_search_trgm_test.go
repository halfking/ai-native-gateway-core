package startup

// migration_909_session_summaries_search_trgm_test.go —— 909 的契约门（R156 会话搜索轮）。
//
// # 这道门守的是什么
//
// 909 给 public.session_summaries 的**三列**补 GIN trigram 索引，
// 修的是「admin 的会话搜索对全表做顺序扫描」这条**生产实测**的慢 SQL。
//
// ★ 缺陷本体（252 生产只读实测，2026-10-27 R154 轮）：
//
//	admin/session_catalog_usage.go 的 catalogSearchSQL 用 likeContains(q)
//	（★ 生成 `%q%`，★ 中缀匹配）对三列做 OR 的 ILIKE：
//	    (session_key ILIKE $1
//	     OR title             ILIKE $1
//	     OR primary_model     ILIKE $1)
//	★ 252 实测（868,711 行）：
//	  · EXPLAIN（不带 ANALYZE）⇒ Parallel Seq Scan on session_summaries
//	  · EXPLAIN ANALYZE ⇒ Execution Time = **40,415.968 ms（40.4 秒）**
//	    （Planning Time 仅 16.679 ms ⇒ ★ **不是规划问题，是执行问题**）
//	  · 对照「只查 session_key 一列」⇒ Index Only Scan using session_summaries_pkey
//	★ 而 mergeCatalogSearch 的 ctx 超时是 **2 秒**
//	⇒ ⇒ ★★★ 每次都超时 ⇒ ⇒ **静默降级成「没搜到」**
//	（★ 用户看到的是空结果，★ 不是报错 —— ★ 这正是最难查的那类缺陷）
//
// ⇒ 909 的修法 = 三列各一个 GIN trigram 索引 ⇒ 规划器可以走 BitmapOr。
//
// # ★★ 本门最有价值的一条：**索引列 ⟷ Go 侧 LIKE 列**的语义耦合
//
// ★★ 事故形态（正是它要防的，★ 而且**没有任何症状能暴露它**）：
//
//	有人把 Go 侧的谓词改回 `COALESCE(title, '') ILIKE $1`
//	（★ 口径**完全等价**！★ 只是多包了一层 NULL 保护）
//	⇒ ★ PG 的表达式索引匹配是**逐字**的 ⇒ 裸列索引**不参与**
//	⇒ ⇒ 三列 OR 退回 Parallel Seq Scan ⇒ **40.4 秒**
//	⇒ ⇒ 而 SQL 照跑、结果照对、不报错、迁移还在台账里
//	⇒ ⇒ ★★ **唯一能暴露它的就是这道耦合门**。
//
// ★ 反向也要拦：Go 侧加了第 4 个 LIKE 列却忘了加索引 ⇒ 那一支不可索引
//	⇒ ⇒ 整条 OR 又退回 Seq Scan ⇒ 同样是「结果照对、只是慢 40 倍」。
//
// ⇒ ★★ 所以本门断言的是**集合相等**，不是「包含」：
//
//	★ 迁移的索引列集合 == Go 侧 catalogSearchSQL 的 LIKE 列集合（不多不少）
//	★ 且 Go 侧的每个 LIKE 表达式都是**裸列**（不得被 COALESCE 包裹）
//
// # 索引类迁移最容易出的错，本门各钉一条
//
//	① **恰好三条**：多一条 = 追加型变异；少一条 = 漏一列 ⇒ 那一列不可索引
//	   ⇒ 整条 OR 退回 Seq Scan。
//	② **必须是 CONCURRENTLY**：session_summaries 是**热写表** ⇒ 普通 CREATE INDEX
//	   在整个构建期持 ShareLock 阻断写入 ⇒ 必须 CONCURRENTLY
//	   ⇒ ★ 且因为 CONCURRENTLY 不能进事务块 ⇒ 必须带 `dbinit:no-transaction` 标记。
//	③ **必须幂等**：IF NOT EXISTS（重跑跳过，不报错）。
//	④ **必须带会话级 lock_timeout / statement_timeout**：共享 PG 服务端
//	   statement_timeout=30s ⇒ GIN trigram 构建必然击穿 ⇒ 57014 之后迁移永不记账、
//	   成为永久卡点 ⇒ ★★ 这是 877/822/908 共同的失败形态。
//	⑤ **必须带形状校验**：CONCURRENTLY 被 ctx 掐断会留下 indisvalid=false 的半成品
//	   （对象在、名字在、planner 不用）⇒ 必须当场报错让迁移不记账。
//	⑥ **投递面（7 点）**：只登记在 canonical 目录 ⇒ 生产库会静默跳过
//	   ⇒「迁移写了但没生效」。
//	⑦ **embeddata 副本必须逐字节一致**：安装器真正执行的是 embeddata 那份。
//	⑧ **台账 sha256 必须是文件实测值**：台账与文件分叉 ⇒ 复核者读到的是假的。

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	mig909Name     = "909_session_summaries_search_trgm_indexes.sql"
	mig909DownName = "909_session_summaries_search_trgm_indexes.down.sql"

	// ★ 三条索引：名字 → 列。**顺序即迁移里的顺序**（主键 → title → model）。
	mig909IndexSessionKey   = "idx_session_summaries_session_key_trgm"
	mig909IndexTitle        = "idx_session_summaries_title_trgm"
	mig909IndexPrimaryModel = "idx_session_summaries_primary_model_trgm"

	mig909Table = "public.session_summaries"

	// ★ Go 侧被本门耦合的文件与常量。
	mig909GoFile  = "admin/session_catalog_usage.go"
	mig909GoConst = "catalogSearchSQL"

	// ★ sha256 冻结（★ 与 docs/db-changelog.md 的台账必须一致）。
	mig909UpSHA256   = "77d829d0289cb3e0b16dfa93b16c70addcbf84d265f58b3e4996680ac4cb8e6b"
	mig909DownSHA256 = "608e7bfe8ea5a638d043b7b0be2f2e9697f1eb50605b3c9da01190ce9697a5cb"
)

// mig909IndexSpec 是「索引名 → 列」的一条规格。
type mig909IndexSpec struct {
	name string
	col  string
}

// mig909ExpectedIndexes 是本迁移必须**恰好**建出的三条索引。
var mig909ExpectedIndexes = []mig909IndexSpec{
	{mig909IndexSessionKey, "session_key"},
	{mig909IndexTitle, "title"},
	{mig909IndexPrimaryModel, "primary_model"},
}

func mig909Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig909Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读 %s: %v", rel, err)
	}
	return string(b)
}

// mig909StripComments 剥掉 SQL 行注释（-- 到行尾）与 /* */ 块注释。
//
// ★ 必须剥：迁移头部的说明里就写着「session_key ILIKE $1」等字面量，
//   不剥的话「列集合」类断言会命中注释 ⇒ 门恒绿。
func mig909StripComments(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	joined := strings.Join(out, "\n")
	for {
		i := strings.Index(joined, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(joined[i:], "*/")
		if j < 0 {
			joined = joined[:i]
			break
		}
		joined = joined[:i] + joined[i+j+2:]
	}
	return joined
}

// mig909ExtractIndexes 从**已剥注释**的 up 文件体里抽出索引规格。
func mig909ExtractIndexes(clean string) []mig909IndexSpec {
	re := regexp.MustCompile(`(?is)CREATE\s+INDEX\s+(CONCURRENTLY\s+)?IF\s+NOT\s+EXISTS\s+(\w+)\s+ON\s+public\.session_summaries\s+USING\s+gin\s*\(\s*([a-z_][a-z0-9_]*)\s+gin_trgm_ops\s*\)`)
	var out []mig909IndexSpec
	for _, m := range re.FindAllStringSubmatch(clean, -1) {
		out = append(out, mig909IndexSpec{name: m[2], col: m[3]})
	}
	return out
}

// ── ① up 文件体形状 ────────────────────────────────────────────────────────
func TestMigration909_UpBodyShape(t *testing.T) {
	root := mig909Tree(t)
	up := mig909Read(t, root, filepath.Join("sql", "migrations", "startup", mig909Name))
	clean := mig909StripComments(up)

	// ①-a 恰好三条索引。
	got := mig909ExtractIndexes(clean)
	if len(got) != len(mig909ExpectedIndexes) {
		t.Fatalf("★ 909 抽到 %d 条 trigram 索引（必须恰好 %d）：%v\n"+
			"    ★ 多一条 = 追加型变异；★ 少一条 = 漏一列 ⇒ ★ **那一列不可索引** ⇒ ★ 整条三列 OR 退回 Seq Scan",
			len(got), len(mig909ExpectedIndexes), got)
	}
	for i, want := range mig909ExpectedIndexes {
		if got[i].name != want.name || got[i].col != want.col {
			t.Errorf("★ 第 %d 条索引是 (%s, %s)，必须是 (%s, %s)",
				i+1, got[i].name, got[i].col, want.name, want.col)
		}
	}

	// ①-b 恰好三条「CREATE INDEX 语句」（防「另开一条不同形态的索引」漏检）。
	//
	// ★★ 为什么**不能**用 strings.Count（★ 这是一条真缺陷，★ 本门自己踩过）：
	//   ★ 第 2 段的 RAISE EXCEPTION **消息文本**里写着 "CREATE INDEX CONCURRENTLY" ⇒
	//   ★ 全文字符串计数 = 4 ⇒ **假红**（★ 语句其实只有 3 条）。
	//   ⇒ ★★ 判据改成【锚定语句起始行】：★ 只有**行首**的 CREATE INDEX 才算一条语句
	//   ⇒ ★ 字符串字面量里的同名字样不再被计入 ⇒ ★ 而真多出来的一条索引语句
	//   （必然独占一行）照样被抓住。
	createIdxStmt := regexp.MustCompile(`(?im)^[ \t]*CREATE[ \t]+INDEX\b`)
	if n := len(createIdxStmt.FindAllString(clean, -1)); n != 3 {
		t.Errorf("★ up 文件体里 CREATE INDEX **语句**有 %d 条（必须恰好 3）—— ★ 追加型变异", n)
	}

	// ①-c 必须 CONCURRENTLY（热写表 ⇒ 普通建索引会阻断写入）。
	concurrentlyStmt := regexp.MustCompile(`(?im)^[ \t]*CREATE[ \t]+INDEX[ \t]+CONCURRENTLY\b`)
	if n := len(concurrentlyStmt.FindAllString(clean, -1)); n != 3 {
		t.Errorf("★ 三条索引必须全部 CONCURRENTLY（实测 %d/3）—— ★ session_summaries 是热写表，★ 普通 CREATE INDEX 会在构建期持 ShareLock 阻断写入", n)
	}

	// ①-d 必须 IF NOT EXISTS（幂等）。
	if n := strings.Count(clean, "IF NOT EXISTS"); n != 3 {
		t.Errorf("★ 三条索引必须全部 IF NOT EXISTS（实测 %d/3）—— ★ 重跑必须跳过而不是报错", n)
	}

	// ①-e 索引表达式必须是**裸列**（不得被 COALESCE 等函数包裹）。
	//   ★ 这是「索引 ⟷ 查询」逐字对齐的前提：若索引建成 COALESCE(title,'')
	//   而查询写成 title ILIKE（反之亦然）⇒ 索引不参与 ⇒ 退回 40.4 秒的 Seq Scan。
	if strings.Contains(clean, "COALESCE(") && strings.Contains(clean, "gin_trgm_ops)") {
		for _, line := range strings.Split(clean, "\n") {
			if strings.Contains(line, "gin_trgm_ops") && strings.Contains(strings.ToUpper(line), "COALESCE") {
				t.Errorf("★ 索引表达式里出现了 COALESCE：%q\n"+
					"    ★ 索引表达式必须与查询表达式**逐字**一致 ⇒ ★ 两边都必须是裸列", strings.TrimSpace(line))
			}
		}
	}

	// ①-f dbinit:no-transaction 标记（CONCURRENTLY 不能进事务块）。
	if !strings.Contains(up, "dbinit:no-transaction") {
		t.Error("★ 909 缺 dbinit:no-transaction 标记 —— ★ CONCURRENTLY 不能在事务块内执行 ⇒ ★ 全新安装会失败于 \"CREATE INDEX CONCURRENTLY cannot run inside a transaction block\"")
	}

	// ①-g 会话级超时与锁（★ 不用 SET LOCAL —— 非事务通道里它无意义）。
	for _, want := range []string{"SET statement_timeout", "SET lock_timeout", "RESET statement_timeout", "RESET lock_timeout"} {
		if !strings.Contains(clean, want) {
			t.Errorf("★ up 文件体里没有 %q —— ★ 共享 PG 服务端 statement_timeout=30s 会掐断 GIN 构建 ⇒ 57014 之后迁移永不记账", want)
		}
	}

	// ①-h 形状校验段（indisvalid=false 的半成品必须当场炸出来）。
	if !strings.Contains(clean, "indisvalid") {
		t.Error("★ 909 缺形状校验（indisvalid）—— ★ CONCURRENTLY 被 ctx 掐断会留下 indisvalid=false 的半成品索引（对象在、planner 不用）⇒ ★ 必须当场报错让迁移不记账")
	}

	// ①-i 表名逐字。
	if strings.Contains(clean, "session_summaries_hot") {
		t.Error("★ 909 不该碰 session_turns_hot / session_summaries_hot —— ★ 本迁移只针对 public.session_summaries")
	}
}

// ── ② down 必须逐条反向 ────────────────────────────────────────────────────
func TestMigration909_DownDropsAllThreeIndexes(t *testing.T) {
	root := mig909Tree(t)
	down := mig909Read(t, root, filepath.Join("sql", "migrations", "startup", mig909DownName))
	clean := mig909StripComments(down)

	if n := strings.Count(clean, "DROP INDEX"); n != 3 {
		t.Errorf("★ down 里 DROP INDEX 出现 %d 次（必须恰好 3）—— ★ 追加型变异", n)
	}
	if n := strings.Count(clean, "DROP INDEX CONCURRENTLY IF EXISTS"); n != 3 {
		t.Errorf("★ down 的三条 DROP 必须都是 CONCURRENTLY IF EXISTS（实测 %d/3）—— ★ 同正本：热写表上普通 DROP INDEX 会持 AccessExclusiveLock 阻断写入", n)
	}
	for _, want := range mig909ExpectedIndexes {
		if !strings.Contains(clean, "public."+want.name) {
			t.Errorf("★ down 里没有 public.%s —— ★ 回滚不完整", want.name)
		}
	}
	if !strings.Contains(down, "dbinit:no-transaction") {
		t.Error("★ down 缺 dbinit:no-transaction 标记 —— ★ DROP INDEX CONCURRENTLY 不能在事务块内执行")
	}
	// ★ down 不该删数据/列。
	for _, forbidden := range []string{"DROP TABLE", "DROP COLUMN", "TRUNCATE", "DELETE FROM"} {
		if strings.Contains(clean, forbidden) {
			t.Errorf("★ down 里出现了 %q —— ★ 回滚只允许删索引，★ 不得动数据", forbidden)
		}
	}
}

// ── ③ embeddata 副本必须逐字节一致 ─────────────────────────────────────────
func TestMigration909_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	root := mig909Tree(t)
	for _, name := range []string{mig909Name, mig909DownName} {
		canonical := mig909Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embedded := mig909Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != embedded {
			t.Errorf("★ embeddata 副本与正本不一致：%s\n"+
				"    ★ 安装器真正执行的是 embeddata 那份 ⇒ ★ 两者不一致 ⇒ ★ 复核者读正本、装的是另一份", name)
		}
	}
}

// ── ④ 投递面（7 点登记面）+ 台账 sha ───────────────────────────────────────
func TestMigration909_RegisteredAtAllDeliveryPoints(t *testing.T) {
	root := mig909Tree(t)

	runner := mig909Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"`+mig909Name+`"`) {
		t.Error("runner.go StartupFiles 未登记 909 —— 新装链会跳过它")
	}

	main := mig909Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig909Name) {
		t.Error("main.go 缺 909 的 go:embed —— embeddata 副本成死文件")
	}
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig909DownName) {
		t.Error("main.go 缺 909 down 的 go:embed")
	}
	if !strings.Contains(main, `"startup/`+mig909Name+`"`) {
		t.Error("main.go 的 embed map 未登记 909")
	}
	if !strings.Contains(main, `"startup/`+mig909DownName+`"`) {
		t.Error("main.go 的 embed map 未登记 909 down")
	}

	seq := mig909Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, "sql/migrations/startup/"+mig909Name) {
		t.Error("生产升级序列 apply-db-revision-sequence.sh 未登记 909 —— 升级库会被静默跳过")
	}

	tsv := mig909Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, mig909Name) {
		t.Error("installed_startup_migrations.tsv 镜像未登记 909")
	}

	changelog := mig909Read(t, root, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, "| 909 | `"+mig909Name+"`") {
		t.Error("docs/db-changelog.md 未登记 909")
	}
	upSum := sha256.Sum256([]byte(mig909Read(t, root, filepath.Join("sql", "migrations", "startup", mig909Name))))
	downSum := sha256.Sum256([]byte(mig909Read(t, root, filepath.Join("sql", "migrations", "startup", mig909DownName))))
	if got := hex.EncodeToString(upSum[:]); got != mig909UpSHA256 {
		t.Errorf("★ 本门冻结的 909 up sha256 与文件实测不符（实测 %s，冻结 %s）—— ★ 请同步更新冻结值与台账", got, mig909UpSHA256)
	}
	if got := hex.EncodeToString(downSum[:]); got != mig909DownSHA256 {
		t.Errorf("★ 本门冻结的 909 down sha256 与文件实测不符（实测 %s，冻结 %s）—— ★ 请同步更新冻结值与台账", got, mig909DownSHA256)
	}
	if got := hex.EncodeToString(upSum[:]); !strings.Contains(changelog, got) {
		t.Errorf("db-changelog.md 里 909 的 sha256 与文件实测不符（实测 %s）—— 台账与文件已分叉", got)
	}
	if got := hex.EncodeToString(downSum[:]); !strings.Contains(changelog, got) {
		t.Errorf("db-changelog.md 里 909 down 的 sha256 与文件实测不符（实测 %s）—— 台账与文件已分叉", got)
	}
}

// ── ★★ 语义耦合门：索引列 ⟷ Go 侧 LIKE 列（集合相等） ──────────────────────
//
// ★ 这是本门**最有价值**的一条：它把「数据库里的索引表达式」与「Go 侧查询的
//   过滤表达式」绑在一起，★ 且要求**集合相等**（不是「包含」）。
//
// ★ 为什么用 go/ast 而不是正则扫整个 Go 文件：Go 源里到处是**解释**这个谓词的
//   注释（注释里就写着 `title ILIKE $1`），正则会命中注释 ⇒ 门恒绿。
//   AST 只取真实的字符串字面量。
func TestMigration909_IndexColumnsMatchGoLikeColumns(t *testing.T) {
	root := mig909Tree(t)

	// ── Go 侧：取 catalogSearchSQL 的真实字面量。
	sqlText := mig909GoConstString(t, filepath.Join(root, filepath.FromSlash(mig909GoFile)), mig909GoConst)
	if sqlText == "" {
		t.Fatal("★ 读不到 Go 侧常量 catalogSearchSQL —— ★ 本门的比对基准没了（源被重构？）⇒ 门已失效")
	}
	// ★ 阳性对照：这条查询必须**确实**是 LIKE 查询。
	if !strings.Contains(strings.ToUpper(sqlText), "ILIKE") {
		t.Fatal("★ catalogSearchSQL 里找不到 ILIKE —— ★ 本门守的查询已消失 ⇒ ★ 门已失效（★ 不是「门绿了」）")
	}
	// ★ 阳性对照：★ COALESCE 包裹会让索引失配 ⇒ 必须在 Go 侧判红。
	if strings.Contains(sqlText, "COALESCE(title, '') ILIKE") ||
		strings.Contains(sqlText, "COALESCE(primary_model, '') ILIKE") {
		t.Errorf("★ catalogSearchSQL 的 LIKE 谓词仍被 COALESCE 包裹 —— ★★ PG 的表达式索引匹配是**逐字**的 ⇒ ★ 裸列 trigram 索引**不参与** ⇒ ★ 三列 OR 退回 Parallel Seq Scan（252 生产实测 40.4 秒）")
	}

	goCols := mig909LikeColumns(sqlText)
	if len(goCols) == 0 {
		t.Fatal("★ 没能从 catalogSearchSQL 解析出任何 LIKE 列 ⇒ 解析口径坏了 ⇒ 门已失效")
	}

	// ── 迁移侧：索引列。
	clean := mig909StripComments(mig909Read(t, root, filepath.Join("sql", "migrations", "startup", mig909Name)))
	idx := mig909ExtractIndexes(clean)
	idxCols := map[string]bool{}
	for _, s := range idx {
		idxCols[s.col] = true
	}

	// ── 集合相等（双向）。
	var missing, extra []string
	for c := range goCols {
		if !idxCols[c] {
			missing = append(missing, c)
		}
	}
	for c := range idxCols {
		if !goCols[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("★ Go 侧 catalogSearchSQL 对列 %v 做了 ILIKE，★ 但迁移 909 没有给它们建 trigram 索引 ⇒\n"+
			"    ★ 三列 OR 只要有一支不可索引 ⇒ ★ 规划器就放弃全部索引 ⇒ ★ 退回全表 Seq Scan（252 生产实测 40.4 秒）\n"+
			"    ★ 且症状是「结果照对、只是慢 40 倍」⇒ ★ **没有任何报错能暴露它**", missing)
	}
	if len(extra) > 0 {
		t.Errorf("★ 迁移 909 给列 %v 建了 trigram 索引，★ 但 Go 侧没有对它们做 ILIKE ⇒ ★ 索引白建（纯写放大）", extra)
	}
}

// mig909LikeColumns 从 catalogSearchSQL 里抽出被 ILIKE 的列名（裸列形态）。
//
// ★ 只认**裸列**形态 `col ILIKE` —— ★ 因为 COALESCE(col, '') ILIKE
//   与索引表达式**不匹配**，★ 本门不允许那种形态混进来。
func mig909LikeColumns(sqlText string) map[string]bool {
	out := map[string]bool{}
	// 去掉 SQL 行注释后再扫。
	clean := mig909StripComments(sqlText)
	re := regexp.MustCompile(`(?i)([a-z_][a-z0-9_]*)\s+ILIKE`)
	for _, m := range re.FindAllStringSubmatch(clean, -1) {
		out[m[1]] = true
	}
	return out
}

// mig909GoConstString 取某个 Go 文件里某个字符串常量的值；取不到返回 ""。
func mig909GoConstString(t *testing.T, path, constName string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	out := ""
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for _, id := range vs.Names {
			if id.Name != constName {
				continue
			}
			for _, val := range vs.Values {
				lit, ok := val.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				out = s
			}
		}
		return true
	})
	return out
}
