package ursm_test

// R90 轮的门：**候选集包级化** —— 把 R89 轮留下的「候选集仍是文件级」这条边界补上。
//
// # 这道门补的是哪块缺口
//
// R87~R89 轮的**候选集**是**文件级**的 ⇒⇒ 一个文件必须**自己**同时满足
//
//	① 自己读事实源导出的 DSN 名 ② 自己开 PG 连接 ③ 自己含硬破坏性 SQL
//
// ⇒⇒ 于是「**破坏性 SQL 在 A 文件、读 DSN 在 B 文件**」的腿
//
//	**根本进不了候选集** ⇒⇒ 门**连看都没看见它**
//
// ⇒⇒ 这不是「判错」，是**不在视野里**（R89 轮实测：仓内 106 个破坏性测试文件）。
//
// ★ 本轮的实现：**腿的「事实源 DSN 消费」判据改成包级可达** ——
//
//	这条腿的**包级可达集**里，有没有函数读了事实源导出的 DSN 名？
//	这条腿的**包级可达集**里，有没有函数打开了 PG 连接？
//
//	（另加两条「不在函数体内」的来源：**包作用域**初始化、**`TestMain`**。）
//
// # 本文件的作用
//
// 八个夹具，**每个一个独立子目录**（= 独立包）★
// ⇒ 原因：包级索引按**函数名**建 ⇒ 同名函数会被合并 ⇒ 夹具互相污染（R89 轮实测）。
//
//	⑲ 跨文件候选：**破坏性在 A 文件、读 DSN 在 B 文件**（同包、腿可达）⇒ 必须**看得见**
//	⑳ 跨**包**消费：读 DSN 在另一个包 ⇒ ★★ R91 轮**必须看得见**（R90 轮时是 0 ⇒ 边界已闭合）
//	㉑ 同包但**不可达**：腿不调用那个读 DSN 的函数 ⇒ **看不见**（不该误报）
//	㉒ **包作用域**读 DSN（`var dsn = os.Getenv(…)`）⇒ 必须看得见
//	㉓ **`TestMain`** 读 DSN ⇒ 必须看得见（TestMain 跑在本包所有测试之前）
//	㉔ 裸关键字（`Strategy: "truncate"`）⇒ **不是 SQL** ⇒ 不得算破坏性语句
//	㉕ **正则**里的 `DROP\s+TABLE` ⇒ **不是 SQL** ⇒ 不得算破坏性语句
//	㉖ **运行时变量**形态（`"DROP TABLE " + tbl`）⇒ 必须仍然算破坏性语句
//
// ★ ⑲ 是本轮的**核心新形态**：旧口径对它**完全看不见** ⇒⇒ 它就是这个门存在的理由。
// ★ ㉖ 是本轮**收紧探测器**时的护栏：收紧不能伤到「对象名静态解析不出来」的真腿。

import (
	"os"
	"path/filepath"
	"testing"
)

