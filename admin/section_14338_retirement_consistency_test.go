package admin

// §143.38 的门：**v1 族退役链（迁移 890）与仓内对象清单的一致性**。
//
// 背景（runbook §143.38，生产只读实测 2026-10-21）：
// ① 生产迁移水位 = **889** ⇒ 迁移 890（v1 `request_logs` 族真删）**未执行**
//    ⇒ v1 族仍在生产（`request_logs_2026_10` **334,669 行 / 1,015 MB** +
//    `request_logs_bodies_2026_10` 816,144 行 / 579 MB + hot 两张 ≈ 350 MB）。
// ② 890 的**非空闸** ⇒ 族里有行就 RAISE EXCEPTION ⇒ **890 现在会拒绝执行**
//    （这既是保护，也把退役堵住了）。
// ③ ★★ **部署漂移已被证实**：生产上活跃的 auto-route 亲和度聚合语句
//    （queryid `-7870179464850598482`，408 次 / 1,209.0 s）**逐字仍是三条臂**
//    （引用 `_request_logs_hot` / `_request_logs`），
//    而仓内 HEAD 是**一条臂**（commit 5f64031cb「退役 v1 族 + 全仓读面切会话族」）。
//    ⇒ ★★ **执行顺序反了 ⇒ 那条聚合整句 42P01**（同一条 FROM 里任何被提及的
//    关系不存在 ⇒ 整句失败，不是那条臂失败）
//    ⇒ ⇒ 亲和度学习链断裂，且**静默**（stale-while-error ⇒ 仍服务旧快照）。
//
// # 这道门在挡什么
//
// ★★ **「退役链」与「仓内对象清单」是两套名单，只有两边对齐才有意义。**
// 本仓已有两道相关的东西，但它们**各管一头**，中间的空档正是本轮要堵的：
//
// ① `cmd/tools/sql_source_indirection_audit`（v1 读点审计，**独立 main module**）
//    ⇒ 它扫的是「SQL 的 FROM/JOIN 片段」⇒ ★ **表名写在 Go 结构体里它看不见**
//    （本轮实测：`admin/data_lifecycle_partition.go` 的 `partitionedTables`
//    与 `internal/partguard/parents.go` 的父表名单，都不在它的视野里）。
// ② `internal/partguard` 的父表名单（守卫**写入**）
//    ⇒ 它守的是「别往这些分区父表写」⇒ 890 之后名单会陈旧，但**不写就无害**。
//
// ⇒ ⇒ 本门把「**890 要删什么**」与「**仓里还有什么在引用它**」绑在一起：
//   · T3：仓内任何 Go 的**类 SQL 字符串字面量**都不得含 890 要 DROP 的表名。
//
// ★ 与既有纪律同源：**退役链的完整性必须被门钉住**，否则「迁移文件多加一个
// DROP 对象」会静默地让某个读面在迁移后 42P01（而那正是本轮实测到的形态）。

import (
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
	mig890RelPath14338   = "../sql/migrations/startup/890_drop_request_logs_v1_family.sql"
	precheckRelPath14338 = "../scripts/audit/verify_890_cutover_precheck.sql"
	runbookRelPath14338  = "../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
)

// ─────────────────────────────────────────────────────────────────────────
// 890 的 DROP 清单（**唯一真相源 = 迁移文件本身**）
// ─────────────────────────────────────────────────────────────────────────

// drop890Tables14338 从迁移 890 的文件里抽出它会 DROP 的**表**名。
//
// ★ 口径（踩过坑才写成这样）：
//
//	· 890 用**动态 SQL**（`EXECUTE format('DROP TABLE IF EXISTS public.%I', v_name)`）
//	  ⇒ 表名在 `FOREACH v_name IN ARRAY ARRAY[...]` 列表里，不在 DROP 语句里。
//	· 同一个列表里**混着序列**（`relkind='S'` ⇒ 走 DROP SEQUENCE 分支）
//	  ⇒ 序列不是表，不进本集合。
//	· 视图在**另一个** FOREACH 列表里（步骤 4）⇒ 也不进本集合。
//	· 静态 `DROP FUNCTION IF EXISTS public.X` 是函数 ⇒ 不进本集合。
func drop890Tables14338(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(mig890RelPath14338)
	if err != nil {
		t.Fatalf("read %s: %v", mig890RelPath14338, err)
	}
	code := stripSQLComments14338(string(raw))

	// 找出「步骤 5」那个表/序列列表：它后面跟着 relkind='S' 的判断。
	idx := strings.Index(code, "DROP TABLE IF EXISTS")
	if idx < 0 {
		t.Fatalf("%s 里找不到 DROP TABLE 的动态模板 ⇒ 解析器口径失效", mig890RelPath14338)
	}
	// 往前找最近的 FOREACH 数组起点
	fIdx := strings.LastIndex(code[:idx], "FOREACH v_name IN ARRAY ARRAY[")
	if fIdx < 0 {
		t.Fatalf("%s 里找不到 DROP TABLE 之前的 FOREACH 列表", mig890RelPath14338)
	}
	end := strings.Index(code[fIdx:], "]")
	if end < 0 {
		t.Fatalf("%s 里 FOREACH 列表没有闭合", mig890RelPath14338)
	}
	block := code[fIdx : fIdx+end]

	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(block, -1) {
		name := m[1]
		if strings.HasSuffix(name, "_id_seq") {
			continue // 序列
		}
		out[name] = true
	}
	if len(out) == 0 {
		t.Fatalf("从 %s 抽不到任何表名 ⇒ 解析器口径失效", mig890RelPath14338)
	}
	return out
}

