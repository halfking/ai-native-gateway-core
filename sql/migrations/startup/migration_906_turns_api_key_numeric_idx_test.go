package startup

// migration_906_turns_api_key_numeric_idx_test.go —— 906 的契约门（R94 部分索引轮）。
//
// # 这道门守的是什么
//
// 906 给 public.session_turns（分区父表）与 public.session_turns_hot 补两条
// **部分索引**（`WHERE api_key_id ~ '^[0-9]+$'`），配合
// db/request_logs_view_schema.go 里 `SessionFamilyLatestTurnPerSessionSQL`
// 把过滤条件从 **CASE 形式**改成**裸正则**形式，
// 修的是「会话列表的 api_key 回填查询把 **82,297 行堆全扫一遍、全部过滤掉、
// 产出 0 行**，却花了 1,350~1,569 ms」。
//
// ★ 事故形态（252 生产只读实测，2026-10-24）：
//
//	R93 上线后（build 2843）`?model=` 已从「恒 500@30s」变成 200，
//	但端点仍要 ~1.5 s ⇒ pg_stat_statements 窗口差分：6 次请求里
//	enrich 占 9,143 ms（均值 **1,523.8 ms**），而列表查询只 17.2 ms。
//	⇒ enrich 是端点的绝对主力。
//	⇒ 生产 EXPLAIN 实测：
//	     Index Scan using session_turns_2026_09_heap_tenant_id_session_id_turn_no_idx
//	       Filter: (CASE WHEN (api_key_id ~ '^[0-9]+$') THEN (api_key_id)::bigint END IS NOT NULL)
//	       Rows Removed by Filter: **82,297**
//	       Buffers: shared hit=66,606
//	⇒ ★★ 扫 82,297 行堆**全部被过滤掉、产出 0 行**。
//	⇒ 根因：探针会话（`sys:probe:*`）的 api_key_id **全是 NULL**
//	  （252 实测 462,394 行全空）⇒ 过滤条件**无法从索引判定** ⇒ 必须回表。
//
// ★ 修法效果（252 生产，事务内 CREATE INDEX + EXPLAIN ANALYZE + ROLLBACK ⇒ 零残留）：
//
//	CASE 形式过滤（无索引）              1,350.8 ms
//	裸正则过滤（无索引）                  1,569.3 ms
//	★★ 裸正则过滤 + 本迁移的部分索引      **4.57 ms**   ← 295×
//	（对照）INCLUDE(api_key_id) 普通索引   0.62 ms，但需 ANALYZE（实测 35.9 s）⇒ 出局
//
// ★★ 口径等价（252 生产真实**非空**批次，同一事务内同一快照，双向 EXCEPT ALL）：
//
//	old_rows=**20** / new_rows=**20** / only_new=**0** / only_old=**0**
//	⇒ 裸正则与 CASE 形式**逐行一致** ⇒ 只差「能不能用上索引」。
//
// # 索引类迁移最容易出的错，本门各钉一条
//
//	① **必须是部分索引**：谓词 `WHERE api_key_id ~ '^[0-9]+$'` 必须逐字在位。
//	   写成普通索引 ⇒ 索引里塞满 api_key_id 为 NULL 的行（探针流量占大头）
//	   ⇒ 索引巨大且规划器仍要回表 ⇒ 收益归零。
//	② **语义耦合（★ 本门最有价值的一条）**：迁移里的谓词必须与 Go 侧常量
//	   `db.SessionFamilyAPIKeyIDNumericPredExpr` **逐字一致**（去掉表别名 `t.` 后）。
//	   ★ PG 的谓词蕴含判定是**语法的** ⇒ 谓词写成 CASE 形式
//	     `WHERE (CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END) IS NOT NULL`
//	     ⇒ 规划器**认不出来** ⇒ 用不上部分索引 ⇒ 退回 1,350~1,569 ms，
//	     而**口径完全等价** ⇒ ★★ **没有任何症状能暴露它**（SQL 照跑、结果照对、
//	     只是慢 295×）⇒ 只有这条耦合门能抓住。
//	③ **只断言存在抓不到多一条**：必须**集合精确比对** —— up 体内
//	   `CREATE INDEX` 的条数与名字必须**恰好**是那两条（追加型变异同样判红）。
//	④ **锁与超时**：startup 迁移默认在事务里跑 ⇒ 不能 CONCURRENTLY；
//	   且必须带 lock_timeout（抢不到锁快速失败）与 statement_timeout
//	   （共享 PG 服务端 30s 会掐断长索引构建 ⇒ 57014 击穿后迁移永不记账）。
//	⑤ **幂等**：重跑必须跳过而不是报错（IF NOT EXISTS）。
//	⑥ **投递面（7 点）**：只登记在 canonical 目录而没登记在 installer /
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
	mig906Name     = "906_turns_api_key_numeric_partial_index.sql"
	mig906DownName = "906_turns_api_key_numeric_partial_index.down.sql"

	// C1：父表（分区父表 ⇒ 自动传播到子分区）。
	mig906C1Index = "idx_session_turns_api_key_numeric"
	mig906C1Table = "public.session_turns"

	// C2：hot 腿（同构）。
	mig906C2Index = "idx_session_turns_hot_api_key_numeric"
	mig906C2Table = "public.session_turns_hot"

	// ★ 部分索引的谓词（**不带表别名** —— 索引定义里没有别名）。
	//   必须与 Go 侧 db.SessionFamilyAPIKeyIDNumericPredExpr 去掉 `t.` 前缀后逐字一致。
	mig906Predicate = "api_key_id ~ '^[0-9]+$'"

	// ★ 列序：(session_id, turn_no DESC) ⇒ 供 GROUP BY session_id + max(turn_no)
	//   走 index-only（不用回表拿 turn_no）。
	mig906Columns = "(session_id, turn_no DESC)"

	mig906UpSHA256   = "25de81c12b8805f9474d711f4391dea6847e8df98b49eea37274711c3d46979f"
	mig906DownSHA256 = "3505fcbd1400b91c1968d2b4c37c02de16d8546849562bc686625bed9657cb54"
)

