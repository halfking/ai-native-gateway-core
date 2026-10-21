package ursm_test

// R88 轮的门：**腿级支配性** —— 把 R87 的「文件级位置序」口径换成「按腿判」。
//
// # 这道门补的是哪块缺口
//
// R87 轮的门（`destructive_target_guard_test.go`）问的是
// 「**这个文件里**在第一条破坏性语句之前，有没有目标库判定证据」
// ⇒ 那是一条**文件级**断言。
//
// 而危害的单位是**腿**（一条会真的执行破坏性 SQL 的测试路径）⇒⇒ 于是这条恒真推论成立：
//
//	「一个文件里有一条腿带守卫 ⇒ 整个文件（含别的腿）都被判成有守卫」
//
// # ★ R88 轮实测出来的洞（变异 M1，同基线对照）
//
//	BEFORE（origin/main `f0a1af2be`，文件级口径）：
//	    给一条**已带守卫**的文件追加一个**不带守卫**的第二入口
//	    （自己建池、自己 TRUNCATE 产品表）⇒ 门看到「判定在前面」⇒ **门仍然绿**。
//	AFTER（本轮，腿级口径）：同一个变异 ⇒ **门转红**（未登记 1 条）。
//
// ⇒⇒ 危害：事实源给什么值，那条新腿都会清空产品表（数据事故级），
//     而门**看不见**它 —— 恰恰是这道门存在的目的被自己绕过去了。
//
// # 判定单位（腿根）
//
//	腿根 = 能到达该破坏性语句的**测试入口**（`Test` 前缀的函数）；
//	若没有任何测试入口能到达它（退化形态）⇒ 以**该语句所在函数**为腿根。
//	腿根**被守卫支配** ⇔ 它的同文件可达集合里存在一个
//	「体内含目标库判定」或「调用了跨包闸门」的函数。
//
// ★ 为什么不是拿「语句所在函数」当腿根：仓内四条真腿的破坏性字面量住在**兄弟
//   helper** 里（`reset` / `resetOutboxAndSessions` / `attachFixtureReset` /
//   `bootstrapSupplierSchema`），守卫住在**另一个** helper 里（`openPool` /
//   `openAuditPool` / `openAttachPool` / `openSupplierPG`）⇒⇒ 两者是**兄弟**
//   不是祖先 ⇒⇒ 只看「语句所在函数」会把四条真腿全判成没守卫（假阳性）。
//   ⇒⇒ 真正的支配关系在**调用者**那一侧：测试函数同时调了那两个 helper。
//
// # 本文件的作用
//
// 上面那条主门跑的是**全仓扫描**（读数慢、且一旦漏放很难定位）。本文件是
// **纯夹具门**：把腿级判据的六种形态逐条钉死 ⇒⇒
//
//	① 同一个洞**再出现**时，夹具门**当场红**，而不是等主门在某次全仓扫描里
//	   偶然撞见；
//	② 每个夹具都有**正控**（必须判成有守卫）与**反控**（必须判成无守卫），
//	   ⇒⇒ 判据「过宽」与「过窄」两侧都会红。

// ★ 本文件里的夹具**只被解析、从不被执行** ⇒ 夹具里的 DSN 用
//   `fixture://not-a-real-dsn` 这种**非 DSN 形态**的占位符。
//   ★ 原因（R88 轮实测）：仓内已有的「写死 DSN」门（`hardcoded_dsn_fallback_test.go`）
//   是**文本级**扫描器 ⇒ 它会把**门文件里作为字符串嵌入的夹具源码**也当成真源码扫
//   ⇒ 夹具里一旦出现 `postgres://…` 字面量 ⇒ 那道门当场红（实测 3 处命中）。
//   ⇒ ⇒ **新增门文件时，夹具源码同样要遵守既有文本级门的口径。**

import (
	"os"
	"path/filepath"
	"testing"
)

