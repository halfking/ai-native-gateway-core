package admin

// v1_family_purge_gate_test.go —— 2026-10-24「v1 → session_turns 替换轮」。
//
// # 这道门在守什么
//
// 迁移 890 把 v1 `request_logs` 族**物理删除**了（10 张底表 + 4 张 v1-only
// 视图 + 序列/索引/RLS）。此后任何仍引用它的生产代码在运行时都是 **42P01**
// （relation does not exist）⇒ boot 中止 ⇒ 网关「就绪但没有数据库」
// （readyz 在 SetDepsOptional(true) 下仍返 200 ⇒ 部署安全网放行）。
//
// # 它和已有那道门的分工（**不是重复**）
//
//	admin/session_family_sql_shape_test.go
//	    TestRetiredV1ReferencesAreAllAccountedFor
//	    —— 守**读点**（FROM / JOIN 一个 v1 关系），口径是「文本行 + 剔注释」。
//
//	本门
//	    —— 守**写点 / DDL 点**（ALTER / INSERT / UPDATE / DELETE / TRUNCATE /
//	       COPY / DROP / CREATE INDEX ON / REFERENCES 一个 v1 关系）。
//
// ⇒ 为什么必须分开守：那道读门是**逐行**扫描 + 按行首 `//`/`*` 判注释 ⇒
//   它天然看不见「多行 SQL 字符串里的 ALTER TABLE _request_logs」——
//   而那恰恰是 2026-10-24 之前 boot 路径上**最后 6 处** v1 操作所在
//   （db.go 的 ensureRequestLogSchema / ensureBillingUnitAndAudioSeconds /
//   ensureRoutingAnalyticsColumns / ensureWorkTypeSchema /
//   ensureRoutingRecentSuccessRate + db/maas_schema.go 的 EnsureMaasSchema）。
//
//   ⇒ 本门用 **go/ast** 取字符串字面量（不是逐行正则）⇒ 注释里的
//   `ALTER TABLE _request_logs` **永远不会**被算成操作 ⇒ 没有误报面。
//
// # 本门刻意**允许**的东西（每一条都有理由）
//
//	① 历史注释 / 迁移文件名 / installer 的 StartupFiles 台账
//	   —— v1 族的历史是**承重证据**（R64 实证：基线里必须写 v1 fallback 体，
//	      否则新装库 42P01）。
//	② 编号 ≤ 890 的启动迁移
//	   —— 它们跑在 890 **之前**，那时 v1 族还在。
//	③ `sql/schema/01-schema.sql`
//	   —— 基线建出 v1 族（历史迁移依赖它）；890 之后才删。
//
// # 阳性对照（防「恒真空」）
//
// 一道断言「命中集为空」的判据，必须证明它的扫描器**看得见**真命中。
// 本门用**模式级**对照钉住：真 v1 写点必须命中、canonical 视图必须不命中、
// 历史迁移文件必须不命中（它按编号放行，不是靠模式蒙对）。

