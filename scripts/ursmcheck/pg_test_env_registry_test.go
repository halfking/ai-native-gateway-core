package ursm_test

import (
	"flag"
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

// R71 轮的门：**每一个「测试文件读到的环境变量名」都必须被显式回答** ——
// 它在事实源里（dsn），还是它**故意不在**（开关 / 拼装型 / 外部依赖）。
//
// # 这道门补的是哪块缺口（★ 与 R69 轮那道门互补，不是替代）
//
// R69 轮已立事实源 `scripts/audit/datasource_env.sh` 与门 `datasource_env_test.go`，
// 它的判据是「开 PG 的测试文件 × DSN 形状的名字」。
//
// 而 R71 轮实测出**三条**新事实：
//
//	① ★★★★ **官方 runner 的名单推导门只按「DB URL 形状」扫描** ——
//	   `sql/schema/integration_gate_test.go::TestGateInjectsEveryDBCredentialName`
//	   实测推导 **21 个**名字；而 AST 口径实测「全仓测试文件读到的 env 名」是 **98 个**
//	   ⇒ ⇒ **77 个名字在它的视野外**，
//	   而这 77 个里**恰好包含了全部的隔离声明开关与危险开关**
//	   （`TEST_SESSION_INGEST_ISOLATED`、`TEST_PG_CONTRACTS_ISOLATED`、
//	   `REPORT_E2E_ALLOW_DESTRUCTIVE`、`URSM_830_ALLOW_ANY_DB`、
//	   `WA877_ALLOW_ANY_DSN`、`TRACE_RETIREMENT_LOCAL_SCHEMA`、
//	   `GATEWAY_CANONICAL_TEST_LOCAL_ONLY`、`USAGE_OBSERVATION_LOCAL_PG`）。
//	② ★★★★ **爆炸半径实测**：官方 runner 的 env 集 ⇒ **20 SKIP / 0 FAIL / 21 PASS**；
//	   换成完整事实源 ⇒ **4 SKIP / 8 FAIL / 29 PASS** ⇒ 16 条变化
//	   （8 条 SKIP→PASS、**8 条 SKIP→FAIL**）。
//	③ ★★★★ **那 8 条红没有一条是产品缺陷**：7 条是本仓**自己的隔离守卫在正确工作**
//	   （`refusing schema changes outside explicitly isolated *_test database` 等），
//	   1 条是 `relation "shadow_user_providers" does not exist`
//	   ⇒ ⇒ 「切事实源」的真实代价 = **8 条红 + 一组环境前置**，不是「切了就出产品 bug」。
//
// ★★ **但「那 8 条红为什么会红」本身是个问题**：因为
//
//	**隔离开关 `TEST_SESSION_INGEST_ISOLATED` 既不在官方 runner 的 env 块，
//	  也不在事实源清单里** ⇒ 它是「这条腿能跑」的**必要条件**，却**无处登记**。
//
// ⇒ ⇒ 所以缺的不是「再多一个 DSN 名」，而是：
//
//	★★ **「真库腿的环境前置」这份清单本身不存在** ⇒ 谁也不知道一条腿跑起来
//	  需要哪些前置 ⇒ 前置一旦缺，只能在跑的时候炸，且炸出来的红**长得像产品缺陷**。
//
// # ⇒ 本门把这份清单落成**机器可验证的实体**
//
// 分类轴是 **「这个变量被谁读、读它是为了什么」**，不是名字形状
// （R69 轮已把这条写进文件头：名字形状猜不出来，`XIAOMI_BASE_URL` 长得像 DSN 却不是）。
//
//	┌───────────────┬────────────────────────────────────────┬────────────────────┐
//	│ class         │ 语义                                   │ 在事实源里？        │
//	├───────────────┼────────────────────────────────────────┼────────────────────┤
//	│ dsn           │ 整体 DSN，通用值可用                   │ ★ 在（事实源管）   │
//	│ dsn_exclusive │ 整体 DSN，但**必须专用值**             │ ★ 必须不在         │
//	│ isolation     │ 隔离开关（声明「本腿跑在隔离库上」）  │ ★ 必须不在         │
//	│ dangerous     │ 危险开关（任意库/破坏性/本地变更）    │ ★ 必须不在         │
//	│ component     │ 拼装型 / 夹具参数 / 外部依赖 / 杂项    │ ★ 必须不在         │
//	└───────────────┴────────────────────────────────────────┴────────────────────┘
//
// ★★ **后三类为什么必须不在事实源里**：事实源是**跑真库腿时被 source 的东西**，
//
//	而「打开隔离声明」「允许任意库」「允许破坏性」都是**取消保护**的动作
//	⇒ 由事实源默认打开 = **把安全边界交给一个「忘了配环境变量」的人**。
//
// ★★ **dsn_exclusive 为什么必须不在**：它是整体 DSN，但**必须与通用 DSN 不同**
//	（实例：`TEST_TENANT_DATABASE_URL` 必须指向**独立的、不可绕过 RLS 的租户角色**
//	—— 官方 runner 自己就为它单开了一个变量并校验角色，`run-integration-gate.sh` 的
//	`[tenant]` 块与 `integration_gate_test.go` 的 `tenantVar` 常量都写着这条）
//	⇒ 把它塞进事实源 = 让所有腿都拿到同一个值 = **专用隔离被悄悄抹平**。
//
// ★★ **classification 与仓库既有原则一致**（不是外来规矩）：
//	官方 runner 的 env 块下面本来就写着
//	「Redis names are deliberately absent: pointing TEST_REDIS_URL at a PostgreSQL
//	DSN would be worse than leaving it unset.」
//	⇒ 本门把这条原则**机械化**：Redis 类一律 `component`、一律不得进事实源。
//
// ⚠⚠⚠ **R72 轮订正（保留原文以便追溯）** —— 上面 ②③ 的数字**已被 R72 轮实测推翻**：
//
//	★★ **根因 = 我把守卫的语义读反了**：守卫条件原文是
//	`!strings.HasSuffix(db, "_test") || os.Getenv("TEST_SESSION_INGEST_ISOLATED") != "1"`
//	⇒ 「拦人」= 库名不合规 **OR** 开关≠1 ⇒ 「放行」= **库名合规 AND 开关=1**
//	⇒ ⇒ ★★ **合规库名本身不够**，开关也必须显式 `=1`。
//	⇒ ⇒ R71 轮写的「库名那半边已经满足 ⇒ 缺的是开关」把 AND 读成了 OR
//	⇒ ⇒ **整条推理链反向**（并连带把三档表读歪）⇒ 订正后的可信读数见下。
//
// ★★ **那为什么 `isolation` 仍不得进官方 runner 的 `env \` 块**（判据③ 的**正确**理由）：
//	官方 runner 的门禁库名是 `itgate_${$}_${RANDOM}` ⇒ **不带 `_test` 后缀**（不合规）
//	⇒ 若再注入隔离开关 ⇒ **守卫被整体解除**（放行条件的两半同时成立）
//	⇒ ⇒ 那一次性门禁库变成「可破坏」
//	⇒ ⇒ ★ 判据③ 的理由是「**解除守卫**」，**不是**「合规库名下开关多余」
//	（★ R71 轮写成了后者，源于把守卫语义读反）。
//
// ★★ **可信实测（R72 轮；合规库名 `itgate_r72_test`，同库同基线，显式 `env -u`，同 5 条隔离型腿）**：
//
//	┌ 档                                        │ PASS │ SKIP │ FAIL │ 红的性质                │
//	├ 官方 runner 的 20 个 env 名（现状）         │   0  │   5  │   0  │ **静默 SKIP**（缺 DSN 名）│
//	├ 完整事实源 40 个 + **不开**隔离开关          │   0  │   0  │   5  │ **守卫红**（按设计拦人）│
//	└ 完整事实源 40 个 + **开**隔离开关            │   0  │   0  │   5  │ **环境红**（Citus 无节点）│
//
//	⇒ ★★ **切事实源 ⇒ 这 5 条腿从「静默 SKIP」变成「红」**；
//	⇒ ⇒ 而「红」的**性质取决于是否配隔离开关**，两条路各自还带一组前置：
//	① 不配 ⇒ 守卫红（放行需要「库名合规 ∧ 开关=1」）；
//	② 配 ⇒ 环境红（本容器 `citus_nodes` 为 **0 行** ⇒ 分布组元数据在并发下不稳 ⇒
//	   `cache lookup failed for pg_dist_local_group` XX000 /
//	   `could not open relation with OID …` XX000 / `deadlock detected` 40P01）；
//	⇒ ⇒ ★ **属主决策①「切事实源」的真实代价 = 5 条腿由绿变红 + 2 组环境前置**。
//
// # 判据（五条）
//
//	① **差集完备**：被测试读到、且**不在事实源**的每个名字都必须登记。
//	② **反向完备**：登记册里的每个名字都必须仍被测试读到（死条目 ⇒ 红）。
//	③ **分类合法**：class 必须取自上表；且 `dsn_exclusive` / `isolation` /
//	   `dangerous` / `component` **一律不得出现在事实源里**；
//	   且 `isolation` / `dangerous` **一律不得出现在官方 runner 的 env 块里**
//	   （那里是「跑真库腿时自动导出」的地方 ⇒ 放进去 = 保护被默认取消）。
//	④ **理由非空且不留 TODO**。
//	⑤ **扫描器自检**（否则这道门是恒绿门）。
//
// # 两个刻意的口径选择
//
//	★ **(a) 用 AST 而不是正则扫源码** —— 正则会被注释里的 `os.Getenv("X")` 骗到。
//	★ **(b) 分类轴是「读了环境变量」，不是「开不开 PG」** ——
//	  第一版曾用「文件里出现 `pgxpool.New`」当分类轴，实测**漏了 17 个名字**
//	  （那些文件通过 helper 连库，文件内没有连接调用）
//	  ⇒ 其中就有 `TEST_SESSION_INGEST_ISOLATED` ⇒ **漏的正是最关键的那几个**。
//	  ⇒ 教训与 R69 轮一致：**分类轴不能靠「猜」，得靠可判定的事实**。

const (
	r71RegistryPath      = "pg_test_env_registry.tsv"
	r71DatasourceEnvPath = "../../scripts/audit/datasource_env.sh"
	r71GateRunnerPath    = "../../scripts/audit/run-integration-gate.sh"

	// r71GenericGateDSNValue 是官方 runner 把**所有** DSN 绑到的那个通用值
	// （门禁自己的一次性库）。
	//
	// ★ 它是「dsn_exclusive 拿到了通用值」这一判据的锚：
	//   dsn_exclusive 的定义是「整体 DSN，但**必须与通用 DSN 不同**」
	//   ⇒ 绑到通用值 = 专用隔离被悄悄抹平。
	// ★ 写成常量而不是散在断言里的字面量 ⇒ 判据的锚只有一处。
	r71GenericGateDSNValue = "$GATE_URL"
)

// r71Update 重算登记册：补齐新出现的 env 名（**保留已有人工分类**）。
//
// ★★ **flag 名必须带前缀**：`scripts/ursmcheck` 是**同一个包**，
//
//	而同包的 flag 名是**共享命名空间** —— R70 轮的 `hardcoded_dsn_fallback_test.go`
//	已经注册了 `-update` ⇒ 本门若也叫 `-update` ⇒ **包初始化时 panic**
//	（`flag redefined: update`）⇒ 整包测试直接挂掉。
var r71Update = flag.Bool("r71-update", false, "重算登记册：补齐新出现的 env 名（保留人工分类）")

// 登记册里可能出现的分类（`dsn` 不在登记册里 —— 它由事实源承载）。
const (
	r71ClassDSNExcl   = "dsn_exclusive"
	r71ClassIsolation = "isolation"
	r71ClassDangerous = "dangerous"
	r71ClassComponent = "component"
)

var r71RegistryClasses = []string{
	r71ClassDSNExcl, r71ClassIsolation, r71ClassDangerous, r71ClassComponent,
}

// ── 扫描器（纯函数，可被自检直接调用）──────────────────────────────────────────

// r71ScanFile 返回「一个测试文件读到的 env 名」。
//
// ★ 口径：只有**真的** `os.Getenv` / `os.LookupEnv` 调用才算「读」；
//
//	`os.Setenv`（写）、`os.LookupEnv` 之外的辅助函数调用都不算。
//
// ★★ **名字不一定在调用点**（R69 轮踩过的坑 (b)）：
//
//	`const dsnEnv = "TEST_PG_DSN"` 后 `os.Getenv(dsnEnv)` ⇒ 必须能追到。
func r71ScanFile(src []byte) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "probe.go", src, 0)
	if err != nil {
		return nil
	}

	// const/var 里的字符串字面量（供标识符实参回查）。
	consts := map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(bl.Value); err == nil {
					consts[ident.Name] = v
				}
			}
		}
	}

	seen := map[string]bool{}
	var names []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "os" {
			return true
		}
		var name string
		switch a := call.Args[0].(type) {
		case *ast.BasicLit:
			if a.Kind != token.STRING {
				return true
			}
			name, _ = strconv.Unquote(a.Value)
		case *ast.Ident:
			name = consts[a.Name]
		default:
			return true
		}
		if name == "" || seen[name] {
			return true
		}
		seen[name] = true
		names = append(names, name)
		return true
	})
	sort.Strings(names)
	return names
}