// TestR90PkgCandidateScopeSelfCheck 候选集包级化 + 探测器精确化的夹具门。
func TestR90PkgCandidateScopeSelfCheck(t *testing.T) {
	exported := r71ParseDatasourceExports(t, r86DSPath)
	if len(exported) < 30 {
		t.Fatalf("事实源只解析出 %d 个 export ⇒ 自检前提不成立", len(exported))
	}
	var name string
	for n := range exported {
		name = n
		break
	}
	dir := t.TempDir()

	// write 把夹具写进**独立子目录**（= 独立包）⇒ 互不污染。
	write := func(fixture, fn, body string) {
		p := filepath.Join(dir, fixture, fn)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir fixture dir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", fn, err)
		}
	}

	// 破坏性 helper（多个夹具共用同一段文本，但**各写各的文件** ⇒ 不同包）。
	const destructiveBody = "package fx\n\nimport (\n\t\"context\"\n\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n" +
		"func crossFileReset(pool *pgxpool.Pool) {\n" +
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.some_table\")\n}\n"

	// ── ⑲：破坏性在 A 文件、读 DSN 在 B 文件（同包、腿可达）⇒ 必须看得见 ──────
	write("fx19", "pool_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func factSourcePool(t *testing.T) *pgxpool.Pool {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tif dsn == \"\" {\n\t\tt.Skip(\"unset\")\n\t}\n"+
		"\tpool, err := pgxpool.New(context.Background(), dsn)\n\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n"+
		"\tt.Cleanup(pool.Close)\n\treturn pool\n}\n")
	write("fx19", "destructive_test.go", destructiveBody)
	write("fx19", "leg_test.go", "package fx\n\nimport (\n\t\"testing\"\n)\n\n"+
		"func TestCrossFileCandidate(t *testing.T) {\n"+
		"\tpool := factSourcePool(t)\n\tcrossFileReset(pool)\n}\n")

	// ── ⑳：读 DSN 在**另一个包** ⇒ 看不见（跨包可达性未做 ⇒ 已知边界）────────
	write("fx20", "otherpkg/other.go", "package otherpkg\n\nimport (\n\t\"context\"\n\t\"os\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func FactSourcePool() *pgxpool.Pool {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tpool, _ := pgxpool.New(context.Background(), dsn)\n\treturn pool\n}\n")
	write("fx20", "destructive_test.go", destructiveBody)
	write("fx20", "leg_test.go", "package fx\n\nimport (\n\t\"testing\"\n\n"+
		"\"example.com/x/fx20/otherpkg\"\n)\n\n"+
		"func TestCrossPackageCandidate(t *testing.T) {\n"+
		"\tpool := otherpkg.FactSourcePool()\n\tcrossFileReset(pool)\n}\n")

	// ── ㉑：同包但**不可达**（腿调的是另一个池子）⇒ 看不见（不该误报）────────
	write("fx21", "pool_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func factSourcePool(t *testing.T) *pgxpool.Pool {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tpool, err := pgxpool.New(context.Background(), dsn)\n\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n"+
		"\tt.Cleanup(pool.Close)\n\treturn pool\n}\n")
	write("fx21", "hardcoded_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func fixedPool(t *testing.T) *pgxpool.Pool {\n"+
		"\tpool, err := pgxpool.New(context.Background(), \"fixture://not-a-real-dsn\")\n"+
		"\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n\treturn pool\n}\n")
	write("fx21", "destructive_test.go", destructiveBody)
	write("fx21", "leg_test.go", "package fx\n\nimport (\n\t\"testing\"\n)\n\n"+
		"func TestUnreachableCandidate(t *testing.T) {\n"+
		"\tpool := fixedPool(t)\n\tcrossFileReset(pool)\n}\n")

	// ── ㉒：**包作用域**读 DSN ⇒ 必须看得见 ───────────────────────────────────
	write("fx22", "scope_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"var scopeDSN = os.Getenv(\""+name+"\")\n\n"+
		"func scopePool(t *testing.T) *pgxpool.Pool {\n"+
		"\tpool, err := pgxpool.New(context.Background(), scopeDSN)\n\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n"+
		"\tt.Cleanup(pool.Close)\n\treturn pool\n}\n")
	write("fx22", "destructive_test.go", destructiveBody)
	write("fx22", "leg_test.go", "package fx\n\nimport (\n\t\"testing\"\n)\n\n"+
		"func TestScopeCandidate(t *testing.T) {\n"+
		"\tpool := scopePool(t)\n\tcrossFileReset(pool)\n}\n")

	// ── ㉓：**`TestMain`** 读 DSN ⇒ 必须看得见（TestMain 跑在本包所有测试之前）──
	write("fx23", "main_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func TestMain(m *testing.M) {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tif dsn != \"\" {\n\t\t_, _ = pgxpool.New(context.Background(), dsn)\n\t}\n"+
		"\tos.Exit(m.Run())\n}\n")
	write("fx23", "destructive_test.go", destructiveBody)
	write("fx23", "leg_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func TestTestMainCandidate(t *testing.T) {\n"+
		"\tpool, _ := pgxpool.New(context.Background(), \"fixture://not-a-real-dsn\")\n"+
		"\tif pool == nil {\n\t\tt.Skip(\"no pg\")\n\t}\n\tcrossFileReset(pool)\n}\n")

	// ── ㉔：裸关键字（`applyStrategy("truncate")`）⇒ **不是 SQL** ──────────────
	//
	// ★★★ **这个夹具必须是「调用实参」形态**，不能写成复合字面量
	//   （`strategy{Name: "truncate"}`）⇒⇒ 复合字面量**不是 CallExpr**
	//   ⇒⇒ 探测器**压根看不见它** ⇒⇒ 那种写法下夹具会**恒绿**，
	//   变异打不动它（R90 轮实测踩中：第一次写成复合字面量 ⇒ M10 打不动 ⇒ 假绿）。
	write("fx24", "destructive_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func applyStrategy(name string) string { return name }\n\n"+
		"func TestBareKeyword(t *testing.T) {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tpool, err := pgxpool.New(context.Background(), dsn)\n\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n"+
		"\tt.Cleanup(pool.Close)\n"+
		"\ts := applyStrategy(\"truncate\")\n\t_ = s\n}\n")

	// ── ㉕：**正则**里的 `DROP\s+TABLE` ⇒ **不是 SQL** ─────────────────────────
	write("fx25", "destructive_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"regexp\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func destructivePattern() *regexp.Regexp {\n"+
		"\treturn regexp.MustCompile(`(?i)\\b(TRUNCATE|DROP TABLE)\\b`)\n}\n\n"+
		"func TestRegexCarrier(t *testing.T) {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tpool, err := pgxpool.New(context.Background(), dsn)\n\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n"+
		"\tt.Cleanup(pool.Close)\n\t_ = destructivePattern()\n}\n")

	// ── ㉖：**运行时变量**形态（`\"DROP TABLE \" + tbl`）⇒ 必须仍然算数 ──────────
	write("fx26", "destructive_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func resetWithRuntimeName(pool *pgxpool.Pool, tbl string) {\n"+
		"\t_, _ = pool.Exec(context.Background(), \"DROP TABLE \"+tbl)\n}\n\n"+
		"func TestRuntimeName(t *testing.T) {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tpool, err := pgxpool.New(context.Background(), dsn)\n\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n"+
		"\tt.Cleanup(pool.Close)\n\tresetWithRuntimeName(pool, \"some_table\")\n}\n")

	pkgs, gidx := r87BuildPkgFacts(t, dir, "example.com/x", exported)

	analyze := func(fixture, fn string) []r87Pair {
		p := filepath.Join(dir, fixture, fn)
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		// ★ 最后一个参数是**导入解析根目录**（跨包闸门要用），不是包目录
		//   ⇒⇒ 包目录由 `r87AnalyzeFile` 内部按 `filepath.Dir(path)` 推导。
		return r87AnalyzeFile(t, p, fn, string(raw), exported, pkgs, gidx, "example.com/x", dir)
	}

	// ⑲ ★★ 本轮核心：**破坏性在 A 文件、读 DSN 在 B 文件** ⇒ 必须**看得见**。
	//
	// ★ 要分析的是**含破坏性语句的那个文件**（`destructive_test.go`）——
	//   分析测试文件是**瞄错对象**（那个文件根本没有破坏性语句 ⇒ 恒绿）。
	if got := analyze("fx19", "destructive_test.go"); len(got) != 1 || got[0].Name != name {
		t.Fatalf("夹具 ⑲（破坏性在 A 文件、读 DSN 在 B 文件）应判出 1 条候选，实得 %d 条（%v）⇒ "+
			"★ 候选集仍是**文件级** ⇒ 「破坏性 SQL 与读 DSN 分居两个文件」的腿"+
			"**压根进不了候选集** ⇒ 门连看都没看见它（R89 轮实测：106 个文件）", len(got), got)
	}

	// ⑳ ★★ R91 轮**翻转**：跨**包** ⇒ 现在**看得见**了（期望从 0 改成 1）。
	//
	// ★ R90 轮时它是「已知边界」的钉子（跨包可达性未实现 ⇒ 0 条）；
	//   R91 轮实现了跨包可达 ⇒⇒ 「DSN 在另一个包里被读取并连上」的腿
	//   ⇒⇒ **必须**进候选集 ⇒⇒ 这就是**漏放方向被闭合**的证明。
	if got := analyze("fx20", "destructive_test.go"); len(got) != 1 {
		t.Fatalf("夹具 ⑳（读 DSN 在**另一个包**）应判出 1 条候选，实得 %d 条（%v）⇒ "+
			"★ 跨包可达性退回去了 ⇒ 「DSN 在别的包里被读取并连上」的腿"+
			"**进不了候选集** ⇒ 门看不见它 ⇒ **漏放**（数据事故级）", len(got), got)
	}

	// ㉑ 同包但**不可达** ⇒ 看不见（这条腿压根没消费事实源的 DSN）。
	if got := analyze("fx21", "destructive_test.go"); len(got) != 0 {
		t.Fatalf("夹具 ㉑（同包有读 DSN 的函数，但这条腿够不到它）被判成 %d 条候选（%v）⇒ "+
			"★ 判据被放宽成「同包任一函数读了 DSN 就算」⇒ 会把无关的腿误报成高危", len(got), got)
	}

	// ㉒ **包作用域**读 DSN ⇒ 必须看得见。
	if got := analyze("fx22", "destructive_test.go"); len(got) != 1 {
		t.Fatalf("夹具 ㉒（包作用域 `var dsn = os.Getenv(…)`）应判出 1 条候选，实得 %d 条（%v）⇒ "+
			"★ 包作用域的 DSN 读取**不在任何函数体内** ⇒ 函数级扫描看不见它 ⇒ 漏放", len(got), got)
	}

	// ㉓ **`TestMain`** 读 DSN ⇒ 必须看得见（TestMain 跑在本包所有测试之前）。
	if got := analyze("fx23", "destructive_test.go"); len(got) != 1 {
		t.Fatalf("夹具 ㉓（`TestMain` 读 DSN）应判出 1 条候选，实得 %d 条（%v）⇒ "+
			"★ `TestMain` 读的 DSN 流进本包**每一条**腿 ⇒ 不单列就会漏掉「TestMain 建池 + 各测试各清各的表」这一形态",
			len(got), got)
	}

	// ㉔ 裸关键字 ⇒ **不是 SQL** ⇒ 零候选。
	if got := analyze("fx24", "destructive_test.go"); len(got) != 0 {
		t.Fatalf("夹具 ㉔（`applyStrategy(\"truncate\")` 这种**非 SQL** 字符串）被判成 %d 条候选（%v）⇒ "+
			"★ 探测器把「出现过关键字」当成了「破坏性语句」⇒ 读数被灌水", len(got), got)
	}

	// ㉕ 正则里的关键字 ⇒ **不是 SQL** ⇒ 零候选。
	if got := analyze("fx25", "destructive_test.go"); len(got) != 0 {
		t.Fatalf("夹具 ㉕（`regexp.MustCompile(`…DROP\\s+TABLE…`)`）被判成 %d 条候选（%v）⇒ "+
			"★ 探测器把**正则模式**当成了被执行的 SQL ⇒ 读数被灌水", len(got), got)
	}

	// ㉖ 运行时变量形态 ⇒ 必须仍然算数（收紧探测器不能伤到它）。
	if got := analyze("fx26", "destructive_test.go"); len(got) != 1 {
		t.Fatalf("夹具 ㉖（`\"DROP TABLE \" + tbl`，对象名静态解析不出来）应判出 1 条候选，实得 %d 条（%v）⇒ "+
			"★ 收紧探测器时误用了「必须能解析出对象名」⇒ 会把运行时变量形态的**真腿**漏掉", len(got), got)
	}
}