func mig906Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig906Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读 %s: %v", rel, err)
	}
	return string(b)
}

// mig906StripComments 剥掉 SQL 行注释（-- 到行尾）与 /* */ 块注释。
//
// ★ 必须剥：迁移头部的说明里就写着「api_key_id ~ '^[0-9]+$'」等字面量，
//   不剥的话「谓词」类断言会命中注释 ⇒ 门恒绿。
func mig906StripComments(s string) string {
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

// mig906CheckUpBody 校验 up 文件体（**已剥注释**），返回问题描述列表。
func mig906CheckUpBody(clean string) []string {
	var problems []string

	// ① 恰好两条 CREATE INDEX（多一条 = 追加型变异；少一条 = 漏一条腿）。
	createIdx := strings.Count(clean, "CREATE INDEX")
	if createIdx != 2 {
		problems = append(problems, "CREATE INDEX 出现 "+itoa906(createIdx)+" 次（必须恰好 2）")
	}
	stmts := strings.Split(clean, "CREATE INDEX")
	if len(stmts)-1 != 2 {
		problems = append(problems, "CREATE INDEX 语句数 = "+itoa906(len(stmts)-1)+"（必须恰好 2）")
	}
	for _, want := range []struct {
		index string
		table string
	}{
		{mig906C1Index, mig906C1Table},
		{mig906C2Index, mig906C2Table},
	} {
		found := false
		for _, st := range stmts[1:] {
			if !strings.Contains(st, want.index) {
				continue
			}
			found = true
			if !strings.Contains(st, "ON "+want.table+" ") {
				problems = append(problems, "索引 "+want.index+" 不在 "+want.table+" 上")
			}
			// ★ 列序必须是 (session_id, turn_no DESC)。
			if !strings.Contains(st, mig906Columns) {
				problems = append(problems, "索引 "+want.index+" 的列序不是 "+mig906Columns)
			}
			// ★★ 必须是**部分索引**且谓词逐字一致。
			//   ⇒ 写成普通索引 ⇒ 索引里塞满 api_key_id 为 NULL 的行（探针流量占大头）
			//   ⇒ 索引巨大且规划器仍要回表 ⇒ 收益归零。
			if !strings.Contains(st, "WHERE "+mig906Predicate) {
				problems = append(problems, "索引 "+want.index+" 缺部分索引谓词 `WHERE "+mig906Predicate+
					"` —— 写成普通索引会把 api_key_id 为 NULL 的行（探针流量占大头）全塞进去 ⇒ 收益归零")
			}
			break
		}
		if !found {
			problems = append(problems, "找不到索引 "+want.index)
		}
	}
	// ② 幂等：两条都必须是 IF NOT EXISTS。
	if strings.Count(clean, "IF NOT EXISTS") != 2 {
		problems = append(problems, "IF NOT EXISTS 出现 "+itoa906(strings.Count(clean, "IF NOT EXISTS"))+
			" 次（必须恰好 2 ⇒ 幂等）")
	}
	// ③ 不得用 CONCURRENTLY（startup 迁移默认在事务里跑 ⇒ 会失败）。
	if strings.Contains(strings.ToUpper(clean), "CONCURRENTLY") {
		problems = append(problems, "用了 CREATE INDEX CONCURRENTLY —— startup 迁移在事务里跑 ⇒ 会失败")
	}
	// ④ 不得用 INCLUDE(api_key_id) 形态（那是被实测淘汰的候选：需 ANALYZE 35.9 s
	//   且索引体积大得多）⇒ 若有人「顺手换形态」必须先改本门与审计档。
	if strings.Contains(strings.ToUpper(clean), "INCLUDE (") {
		problems = append(problems, "用了 INCLUDE(...) 形态 —— 实测那个候选需要 ANALYZE（35.9 s）" +
			"且索引体积大得多 ⇒ 已出局；若要换形态必须先改本门与审计档")
	}
	// ⑤ 锁与超时必须带。
	if !strings.Contains(clean, "lock_timeout") {
		problems = append(problems, "缺 lock_timeout（抢不到锁时应当快速失败而不是排队阻塞热写表）")
	}
	if !strings.Contains(clean, "statement_timeout") {
		problems = append(problems, "缺 statement_timeout（共享 PG 服务端 30s 会掐断长索引构建 ⇒ 迁移永不记账）")
	}
	return problems
}

func itoa906(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func TestMigration906_UpBodyShape(t *testing.T) {
	root := mig906Tree(t)
	clean := mig906StripComments(mig906Read(t, root, filepath.Join("sql", "migrations", "startup", mig906Name)))
	if problems := mig906CheckUpBody(clean); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("906 up 文件体：%s", p)
		}
	}
}