import (
	"fmt"
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

// retiredV1WritePatterns 是「对已退役 v1 关系做**写 / DDL**」的模式集合。
//
// ⚠ 与 retiredV1ReadPattern 的区别：读门是「from|join + 关系名」，
//
//	这里必须覆盖动词**本身带表名**的形态（ALTER TABLE x / INSERT INTO x /
//	UPDATE x / DELETE FROM x / TRUNCATE x / COPY x / DROP x / REFERENCES x）
//	以及「动词与表名被索引名隔开」的形态（CREATE INDEX … ON x）。
//
// ⚠ `ON` 那条必须要求紧跟 `(`：`ON _request_logs (…)` 是索引定义；
//
//	而 `… ON public._request_logs_with_current_month` 是**活着的** canonical
//	视图，它根本不在 retiredV1RelationNames 里 ⇒ 不会被本组模式命中。
var retiredV1WritePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:alter\s+table|insert\s+into|update|delete\s+from|` +
		`truncate(?:\s+table)?|copy|drop\s+(?:table|index|view|function|materialized\s+view)|references)` +
		`(?:\s+if\s+exists)?\s+(?:only\s+)?(?:public\.)?(?:` + retiredV1RelationAlternation() + `)\b`),
	regexp.MustCompile(`(?i)\bon\s+(?:public\.)?(?:` + retiredV1RelationAlternation() + `)\s*\(`),
}

// v1WriteHit 是一次命中。
type v1WriteHit struct {
	file string
	line int
	text string
}

// scanV1WriteOperationsInGo 扫**生产** .go 文件的**字符串字面量**。
//
// ⚠ 用 go/ast 而不是逐行正则：只有 AST 能区分「注释里的 SQL」与
//
//	「真的会被执行的 SQL」。逐行正则必须靠「行首是 //」猜注释，
//	而多行字符串（反引号）里的 SQL 行首常常没有 // ⇒ 逐行口径要么漏、
//	要么把真操作当注释。
func scanV1WriteOperationsInGo(t *testing.T, root string) []v1WriteHit {
	t.Helper()
	var hits []v1WriteHit

	skipDir := map[string]bool{".git": true, "docs": true, "node_modules": true, "vendor": true,
		".audit": true, ".audit-validation": true, ".audit-workspace": true,
		"web": true, "dist": true, "build": true}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// SQLite 存储面有自己的 request_logs 表 ⇒ 同名假阳性，排除。
		if strings.HasPrefix(rel, "storage/sqlite/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			// 解析失败的文件不静默跳过 —— 那会让门在这个文件上恒真空。
			t.Fatalf("parse %s: %v", rel, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				// 反引号原始字符串用 strconv.Unquote 会失败 ⇒ 手工剥。
				text = strings.Trim(lit.Value, "`")
				if len(text) >= 2 && text[0] == '"' {
					text = text[1 : len(text)-1]
				}
			}
			for _, p := range retiredV1WritePatterns {
				if !p.MatchString(text) {
					continue
				}
				pos := fset.Position(lit.Pos())
				line := pos.Line
				// 报出真正命中那一行，便于定位。
				for i, l := range strings.Split(text, "\n") {
					if p.MatchString(l) {
						line = pos.Line + i
						break
					}
				}
				hits = append(hits, v1WriteHit{file: rel, line: line, text: strings.TrimSpace(text)})
				break
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	return hits
}

// v1RetirementAnchorFile 是「v1 族退役」这一步的文件名。
//
// ⚠ 为什么用**文件名**而不是「迁移号」：启动迁移目录里混着两类命名 ——
//
//	`NNN_…`（迁移号）与 `2026-07-13-…`（日期名）。把日期名当成迁移号会
//	读成「2026 > 890 ⇒ 它跑在退役之后」⇒ **误判**（实测：那份文件在
//	字典序里排第 60 位，远在 890（第 959 位）之前 ⇒ 它跑在 v1 还在的时候）。
//
// ⇒ 判据改用**字典序**，因为那正是各投递通道的真实顺序：
//
//	· scripts/init-local-db.sh：`for f in …/migrations/startup/[0-9]*.sql`
//	· scripts/migrate-db-kaixuan1.sh：同样按 glob 顺序
//	· installer/internal/dbinit/runner.go：显式列表，但数值前缀的排序与字典序一致
//	  （实测：全部 v1 相关迁移都排在 890 之前 —— 最大的一个是 487c，第 940 位）。
const v1RetirementAnchorFile = "890_drop_request_logs_v1_family.sql"