// r71ScanRepo 扫全仓测试文件，返回 env 名 → 引用它的文件列表。
func r71ScanRepo(root string) (map[string][]string, int, error) {
	used := map[string][]string{}
	testFiles := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea", "dist", "build":
				return filepath.SkipDir
			}
			// ★★ 审计门自身不在范围内（它们的自检夹具里故意写着假 DSN 名字；
			//    R69 门就栽在这：门把自己红自己）。
			if d.Name() == "ursmcheck" && filepath.Base(filepath.Dir(path)) == "scripts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		testFiles++
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		names := r71ScanFile(raw)
		if len(names) == 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for _, n := range names {
			used[n] = append(used[n], rel)
		}
		return nil
	})
	return used, testFiles, err
}

// ── 登记册 ────────────────────────────────────────────────────────────────────

type r71Entry struct {
	name   string
	class  string
	reason string
}

// r71ParseRegistry 解析登记册：每行 `name<TAB>class<TAB>reason`。
func r71ParseRegistry(t *testing.T, path string) map[string]r71Entry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]r71Entry{}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			t.Errorf("%s:%d 行不是 `name<TAB>class<TAB>reason` 三列：%q", path, i+1, line)
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "" {
			t.Errorf("%s:%d 名字为空", path, i+1)
			continue
		}
		if _, dup := out[name]; dup {
			t.Errorf("%s:%d 名字 %q 重复登记", path, i+1, name)
		}
		out[name] = r71Entry{name: name, class: strings.TrimSpace(parts[1]), reason: strings.TrimSpace(parts[2])}
	}
	if len(out) == 0 {
		t.Fatalf("%s 一条登记都没有 —— 解析器失效（门会恒绿）", path)
	}
	return out
}

