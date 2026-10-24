package startup

// migration_905_turns_session_model_idx_test.go —— 905 的契约门（R92 稀有过滤 500@30s 轮）。
//
// # 这道门守的是什么
//
// 905 给 public.session_turns（分区父表）与 public.session_turns_hot 补两条
// **model 维度的复合索引**，配合 admin/turns_sessions.go 的 V2 形态
// （派生表 UNION ALL + 单一 EXISTS），修的是
// GET /api/admin/turns/sessions 带 `?model=` 时**生产恒 500@30s**。
//
// ★ 事故形态（252 生产只读实测，2026-10-24）：
//
//	build 2841（R90 的两腿 EXISTS 已上线）直连 REPEAT=3：
//	  ?limit=20                       200 200 200   0.878 0.155 0.111 s
//	  ?limit=20&api_key_id=105        200 200 200   9.576 0.216 0.151 s
//	  ?limit=20&model=deepseek-ai%2F…  500 500 500  30.008 30.007 30.018 s
//	  ?limit=20&provider=14           200 200 200   0.358 0.100 0.080 s
//	  ?limit=20&status_code=500       200 200 200   0.554 0.181 0.153 s
//	⇒ ★★ **R90「四条过滤路径全部修好」的结论被证伪**（只有 model 那条仍 500）。
//
//	真因不是「形状」，是**稀有过滤值 ⇒ 外层会话深扫**：
//	  该 model 命中 458 轮 / **30 个会话**，而 public.sessions 有 441,112 个会话
//	  ⇒ 列表是 `ORDER BY s.updated_at DESC LIMIT 21` ⇒ 必须扫到凑够 21 行
//	  ⇒ EXPLAIN 实测外层 Rows Removed by Filter: 72,455 + 277,899 = **350,354**。
//
//	加索引 + 改形态后（事务内建索引 + EXPLAIN ANALYZE + ROLLBACK，生产零持久变更）：
//	  model（稀有）  30,008 ms ⇒ **8.0 ms**；provider='14' 358 ms ⇒ **48.6 ms**。
//
// 索引类迁移最容易出的错有六种，本门各钉一条：
//
//	① **过滤列钉死**：索引首列必须是**真正稀有**的那一列（model）。
//	   ★ 抄成 provider / status_code 会怎样？实测 provider n_distinct=20、
//	     top_freq 0.194；status_code n_distinct=8、top_freq 0.635
//	     ⇒ 这两列**没有选择性** ⇒ 给它们建索引会让规划器更倾向于
//	     「轮次驱动 + 大哈希构建」⇒ 实测退化到 **10,080 ms**。
//	② **语义耦合**：Go 侧改了过滤列，索引没跟着改 ⇒ 索引静默失效。
//	   ⇒ 本门用 **go/ast** 抽出 admin/turns_sessions.go 里
//	     `db.SessionFamilyTurnsSessionExistsSQL` 调用点的**字面量**参数
//	     （colExpr / colName），要求 (colExpr, colName) 中出现 model 那一对，
//	     且与迁移里的索引首列**逐字一致**。
//	③ **只断言存在抓不到多一条**：必须**集合精确比对** —— up 体内
//	   `CREATE INDEX` 的条数与名字必须**恰好**是那两条，多一条（追加型变异）
//	   同样判红。
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
	"strings"
	"testing"
)

const (
	mig905Name     = "905_turns_session_model_index.sql"
	mig905DownName = "905_turns_session_model_index.down.sql"

	// C1：父表（分区父表 ⇒ 自动传播到子分区）。
	mig905C1Index = "idx_session_turns_model_session"
	mig905C1Table = "public.session_turns"
	mig905C1Col   = "model"

	// C2：hot 腿（同构）。
	mig905C2Index = "idx_session_turns_hot_model_session"
	mig905C2Table = "public.session_turns_hot"
	mig905C2Col   = "model"

	mig905UpSHA256   = "59ae826b819edd652b6825ec61c02282bfabe04d716a71323e1e40b5cb038922"
	mig905DownSHA256 = "52da420021864b4639957b721dc235b3a2b4344ef68f55d5d06c9f41035c32d9"
)

func mig905Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig905Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读 %s: %v", rel, err)
	}
	return string(b)
}

