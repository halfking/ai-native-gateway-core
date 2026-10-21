package ursm_test

// R86 轮的门：**「事实源承载 ⇒ 免于被分类」这个结构盲区**。
//
// # 这道门补的是哪块缺口
//
// R71 轮那道登记册门（`pg_test_env_registry_test.go`）的判据 ① 是：
//
//	「被测试读到、且**不在事实源**的名字，必须逐条登记」；
//	判据 ② 是「登记册里的名字必须仍被读到」。
//
// ★★ 而它的**差集完备**判据里有一行：
//
//	if _, ok := ds[name]; ok { continue }   // 事实源承载的 dsn 类，不进登记册
//
// ⇒⇒ **事实源承载的名字，一个都不进登记册** ⇒⇒ **也就一个都不被分类** ⇒⇒
// ⇒ **没有任何门会问「这条事实源承载的名字，其实是不是该 dsn_exclusive」**。
//
// ⇒ R85 轮那条 `GATEWAY_CANONICAL_TEST_DSN` 是**手工读文件**才发现的
// ⇒ ⇒ 这不是「一次手滑」，是**结构盲区**：盲区会稳定地产出下一条。
//
// # R86 轮的实测：这盲区里已经躺着多少条
//
// 用下面的判据（收敛过程见文件尾「判据的收敛史」）全仓实测：
//
//	**真·靶子错配 = 7 例**（R86 轮修掉 6 例，登记 1 例），
//	形态分三类：
//	  · **死腿**：事实源给的值**必被消费腿拒绝** ⇒ 事实源档下那条腿从来没跑过；
//	  · **假红**：消费腿用 t.Fatal 拒绝 ⇒ 切事实源档下门禁变红（红的不是产品缺陷）；
//	  · **数据事故级**：消费腿**没有任何库名守卫** ⇒ 事实源给的值会让破坏性 SQL
//	    打到产品库（`TRUNCATE credentials, providers CASCADE`）。
//
// ⇒⇒ 所以这道门的判据不是「名字有没有被分类」，而是
// **「事实源给的这个值，这条腿到底会不会用」**。
//
// # 判据（两条 + 一条自检）
//
//	① **新增红**：探测到、但**不在**台账 `dsn_target_mismatch_ledger.tsv` 里的错配 ⇒ 红
//	   （新增 ⇒ 要人判：是移出事实源？是给腿换专用名？还是补守卫？）。
//	② **腐化红**：台账里、但**探测不到**的行 ⇒ 红（台账腐化 ⇒ 删行）。
//	   ⇒ ⇒ 合起来就是**棘轮**：台账只减不增。
//	③ **探测器自检**：拿一个真导出的名字造夹具 ⇒ 探测器必须判它是错配；
//	   拿一个**没有守卫**的夹具 ⇒ 探测器必须**不**判它。⇒ 否则门是恒绿门。
//
// # 判据的机器可判形态（R86 轮踩过的坑都写在这里）
//
//	★ **「真读」**：排除「名字只出现在字面量里」——
//	  R63 轮的老坑（按名命中多是假阳性）在守卫层**重演了一次**。
//	  ⇒ 这里用 AST 口径：**只有 `os.Getenv` / `os.LookupEnv` 的实参**才算「读」。
//	★ **「读形态守卫」**：库名被**读取**才算守卫（`current_database()` 或读 `.Database`）；
//	  而 `cfg.ConnConfig.Database = x`（**把连接改指**到自己建的库）是**好形态** ——
//	  那条腿自带隔离，事实源的 DSN 只是**引导连接** ⇒ **不是错配**。
//	  ⇒ ★★ R86 轮第一版判据把「赋值」当「守卫」⇒ 11 个候选里 **9 个是假阳性**。
//	★ **「自建库」**：`CREATE DATABASE` ⇒ 自带隔离 ⇒ 排除。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	r86LedgerPath = "dsn_target_mismatch_ledger.tsv"
	r86DSPath     = "../../scripts/audit/datasource_env.sh"
)

// r86ReCurrentDB 认「读当前库名」这句 SQL（R86 轮）。
var r86ReCurrentDB = regexp.MustCompile(`current_database\s*\(`)

// r86ReCreateDB 认「这条腿自己建库」⇒ 自带隔离 ⇒ 事实源的 DSN 只是引导连接。
var r86ReCreateDB = regexp.MustCompile(`(?i)CREATE\s+DATABASE`)

// r86Pair 一条「事实源承载 × 消费腿按库名做决定」的错配对。
type r86Pair struct {
	Name string
	File string
}