// TestMigration906_DetectorRejectsBadShapes 阳性对照：坏样本必须被判红。
func TestMigration906_DetectorRejectsBadShapes(t *testing.T) {
	root := mig906Tree(t)
	good := mig906StripComments(mig906Read(t, root, filepath.Join("sql", "migrations", "startup", mig906Name)))
	if problems := mig906CheckUpBody(good); len(problems) != 0 {
		t.Fatalf("阳性对照失败：真文件被判为不合格 —— 检测器坏了：%v", problems)
	}

	// 坏样本①：追加第三条索引（追加型变异）。
	bad1 := good + "\nCREATE INDEX IF NOT EXISTS idx_session_turns_extra ON public.session_turns (foo) WHERE x = 1;\n"
	if len(mig906CheckUpBody(bad1)) == 0 {
		t.Error("坏样本①（追加第三条索引）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本②：★ 去掉部分索引谓词（⇒ 退化成普通索引 ⇒ 收益归零）。
	bad2 := strings.ReplaceAll(good, "WHERE api_key_id ~ '^[0-9]+$'", "")
	if len(mig906CheckUpBody(bad2)) == 0 {
		t.Error("坏样本②（去掉部分索引谓词）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本③：★ 把谓词抄成 CASE 形式（★ 口径等价但规划器认不出 ⇒ 索引白建）。
	bad3 := strings.ReplaceAll(good, "WHERE api_key_id ~ '^[0-9]+$'",
		"WHERE (CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END) IS NOT NULL")
	if len(mig906CheckUpBody(bad3)) == 0 {
		t.Error("坏样本③（谓词写成 CASE 形式）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本④：去掉 lock_timeout。
	bad4 := strings.Replace(good, "SET LOCAL lock_timeout = '60s';", "", 1)
	if len(mig906CheckUpBody(bad4)) == 0 {
		t.Error("坏样本④（去掉 lock_timeout）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本⑤：改成 CONCURRENTLY。
	bad5 := strings.Replace(good, "CREATE INDEX IF NOT EXISTS", "CREATE INDEX CONCURRENTLY IF NOT EXISTS", 1)
	if len(mig906CheckUpBody(bad5)) == 0 {
		t.Error("坏样本⑤（用 CONCURRENTLY）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本⑥：★ 换成人选表别名形式（⇒ 索引定义里没有别名 ⇒ 跑不起来）。
	bad6 := strings.ReplaceAll(good, "WHERE api_key_id ~ '^[0-9]+$'", "WHERE t.api_key_id ~ '^[0-9]+$'")
	if len(mig906CheckUpBody(bad6)) == 0 {
		t.Error("坏样本⑥（谓词里带表别名 t.）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本⑦：换列序（把 turn_no 拿掉 ⇒ planner 用不上 index-only）。
	bad7 := strings.ReplaceAll(good, "(session_id, turn_no DESC)", "(session_id)")
	if len(mig906CheckUpBody(bad7)) == 0 {
		t.Error("坏样本⑦（去掉 turn_no 列）被判合格 —— 检测器没有鉴别力")
	}
}

func TestMigration906_FilesAreFrozen(t *testing.T) {
	root := mig906Tree(t)
	for _, c := range []struct{ rel, want string }{
		{filepath.Join("sql", "migrations", "startup", mig906Name), mig906UpSHA256},
		{filepath.Join("sql", "migrations", "startup", mig906DownName), mig906DownSHA256},
	} {
		sum := sha256.Sum256([]byte(mig906Read(t, root, c.rel)))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Errorf("%s 的 sha256 = %s，期望 %s —— 迁移文件已改（改了必须同步台账与本门）", c.rel, got, c.want)
		}
	}
}

func TestMigration906_DownDropsBothIndexes(t *testing.T) {
	root := mig906Tree(t)
	clean := mig906StripComments(mig906Read(t, root, filepath.Join("sql", "migrations", "startup", mig906DownName)))
	for _, name := range []string{mig906C1Index, mig906C2Index} {
		pat := regexp.MustCompile(`(?i)DROP\s+INDEX\s+IF\s+EXISTS\s+public\.` + regexp.QuoteMeta(name))
		if !pat.MatchString(clean) {
			t.Errorf("down 文件没有删 %s", name)
		}
	}
	if !strings.Contains(clean, "IF EXISTS") {
		t.Error("down 文件的 DROP INDEX 缺 IF EXISTS ⇒ 不幂等")
	}
}

func TestMigration906_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	root := mig906Tree(t)
	for _, name := range []string{mig906Name, mig906DownName} {
		canon := mig906Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embed := mig906Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canon != embed {
			t.Errorf("%s 的 embeddata 副本与 canonical **不逐字节一致** —— 两份会各自漂移", name)
		}
	}
}

func TestMigration906_RegisteredAtAllDeliveryPoints(t *testing.T) {
	root := mig906Tree(t)

	runner := mig906Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"`+mig906Name+`"`) {
		t.Error("runner.go StartupFiles 未登记 906 —— 新装链会跳过它")
	}

	main := mig906Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig906Name) {
		t.Error("main.go 缺 906 的 go:embed —— embeddata 副本成死文件")
	}
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig906DownName) {
		t.Error("main.go 缺 906 down 的 go:embed")
	}
	if !strings.Contains(main, `"startup/`+mig906Name+`"`) {
		t.Error("main.go 的 embed map 未登记 906")
	}
	if !strings.Contains(main, `"startup/`+mig906DownName+`"`) {
		t.Error("main.go 的 embed map 未登记 906 down")
	}

	seq := mig906Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, "sql/migrations/startup/"+mig906Name) {
		t.Error("生产升级序列 apply-db-revision-sequence.sh 未登记 906 —— 升级库会被静默跳过")
	}

	tsv := mig906Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, mig906Name) {
		t.Error("installed_startup_migrations.tsv 镜像未登记 906")
	}

	changelog := mig906Read(t, root, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, "| 906 | `"+mig906Name+"`") {
		t.Error("docs/db-changelog.md 未登记 906")
	}
	// ★ 台账里的 sha 必须与**文件实测**一致。
	upSum := sha256.Sum256([]byte(mig906Read(t, root, filepath.Join("sql", "migrations", "startup", mig906Name))))
	downSum := sha256.Sum256([]byte(mig906Read(t, root, filepath.Join("sql", "migrations", "startup", mig906DownName))))
	if got := hex.EncodeToString(upSum[:]); !strings.Contains(changelog, got) {
		t.Errorf("db-changelog.md 里 906 的 sha256 与文件实测不符（实测 %s）—— 台账与文件已分叉", got)
	}
	if got := hex.EncodeToString(downSum[:]); !strings.Contains(changelog, got) {
		t.Errorf("db-changelog.md 里 906 down 的 sha256 与文件实测不符（实测 %s）—— 台账与文件已分叉", got)
	}
}

// ── ★★ 语义耦合门：部分索引的谓词必须与 Go 侧常量逐字一致 ────────────────────
//
// ★ 这是本门**最有价值**的一条：它把「数据库里的索引谓词」与「Go 侧查询的
//   过滤谓词」绑在一起。
//
// 事故形态（正是它要防的）：有人把 Go 侧的过滤写回 **CASE 形式**
//   （口径完全等价！）**没有同步改索引** ⇒ 索引仍在位、迁移仍在台账里、
//   SQL 照跑、结果照对 ⇒ 只是**慢 295×**（1,350~1,569 ms vs 4.57 ms）
//   ⇒ ⇒ ★★ **没有任何症状能暴露它**（不报错、不超时、结果不变）。
//
// ★ 为什么用 go/ast 而不是正则：Go 源里到处是**解释**这个谓词的注释
//   （注释里就写着 `api_key_id ~ '^[0-9]+$'`），正则会命中注释 ⇒ 门恒绿。
//   AST 只取真实的字符串字面量。
func TestMigration906_IndexPredicateMatchesGoConstant(t *testing.T) {
	root := mig906Tree(t)

	goConst := mig906GoConst(t,
		filepath.Join(root, "db", "request_logs_view_schema.go"),
		"SessionFamilyAPIKeyIDNumericPredExpr")
	if goConst == "" {
		t.Fatal("db/request_logs_view_schema.go 里找不到常量 SessionFamilyAPIKeyIDNumericPredExpr —— " +
			"Go 侧与迁移 906 的谓词已脱钩")
	}
	// Go 侧的谓词带表别名 `t.`（查询里的别名），索引定义里没有别名
	// ⇒ 比对时必须去掉前缀。
	got := strings.TrimPrefix(goConst, "t.")
	if got != mig906Predicate {
		t.Errorf("Go 侧谓词 %q（去别名后）与迁移 906 的部分索引谓词 %q **不一致** —— "+
			"★ PG 的谓词蕴含判定是语法的 ⇒ 认不出来 ⇒ 索引白建（退回 1,350~1,569 ms）",
			got, mig906Predicate)
	}
}

// mig906GoConst 取某个 Go 文件里某个字符串常量的值；取不到返回 ""。
func mig906GoConst(t *testing.T, path, constName string) string {
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