// mig905StripComments 剥掉 SQL 行注释（-- 到行尾）与 /* */ 块注释。
//
// ★ 必须剥：迁移头部的说明里就写着「public.session_turns_hot」等字面量，
//   不剥的话「索引表名」类断言会命中注释 ⇒ 门恒绿。
func mig905StripComments(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	joined := strings.Join(out, "\n")
	// 去掉 /* ... */ 块注释（允许跨行）。
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

// mig905CheckUpBody 校验 up 文件体（**已剥注释**），返回问题描述列表。
func mig905CheckUpBody(clean string) []string {
	var problems []string

	// ① 恰好两条 CREATE INDEX（多一条 = 追加型变异；少一条 = 漏一条腿）。
	createIdx := strings.Count(clean, "CREATE INDEX")
	if createIdx != 2 {
		problems = append(problems, "CREATE INDEX 出现 "+itoa905(createIdx)+" 次（必须恰好 2）")
	}
	// ②③ 逐条 CREATE INDEX 语句精确比对（名字 / 表 / 列序）。
	//
	// ★ 按 "CREATE INDEX" **切句**再逐条看，而不是在整段文本上开窗口匹配 ——
	//   窗口匹配会**跨到下一条索引**（阳性对照当场抓到的漏洞：删掉第一条的
	//   关联键后，第二条的列序把它顶替了 ⇒ 漏判）。
	//   ★ Go 的 RE2 **不支持负向先行断言**（`(?!...)` 会 panic）⇒ 不能用
	//   「窗口内不得出现另一个 CREATE INDEX」那种写法。
	stmts := strings.Split(clean, "CREATE INDEX")
	if len(stmts)-1 != 2 {
		problems = append(problems, "CREATE INDEX 语句数 = "+itoa905(len(stmts)-1)+"（必须恰好 2）")
	}
	wantCols := mig905C1Col + ", tenant_id, session_id"
	for _, want := range []struct {
		index string
		table string
	}{
		{mig905C1Index, mig905C1Table},
		{mig905C2Index, mig905C2Table},
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
			// ★ 列序必须是 (model, tenant_id, session_id)：
			//   过滤列在最左（供 planner 按 model 定位），关联键随后
			//   （供它拿到行后直接定位会话 ⇒ 不用回表）。
			if !strings.Contains(st, "("+wantCols+")") {
				problems = append(problems, "索引 "+want.index+" 的列序不是 ("+wantCols+")")
			}
			break
		}
		if !found {
			problems = append(problems, "找不到索引 "+want.index)
		}
	}
	// ④ 幂等：两条都必须是 IF NOT EXISTS。
	if strings.Count(clean, "IF NOT EXISTS") != 2 {
		problems = append(problems, "IF NOT EXISTS 出现 "+itoa905(strings.Count(clean, "IF NOT EXISTS"))+
			" 次（必须恰好 2 ⇒ 幂等）")
	}
	// ⑤ 不得用 CONCURRENTLY（startup 迁移默认在事务里跑 ⇒ 会报错）。
	if strings.Contains(strings.ToUpper(clean), "CONCURRENTLY") {
		problems = append(problems, "用了 CREATE INDEX CONCURRENTLY —— startup 迁移在事务里跑 ⇒ 会失败")
	}
	// ⑥ 锁与超时必须带。
	if !strings.Contains(clean, "lock_timeout") {
		problems = append(problems, "缺 lock_timeout（抢不到锁时应当快速失败而不是排队阻塞热写表）")
	}
	if !strings.Contains(clean, "statement_timeout") {
		problems = append(problems, "缺 statement_timeout（共享 PG 服务端 30s 会掐断长索引构建 ⇒ 迁移永不记账）")
	}
	return problems
}

func itoa905(n int) string {
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

func TestMigration905_UpBodyShape(t *testing.T) {
	root := mig905Tree(t)
	clean := mig905StripComments(mig905Read(t, root, filepath.Join("sql", "migrations", "startup", mig905Name)))
	if problems := mig905CheckUpBody(clean); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("905 up 文件体：%s", p)
		}
	}
}