// TestR88LegLevelDominanceSelfCheck 腿级支配判据的夹具门。
//
// ★ 这道门是 R88 变异 M1 的教训固化：M1 揭穿了 R87 门自己的洞
//   （「判定存在」被当成「判定一定执行」）⇒ ⇒ 必须有夹具把这条形态钉死。
func TestR88LegLevelDominanceSelfCheck(t *testing.T) {
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

	// ★ R89 轮：**每个夹具一个独立子目录**（= 独立包）。
	//
	// ★ 为什么必须这样：判据是**包级**的（包索引按**函数名**建）⇒⇒ 若多个夹具共用
	//   一个目录且**函数重名**（本文件的夹具里 `openPool` / `reset` 就重名了），
	//   它们会被**合并成同一个函数** ⇒⇒ 一个夹具的守卫会污染另一个夹具
	//   ⇒⇒ 读数失去意义（实测：R87 的老夹具就是这样串味的）。
	//   ⇒ ⇒ 每个夹具自成一个包 ⇒ 互不干扰。
	write := func(fixture, fn, body string) string {
		p := filepath.Join(dir, fixture, fn)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir fixture dir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return p
	}
	// pkgs 在**所有夹具写完之后**才建（包索引要看得见全部夹具文件）。
	var pkgs map[string]*r87PkgFacts
	var gidx map[string]*r91Node
	analyze := func(rel string) []r87Pair {
		p := filepath.Join(dir, rel)
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		// ★ 最后一个参数是**导入解析根目录**（跨包闸门要用），不是包目录
		//   ⇒⇒ 包目录由 `r87AnalyzeFile` 内部按 `filepath.Dir(path)` 推导。
		return r87AnalyzeFile(t, p, rel, string(raw), exported, pkgs, gidx, "example.com/x", dir)
	}

	// ── 夹具 ⑪：兄弟 helper + 调用者侧支配 ⇒ 必须判成「有守卫」（零候选）────
	//
	// ★ 这是仓内四条真腿的**真实形状**：破坏性字面量住在 `reset` 里，
	//   守卫住在 `openPool` 里，两个是**兄弟** ⇒⇒ 支配关系在测试函数那一侧。
	//   ⇒ 若判据只看「语句所在函数」，这四条真腿会全被判成没守卫（假阳性）。
	write("fx1", "sibling_guard_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func openPool(t *testing.T) *pgxpool.Pool {\n\t_ = \"SELECT current_database()\"\n\treturn nil\n}\n\n"+
		"func reset(pool *pgxpool.Pool) {\n"+
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.some_table\")\n}\n\n"+
		"func TestGuardedBySibling(t *testing.T) {\n\t_ = os.Getenv(\""+name+"\")\n"+
		"\tpool := openPool(t)\n\treset(pool)\n}\n")

	// ── 夹具 ⑫：★ 洞的固化 —— 同一文件里「一条有守卫的腿 + 一条没守卫的腿」 ──
	//
	// ★★★ 这就是 R88 变异 M1 的形态：文件级口径看到「判定在前面」⇒ 放行整文件；
	//   腿级口径必须**只**抓出那条没守卫的腿。
	write("fx2", "mixed_legs_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func openPool(t *testing.T) *pgxpool.Pool {\n\t_ = \"SELECT current_database()\"\n\treturn nil\n}\n\n"+
		"func reset(pool *pgxpool.Pool) {\n"+
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.some_table\")\n}\n\n"+
		"func TestGuardedLeg(t *testing.T) {\n\t_ = os.Getenv(\""+name+"\")\n"+
		"\tpool := openPool(t)\n\treset(pool)\n}\n\n"+
		"func TestUnguardedLeg(t *testing.T) {\n\t_ = os.Getenv(\""+name+"\")\n"+
		"\tpool := pgxpool.New(context.Background(), \"fixture://not-a-real-dsn\")\n"+
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.other_table\")\n}\n")

	// ── 夹具 ⑬：判定就在测试函数自己体内 ⇒ 必须判成「有守卫」────────────────
	write("fx3", "inline_guard_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func TestInlineGuard(t *testing.T) {\n\t_ = os.Getenv(\""+name+"\")\n"+
		"\t_ = \"SELECT current_database()\"\n"+
		"\tpool := pgxpool.New(context.Background(), \"fixture://not-a-real-dsn\")\n"+
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.some_table\")\n}\n")

	// ── 夹具 ⑭：守卫在**另一个不可达**的函数里 ⇒ 必须判成「无守卫」──────────
	//
	// ★ 与夹具 ⑫ 的区别：这里守卫**在文件里**，但破坏性那条腿**够不到它**
	//   ⇒⇒ 「文件里有判定」不等于「这条腿有判定」。
	write("fx4", "unreachable_guard_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func guardedHelper() {\n\t_ = \"SELECT current_database()\"\n}\n\n"+
		"func TestUnreachableGuard(t *testing.T) {\n\t_ = os.Getenv(\""+name+"\")\n"+
		"\tpool := pgxpool.New(context.Background(), \"fixture://not-a-real-dsn\")\n"+
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.some_table\")\n}\n")

	// ── 夹具 ⑮：破坏性在**被两个测试共用**的 helper 里，其中一条腿无守卫 ────
	//
	// ★ 这条断言的是**支配方向**：同一个破坏性 helper 被两条腿调用，
	//   只要其中一条够不到守卫 ⇒ 那条腿就是事故级 ⇒ 必须被抓出来。
	write("fx5", "shared_helper_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func openPool(t *testing.T) *pgxpool.Pool {\n\t_ = \"SELECT current_database()\"\n\treturn nil\n}\n\n"+
		"func sharedReset(pool *pgxpool.Pool) {\n"+
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.some_table\")\n}\n\n"+
		"func TestGuardedCaller(t *testing.T) {\n\t_ = os.Getenv(\""+name+"\")\n"+
		"\tpool := openPool(t)\n\tsharedReset(pool)\n}\n\n"+
		"func TestUnguardedCaller(t *testing.T) {\n\t_ = os.Getenv(\""+name+"\")\n"+
		"\tpool := pgxpool.New(context.Background(), \"fixture://not-a-real-dsn\")\n\tsharedReset(pool)\n}\n")

	// ── 夹具 ⑯：跨包闸门在**可达链**上（Test → helper → 闸门）⇒ 有守卫 ───────
	gateDir := filepath.Join(dir, "r88gate")
	if err := os.MkdirAll(gateDir, 0o750); err != nil {
		t.Fatalf("mkdir r88gate: %v", err)
	}
	gateSrc := "package r88gate\n\n" +
		"var GuardFunctions = []string{\"Gate\"}\n\n" +
		"func Gate() string { return \"SELECT current_database()\" }\n\n" +
		"func Verify() string { return \"SELECT current_database()\" }\n"
	if err := os.WriteFile(filepath.Join(gateDir, "gate.go"), []byte(gateSrc), 0o600); err != nil {
		t.Fatalf("write r88gate: %v", err)
	}
	write("fx6", "cross_package_gate_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"example.com/x/r88gate\"\n\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func openPool(t *testing.T) *pgxpool.Pool {\n\t_ = r88gate.Gate()\n\treturn nil\n}\n\n"+
		"func reset(pool *pgxpool.Pool) {\n"+
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.some_table\")\n}\n\n"+
		"func TestCrossPackageGate(t *testing.T) {\n\t_ = os.Getenv(\""+name+"\")\n"+
		"\tpool := openPool(t)\n\treset(pool)\n}\n")

	pkgs, gidx = r87BuildPkgFacts(t, dir, "example.com/x", exported)

	// ── 断言 ────────────────────────────────────────────────────────────────

	// ⑪ 兄弟 helper 形态 ⇒ 零候选（假阳性控制）。
	if got := analyze(filepath.Join("fx1", "sibling_guard_test.go")); len(got) != 0 {
		t.Fatalf("夹具 ⑪（兄弟 helper + 调用者侧支配）被判成 %d 条候选（%v）⇒ "+
			"腿级判据把「兄弟」当成了「无支配」⇒ 仓内四条真腿会被误报", len(got), got)
	}

	// ⑫ ★ 洞的固化：必须**恰好**抓出一条（那条没守卫的腿）。
	got12 := analyze(filepath.Join("fx2", "mixed_legs_test.go"))
	if len(got12) != 1 {
		t.Fatalf("夹具 ⑫（同文件里一有守卫一无守卫）应判出**恰好 1 条**（那条没守卫的腿），实得 %d 条（%v）⇒ "+
			"★ 腿级判据失效 ⇒ R88 变异 M1 那种「追加一条没守卫的第二入口」会漏放", len(got12), got12)
	}
	if got12[0].Root != "TestUnguardedLeg" {
		t.Fatalf("夹具 ⑫ 抓出的腿根应是 TestUnguardedLeg，实得 %q ⇒ 判据抓错了腿", got12[0].Root)
	}

	// ⑬ 判定在测试函数自己体内 ⇒ 零候选。
	if got := analyze(filepath.Join("fx3", "inline_guard_test.go")); len(got) != 0 {
		t.Fatalf("夹具 ⑬（判定在测试函数自己体内）被判成 %d 条候选（%v）⇒ 判据过宽", len(got), got)
	}

	// ⑭ 守卫在不可达函数里 ⇒ 恰好一条。
	if got := analyze(filepath.Join("fx4", "unreachable_guard_test.go")); len(got) != 1 {
		t.Fatalf("夹具 ⑭（守卫在另一个不可达函数里）应判出 1 条，实得 %d 条（%v）⇒ "+
			"★「文件里有判定」被当成了「这条腿有判定」", len(got), got)
	}

	// ⑮ 被共用的破坏性 helper + 其中一条腿无守卫 ⇒ 恰好一条。
	got15 := analyze(filepath.Join("fx5", "shared_helper_test.go"))
	if len(got15) != 1 || got15[0].Root != "TestUnguardedCaller" {
		t.Fatalf("夹具 ⑮（共用 helper + 一条腿无守卫）应判出恰好 1 条且腿根为 TestUnguardedCaller，实得 %d 条（%v）",
			len(got15), got15)
	}

	// ⑯ 跨包闸门在可达链上 ⇒ 零候选。
	if got := analyze(filepath.Join("fx6", "cross_package_gate_test.go")); len(got) != 0 {
		t.Fatalf("夹具 ⑯（跨包闸门在可达链上）被判成 %d 条候选（%v）⇒ 跨包守卫被误判成无守卫", len(got), got)
	}
}