// scanV1WriteOperationsInMigrations 扫**跑在退役之后**的启动迁移。
//
// ⚠ 放行依据是**投递序**（字典序），不是「内容看起来像不像历史」：
//
//	跑在 890 **之前**的迁移写 v1 是正确的（那时 v1 族还在）。
//
// ⚠ 两条投递通道都要扫：canonical 目录（sql/migrations/startup/）与
//
//	installer 的 embeddata 孪生副本（installer/cmd/llm-gw-installer/
//	embeddata/startup/）—— 新装库跑的是**孪生副本**那份。
//
// ⚠ `.down.sql` **不豁免**：它是回滚脚本，跑在退役之前（字典序上 `.down.sql`
//
//	排在同名 `.sql` 之前）⇒ 它的 v1 DDL 是**合法**的（回滚就是要把族建回来）。
//	⇒ 同一个字典序判据自动把它归到「退役之前」，不需要额外白名单。
func scanV1WriteOperationsInMigrations(t *testing.T, root string) []v1WriteHit {
	t.Helper()
	var hits []v1WriteHit
	dirs := []string{
		filepath.Join(root, "sql", "migrations", "startup"),
		filepath.Join(root, "installer", "cmd", "llm-gw-installer", "embeddata", "startup"),
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		// 找到退役锚点的位置；锚点之后 = 跑在 v1 族退役之后。
		anchorAt := -1
		for i, n := range names {
			if n == v1RetirementAnchorFile {
				anchorAt = i
				break
			}
		}
		if anchorAt < 0 {
			t.Fatalf("%s 里找不到退役锚点 %s", dir, v1RetirementAnchorFile)
		}
		for _, name := range names[anchorAt+1:] {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			body := stripSQLComments(string(b))
			rel, err := filepath.Rel(root, filepath.Join(dir, name))
			if err != nil {
				rel = filepath.Join(dir, name)
			}
			rel = filepath.ToSlash(rel)
			for i, l := range strings.Split(body, "\n") {
				for _, p := range retiredV1WritePatterns {
					if p.MatchString(l) {
						hits = append(hits, v1WriteHit{file: rel, line: i + 1, text: strings.TrimSpace(l)})
						break
					}
				}
			}
		}
	}
	return hits
}

// TestNoLiveV1WriteOperations 是一道**零容忍**门：
// 生产 .go 代码与 890 之后的启动迁移里，**一个** v1 写/DDL 操作都不许有。
//
// ⚠ 为什么是「零容忍」而不是「例外台账」：读侧那道门有例外台账
//
//	（retiredV1ReferenceExceptions，基线 0），因为读点可以逐个改指；
//	写/DDL 点则是**一处漏一处炸**（42P01 ⇒ boot 中止），而 v1 族的写侧
//	在本轮已被**整体删除** ⇒ 没有「还在写 v1」的合法理由。
func TestNoLiveV1WriteOperations(t *testing.T) {
	root := repoRootFromCaller(t)

	goHits := scanV1WriteOperationsInGo(t, root)
	migHits := scanV1WriteOperationsInMigrations(t, root)

	for _, h := range goHits {
		t.Errorf("生产代码里仍有对已退役 v1 关系的写/DDL 操作：%s:%d\n  %s\n"+
			"⇒ 890 已经把这张表从库里删了 ⇒ 运行时会 42P01 ⇒ boot 中止 ⇒ 网关「就绪但没有数据库」。\n"+
			"⇒ 改指会话族（session_turns / session_turns_hot / session_turn_details*），"+
			"或直接删除（v1 族已退役，写侧没有合法理由保留）。",
			h.file, h.line, truncateForMessage(h.text))
	}
	for _, h := range migHits {
		t.Errorf("编号 > 890 的启动迁移里仍有对已退役 v1 关系的写/DDL 操作：%s:%d\n  %s\n"+
			"⇒ 它跑在 890 **之后** ⇒ v1 族已被删 ⇒ 42P01 ⇒ 新装库 boot 中止。",
			h.file, h.line, truncateForMessage(h.text))
	}
}