// TestMigration905_DetectorRejectsBadShapes 阳性对照：坏样本必须被判红。
func TestMigration905_DetectorRejectsBadShapes(t *testing.T) {
	root := mig905Tree(t)
	good := mig905StripComments(mig905Read(t, root, filepath.Join("sql", "migrations", "startup", mig905Name)))
	if problems := mig905CheckUpBody(good); len(problems) != 0 {
		t.Fatalf("阳性对照失败：真文件被判为不合格 —— 检测器坏了：%v", problems)
	}

	// 坏样本①：追加第三条索引（追加型变异）。
	bad1 := good + "\nCREATE INDEX IF NOT EXISTS idx_session_turns_extra ON public.session_turns (foo);\n"
	if len(mig905CheckUpBody(bad1)) == 0 {
		t.Error("坏样本①（追加第三条索引）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本②：把过滤列抄成 provider（实测会让常见过滤退化到 10,080 ms）。
	bad2 := strings.Replace(good, "(model, tenant_id, session_id)", "(provider, tenant_id, session_id)", 1)
	if len(mig905CheckUpBody(bad2)) == 0 {
		t.Error("坏样本②（过滤列抄成 provider）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本③：去掉关联键。
	bad3 := strings.Replace(good, "(model, tenant_id, session_id)", "(model)", 1)
	if len(mig905CheckUpBody(bad3)) == 0 {
		t.Error("坏样本③（去掉关联键）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本④：去掉 lock_timeout。
	bad4 := strings.Replace(good, "SET LOCAL lock_timeout = '60s';", "", 1)
	if len(mig905CheckUpBody(bad4)) == 0 {
		t.Error("坏样本④（去掉 lock_timeout）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本⑤：改成 CONCURRENTLY。
	bad5 := strings.Replace(good, "CREATE INDEX IF NOT EXISTS", "CREATE INDEX CONCURRENTLY IF NOT EXISTS", 1)
	if len(mig905CheckUpBody(bad5)) == 0 {
		t.Error("坏样本⑤（用 CONCURRENTLY）被判合格 —— 检测器没有鉴别力")
	}
}

func TestMigration905_FilesAreFrozen(t *testing.T) {
	root := mig905Tree(t)
	for _, c := range []struct{ rel, want string }{
		{filepath.Join("sql", "migrations", "startup", mig905Name), mig905UpSHA256},
		{filepath.Join("sql", "migrations", "startup", mig905DownName), mig905DownSHA256},
	} {
		sum := sha256.Sum256([]byte(mig905Read(t, root, c.rel)))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Errorf("%s 的 sha256 = %s，期望 %s —— 迁移文件已改（改了必须同步台账与本门）", c.rel, got, c.want)
		}
	}
}

func TestMigration905_DownDropsBothIndexes(t *testing.T) {
	root := mig905Tree(t)
	clean := mig905StripComments(mig905Read(t, root, filepath.Join("sql", "migrations", "startup", mig905DownName)))
	for _, name := range []string{mig905C1Index, mig905C2Index} {
		pat := regexp.MustCompile(`(?i)DROP\s+INDEX\s+IF\s+EXISTS\s+public\.` + regexp.QuoteMeta(name))
		if !pat.MatchString(clean) {
			t.Errorf("down 文件没有删 %s", name)
		}
	}
	if !strings.Contains(clean, "IF EXISTS") {
		t.Error("down 文件的 DROP INDEX 缺 IF EXISTS ⇒ 不幂等")
	}
}

func TestMigration905_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	root := mig905Tree(t)
	for _, name := range []string{mig905Name, mig905DownName} {
		canon := mig905Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embed := mig905Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canon != embed {
			t.Errorf("%s 的 embeddata 副本与 canonical **不逐字节一致** —— 两份会各自漂移", name)
		}
	}
}

func TestMigration905_RegisteredAtAllDeliveryPoints(t *testing.T) {
	root := mig905Tree(t)

	runner := mig905Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"`+mig905Name+`"`) {
		t.Error("runner.go StartupFiles 未登记 905 —— 新装链会跳过它")
	}

	main := mig905Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig905Name) {
		t.Error("main.go 缺 905 的 go:embed —— embeddata 副本成死文件")
	}
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig905DownName) {
		t.Error("main.go 缺 905 down 的 go:embed")
	}
	if !strings.Contains(main, `"startup/`+mig905Name+`"`) {
		t.Error("main.go 的 embed map 未登记 905")
	}
	if !strings.Contains(main, `"startup/`+mig905DownName+`"`) {
		t.Error("main.go 的 embed map 未登记 905 down")
	}

	seq := mig905Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, "sql/migrations/startup/"+mig905Name) {
		t.Error("生产升级序列 apply-db-revision-sequence.sh 未登记 905 —— 升级库会被静默跳过")
	}

	tsv := mig905Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, mig905Name) {
		t.Error("installed_startup_migrations.tsv 镜像未登记 905")
	}

	changelog := mig905Read(t, root, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, "| 905 | `"+mig905Name+"`") {
		t.Error("docs/db-changelog.md 未登记 905")
	}
	// ★ 台账里的 sha 必须与**文件实测**一致。
	upSum := sha256.Sum256([]byte(mig905Read(t, root, filepath.Join("sql", "migrations", "startup", mig905Name))))
	downSum := sha256.Sum256([]byte(mig905Read(t, root, filepath.Join("sql", "migrations", "startup", mig905DownName))))
	if got := hex.EncodeToString(upSum[:]); !strings.Contains(changelog, got) {
		t.Errorf("db-changelog.md 里 905 的 sha256 与文件实测不符（实测 %s）—— 台账与文件已分叉", got)
	}
	if got := hex.EncodeToString(downSum[:]); !strings.Contains(changelog, got) {
		t.Errorf("db-changelog.md 里 905 down 的 sha256 与文件实测不符（实测 %s）—— 台账与文件已分叉", got)
	}
}

// ── ★★ 语义耦合门：索引的过滤列必须与 Go 侧过滤列逐字一致 ──────────────────
//
// ★ 这是本门**最有价值**的一条：它把「数据库里的索引形态」与「Go 侧查询的
//   过滤列」绑在一起。
//
// 事故形态（正是它要防的）：有人改了 admin/turns_sessions.go 的过滤列
// （比如把 model 换成别的列）**没有同步改索引** ⇒ 索引仍在位、迁移仍在台账里、
// 端点却悄悄回到 500@30s ⇒ 而**没有任何门会红**。
//
// ★ 为什么用 go/ast 而不是正则：Go 源里到处是**解释**这些列的注释
//   （注释里就写着 `t.model`），正则会命中注释 ⇒ 门恒绿。
//   AST 只取真实的字符串字面量。
func TestMigration905_IndexColumnMatchesGoFilterColumn(t *testing.T) {
	root := mig905Tree(t)

	// 从 Go 侧抽出 (colExpr, colName) 字面量对。
	pairs := mig905GoCallArgs(t, filepath.Join(root, "admin", "turns_sessions.go"),
		"SessionFamilyTurnsSessionExistsSQL")

	found := false
	for _, p := range pairs {
		if p[0] == "t."+mig905C1Col && p[1] == mig905C1Col {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("admin/turns_sessions.go 里没有 (colExpr=%q, colName=%q) 的调用点 —— "+
			"Go 侧的 model 过滤列与迁移 905 的索引首列（%s）已经脱钩",
			"t."+mig905C1Col, mig905C1Col, mig905C1Col)
	}

	// ★★ R92 变异臂 M6 暴露的缺口：**colName 必须与 pred 引用的列逐字一致**。
	//   不一致时 SQL 在**运行期**才报错（column does not exist）⇒ 端点 500，
	//   而第一版门禁只抽前两个参数 ⇒ 这个错误**没有任何门会红**。
	for _, p := range pairs {
		col := mig905PredCol(p[2])
		if col == "" {
			t.Errorf("调用点 (colExpr=%q, colName=%q, pred=%q) 的谓词不是「ft.<col> = $N」形态 —— "+
				"派生表形态下这种谓词无法生效", p[0], p[1], p[2])
			continue
		}
		if col != p[1] {
			t.Errorf("调用点 (colExpr=%q, colName=%q, pred=%q)：谓词引用 ft.%s，但投影别名是 %q —— "+
				"不一致 ⇒ SQL 运行期报 column does not exist ⇒ 端点 500", p[0], p[1], p[2], col, p[1])
		}
	}

	// 反向：Go 侧出现的**每一个**过滤列，要么是本迁移索引的那一列，
	// 要么在「**故意不建索引**」清单里且写明理由 ——
	// ★ 不写成「都必须有索引」是因为 provider / status_code **故意不建**：
	//   n_distinct=20 / 8 ⇒ 没有选择性 ⇒ 建索引反而让常见过滤退化到 10,080 ms。
	//   ⇒ 但这个决定必须被**钉住**，否则下次有人「顺手补个索引」就会踩坑。
	unindexed := map[string]string{
		"provider":    "n_distinct=20 / top_freq 0.194 ⇒ 无选择性；建索引会让常见过滤退化成「轮次驱动 + 大哈希构建」（实测 10,080 ms）",
		"status_code": "n_distinct=8 / top_freq 0.635 ⇒ 无选择性；同上",
		"api_key_id":  "过滤侧是 CASE 表达式（db.SessionFamilyAPIKeyIDExpr）⇒ 本来就不可索引",
	}
	for _, p := range pairs {
		if p[1] == "" || p[1] == mig905C1Col {
			continue
		}
		reason, ok := unindexed[p[1]]
		if !ok {
			t.Errorf("Go 侧的过滤列 %q 既没有对应索引、也不在「故意不建索引」清单里 —— "+
				"要么补索引，要么把不建的理由写进清单（钉住这个决定）", p[1])
			continue
		}
		if reason == "" {
			t.Errorf("过滤列 %q 被列为「故意不建索引」但没写理由", p[1])
		}
	}
}

// mig905GoCallArgs 抽出某个函数调用点的**三个参数**（colExpr / colName / pred）。
//
// ★★ R92 变异臂 M6 当场暴露的改进点：
//   第一版只抽**前两个**参数 ⇒ pred（第三个）不在判据面里 ⇒
//   「colName 与谓词列名不一致」这个错误**没有任何门会红**。
//   ⇒ 现在抽三个，并逐个调用点核对「colName ≡ pred 里引用的 ft.<col>」。
//
// ★ arg[0]（colExpr）允许是**选择器表达式**（如 db.SessionFamilyAPIKeyIDExpr）
//   ⇒ 这类调用点不能被跳过，否则 api_key_id 那一支就完全不在判据面里
//   ⇒（第一版正是因此让「故意不建索引」清单里的 api_key_id 条目成为**死条目**）。
func mig905GoCallArgs(t *testing.T, path, funcName string) [][3]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	var out [][3]string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != funcName {
			return true
		}
		if len(call.Args) < 3 {
			return true
		}
		a := mig905ArgText(call.Args[0])
		b := mig905StrLit(call.Args[1])
		c := mig905LitStrings(call.Args[2])
		if a == "" || b == "" || c == "" {
			return true
		}
		out = append(out, [3]string{a, b, c})
		return true
	})
	if len(out) == 0 {
		t.Fatalf("在 %s 里没找到 %s 的调用点 —— 形态漂移了，请人工复核", path, funcName)
	}
	return out
}

// mig905StrLit 取字符串字面量的内容；不是字面量时返回 ""。
func mig905StrLit(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.Trim(lit.Value, `"`)
}

// mig905LitStrings 收集表达式里的全部字符串字面量（按出现顺序拼接）。
//
// ★★ 为什么不能只认单个 *ast.BasicLit：第三个参数（pred）是**拼接式**的
//   —— `"ft.model = $"+strconv.Itoa(argIdx)` ⇒ 只认 BasicLit 会把
//   **全部四个调用点都漏掉** ⇒ 判据面直接空了（第一版就栽在这里：
//   门不是红，而是「找不到调用点」的红 —— 读数上像形态漂移，实则是提取器缺陷）。
func mig905LitStrings(e ast.Expr) string {
	var out []string
	ast.Inspect(e, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			out = append(out, strings.Trim(lit.Value, `"`))
		}
		return true
	})
	return strings.Join(out, "")
}

// mig905ArgText 把「字符串字面量」或「选择器表达式」渲染成可比对的文本。
func mig905ArgText(e ast.Expr) string {
	if s := mig905StrLit(e); s != "" {
		return s
	}
	if sel, ok := e.(*ast.SelectorExpr); ok {
		if x, ok := sel.X.(*ast.Ident); ok {
			return x.Name + "." + sel.Sel.Name
		}
		return sel.Sel.Name
	}
	return ""
}

// mig905PredColRe 抽「ft.<列> = $N」形态的谓词列。
//
// ★ 序号部分允许**为空**（源码里是拼接式：`"ft.model = $"+strconv.Itoa(...)`
//   ⇒ 抽出来的字面量前缀只有 `$`）⇒ 序号段用 `[0-9]*`。
var mig905PredColRe = regexp.MustCompile(`\bft\.([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\$[0-9]*`)

// mig905PredCol 返回 pred 里引用的派生表列名；不是该形态时返回 ""。
func mig905PredCol(pred string) string {
	m := mig905PredColRe.FindStringSubmatch(pred)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