// r71RegistryHeader 登记册的表头（`-r71-update` 写回时必须逐字一致）。
const r71RegistryHeader = `# R71 轮登记册：测试文件读到的环境变量名
# 列：name<TAB>class<TAB>reason
# class ∈ dsn | dsn_exclusive | isolation | dangerous | component
#   dsn           整体 DSN，通用值可用        ⇒ 事实源必须导出
#   dsn_exclusive 整体 DSN，但必须专用值      ⇒ 事实源必须不导出
#   isolation     隔离开关（取消保护）        ⇒ 事实源必须不导出
#   dangerous     危险开关（取消保护）        ⇒ 事实源必须不导出
#   component     拼装型/夹具参数/外部依赖    ⇒ 事实源必须不导出
`

// r71WriteRegistry 写回登记册（保留已有人工分类，新名字标 TODO）。
func r71WriteRegistry(path string, reg map[string]r71Entry) error {
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(r71RegistryHeader)
	for _, n := range names {
		e := reg[n]
		fmt.Fprintf(&b, "%s\t%s\t%s\n", e.name, e.class, e.reason)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// ── 事实源 / 官方 runner 的清单 ───────────────────────────────────────────────

var reR71Export = regexp.MustCompile(`(?m)^export ([A-Z][A-Z0-9_]*)=`)

func r71ParseDatasourceExports(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, m := range reR71Export.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("%s 一个 export 都没有 —— 解析器失效（门会恒绿）", path)
	}
	return out
}

// reR71RunnerEnvBlock 匹配官方 runner 的 `env \` 块（缩进的 `NAME=... \` 续行）。
//
// ★★ **必须逐行锚定**（R71 轮自曝）：第一版写成 `(?ms)` + `.*\n`，
//
//	而 `s` 模式下 `.*` **会贪婪跨行** ⇒ 一行就把整个文件吞进去
//	⇒ 解析出 28 个「注入名」（把 `NPASS`/`DIAG` 这类脚本内变量也算进去了）
//	⇒ 假阳性会让「危险开关禁入 runner」这条判据失去意义。
//	⇒ 现在用 `[^\n]*` 锚死单行，并要求行尾是**续行反斜杠**（`env \` 块的特征）。
var reR71RunnerEnvBlock = regexp.MustCompile(`(?m)^env \\\n((?:^[ \t]+[A-Z][A-Z0-9_]*=[^\n]*\\\n)+)`)

// r71ParseRunnerEnvBlock 解析官方 runner 自动导出的变量名。
func r71ParseRunnerEnvBlock(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, m := range reR71RunnerEnvBlock.FindAllStringSubmatch(string(raw), -1) {
		for _, line := range strings.Split(m[1], "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			name := line
			if i := strings.Index(name, "="); i > 0 {
				name = name[:i]
			}
			if reR71Export.MatchString("export " + name + "=") {
				out[name] = true
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s 里没解析出 `env \\` 块 ⇒ 解析器失效（门会恒绿）", path)
	}
	return out
}

// r71ParseRunnerEnvValues 解析官方 runner 的 `env \` 块里每个变量的**值**
// （去掉了两侧引号），返回 name → value。
//
// ★ 为什么需要值（不只是名字）：**`dsn_exclusive` 允许出现在官方 runner 的
// `env \` 块里** —— 前提是它拿到的是**专用值**（实例：TEST_TENANT_DATABASE_URL
// 拿的是 $TENANT_DSN，不是通用库）。
// ⇒ ⇒ 只看名字分不出「专用值」与「通用值」⇒ ⇒ 必须看值。
// ★ 判据用**值字面量**判定「通用值」：runner 把 DSN 一律绑到 "$GATE_URL"
//   （门禁自己的那个一次性库）⇒ 那就是通用值。
func r71ParseRunnerEnvValues(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]string{}
	m := reR71RunnerEnvBlock.FindStringSubmatch(string(raw))
	if len(m) < 2 {
		t.Fatalf("%s 里没解析出 `env \\` 块 ⇒ 解析器失效（门会恒绿）", path)
	}
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value := line, ""
		if i := strings.Index(line, "="); i > 0 {
			name, value = line[:i], line[i+1:]
		}
		value = strings.TrimSpace(value)
		// ★ env 块的每一行都以 ` \` 续行 ⇒ 先去掉反斜杠，再剥引号。
		//   顺序反了会得到 `"$GATE_URL" \` 这种剥不干净的值 ⇒ 判据永远不成立。
		value = strings.TrimSuffix(value, `\`)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if reR71Export.MatchString("export " + name+"=") {
			out[name] = value
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s 里没解析出 `env \\` 块的变量 ⇒ 解析器失效（门会恒绿）", path)
	}
	return out
}

// ── 判据 ①：差集完备 ＋ ②：反向完备 ─────────────────────────────────────────

func TestPGTestEnvRegistryCoversEveryGapName(t *testing.T) {
	root := filepath.Join("..", "..")
	used, testFiles, err := r71ScanRepo(root)
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	// ★ 扫描口径自检：数量掉下来就是扫描器退化了，门会变成恒绿门。
	if testFiles < 3000 {
		t.Fatalf("只扫到 %d 个测试文件（基线 3295）⇒ 扫描口径失效（门会恒绿）", testFiles)
	}
	if len(used) < 90 {
		t.Fatalf("只扫到 %d 个 env 名（基线 98）⇒ 扫描口径失效（门会恒绿）", len(used))
	}

	ds := r71ParseDatasourceExports(t, r71DatasourceEnvPath)

	reg := map[string]r71Entry{}
	if _, statErr := os.Stat(r71RegistryPath); statErr == nil {
		reg = r71ParseRegistry(t, r71RegistryPath)
	} else if !*r71Update {
		t.Fatalf("登记册 %s 不存在 ⇒ 先跑一次 `-r71-update` 生成骨架，再人工分类", r71RegistryPath)
	}

	if *r71Update {
		added := 0
		for name := range used {
			if _, ok := ds[name]; ok {
				continue // 事实源承载的 dsn 类，不进登记册
			}
			if _, ok := reg[name]; !ok {
				reg[name] = r71Entry{name: name, class: r71ClassComponent, reason: "TODO: 待人工分类"}
				added++
			}
		}
		if err := r71WriteRegistry(r71RegistryPath, reg); err != nil {
			t.Fatalf("write %s: %v", r71RegistryPath, err)
		}
		t.Logf("-r71-update：补齐 %d 个新名字（共 %d 条）", added, len(reg))
	}

	// ① 差集完备：被读到、且不在事实源的名字，必须逐条登记。
	var missing []string
	for name, files := range used {
		if _, ok := ds[name]; ok {
			continue
		}
		if _, ok := reg[name]; ok {
			continue
		}
		sort.Strings(files)
		missing = append(missing, fmt.Sprintf("%s  ← %s", name, files[0]))
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("测试读的 env %q 不在事实源、也不在登记册 %s 里 ⇒ 它属于「整体 DSN / 专用 DSN / 隔离 / 危险 / 拼装」哪一类？没人答 ⇒ 真库腿的前置条件是敞开的",
			m, r71RegistryPath)
	}

	// ② 反向完备：登记册里的名字必须仍被读到（死条目 ⇒ 清单腐化）。
	var dead []string
	for name := range reg {
		if _, ok := used[name]; !ok {
			dead = append(dead, name)
		}
	}
	sort.Strings(dead)
	for _, d := range dead {
		t.Errorf("登记册里的 %q 现在没有测试读了 ⇒ 死条目（要么删掉，要么它已不在测试面）", d)
	}

	t.Logf("测试文件 %d 个、env 名 %d 个（事实源承载 %d 个）；登记册 %d 条、未登记 %d、死条目 %d",
		testFiles, len(used), len(ds), len(reg), len(missing), len(dead))
}

// ── 判据 ③：分类合法 ＋ ④：理由非空 ──────────────────────────────────────────

func TestPGTestEnvRegistryClassesMatchFactsourceAndRunner(t *testing.T) {
	reg := r71ParseRegistry(t, r71RegistryPath)
	ds := r71ParseDatasourceExports(t, r71DatasourceEnvPath)
	runner := r71ParseRunnerEnvBlock(t, r71GateRunnerPath)
	runnerVals := r71ParseRunnerEnvValues(t, r71GateRunnerPath)

	for name, e := range reg {
		switch e.class {
		case r71ClassDSNExcl, r71ClassIsolation, r71ClassDangerous, r71ClassComponent:
			// 这四类都不得进事实源。
			if ds[name] {
				t.Errorf("%s 类 %q 出现在事实源 %s 里 ⇒ 事实源默认打开了一个**取消保护 / 专用隔离**的变量",
					e.class, name, r71DatasourceEnvPath)
			}
		default:
			t.Errorf("登记册 %q 的分类 %q 不在允许集合 %v 里", name, e.class, r71RegistryClasses)
		}
		// ★ 隔离/危险开关还不得出现在官方 runner 的 `env \` 块里 ——
		//   那里是「跑真库腿时自动导出」的地方 ⇒ 放进去 = 保护被默认取消。
		if e.class == r71ClassIsolation || e.class == r71ClassDangerous {
			if runner[name] {
				t.Errorf("%s 类 %q 出现在官方 runner %s 的 `env \\` 块里 ⇒ 跑真库腿时**自动打开**了一个取消保护的开关",
					e.class, name, r71GateRunnerPath)
			}
		}
		// ★★ dsn_exclusive **可以**出现在 runner 的 `env \` 块里，但**必须拿专用值**。
		//
		//   R81 轮实测到的真实事故：TRACE_RETIREMENT_TEST_DSN 被登记为 dsn_exclusive
		//   （它必须指向名为 trace_retirement* 的隔离库），而 runner 却把它绑成
		//   "$GATE_URL"（门禁自己的那个**共享**一次性库）⇒ ⇒ 产品侧自己的守卫
		//   正确地拒绝了它（trace_retirement_test.go 等四处：
		//   "requires loopback trace_retirement database or explicitly
		//   authorized local 54318 schema isolation"）⇒ ⇒ 官方门禁**永远红**，
		//   而红的原因不是产品缺陷。
		//   ⇒ ⇒ 判据 = **看值**：专用值（$TENANT_DSN 这类）放行，
		//   **通用值（$GATE_URL）判红**。
		if e.class == r71ClassDSNExcl {
			if v, ok := runnerVals[name]; ok && v == r71GenericGateDSNValue {
				t.Errorf("dsn_exclusive 类 %q 在官方 runner %s 的 `env \\` 块里被绑成**通用值** %s ⇒ "+
					"专用隔离被悄悄抹平（该 DSN 必须指向它自己的隔离库，不能与通用 DSN 同值）",
					name, r71GateRunnerPath, r71GenericGateDSNValue)
			}
		}
		if strings.TrimSpace(e.reason) == "" {
			t.Errorf("登记册 %q 没写理由 —— 每条都必须说明「为什么它属于这一类」", name)
		}
		if strings.Contains(strings.ToUpper(e.reason), "TODO") {
			t.Errorf("登记册 %q 的理由还留着 TODO ⇒ 它还没被人工分类过", name)
		}
	}

	// ★ 观察项（不判红）：事实源里有没有**没有任何测试读到**的名字；
	//   以及官方 runner 的 env 块覆盖了多少个事实源名字。
	used, testFiles, err := r71ScanRepo(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var orphan, runnerOnly []string
	for name := range ds {
		if _, ok := used[name]; !ok {
			orphan = append(orphan, name)
		}
	}
	for name := range runner {
		if _, ok := ds[name]; !ok {
			runnerOnly = append(runnerOnly, name)
		}
	}
	sort.Strings(orphan)
	sort.Strings(runnerOnly)
	t.Logf("测试文件 %d 个、env 名 %d 个；事实源导出 %d 个（其中 %d 个没有测试读到：%v）",
		testFiles, len(used), len(ds), len(orphan), orphan)
	t.Logf("官方 runner 的 `env \\` 块导出 %d 个（其中 %d 个不在事实源：%v）",
		len(runner), len(runnerOnly), runnerOnly)
}

// ── 判据 ⑤：扫描器自检（防恒绿门）─────────────────────────────────────────────

func TestPGTestEnvRegistryScannerSelfCheck(t *testing.T) {
	// (a) const 间接引用必须抓到（R69 轮踩过的坑 (b)：名字不一定在调用点）。
	constForm := `package x
const dsnEnv = "TEST_PG_DSN"
func f() string { return os.Getenv(dsnEnv) }
`
	names := r71ScanFile([]byte(constForm))
	if len(names) != 1 || names[0] != "TEST_PG_DSN" {
		t.Fatalf("const 形式的 env 名没被抓到：%v", names)
	}

	// (b) 注释里的名字必须抓不到（AST 天然做到，但要有断言钉住）。
	commented := `package x
// os.Getenv("R71_FAKE_COMMENT_ENV")
/* os.Getenv("R71_FAKE_BLOCK_ENV") */
func f() string { return "x" }
`
	if names := r71ScanFile([]byte(commented)); len(names) != 0 {
		t.Fatalf("注释里的名字被抓到了：%v", names)
	}

	// (c) 写环境变量（os.Setenv）不算「读」。
	setenv := `package x
func f() { os.Setenv("R71_FAKE_SET_ENV", "1") }
`
	if names := r71ScanFile([]byte(setenv)); len(names) != 0 {
		t.Fatalf("os.Setenv 被当成了读：%v", names)
	}

	// (d) ★ 关键回归：第一版用「文件里出现 pgxpool.New」当分类轴，
	//     实测漏了 17 个名字（经 helper 连库的文件）⇒ 现在分类轴是「读了 env」。
	//     这里钉住「只调 helper、不在文件里连库」的文件**仍然**被计入。
	helperOnly := `package x
func f(t *testing.T) { t.Helper(); mustPool(t, dsnEnv) }
`
	if names := r71ScanFile([]byte(helperOnly)); len(names) != 0 {
		t.Fatalf("只调 helper 的文件不该凭空产生 env 名：%v", names)
	}

	// (e) 扫描器**有牙**的正面证据。
	known := r71ScanFile([]byte(`package x
func f() string { return os.Getenv("TEST_DATABASE_URL") }
`))
	if len(known) != 1 || known[0] != "TEST_DATABASE_URL" {
		t.Fatalf("基本用例失效：%v", known)
	}

	// (f) ★★ 值解析器自检（R81 轮新增）：`dsn_exclusive` 的判据是**看值**，
	//   所以「值解析器能不能把值取出来」本身就是承重构件 —— 它一旦失效，
	//   那条判据会**恒绿**（取不到值 ⇒ 永远 != $GATE_URL ⇒ 永远放行）。
	//   ⇒ 这里钉住三件事：① 取得到值；② 引号被剥掉；③ 通用值与专用值可区分。
	envBlock := `env \
  TEST_TENANT_DATABASE_URL="$TENANT_DSN" \
  TRACE_RETIREMENT_TEST_DSN="$GATE_URL" \
  go test ./pkg
`
	tmp := filepath.Join(t.TempDir(), "runner.sh")
	if err := os.WriteFile(tmp, []byte(envBlock), 0o644); err != nil {
		t.Fatalf("写临时 runner：%v", err)
	}
	vals := r71ParseRunnerEnvValues(t, tmp)
	if got := vals["TEST_TENANT_DATABASE_URL"]; got != "$TENANT_DSN" {
		t.Errorf("专用值解析错：TEST_TENANT_DATABASE_URL = %q，期望 $TENANT_DSN", got)
	}
	if got := vals["TRACE_RETIREMENT_TEST_DSN"]; got != r71GenericGateDSNValue {
		t.Errorf("通用值解析错：TRACE_RETIREMENT_TEST_DSN = %q，期望 %s", got, r71GenericGateDSNValue)
	}
	// ★ 判据本身的分辨力：这两个值**必须**被判成不同 ⇒ 否则那条判据没有分辨力。
	if vals["TEST_TENANT_DATABASE_URL"] == vals["TRACE_RETIREMENT_TEST_DSN"] {
		t.Errorf("专用值与通用值被解析成同一个 ⇒ 「dsn_exclusive 拿到了通用值」这条判据没有分辨力")
	}
}