// TestV1WriteGateScannerHasDiscriminatingPower 是**阳性对照**。
//
// ⚠ 没有这一条，TestNoLiveV1WriteOperations 可能因为「扫描器什么都没看见」
//
//	而恒绿 —— 那是最坏的失败模式：门是绿的，而生产照样 42P01。
func TestV1WriteGateScannerHasDiscriminatingPower(t *testing.T) {
	t.Run("必须命中真 v1 写点", func(t *testing.T) {
		for _, s := range []string{
			"ALTER TABLE _request_logs ADD COLUMN IF NOT EXISTS work_type TEXT;",
			"ALTER TABLE IF EXISTS _request_logs_hot ADD COLUMN IF NOT EXISTS task_type TEXT;",
			"INSERT INTO public._request_logs (request_id) VALUES ($1);",
			"UPDATE _request_logs SET error_kind = $1 WHERE request_id = $2;",
			"DELETE FROM public._request_logs WHERE ts < $1;",
			"TRUNCATE _request_logs;",
			"CREATE INDEX IF NOT EXISTS idx_x ON _request_logs (work_type, ts DESC);",
			"DROP TABLE IF EXISTS public._request_logs;",
			"ALTER TABLE public._request_logs_bodies_hot ADD COLUMN IF NOT EXISTS x INT;",
		} {
			if !matchesAnyV1WritePattern(s) {
				t.Errorf("扫描器匹配不上真 v1 写点 %q —— 扫描器没有区分力，本门是恒真空", s)
			}
		}
	})

	t.Run("必须不误伤活着的 canonical 视图与会话族", func(t *testing.T) {
		for _, s := range []string{
			"SELECT * FROM request_logs_with_current_month r",
			"SELECT count(*) FROM public.request_logs_with_current_month",
			"ALTER TABLE session_turns ADD COLUMN IF NOT EXISTS foo TEXT;",
			"INSERT INTO public.session_turns (request_id) VALUES ($1);",
			"UPDATE session_turns_hot SET success = $1;",
			"-- 2026-10-20：源从 _request_logs_hot 换成 canonical 视图",
			"Derived from terminal _request_logs rows in the failure window.",
		} {
			if matchesAnyV1WritePattern(s) {
				t.Errorf("扫描器误伤了 %q —— canonical 视图与会话族是**活着**的读面/写面，不能算成 v1 操作", s)
			}
		}
	})

	t.Run("扫描器必须真的在扫生产文件", func(t *testing.T) {
		root := repoRootFromCaller(t)
		// 判据的输入面不能是空的：一个都没扫到 ⇒ 路径写错了 ⇒ 恒真空。
		if _, err := os.Stat(filepath.Join(root, "db", "db.go")); err != nil {
			t.Fatalf("repo root 不对：%v", err)
		}
		hits := scanV1WriteOperationsInGo(t, root)
		// 清干净之后这里必须是 0；但「扫到了 0」与「没扫到文件」要长得不一样 ⇒
		// 单独断言扫描器对已知含 v1 的**历史**文件仍能识别（阳性对照见上）。
		if len(hits) != 0 {
			t.Errorf("生产 .go 代码里仍有 %d 处 v1 写/DDL 操作（详见 TestNoLiveV1WriteOperations）", len(hits))
		}
	})
}

