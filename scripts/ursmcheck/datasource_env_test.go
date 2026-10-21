package ursm_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// R69 轮的门：**真库腿认的 DSN 环境变量名 ⊆ `scripts/audit/datasource_env.sh` 的清单**。
//
// # 为什么要这道门
//
// R69 轮实测出三条独立证据：
//
//	① ★★★★ **官方 gate runner 自己漏了 21 个变量名**：
//	   `scripts/audit/run-integration-gate.sh` 的 `env \` 块只导出 20 个名字，
//	   而「打开 PG 连接的测试文件」一共认 **40 个**
//	   ⇒ **≥24 条真库腿在官方跑法下拿不到 DSN**。
//	② ★★★★ **「静默 SKIP」有两种形态，`go test` 都把它们算成绿**：
//	   (a) 「变量没设 ⇒ t.Skip」；
//	   (b) 「变量没设 ⇒ 去连一个**默认/写死**的库 ⇒ 连不上 ⇒ 仍然 Skip」
//	   （实例：`bg/feature_stats_worker_test.go:341`
//	   `dsn = "postgres://localhost/llm_gateway_test?sslmode=disable"`）。
//	③ ★★★★ **人写的清单必然漏** —— 连事实源的**第一版**都漏了
//	   （只抄了官方 runner 的 20 个 ⇒ 实测 `TEST_RETIREMENT_HTTP_DSN` 仍 SKIP）。
//
// ⇒ ⇒ 所以「把 DSN 变量集手工抄全」这条路走不通：
// **必须有一道自动对账的门**，而且它的匹配器必须**先自证有牙**。
//
// # 匹配器的三个坑（都踩过）
//
//	★ **(a) 注释要剥，但 `//` 不能乱剥** —— Go 源码里到处是
//	  `"postgres://host/db"` ⇒ 一个朴素的「`//` 到行尾」会把它连同后面的内容一起吃掉，
//	  **从而把真实存在的名字藏起来** ⇒ 剥注释必须**字符串字面量感知**。
//	★ **(b) 名字不一定在调用点** —— `admin/routing_resolve_invariant_test.go` 用
//	  `const resolveInvariantDBEnv = "TEST_RESOLVE_INVARIANT_DB_URL"` 读它
//	  ⇒ **只扫 `os.Getenv("X")` 的门是恒绿门**（R65 同族坑）⇒ 必须扫**全部字符串字面量**。
//	★ **(c) 「哪些名字算 DSN」不能按名字形状猜** —— `NETBIRD_API_URL`、
//	  `XIAOMI_BASE_URL`、`RPM_REDIS_URL` 都长得像 DSN 却不是 PG DSN
//	  ⇒ ⇒ **分类轴是「这个文件是否打开 PG 连接」**，不是名字长什么样。

const (
	datasourceEnvPath = "../../scripts/audit/datasource_env.sh"
	gateRunnerPath    = "../../scripts/audit/run-integration-gate.sh"
)