func (p r86Pair) key() string { return p.Name + "\t" + p.File }

// r86ReadsDBName 判断这条腿**按库名做决定**（读形态守卫）。
//
// ★ 与「把连接改指到自建库」严格区分：
//
//	读形态：current_database() / 读 cfg.ConnConfig.Database ⇒ 守卫；
//	赋值形态：cfg.ConnConfig.Database = x ⇒ **改指**，不是守卫。
func r86ReadsDBName(t *testing.T, path string, src string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// 解析失败 ⇒ 不能当成有守卫（保守：少报；否则门会因为语法问题红一片）。
		return false
	}
	// ① 先标出所有「赋值左值」⇒ 它们是改指，不是读。
	assigned := map[ast.Node]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || as.Tok != token.ASSIGN {
			return true
		}
		for _, lhs := range as.Lhs {
			assigned[lhs] = true
		}
		return true
	})
	// ② 再找「被读取的 .Database」。
	read := false
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Database" {
			return true
		}
		if assigned[n] {
			return true // 改指 ⇒ 不是守卫
		}
		read = true
		return true
	})
	if read {
		return true
	}
	// ③ current_database() 住在 SQL 字符串里 ⇒ 走文本口径（先剥注释）。
	return r86ReCurrentDB.MatchString(stripGoComments(src))
}

// r86CandidatePairsOf 判定「一个测试文件 × 一个事实源导出名」是不是错配对。
//
// ★ 三个必要条件同时成立才算（缺一不可）：
//
//	① 该腿**真读**这个名字（os.Getenv / os.LookupEnv 的实参）；
//	② 该腿**按库名做决定**（读形态守卫）；
//	③ 该腿**不自建库**（自建库 ⇒ 自带隔离 ⇒ 事实源的 DSN 只是引导连接）。
func r86CandidatePairsOf(t *testing.T, path string, rel string, src string, exported map[string]bool) []r86Pair {
	t.Helper()
	if !opensPostgresConn(src) {
		return nil
	}
	if r86ReCreateDB.MatchString(stripGoComments(src)) {
		return nil // 自带隔离
	}
	if !r86ReadsDBName(t, path, src) {
		return nil
	}
	var out []r86Pair
	for _, n := range r71ScanFile([]byte(src)) {
		if exported[n] {
			out = append(out, r86Pair{Name: n, File: rel})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].File < out[j].File
	})
	return out
}

// r86ScanRepo 全仓扫出错配对。
func r86ScanRepo(t *testing.T, root string, exported map[string]bool) []r86Pair {
	t.Helper()
	var out []r86Pair
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea", "dist", "build":
				return filepath.SkipDir
			}
			// ★ 审计门自身不在范围内（自检夹具里故意写着假 DSN 名字；R69 门就栽在这）。
			if d.Name() == "ursmcheck" && filepath.Base(filepath.Dir(path)) == "scripts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(raw)
		rel, _ := filepath.Rel(root, path)
		out = append(out, r86CandidatePairsOf(t, path, rel, src, exported)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// r86ParseLedger 解析台账：每行 `name<TAB>file<TAB>verdict`。
func r86ParseLedger(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("read %s: %v", path, err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			t.Errorf("%s:%d 格式不对（要 name<TAB>file<TAB>verdict）：%q", path, i+1, line)
			continue
		}
		if strings.TrimSpace(parts[2]) == "" {
			t.Errorf("%s:%d 的判词是空的 —— 台账必须逐条写明为什么是错配", path, i+1)
		}
		out[parts[0]+"\t"+parts[1]] = parts[2]
	}
	return out
}

// ── 门 ①：新增错配 ⇒ 红 ──────────────────────────────────────────────────────