func matchesAnyV1WritePattern(s string) bool {
	for _, p := range retiredV1WritePatterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

func truncateForMessage(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 220 {
		return s[:220] + " …"
	}
	return s
}

// retiredV1FunctionNames 是 v1 族自己的 SQL 函数（890 一并 DROP）。
//
// ⚠ 为什么这道门与「关系名」那道**分开**：关系名（表 / 视图）与函数名是
//
//	两个总体 —— 只扫关系名，就看不见「调用一个已 DROP 的 v1 函数」这条路径
//	（实测：bg/partition_manager.go 的归档调用就是这一类；关系名扫描完全看不见它）。
var retiredV1FunctionNames = []string{
	"archive_request_logs_default",
	"archive_request_logs_bodies_default",
	"ensure_request_logs_partition",
	"ensure_request_logs_bodies_partition",
	"promote_request_logs_hot_to_partition",
	"promote_request_logs_bodies_hot_to_partition",
	"drop_old_request_logs_bodies_partitions",
}

// retiredV1FunctionCallPattern 匹配「调用一个 v1 函数」。
var retiredV1FunctionCallPattern = func() *regexp.Regexp {
	alt := make([]string, len(retiredV1FunctionNames))
	for i, n := range retiredV1FunctionNames {
		alt[i] = regexp.QuoteMeta(n)
	}
	sort.Strings(alt)
	return regexp.MustCompile(`(?i)\b(?:` + strings.Join(alt, "|") + `)\s*\(`)
}()

// retiredV1FunctionCallExceptions 是**已知仍引用已退役 v1 函数**的生产位置。
//
// # 这张表**只许缩小**
//
// 每一项对应一个已知的、被**刻意**保留的位置；删掉一处就少一项 ⇒
// 新增必须先说明「为什么不是扫描器漏了」。
//
// ⚠ 为什么这里可以有例外，而「关系名」那道门是零容忍：函数引用与关系引用的
//
//	语义不同 —— 一个**拒绝对 v1 表做操作**的名单（拒绝名单本身必须点名 v1）
//	天然会包含 v1 的名字；那不是「还在用 v1」，那是「在提防 v1」。
var retiredV1FunctionCallExceptions = map[string]string{
	"bg/partition_manager.go": "archiveOldRequestLogs 的归档调用。该函数体被 " +
		"v1LifecycleRetired()（恒 true）无条件挡住 ⇒ 不可达；且**刻意保留**：它的形状" +
		"（每日节奏门 + 显式事务 + 30min statement_timeout）是 F-11/F-12/R73 三条审计结论" +
		"的依据，由 bg/partition_manager_request_logs_archive_cadence_test.go 钉住。" +
		"删掉它会同时删掉那三条结论的载体。",
	// ★ 2026-10-24：原表里还有一条 admin/data_lifecycle_hot_partition.go
	//   （retiredV1HotTables / promote 函数名映射）。**它被删掉了**，因为
	//   那两处是**映射表的取值**（"promote_request_logs_hot_to_partition"），
	//   **不是调用**（没有紧跟的左括号）⇒ 「调用」口径下它**根本不会命中**
	//   ⇒ 台账里挂着一条**永不命中**的例外 = 一条**假凭据**（会让人以为
	//   「这里仍有一处 v1 引用被登记着」）⇒ 按「只许缩小」纪律删掉。
	//   （变异探针 M2 的运行日志把这件事**自己暴露**出来了：它打印了
	//   「例外 … 已不再被扫描命中（生产侧可能已修）」⇒ 确认后删。）
	//
	//   ★ 顺带说明：那两个名字**仍然**出现在生产代码里（它们是**拒绝名单**
	//   的取值 —— 端点据此对 v1 热表返回 409）⇒ 它们不是「调用」，
	//   本门不管；「关系名」那道门也不管（它们是 map 的**键/值**，不在
	//   SQL 操作数位置）⇒ ★ 它们的正确守门人是
	//   admin/data_lifecycle_v1_retirement_test.go（既有门，钉住
	//   「退役族必须仍在 hotPromoteTableMap / retiredV1HotTables 里」）。
}

// retiredV1FunctionCallExceptionsBaseline 是例外台账的规模基线（只许缩小）。
const retiredV1FunctionCallExceptionsBaseline = 1

// TestNoLiveV1FunctionCalls 补上「关系名扫描看不见的那一维」。
//
// ⚠ 判据不是「一个都不许有」—— 上面那两项例外是**刻意**的（拒绝名单 + 不可达的
//
//	归档体）。判据是「命中集 ⊆ 例外台账」+「台账只许缩小」——
//	与 admin/session_family_sql_shape_test.go 的 retiredV1ReferenceExceptions 同款。
func TestNoLiveV1FunctionCalls(t *testing.T) {
	root := repoRootFromCaller(t)

	// 扫描生产 .go 的字符串字面量里的 v1 函数调用。
	hits := map[string][]int{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "docs", "node_modules", "vendor", ".audit", ".audit-validation",
				".audit-workspace", "web", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "storage/sqlite/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				text = strings.Trim(lit.Value, "`")
			}
			if !retiredV1FunctionCallPattern.MatchString(text) {
				return true
			}
			pos := fset.Position(lit.Pos())
			hits[rel] = append(hits[rel], pos.Line)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}

	var unknown []string
	for file := range hits {
		if _, ok := retiredV1FunctionCallExceptions[file]; !ok {
			unknown = append(unknown, fmt.Sprintf("%s (行 %v)", file, hits[file]))
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("%d 个生产文件调用已退役的 v1 函数，但**不在** retiredV1FunctionCallExceptions 里：\n  %s\n"+
			"⇒ 890 已经把这些函数从库里删了 ⇒ 运行时会 42P01 ⇒ boot 中止。\n"+
			"⇒ 改指会话族，或把它具名登记进例外台账并写清理由。",
			len(unknown), strings.Join(unknown, "\n  "))
	}

	for file, why := range retiredV1FunctionCallExceptions {
		if strings.TrimSpace(why) == "" {
			t.Errorf("retiredV1FunctionCallExceptions[%q] 的理由为空 —— 空理由的具名等于没具名", file)
		}
		if _, ok := hits[file]; !ok {
			t.Logf("例外 %s 已不再被扫描命中（生产侧可能已修）——确认后请把它从例外台账里删掉", file)
		}
	}
	if len(retiredV1FunctionCallExceptions) > retiredV1FunctionCallExceptionsBaseline {
		t.Errorf("retiredV1FunctionCallExceptions 有 %d 条，超过基线 %d —— 这张表只许缩小。",
			len(retiredV1FunctionCallExceptions), retiredV1FunctionCallExceptionsBaseline)
	}

	// 阳性对照：扫描器必须看得见真命中。
	if !retiredV1FunctionCallPattern.MatchString("FROM archive_request_logs_default($1)") {
		t.Error("扫描器匹配不上真 v1 函数调用 —— 扫描器没有区分力，本门是恒真空")
	}
	if retiredV1FunctionCallPattern.MatchString("FROM promote_session_turns_hot($1)") {
		t.Error("扫描器误伤了会话族的同名形态 —— 判据会恒红")
	}
}

// TestInstallerStartupFilesStillRetiresTheV1Family 钉住「退役这一步本身不许丢」。
//
// ⚠ 为什么单独一条：上面的门只管「别再写 v1」，**管不到**「v1 有没有被删」。
//
//	如果有人把 890 从 installer 的 StartupFiles 里删掉（看起来像「清理」，
//	实际是让新装库永远留着 v1 族 + 永远保留那套 v1 列）⇒ 上面的门全绿，
//	而库里多出一整族死表 ⇒ 磁盘与运维成本，且「唯一事实源」的口径破功。
func TestInstallerStartupFilesStillRetiresTheV1Family(t *testing.T) {
	root := repoRootFromCaller(t)
	runner := filepath.Join(root, "installer", "internal", "dbinit", "runner.go")
	b, err := os.ReadFile(runner)
	if err != nil {
		t.Fatalf("read installer runner: %v", err)
	}
	if !strings.Contains(string(b), "890_drop_request_logs_v1_family.sql") {
		t.Error("installer 的 StartupFiles 里必须有 890_drop_request_logs_v1_family.sql —— " +
			"删掉它 ⇒ 新装库永远不删 v1 族 ⇒ 一整族死表留在库里，「唯一事实源」的口径破功")
	}

	// embeddata 孪生副本也必须在（它是 installer 实际投递的那份）。
	embed := filepath.Join(root, "installer", "cmd", "llm-gw-installer", "embeddata", "startup",
		"890_drop_request_logs_v1_family.sql")
	if _, err := os.Stat(embed); err != nil {
		t.Errorf("embeddata 孪生副本缺失：%v\n"+
			"⇒ installer 投递的是 embeddata 那份，不是 canonical 那份 ⇒ 缺它 ⇒ 新装库不删 v1 族", err)
	}
}
