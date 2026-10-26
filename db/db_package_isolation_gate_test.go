package db

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

// db_package_isolation_gate_test.go —— **db 包不得直连共享库做破坏性 DDL** 的静态棘轮。
//
// ## 它守的是什么（2026-10-21 R78 实测）
//
// R78 轮实测：`go test ./db/` 会把形态库洗成玩具形态 ——
// 7 张产品表被 DROP、12 张表掉列（433 列）、4 张表被换成 3 列玩具形态，
// CASCADE 带走 25 视图 + 5 物化视图 + 103 索引 + 5 序列。
// 后果不是「库脏了」，是**别的门全假红**（relation does not exist / 列数不对）。
//
// 根因不是某一个人写错，而是**形态测量与包级测试共用一个库**。
// 修法有两层：
//
//	① 行为层：破坏性门自己带一次性空库（db/scratch_db_test.go）—— 已落地。
//	② 静态层（本文件）：**新写的破坏性门不许再直连共享库** —— 本文件守这条。
//
// ## 为什么用 AST 而不是 grep
//
// 破坏性判据必须落在**字符串字面量**上（SQL 都在字面量里），
// 而注释里的 SQL 一律不算（db/db_omnifree_test.go 的破坏块就在注释里，
// 且该门顶层 t.Skip ⇒ 从不执行 ⇒ 不该被算成破坏源）。
// ⇒ 用 go/parser 只取 *ast.BasicLit（token.STRING），注释天然被排除。
//
// ## 量具自检（必须存在，否则本门恒绿）
//
// TestDbPackageIsolationScannerDetectsHistoricalPattern 用**历史形态的源码**
// （直连共享 DSN + DROP 具名产品表）验证扫描器确实会命中；
// 同一个测试里还有**负对照**（走 scratch helper 的写法必须不被命中）。
// 扫描器一旦失灵（正则写错、AST 形状认错），这两条先红。

// dbIsolationExemptions：静态棘轮的**显式豁免**名单。
//
// 每条豁免必须写清理由，且理由要能回答「它为什么不会污染形态库」。
// 数量有上限（<= 3）：豁免是例外机制，不是默认机制；名单变大就是信号。
var dbIsolationExemptions = map[string]string{
	"db_750_ensure_realdb_test.go": "只 CREATE/DROP **自己建的探测分区**" +
		"（ensure_usage_facts_daily_partition 建 → DETACH → DROP ⇒ net zero），" +
		"且它需要产品表 usage_facts 在场 ⇒ 不能进空 scratch 库",
	"db_751_tz_pin_realdb_test.go": "同上（db_750 的时区孪生）：只动自己的探测分区，net zero",
	"request_status_projection_realdb_test.go": "只 DROP/CREATE **自己的 rs160 专用 schema**，" +
		"从不碰 public ⇒ 形态库的 public 一字不动",
}

// dbIsolationExemptionCeiling 豁免数量上限（是上限，不是精确 pin —— 精确 pin 必然腐烂）。
const dbIsolationExemptionCeiling = 3