func TestDatasourceEnvTargetGuardNoUnregisteredMismatch(t *testing.T) {
	root := filepath.Join("..", "..")
	exported := r71ParseDatasourceExports(t, r86DSPath)
	// ★ 扫描口径自检：事实源的 export 掉下来就是扫描器退化了，门会变成恒绿门。
	if len(exported) < 30 {
		t.Fatalf("事实源 %s 只解析出 %d 个 export ⇒ 扫描口径失效（门会恒绿）", r86DSPath, len(exported))
	}

	candidates := r86ScanRepo(t, root, exported)
	ledger := r86ParseLedger(t, r86LedgerPath)

	// ① 探测到但不在台账里 ⇒ 红。
	var unregistered []r86Pair
	for _, p := range candidates {
		if _, ok := ledger[p.key()]; !ok {
			unregistered = append(unregistered, p)
		}
	}
	sort.Slice(unregistered, func(i, j int) bool {
		if unregistered[i].Name != unregistered[j].Name {
			return unregistered[i].Name < unregistered[j].Name
		}
		return unregistered[i].File < unregistered[j].File
	})
	for _, p := range unregistered {
		t.Errorf("事实源承载的 %q 被 %s 消费，而那条腿**按库名做决定**（读形态守卫）⇒ "+
			"事实源给的值这条腿一定会拒绝（死腿/假红/数据事故三选一）⇒ "+
			"要么把该名字移出事实源（专用值）、要么给这条腿换专用名、要么补库名守卫；"+
			"若确认是错配，请登记进 %s 并写明判词", p.Name, p.File, r86LedgerPath)
	}

	// ② 台账里但探测不到 ⇒ 红（台账腐化）。
	var stale []string
	for key := range ledger {
		found := false
		for _, p := range candidates {
			if p.key() == key {
				found = true
				break
			}
		}
		if !found {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("台账 %s 里的 %q 现在探测不到了 ⇒ 台账腐化（请删行；该错配已消失）", r86LedgerPath, s)
	}

	t.Logf("事实源导出 %d 个名字；探测到 %d 条错配对，台账登记 %d 条（未登记 %d、腐化 %d）",
		len(exported), len(candidates), len(ledger), len(unregistered), len(stale))
}

// ── 门 ②：探测器自检（防恒绿门）─────────────────────────────────────────────

func TestR86TargetGuardDetectorSelfCheck(t *testing.T) {
	exported := r71ParseDatasourceExports(t, r86DSPath)
	if len(exported) < 30 {
		t.Fatalf("事实源只解析出 %d 个 export ⇒ 自检前提不成立", len(exported))
	}
	// 取一个**真的被事实源导出**的名字做夹具 ⇒ 探测器必须只在「有守卫」时才判错配。
	var name string
	for n := range exported {
		name = n
		break
	}

	dir := t.TempDir()
	// 夹具 ①：真读 + 读形态守卫（current_database()）+ 不自建库 ⇒ **必须**被判成错配对。
	withGuard := "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n" +
		"func f() {\n\t_ = os.Getenv(\"" + name + "\")\n" +
		"\t_ = pgxpool.New\n" +
		"\t_, _ = os.ReadFile(\"SELECT current_database()\")\n}\n"
	p1 := filepath.Join(dir, "with_guard_test.go")
	if err := os.WriteFile(p1, []byte(withGuard), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// 夹具 ②：真读 + **没有**守卫 ⇒ **不得**被判成错配对。
	noGuard := "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n" +
		"func f() {\n\t_ = os.Getenv(\"" + name + "\")\n" +
		"\t_ = pgxpool.New\n}\n"
	p2 := filepath.Join(dir, "no_guard_test.go")
	if err := os.WriteFile(p2, []byte(noGuard), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// 夹具 ③：真读 + 守卫，但**自建库** ⇒ 不得被判成错配对（自带隔离）。
	selfDB := "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n" +
		"func f() {\n\t_ = os.Getenv(\"" + name + "\")\n" +
		"\t_ = pgxpool.New\n" +
		"\t_, _ = os.ReadFile(\"SELECT current_database()\")\n" +
		"\t_, _ = os.ReadFile(\"CREATE DATABASE my_own_scratch\")\n}\n"
	p3 := filepath.Join(dir, "self_db_test.go")
	if err := os.WriteFile(p3, []byte(selfDB), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got := r86CandidatePairsOf(t, p1, "fixture_with_guard_test.go", withGuard, exported)
	if len(got) != 1 || got[0].Name != name {
		t.Fatalf("探测器没认出「真读 + 读形态守卫」的夹具（应判 1 条，实得 %d 条：%v）⇒ 探测器失效（门会恒绿）", len(got), got)
	}
	if got := r86CandidatePairsOf(t, p2, "fixture_no_guard_test.go", noGuard, exported); len(got) != 0 {
		t.Fatalf("探测器把「没有守卫」的夹具也判成错配（%v）⇒ 判据过宽", got)
	}
	if got := r86CandidatePairsOf(t, p3, "fixture_self_db_test.go", selfDB, exported); len(got) != 0 {
		t.Fatalf("探测器把「自建库（自带隔离）」的夹具也判成错配（%v）⇒ 判据过宽", got)
	}
}