// datasourceEnvExemptions：**不进**事实源的名字，每个都必须写明理由。
//
// 门会强制「豁免表里的理由非空」且「豁免表与事实源不相交」，
// ⇒ 豁免表不会变成「什么都往里塞」的垃圾桶。
var datasourceEnvExemptions = map[string]string{
	"TEST_REDIS_URL":           "Redis DSN —— 把 PG DSN 指过去比留空更糟（官方 runner 自述）",
	"NETBIRD_API_URL":          "HTTP API 端点，不是 DSN",
	"XIAOMI_BASE_URL":          "HTTP base URL，不是 DSN",
	"WA877_ALLOW_ANY_DSN":      "是「允许任意 DSN」的开关，不是 DSN 本身",
	"TEST_TENANT_DATABASE_URL": "必须指向**独立的、不可绕过 RLS 的租户角色**（官方 runner 现场建）⇒ 不能与通用 DSN 同值",
	// ★★ R81 轮新增：与上面 TEST_TENANT_DATABASE_URL **同族**（dsn_exclusive）。
	//
	// ★ 它为什么必须豁免（而不进事实源）：trace-retirement 那条腿做的是
	//   **破坏性清理**（删表/改 schema）⇒ 它只能跑在自己的隔离库上 ——
	//   产品侧自己的守卫要求 DSN 指向**名为 trace_retirement* 的库**
	//   （或显式授权的本地 54318 schema）。
	//   ⇒ 与通用 DSN 同值 = 让破坏性清理打到共享库上。
	//   ⇒ 而事实源与官方 runner 给的**恰恰就是通用 DSN** ⇒ ⇒
	//   产品守卫会正确地拒绝它 ⇒ ⇒ 门禁得到 4 条**假红**（红的原因不是产品缺陷）。
	//   ⇒ 不进事实源 ⇒ 那条腿改走它本来就设计好的 t.Skip 分支。
	"TRACE_RETIREMENT_TEST_DSN": "破坏性 trace-retirement 腿的专用 DSN：必须指向名为 trace_retirement* 的隔离库（或显式授权的本地 54318 schema）⇒ 不能与通用 DSN 同值",
	// ★★ R85 轮新增：与 TRACE_RETIREMENT_TEST_DSN **同族**（dsn_exclusive）。
	//
	// ★ 它为什么必须豁免（而不进事实源）：canonical fixture 除主库外**还要连第二个库**
	//   `identity_shadow`（admin/shared_principal_realdb_test.go:113）⇒ 它本来就只能
	//   指向「本地联调专用库」。
	// ⇒ 与通用 DSN 同值 = 把「连本地库」的前提给了一个通用库。
	// ⇒ 产品侧自己的守卫会正确地拒绝它（授权开关 GATEWAY_CANONICAL_TEST_LOCAL_ONLY
	//   未开 ⇒ t.Fatal）⇒ ⇒ 事实源默认注入它，只会把「靶子不对」显示成红。
	// ⇒ 不进事实源 ⇒ 那条腿改走它本来就设计好的 t.Skip 分支（dsn == "" ⇒ Skip）。
	// ★ ★ 与「把 t.Fatal 改成 t.Skip」是**两件事**：守卫一个字都不用改 ——
	//   「没配 ⇒ Skip」与「配了但没授权 ⇒ Fatal」是**互补的两条边界**，
	//   而 t.Fatal 那一侧是**安全拒绝**（不是「跑不起来」）⇒ 改成 SKIP 会把安全拒绝降级成静默跳过。
	// ★ 授权开关本身（GATEWAY_CANONICAL_TEST_LOCAL_ONLY，dangerous 类）**绝不由事实源打开**
	//   ⇒ 门禁不能替人授予「连本地库」的权限。
	"GATEWAY_CANONICAL_TEST_DSN": "canonical fixture 的专用 DSN：还要连第二个库 `identity_shadow` ⇒ 只能指向「本地联调专用库」⇒ 不能与通用 DSN 同值",
	// ★★ R86 轮新增：四个新实例（③④⑤⑥）—— **「事实源给了通用 DSN，但这条腿一定拒绝它」**。
	//
	// ★ 这一族的判别形态与上面两条**不同**：
	//   TRACE_RETIREMENT_TEST_DSN / GATEWAY_CANONICAL_TEST_DSN 是「**腿自己会拒绝**」
	//   （守卫要求特定库名/额外授权）⇒ 事实源注入它只会得到假红或死腿。
	//   R86 轮把这条判据**做成了机器可判的门**（(事实源导出名 × 消费腿) 靶子错配探测器），
	//   豁免表就是那张门的台账入口。
	//
	// ★ LLM_GATEWAY_FEATURE_STATS_TEST_DSN（死腿）：
	//   setupTestDB 会 TRUNCATE 三张表，库名守卫只放行 itgate|scratch|test|tmp|bench|pg_
	//   ⇒ 通用 DSN（llm_gateway）必被拒绝 ⇒ 事实源档下这条腿**从来没跑过**。
	//   ⇒ 移出事实源零覆盖损失（移出前死、移出后也死；差别只是不再给一个用不了的值）。
	"LLM_GATEWAY_FEATURE_STATS_TEST_DSN": "feature-stats 破坏性腿的专用 DSN：TRUNCATE 三张表 + 库名守卫只放行一次性库 ⇒ 通用 DSN 必被拒绝 ⇒ 事实源注入它只会造出一条死腿",
	// ★ TEST_SESSION_INGEST_DATABASE_URL（假红）：
	//   守卫要求库名以 _test 结尾且 TEST_SESSION_INGEST_ISOLATED=1，随后 DROP SCHEMA public CASCADE
	//   ⇒ 通用 DSN 不满足 ⇒ t.Fatal（响亮假红）⇒ 移出后改走它设计好的 t.Skip 分支。
	"TEST_SESSION_INGEST_DATABASE_URL": "session-ingest 夹具的专用 DSN：必须指向 *_test 一次性库且配 TEST_SESSION_INGEST_ISOLATED=1，否则守卫 t.Fatal ⇒ 通用 DSN 必被拒绝 ⇒ 事实源注入它只会造出假红",
	// ★ GUARD_IT_DSN（数据事故级）：
	//   bg/balance_floor_guard_integration_test.go 在建池后裸 TRUNCATE credentials, providers CASCADE
	//   ⇒ 文件头明写「本测试绝不写线上 llm_gateway 库」⇒ 契约此前**只写在注释里，代码里没有**
	//   ⇒ R86 轮补了库名守卫，并把本名字移出事实源。
	"GUARD_IT_DSN": "balance-floor 集成腿的专用 DSN：TRUNCATE credentials, providers CASCADE ⇒ 必须指向一次性 scratch 库（文件头已写明）⇒ 不能与通用 DSN 同值",
	// ★ ROUTEINCIDENT_IT_DSN（污染级）：
	//   会 CREATE TABLE route_incidents（715 表形）在目标库 ⇒ 文件头写明 Cleanup: DROP DATABASE llm_routeincident_it
	//   ⇒ 同族：必须指向一次性库 ⇒ 不能与通用 DSN 同值。
	"ROUTEINCIDENT_IT_DSN": "routeincident 集成腿的专用 DSN：会在目标库建 route_incidents 表（715 表形）⇒ 必须指向一次性库 ⇒ 不能与通用 DSN 同值",
	// ★ REPORT_E2E_TEST_DATABASE_URL（R86 轮**新建**的名字）：
	//   原本这条腿读的是**共享名** TEST_DATABASE_URL / TEST_DB_URL
	//   ⇒ 而事实源把这两个名字都指向产品库 ⇒ 该腿在事实源档下必然 t.Fatal（假红）。
	//   ⇒ R86 轮给它换成**专用名**（共享名无法只服务这一条破坏性腿）
	//   ⇒ 守卫语义**一字未改**（非 scratch 库仍然 t.Fatal）。
	"REPORT_E2E_TEST_DATABASE_URL": "reportrollup 破坏性 E2E 的专用 DSN：会 DELETE report_snapshots / usage_facts ⇒ 必须指向一次性 scratch 库 ⇒ 不能与通用 DSN 同值（R86 轮把共享名 TEST_DATABASE_URL/TEST_DB_URL 拆成专用名）",
	// ★ TEST_SESSION_SETTINGS_DATABASE_URL（R86 轮**第七例**，由「读形态守卫」判据挖出）：
	//   守卫要求库名以 _test 结尾**且** TEST_SESSION_INGEST_ISOLATED=1（隔离开关），
	//   随后 DROP SCHEMA public CASCADE ⇒ 通用 DSN 两个条件都不满足 ⇒ t.Fatal（假红）。
	"TEST_SESSION_SETTINGS_DATABASE_URL": "session-settings 夹具的专用 DSN：守卫要求 *_test 库 + 隔离开关，随后 DROP SCHEMA public CASCADE ⇒ 通用 DSN 必被拒绝 ⇒ 事实源注入它只会造出假红",
}