// 破坏性 DDL 的字面量形态。
//
// ⚠ `DROP TABLE` 一律算破坏性，**包括** `DROP TABLE IF EXISTS %s` 这种
// 「名字来自变量」的写法 —— 静态判据判不出那个变量指向产品表还是玩具表，
// 所以宁可多报（豁免有显式名单兜着），不可漏报。
var dbIsolationDestructivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bDROP\s+TABLE\b`),
	regexp.MustCompile(`(?i)\bDROP\s+(VIEW|MATERIALIZED\s+VIEW|SCHEMA|FUNCTION|PROCEDURE)\b`),
	regexp.MustCompile(`(?i)\bTRUNCATE\b`),
	regexp.MustCompile(`(?i)\bCREATE\s+TABLE(\s+IF\s+NOT\s+EXISTS)?\s+public\.`),
	regexp.MustCompile(`(?i)\bCREATE\s+(OR\s+REPLACE\s+)?(MATERIALIZED\s+)?VIEW\s+public\.`),
}

// dbIsolationScratchHelpers 本仓提供的「一次性空库」入口。
// 破坏性门必须走它们之一（这正是「库要假、表名要真」的落点）。
var dbIsolationScratchHelpers = map[string]bool{
	"scratchPool":   true,
	"scratchDSNFor": true,
	"newGuardEnv":   true,
}

// dbIsolationConnectionCalls 开库调用的形状（按**包**限定，避免把任意 New 当开库）。
var dbIsolationConnectionCalls = map[string][]string{
	"pgxpool": {"New", "NewWithConfig"},
	"pgx":     {"Connect"},
	"sql":     {"Open"},
	"db":      {"Open"},
}

// dbIsolationCreateDatabase 识别「自带一次性库」的老写法。
var dbIsolationCreateDatabase = regexp.MustCompile(`(?i)\bCREATE\s+DATABASE\b`)

// sharedDSNVarNames 读出「共享 DSN 环境变量名」全集。
//
// 单一事实源 = scripts/audit/datasource_env.sh（R69 轮建的权威清单），
// 本门不另抄一份 —— 人抄的清单必然漏（那个脚本的文件头就是这么写的）。
func sharedDSNVarNames(t *testing.T) map[string]bool {
	t.Helper()
	const scriptPath = "../scripts/audit/datasource_env.sh"
	src, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("读不到权威 DSN 清单 %s: %v（清单搬家了 ⇒ 本门必须同步指路）", scriptPath, err)
	}
	names := map[string]bool{}
	re := regexp.MustCompile(`(?m)^export ([A-Z0-9_]+)="\$__datasource_dsn"$`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		names[m[1]] = true
	}
	if len(names) < 20 {
		t.Fatalf("从 %s 只解析出 %d 个 DSN 变量名（预期 40 个上下）⇒ 清单格式变了，本门的解析器要跟着改",
			scriptPath, len(names))
	}
	return names
}

// dbIsolationScan 是一个测试文件的扫描结果。
type dbIsolationScan struct {
	file         string
	destructive  []string // 命中的破坏性字面量（截断到 90 字符）
	sharedConns  []string // 「从共享 DSN 变量直连」的形如 pgxpool.New(ctx, dsn)
	usesScratch  bool     // 是否调用了 scratch helper
	createsDB    bool     // 是否自己 CREATE DATABASE（自带一次性库的老写法）
	opensDB      bool     // 是否真的开库连接
	sharedDSNUse []string // 读到的共享 DSN 变量名
}

// dbTestSourceFiles 列出 db 包自己的测试文件（递归，跳过 testdata）。
func dbTestSourceFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(info.Name(), "_test.go") {
			out = append(out, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 db 包测试文件失败: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("没找到任何 *_test.go ⇒ 扫描范围出了问题（门会恒绿）")
	}
	return out
}

// scanDbTestSource 扫一个 Go 源码，产出破坏性判据的读数。
//
// 只认**字符串字面量**（SQL 都在字面量里），注释天然被排除。
func scanDbTestSource(t *testing.T, file string, src []byte, shared map[string]bool) dbIsolationScan {
	t.Helper()
	out := dbIsolationScan{file: file}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", file, err)
	}

	// 共享 DSN 变量的持有者：x := os.Getenv("TEST_DATABASE_URL")
	holders := map[string]bool{}
	ast.Inspect(parsed, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "os" || sel.Sel.Name != "Getenv" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if !shared[name] {
			return true
		}
		out.sharedDSNUse = append(out.sharedDSNUse, name)
		for _, lhs := range assign.Lhs {
			if id, ok := lhs.(*ast.Ident); ok {
				holders[id.Name] = true
			}
		}
		return true
	})

	ast.Inspect(parsed, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BasicLit:
			if node.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(node.Value)
			if err != nil {
				return true
			}
			// 自带一次性库的老写法（CREATE DATABASE <name>）。
			if dbIsolationCreateDatabase.MatchString(text) {
				out.createsDB = true
			}
			for _, re := range dbIsolationDestructivePatterns {
				if re.MatchString(text) {
					snippet := strings.Join(strings.Fields(text), " ")
					if len(snippet) > 90 {
						snippet = snippet[:90] + "…"
					}
					out.destructive = append(out.destructive, snippet)
					break
				}
			}
		case *ast.CallExpr:
			// scratch helper？
			if id, ok := node.Fun.(*ast.Ident); ok && dbIsolationScratchHelpers[id.Name] {
				out.usesScratch = true
			}
			// 开库连接？
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkgID, ok := sel.X.(*ast.Ident); ok {
				for _, m := range dbIsolationConnectionCalls[pkgID.Name] {
					if sel.Sel.Name == m {
						out.opensDB = true
					}
				}
			}
			// pgxpool.New(ctx, dsn) / pgx.Connect(ctx, dsn)，dsn 来自共享变量？
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || (pkg.Name != "pgxpool" && pkg.Name != "pgx") {
				return true
			}
			if sel.Sel.Name != "New" && sel.Sel.Name != "Connect" {
				return true
			}
			for _, arg := range node.Args {
				id, ok := arg.(*ast.Ident)
				if !ok || !holders[id.Name] {
					continue
				}
				out.sharedConns = append(out.sharedConns,
					pkg.Name+"."+sel.Sel.Name+"(…, "+id.Name+")")
			}
		}
		return true
	})
	return out
}

// TestDbPackageNoDestructiveTestHitsSharedDB 是本门的主判据。
//
// 规则：db 包里**含破坏性 DDL 字面量**的测试文件，
// ① 不得从共享 DSN 环境变量直连（pgxpool.New / pgx.Connect）；
// ② 必须走 scratch helper（自己带一次性空库）。
// 唯一的例外是 dbIsolationExemptions 里写明理由的那几条。
func TestDbPackageNoDestructiveTestHitsSharedDB(t *testing.T) {
	shared := sharedDSNVarNames(t)
	files := dbTestSourceFiles(t)

	usedExemptions := map[string]bool{}
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", path, err)
		}
		scan := scanDbTestSource(t, path, src, shared)
		if len(scan.destructive) == 0 {
			continue
		}
		if !scan.opensDB {
			// 这个文件根本不开库 ⇒ 它手里的 DDL 字面量是**迁移脚本文本**，
			// 不是会被执行的语句（db_auto_route_selections_hot_test.go 等四家）。
			continue
		}
		base := filepath.Base(path)
		if reason, ok := dbIsolationExemptions[base]; ok {
			usedExemptions[base] = true
			if strings.TrimSpace(reason) == "" {
				t.Errorf("豁免 %s 没有写理由 ⇒ 豁免必须能回答「它为什么不会污染形态库」", base)
			}
			continue
		}
		isolated := scan.usesScratch || scan.createsDB
		if !isolated {
			t.Errorf("%s 含破坏性 DDL（%s）但没有隔离（既没走 scratch helper，也没自己建一次性库）⇒ "+
				"它会直连 DSN 指向的那个库并把它洗成玩具形态",
				path, strings.Join(scan.destructive, " | "))
		}
		for _, conn := range scan.sharedConns {
			t.Errorf("%s 含破坏性 DDL（%s）且从共享 DSN 变量直连（%s）⇒ "+
				"破坏性门必须自己带一次性空库（库要假、表名要真）",
				path, strings.Join(scan.destructive, " | "), conn)
		}
	}

	// 豁免名单自身的纪律：条目必须存在、理由非空、数量不超上限。
	for base := range dbIsolationExemptions {
		found := false
		for _, path := range files {
			if filepath.Base(path) == base {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("豁免名单里的 %s 已不存在（文件被删/改名）⇒ 豁免要跟着删，别留成僵尸条目", base)
		}
	}
	for base := range usedExemptions {
		_ = base
	}
	if len(dbIsolationExemptions) > dbIsolationExemptionCeiling {
		t.Errorf("豁免名单有 %d 条，超过上限 %d ⇒ 豁免是例外机制，名单变大就是信号",
			len(dbIsolationExemptions), dbIsolationExemptionCeiling)
	}
}

// TestDbPackageIsolationScannerDetectsHistoricalPattern 是**量具自检**。
//
// 正对照：历史形态的写法（从共享 DSN 直连 + DROP 具名产品表）必须被命中。
// 负对照：走 scratch helper 的写法必须**不被**命中。
//
// 没有这一条，扫描器一旦失灵（正则写错、AST 形状认错），
// 主判据会变成恒绿门 —— 而恒绿门的唯一症状就是「看起来一切正常」。
func TestDbPackageIsolationScannerDetectsHistoricalPattern(t *testing.T) {
	shared := map[string]bool{"TEST_DATABASE_URL": true}

	positive := `package db

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHistoricalShape(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		"DROP TABLE IF EXISTS public.report_snapshots"); err != nil {
		t.Fatal(err)
	}
}
`
	scan := scanDbTestSource(t, "positive_control.go", []byte(positive), shared)
	if len(scan.destructive) == 0 {
		t.Fatal("量具自检失败（正对照）：历史形态的 DROP 具名产品表没有被识别 ⇒ " +
			"扫描器的破坏性判据失灵 ⇒ 主判据是恒绿门")
	}
	if len(scan.sharedConns) == 0 {
		t.Fatal("量具自检失败（正对照）：从共享 DSN 变量的 pgxpool.New 直连没有被识别 ⇒ " +
			"扫描器的直连判据失灵 ⇒ 主判据是恒绿门")
	}
	if scan.usesScratch {
		t.Fatal("量具自检失败（正对照）：历史形态里没有 scratch helper，却报成用了 ⇒ 读数不可信")
	}

	negative := `package db

import (
	"context"
	"testing"
)

func TestFixedShape(t *testing.T) {
	pool := scratchPool(t)
	if _, err := pool.Exec(context.Background(),
		"DROP TABLE IF EXISTS public.report_snapshots"); err != nil {
		t.Fatal(err)
	}
}
`
	scan = scanDbTestSource(t, "negative_control.go", []byte(negative), shared)
	if len(scan.destructive) == 0 {
		t.Fatal("量具自检失败（负对照）：破坏性字面量没被识别 ⇒ 破坏性判据失灵")
	}
	if !scan.usesScratch {
		t.Fatal("量具自检失败（负对照）：scratchPool 调用没被识别 ⇒ helper 判据失灵")
	}
	if len(scan.sharedConns) != 0 {
		t.Fatal("量具自检失败（负对照）：没有共享 DSN 直连，却报出 " + strings.Join(scan.sharedConns, ","))
	}
}