// stripSQLComments14338 去掉整行 `--` 注释与 `/* */` 块注释。
//
// ★ 为什么自己写而不是复用仓里的：本题的 needle（表名）在迁移文件里
// **大量出现在注释里**（文件头逐个点名要删哪些表）⇒ 不剥注释的话
// 「从 FOREACH 列表里抽名字」会被注释里的名字带偏。
func stripSQLComments14338(s string) string {
	s = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(s, " ")
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i]
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// ─────────────────────────────────────────────────────────────────────────
// T1：解析器自证（★ 量具必须先证明自己看得见目标）
// ─────────────────────────────────────────────────────────────────────────

func TestSection14338DropListParserSeesTheRealTables(t *testing.T) {
	tables := drop890Tables14338(t)
	for _, want := range []string{
		"request_logs", "_request_logs",
		"request_logs_hot", "_request_logs_hot",
		"request_logs_bodies", "_request_logs_bodies",
		"request_logs_archive", "_request_logs_archive",
	} {
		if !tables[want] {
			t.Errorf("解析器没看到 890 要 DROP 的表 %q（抽到: %v）", want, keys14338(tables))
		}
	}
	// 序列与视图**不应**混进表集合（它们走别的分支）
	for _, notTable := range []string{
		"request_logs_id_seq", "_request_logs_id_seq",
		"request_logs_with_current_month_without_customer_id",
		"request_logs_bodies_with_current_month",
	} {
		if tables[notTable] {
			t.Errorf("序列/视图 %q 被误当成表混进了表集合 ⇒ 判据口径错了", notTable)
		}
	}
}