// ── 匹配器（纯函数，可被自检直接调用）──────────────────────────────────────────

var (
	reDSNNameValue = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	rePGConnector  = regexp.MustCompile(`pgxpool\.New|pgx\.Connect|pgxpool\.Connect|sql\.Open\(\s*"postgres`)
)

// stripGoComments 剥掉 Go 注释，但**必须字符串字面量感知**：
// `"postgres://host/db"` 里的 `//` 不是注释。
//
// ★★ **且必须「保留行数」**（R70 轮自曝）：注释内容用**空格**替换而不是删除，
//
//	否则被剥掉的多行注释会让后面的**行号全部前移**
//	⇒ 凡是「按行号报位置」的判据（写死 DSN 登记册）都会指错地方。
func stripGoComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	for i := 0; i < len(src); {
		switch {
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				b.WriteByte(' ')
				i++
			}
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' {
					b.WriteByte('\n')
				} else {
					b.WriteByte(' ')
				}
				i++
			}
			i += 2
			if i > len(src) {
				i = len(src)
			}
		case src[i] == '"' || src[i] == '`':
			q := src[i]
			b.WriteByte(src[i])
			i++
			for i < len(src) {
				if src[i] == '\\' && q == '"' && i+1 < len(src) {
					b.WriteByte(src[i])
					b.WriteByte(src[i+1])
					i += 2
					continue
				}
				b.WriteByte(src[i])
				if src[i] == q {
					i++
					break
				}
				i++
			}
		case src[i] == '\'':
			b.WriteByte(src[i])
			i++
			for i < len(src) {
				b.WriteByte(src[i])
				if src[i] == '\'' {
					i++
					break
				}
				if src[i] == '\\' && i+1 < len(src) {
					b.WriteByte(src[i+1])
					i += 2
					continue
				}
				i++
			}
		default:
			b.WriteByte(src[i])
			i++
		}
	}
	return b.String()
}

// goStringLiteralValues 走一遍 Go 源码，返回**每个字符串字面量的完整值**
// （跳过注释；raw string 按原样取）。
//
// ★★ **第一版是假绿的**（R69 轮自曝）：它「剥注释后用正则找 `"([A-Z][A-Z0-9_]*)"`」，
//
//	而**剥注释并不会剥掉字符串字面量的内容** ⇒
//	门**把自己自检夹具里的名字当成了真名字**（`REAL_FAKE_TEST_DSN`）⇒ 门自己红自己。
//
// ⇒ ⇒ 正确口径：**一个 DSN 环境变量名 = 「一个字符串字面量，且它的整个值就是这个名字」**。
//
//	⇒ 词法器天然满足：raw string 夹具的**值**是一整段文本，不等于任何名字 ⇒ 不会被误判；
//	⇒ 而 `const x = "TEST_RESOLVE_INVARIANT_DB_URL"` 的值**就是**那个名字 ⇒ 会被抓到。
func goStringLiteralValues(src string) []string {
	var out []string
	for i := 0; i < len(src); {
		switch {
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i += 2
			if i > len(src) {
				i = len(src)
			}
		case src[i] == '`':
			i++
			start := i
			for i < len(src) && src[i] != '`' {
				i++
			}
			out = append(out, src[start:i])
			if i < len(src) {
				i++
			}
		case src[i] == '"':
			i++
			var b strings.Builder
			for i < len(src) {
				if src[i] == '\\' && i+1 < len(src) {
					b.WriteByte(src[i+1])
					i += 2
					continue
				}
				if src[i] == '"' {
					i++
					break
				}
				b.WriteByte(src[i])
				i++
			}
			out = append(out, b.String())
		case src[i] == '\'':
			i++
			for i < len(src) {
				if src[i] == '\\' && i+1 < len(src) {
					i += 2
					continue
				}
				if src[i] == '\'' {
					i++
					break
				}
				i++
			}
		default:
			i++
		}
	}
	return out
}