func keys14338(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────
// T2：仓内受管表名清单 ⇄ 890 的 DROP 清单 必须一致
// ─────────────────────────────────────────────────────────────────────────

// ─────────────────────────────────────────────────────────────────────────
// T3：仓内 Go 的「类 SQL 字符串字面量」不得含 890 要 DROP 的表名
// ─────────────────────────────────────────────────────────────────────────

// sqlKeywordRe14338 判定一个字符串字面量「像一条 SQL **语句**」。
//
// ★★ 这里**不能**把裸 `from` / `join` 当信号 —— 中文散文里到处是
//
//	「读 FROM public.request_logs rl」这种句子
//	⇒ 位置判据单独用会把散文判成读点（本门语义自测当场抓到那一例）。
//
// ⇒ ⇒ 改成**两个条件同时成立**：
//
//	① 字面量里有一条**语句**关键字（SELECT / INSERT INTO / UPDATE / DELETE FROM / WITH）；
//	② 关系名出现在 FROM/JOIN 位置（见 containsRelation14338）。
var sqlKeywordRe14338 = regexp.MustCompile(`(?is)\b(select|insert\s+into|update|delete\s+from|with)\b`)

// identChar14338 是标识符字符（用于词边界锚定）。
func identChar14338(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// containsRelation14338 判断一个标识符是否**出现在 FROM/JOIN 位置**。
//
// ★★ 为什么必须锚在 FROM/JOIN 位置：
//
//	① 词边界：`request_logs` 是 `request_logs_with_current_month` 的【前缀】
//	   ⇒ 朴素 substring 会把 canonical 视图（**不**被 890 删）误判 ⇒ 假阳性。
//	② 位置：仓内有大量**中文散文串**（错误消息、注释块、测试夹具）里提到
//	   `FROM public.request_logs rl` ⇒ 只判「像 SQL」会把它们全算进来
//	   ⇒ 第一版因此报了 **442 处**假阳性。
//
// ⇒ 收窄口径：**FROM/JOIN + 可选 schema 限定 + 关系名**，且词边界成立。
func containsRelation14338(text, rel string) bool {
	// ★★ **先剥 SQL 注释**再判：字符串字面量里的 SQL 注释
	//   （`-- FROM _request_logs_archive`）不是活读点
	//   ⇒ ★ 这条是本门自己的「判据语义自测」当场抓出来的
	//   （第一版没剥 ⇒ 「注释里的 ⇒ 不命中」那条用例直接失败）。
	text = stripSQLComments14338(text)
	lower := strings.ToLower(text)
	from := 0
	for {
		i := strings.Index(text[from:], rel)
		if i < 0 {
			return false
		}
		i += from
		beforeOK := i == 0 || !identChar14338(text[i-1])
		afterOK := i+len(rel) >= len(text) || !identChar14338(text[i+len(rel)])
		if beforeOK && afterOK {
			// 回看前面：可选的 schema 限定 + FROM/JOIN
			// ★ 限定名要**连标识符一起去掉**（`from public.X` ⇒ 先去 `.`，
			//   再去 `public`）⇒ 第一版只去 `.` ⇒ 留下 "public" ⇒ 判不出来
			//   （本门自己的语义自测当场抓到）。
			head := strings.TrimRight(lower[:i], " \t\n\r")
			for k := 0; k < 2; k++ {
				if !strings.HasSuffix(head, ".") {
					break
				}
				head = strings.TrimRight(head[:len(head)-1], " \t\n\r")
				j := len(head)
				for j > 0 && identChar14338(head[j-1]) {
					j--
				}
				head = strings.TrimRight(head[:j], " \t\n\r")
			}
			if strings.HasSuffix(head, "from") || strings.HasSuffix(head, "join") {
				return true
			}
		}
		from = i + 1
	}
}

// TestSection14338NoGoSQLLiteralReadsADroppedV1Table 扫描**全仓** Go 源码，
// 断言没有任何**类 SQL 的字符串字面量**引用了 890 要 DROP 的表。
//
// ★ 与既有审计工具的分工（这正是本门存在的理由）：
//
//	`cmd/tools/sql_source_indirection_audit` 扫的是「SQL 的 FROM/JOIN 片段」
//	⇒ ★ **表名写在 Go 结构体/配置里它看不见**（它还是独立 main module，
//	本门不能 import 它的词表 ⇒ 两份真相源 ⇒ 不重复它的活）。
//	⇒ 本门走**另一条路**：从 AST 里取**全部字符串字面量**，
//	   用「像 SQL」过滤，再按词边界匹配 890 的表名。
func TestSection14338NoGoSQLLiteralReadsADroppedV1Table(t *testing.T) {
	dropped := drop890Tables14338(t)

	fset := token.NewFileSet()
	var hits []string

	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == "vendor" || base == "node_modules" ||
				base == "testdata" || base == "embeddata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// ★ 测试文件不算「产品读面」：夹具与错误消息里出现关系名是合法的
		//   （第一版没排除 ⇒ 442 处假阳性）。
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// ★★ SQLite 存储不算：890 是 **PostgreSQL** 的迁移。
		//   SQLite 的指纹（PG 永远不会同时满足这两条）：
		//     · `rowid`（SQLite 专有）
		//     · `?` 占位符（PG 用 `$1`）+ `main.` schema（SQLite 专有）
		//   ⇒ 实测两类假阳性：storage/sqlite/request_log_store.go
		//     与 bg/lite_retention_worker.go（`DELETE FROM main.request_logs … ?`）。
		//   ⇒ ⚠ 不能只看目录名：bg/lite_retention_worker.go 不在 sqlite/ 目录下
		//     ⇒ 目录名单独不够，指纹单独也不够（storage/sqlite 的 SQL 没有 `main.`）
		//     ⇒ ★ 两条一起用。
		if strings.Contains(filepath.ToSlash(path), "/storage/sqlite/") {
			return nil
		}
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil // 解析不了的（生成物等）不参与判定
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil || !sqlKeywordRe14338.MatchString(val) {
				return true
			}
			// ★ SQLite 指纹排除（PG 不会同时有 `rowid`，也不会同时有 `?` + `main.`）
			if strings.Contains(strings.ToLower(val), "rowid") ||
				(strings.Contains(val, "?") && strings.Contains(strings.ToLower(val), "main.")) {
				return true
			}
			for rel := range dropped {
				if containsRelation14338(val, rel) {
					hits = append(hits, filepath.ToSlash(path)+": "+rel+"  ← "+truncate14338(val, 160))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}

	if len(hits) > 0 {
		t.Errorf("仓内有 %d 处「类 SQL 字符串字面量」引用了迁移 890 要 DROP 的 v1 表：\n  %s\n"+
			"  ⇒ 890 执行后这些读点会 **42P01**（整句失败，不是那条臂失败）。\n"+
			"  ⇒ 修法：读面切会话族（commit 5f64031cb 的形态），或从 890 的 DROP 清单里去掉。",
			len(hits), strings.Join(hits, "\n  "))
	}
}

func truncate14338(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// looksLikeV1ReadPoint14338 是**完整判据**：字面量像一条 SQL 语句
// **且**关系名出现在 FROM/JOIN 位置。
func looksLikeV1ReadPoint14338(text, rel string) bool {
	if !sqlKeywordRe14338.MatchString(text) {
		return false
	}
	return containsRelation14338(text, rel)
}

// TestSection14338RelationMatcherSemantics 钉住判据本身的语义
// （★ 量具必须先证明自己「只在该报的时候报」）。
//
// ★ 三条边界都是本轮实测踩出来的：
//
//	① 注释 / 中文散文里的关系名**不得**被判成读点
//	   （仓内有大量错误消息与注释块提到 `FROM public.request_logs rl`）；
//	② canonical 视图 `request_logs_with_current_month`（**不**被 890 删）
//	   **不得**因为前缀是 `request_logs` 而被误伤；
//	③ schema 限定（`public.`）与大小写都要正确处理。
func TestSection14338RelationMatcherSemantics(t *testing.T) {
	cases := []struct {
		name string
		text string
		rel  string
		want bool
	}{
		{"裸 FROM v1 表 ⇒ 命中", "SELECT 1 FROM request_logs", "request_logs", true},
		{"schema 限定 ⇒ 命中", "SELECT 1 FROM public._request_logs", "_request_logs", true},
		{"JOIN 位置 ⇒ 命中", "SELECT 1 FROM a JOIN _request_logs_hot b ON true", "_request_logs_hot", true},
		{"注释里的 ⇒ 不命中", "-- 历史实现读 FROM _request_logs_archive\nx := 1", "_request_logs_archive", false},
		{"散文里的 ⇒ 不命中", "错误消息：failed to read FROM public.request_logs rl", "request_logs", false},
		{"canonical 视图 ⇒ 不命中", "SELECT 1 FROM request_logs_with_current_month", "request_logs", false},
		{"canonical 视图（_ 前缀）⇒ 不命中", "SELECT 1 FROM _request_logs_with_current_month", "_request_logs", false},
		{"名字后面接标识符字符 ⇒ 不命中", "SELECT 1 FROM request_logs_extra", "request_logs", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// ★ 走**完整判据**（语句关键字 + FROM/JOIN 位置），
			//   而不是只测匹配器 —— 「散文」那一例正是靠前半段滤掉的。
			got := looksLikeV1ReadPoint14338(tc.text, tc.rel)
			if got != tc.want {
				t.Errorf("containsRelation14338(%q, %q) = %v，期望 %v", tc.text, tc.rel, got, tc.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────
// T4：生产前置核验脚本在位 + runbook §143.38 在场
// ─────────────────────────────────────────────────────────────────────────

// TestSection14338CutoverPrecheckScriptExists 钉住「执行 890 之前先跑这个」。
//
// ★ 这条是本节**唯一仓内无法提供**的东西的落地：仓里的门只能看仓里，
//
//	而「生产二进制切没切」只能从**生产上正在跑的那条语句**看出来。
//
// ⇒ ⇒ 判据取「活跃语句里还有没有引用 v1 族的」⇒ 测的是真正在跑的东西。
func TestSection14338CutoverPrecheckScriptExists(t *testing.T) {
	raw, err := os.ReadFile(precheckRelPath14338)
	if err != nil {
		t.Fatalf("read %s: %v", precheckRelPath14338, err)
	}
	body := string(raw)

	for _, needle := range []string{
		"pg_stat_statements",
		"auto_route_selections_all",
		"_request_logs",
		"still_references_v1",
		"禁止执行 890",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("前置核验脚本缺 %q ⇒ 它判不出「生产二进制切没切」", needle)
		}
	}

	// ★ 只读性：脚本里不得出现写/DDL 关键字（生产护栏会直接拒）。
	// ★★ **必须先剥注释** —— 否则脚本文件头里那句「890 会 DROP 整个 v1 族」
	//   就会被当成 DDL 判红（★ 这就是「判据可被注释满足」那条坑，
	//   本轮自己又犯了一次）。
	code := stripSQLComments14338(body)
	for _, forbidden := range []string{
		"INSERT INTO", "UPDATE ", "DELETE FROM", "CREATE ", "DROP ", "ALTER ", "TRUNCATE",
	} {
		if strings.Contains(strings.ToUpper(code), forbidden) {
			t.Errorf("前置核验脚本里出现 %q ⇒ 它不是只读的 ⇒ 生产护栏会拒绝执行", forbidden)
		}
	}
}

func TestSection14338RunbookHasSection(t *testing.T) {
	raw, err := os.ReadFile(runbookRelPath14338)
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}
	body := string(raw)
	for _, needle := range []string{
		"§143.38",
		"334,669",
		"still_references_v1",
		"禁止执行 890",
		"5f64031cb",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("runbook 缺 %q", needle)
		}
	}
}