// dsnNameLiterals 抽出一段 Go 源码里的「DSN 类环境变量名」字面量。
//
// 口径：**全部**大写字符串字面量（含 const 形式），而不是只看 `os.Getenv("X")` 的调用点。
func dsnNameLiterals(src string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range goStringLiteralValues(src) {
		if !reDSNNameValue.MatchString(v) {
			continue
		}
		if !strings.Contains(v, "DSN") && !strings.HasSuffix(v, "_URL") {
			continue
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// opensPostgresConn 判断一段 Go 源码是否打开 PG 连接（分类轴，见文件头坑 (c)）。
func opensPostgresConn(src string) bool {
	return rePGConnector.MatchString(stripGoComments(src))
}

// ── 清单解析 ────────────────────────────────────────────────────────────────

var reEnvExport = regexp.MustCompile(`(?m)^export ([A-Z][A-Z0-9_]*)=`)

// parseDatasourceEnvExports 解析事实源里导出的变量名。
func parseDatasourceEnvExports(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, m := range reEnvExport.FindAllStringSubmatch(string(raw), -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// parseGateRunnerEnvExports 解析官方 runner 的 `env \` 块里导出的变量名。
var reGateRunnerEnv = regexp.MustCompile(`(?m)^\s{2}([A-Z][A-Z0-9_]*)="\$GATE_URL" \\\s*$`)

func parseGateRunnerEnvExports(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, m := range reGateRunnerEnv.FindAllStringSubmatch(string(raw), -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// ── 门 1：覆盖 ──────────────────────────────────────────────────────────────

func TestRealDBLegsUseDeclaredDSNEnvNames(t *testing.T) {
	declared := parseDatasourceEnvExports(t, datasourceEnvPath)
	declaredSet := map[string]bool{}
	for _, n := range declared {
		declaredSet[n] = true
	}
	if len(declared) == 0 {
		t.Fatalf("%s 里一个 export 都没有 —— 解析器失效（门会恒绿）", datasourceEnvPath)
	}

	// 豁免表与事实源必须不相交，且每个豁免都要写理由。
	for name, why := range datasourceEnvExemptions {
		if declaredSet[name] {
			t.Errorf("名字 %q 同时出现在事实源与豁免表里 —— 豁免表会变成垃圾桶（理由：%s）", name, why)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("豁免 %q 没写理由 —— 豁免表必须逐条说明，否则下一个人不敢删", name)
		}
	}

	// 官方 runner 的 env 块不许从事实源里消失（权威清单不许回退）。
	for _, n := range parseGateRunnerEnvExports(t, gateRunnerPath) {
		if !declaredSet[n] && datasourceEnvExemptions[n] == "" {
			t.Errorf("官方 runner %s 导出 %q，但事实源 %s 里没有它 ⇒ 权威清单回退了",
				gateRunnerPath, n, datasourceEnvPath)
		}
	}

	// ★ 主判据：凡是打开 PG 连接的测试文件，它认的 DSN 类变量名必须被覆盖。
	root := filepath.Join("..", "..")
	used := map[string][]string{} // name -> 引用它的文件
	pgFiles := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		// ★★ **门必须排除自己**：本文件的自检夹具里含 `pgxpool.New`
		//    ⇒ 门文件会被分类轴判成「打开 PG 的测试文件」
		//    ⇒ 夹具里的假名字会被当成真名字（第一版就栽在这）。
		if filepath.Base(path) == "datasource_env_test.go" {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(raw)
		if !opensPostgresConn(src) {
			return nil
		}
		pgFiles++
		for _, n := range dsnNameLiterals(src) {
			used[n] = append(used[n], path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if pgFiles < 50 {
		t.Fatalf("只扫到 %d 个打开 PG 的测试文件 —— 扫描口径失效（门会恒绿）", pgFiles)
	}

	var uncovered []string
	for name, files := range used {
		if declaredSet[name] || datasourceEnvExemptions[name] != "" {
			continue
		}
		sort.Strings(files)
		uncovered = append(uncovered, name+"  ← "+files[0])
	}
	sort.Strings(uncovered)
	for _, u := range uncovered {
		t.Errorf("真库腿认的 DSN 变量 %q 不在事实源 %s 里 ⇒ 跑真库腿时会静默 SKIP（go test 把 SKIP 算成绿）",
			u, datasourceEnvPath)
	}
	t.Logf("扫到 %d 个打开 PG 的测试文件、%d 个 DSN 类变量名；事实源导出 %d 个、豁免 %d 个、未覆盖 %d 个",
		pgFiles, len(used), len(declared), len(datasourceEnvExemptions), len(uncovered))
}

// ── 门 2：事实源不得有死条目（清单会腐化）────────────────────────────────────

func TestDatasourceEnvHasNoDeadEntries(t *testing.T) {
	declared := parseDatasourceEnvExports(t, datasourceEnvPath)
	root := filepath.Join("..", "..")
	blob := strings.Builder{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") && !strings.HasSuffix(d.Name(), ".sh") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		blob.Write(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	all := blob.String()
	for _, n := range declared {
		if strings.Count(all, n) <= 1 {
			// 只在事实源自己里出现 ⇒ 没有任何代码读它 ⇒ 死条目。
			t.Errorf("事实源导出了 %q，但仓里没有任何代码读它 ⇒ 死条目（清单腐化）", n)
		}
	}
}

// ── 门 3：匹配器自检（防恒绿门）─────────────────────────────────────────────

func TestDSNEnvMatcherSelfCheck(t *testing.T) {
	// (a) const 形式（调用点不在 os.Getenv 里）必须被抓。
	//     实例：admin/routing_resolve_invariant_test.go 用 const 读 TEST_RESOLVE_INVARIANT_DB_URL。
	constForm := `package x
const resolveEnv = "TEST_RESOLVE_INVARIANT_DB_URL"
func f() string { return resolveEnv }
`
	got := dsnNameLiterals(constForm)
	if len(got) != 1 || got[0] != "TEST_RESOLVE_INVARIANT_DB_URL" {
		t.Fatalf("const 形式的 DSN 名没被抓到：%v", got)
	}

	// (b) 注释里的名字必须**不**被抓。
	commented := `package x
// TEST_FAKE_COMMENT_DSN
/* LLM_GATEWAY_FAKE_BLOCK_DSN */
func f() string { return "X" }
`
	if got := dsnNameLiterals(commented); len(got) != 0 {
		t.Fatalf("注释里的名字被抓到了（剥注释失效）：%v", got)
	}

	// (c) ★ `"postgres://host/db"` 里的 `//` **不能**被当成行注释 ——
	//     朴素剥法会把该行后半截吃掉，从而藏住真实的名字。
	urlInString := `package x
func f() string { return "postgres://user:pw@127.0.0.1:5432/db" + "REAL_FAKE_TEST_DSN" }
`
	if got := dsnNameLiterals(urlInString); len(got) != 1 || got[0] != "REAL_FAKE_TEST_DSN" {
		t.Fatalf("字符串里的 // 被当成注释吃掉了后半行：%v", got)
	}

	// (c-2) ★★ **字面量必须按「整值」判定** —— raw string 夹具里嵌套的引号
	//       不是「一个值等于某个名字」的字面量 ⇒ 不得被当成名字。
	//       （这是本门第一版的假绿点：剥注释并不剥字面量内容。）
	fixture := `package x
func f() string { return "postgres://h/db" + "NESTED_FAKE_TEST_DSN" }
`
	if got := dsnNameLiterals(fixture); len(got) != 1 || got[0] != "NESTED_FAKE_TEST_DSN" {
		t.Fatalf("字面量整值判定失效：%v", got)
	}
	rawFixture := "package x\nconst c = `\n\"NESTED_IN_RAW_TEST_DSN\"\n`\n"
	if got := dsnNameLiterals(rawFixture); len(got) != 0 {
		t.Fatalf("raw string 夹具里的名字被误当成 DSN 变量名：%v", got)
	}

	// (d) 分类轴：不开 PG 的文件里的名字**不**算 DSN 变量（名字形状像也不算）。
	notPG := `package x
func f() string { return os.Getenv("XIAOMI_BASE_URL") }
`
	if opensPostgresConn(notPG) {
		t.Fatal("不开 PG 的文件被判成开 PG（分类轴失效）")
	}
	pgFile := `package x
func f(p *pgxpool.Pool) { _, _ = pgxpool.New(ctx, dsn) }
`
	if !opensPostgresConn(pgFile) {
		t.Fatal("开 PG 的文件没被判出来（分类轴失效）")
	}
}
