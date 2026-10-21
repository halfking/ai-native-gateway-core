package ursm_test

// R89 轮的门：**同包跨文件的可达性** —— 把 R88 轮留下的「可达性只到同文件」这条边界补上。
//
// # 这道门补的是哪块缺口
//
// R88 轮把判据从「文件级」降到「腿级支配性」，但**可达性仍是同文件的** ⇒⇒
//
//	「守卫写在同包的**另一个文件**里」判不出来
//	⇒⇒ 方向是**假阳性**（把一条**有**守卫的腿判成**没**守卫）。
//
// ★ 为什么这是真缺口而不是理论问题：Go 的**测试辅助函数按惯例就住在同包的另一个文件**里
//   （仓内现成的例子：`domains/session/v2/session_request_status_backfill_test.go` 里的
//   `statusBackfillFixtureDB` 被 `session_duration_ms_realdb_test.go` 跨文件调用）
//   ⇒⇒ **一旦有人把守卫放进兄弟文件，旧口径就会误报**，
//   而误报的下场是「门被人加豁免」⇒⇒ **洞被固化**。
//
// ★ 本轮的实现：**按函数名**给每个包建一张「守卫状态 + 调用边 + 是不是测试入口」索引
//   （`r87PkgFacts.pkgFuncs`）⇒⇒ ① 跨文件同包的调用链接上了；
//   ② **腿根（测试入口）也改成按包搜** ⇒⇒ 因为腿根同样可能住在另一个文件。
//
// # 本文件的作用
//
// 三个文件、两个夹具：
//   · `r89destructive_test.go` —— **候选文件**：自己读事实源 DSN 名、自己开连接、
//     并且持有那条破坏性 SQL（**这正是「文件级候选集」能看见的形状**）；
//   · `r89guard_test.go`       —— **守卫住在兄弟文件**里（`openGuardedPool`）；
//   · `r89reachable_test.go`   —— **腿根（测试入口）也住在另一个文件**里。
// ⇒⇒ 夹具 ⑰ 钉「跨文件可达 ⇒ 必须判成有守卫」；夹具 ⑱ 钉「跨文件但不可达 ⇒ 必须判成无守卫」。
//
// ★★★ **本轮没有解决的（已量化，见 handoff §19.20.39）**：**候选集仍是文件级** ⇒⇒
//   「破坏性 SQL 在 A 文件、读 DSN 在 B 文件」的腿**根本进不了候选集**
//   ⇒⇒ 实测仓内 **106 个破坏性测试文件**处在这种形态 ⇒⇒ 下轮第一项。

import (
	"os"
	"path/filepath"
	"testing"
)

// TestR89CrossFileReachabilitySelfCheck 跨文件可达性的夹具门。
func TestR89CrossFileReachabilitySelfCheck(t *testing.T) {
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

	write := func(fn, body string) {
		p := filepath.Join(dir, fn)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", fn, err)
		}
	}

	// ── 候选文件：自己读事实源 DSN 名、自己开连接、持有那条破坏性 SQL ────────
	//
	// ★ 这个文件**自己**满足候选条件 ⇒⇒ 今天的（文件级）候选集也能看见它
	//   ⇒⇒ 于是「跨文件」这件事被**隔离**成可达性/腿根这一个变量。
	write("r89destructive_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func crossFileReset(pool *pgxpool.Pool) {\n"+
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.some_table\")\n}\n\n"+
		"func factSourcePool(t *testing.T) *pgxpool.Pool {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tif dsn == \"\" {\n\t\tt.Skip(\"unset\")\n\t}\n"+
		"\tpool, err := pgxpool.New(context.Background(), dsn)\n\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n"+
		"\tt.Cleanup(pool.Close)\n\treturn pool\n}\n")

	// ── 守卫住在**兄弟文件**里 ────────────────────────────────────────────────
	write("r89guard_test.go", "package fx\n\nimport (\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func openGuardedPool(t *testing.T) *pgxpool.Pool {\n"+
		"\t_ = \"SELECT current_database()\"\n\treturn nil\n}\n")

	// ── 腿根（测试入口）住在**第三个文件**里 ─────────────────────────────────
	write("r89reachable_test.go", "package fx\n\nimport (\n\t\"testing\"\n)\n\n"+
		"func TestCrossFileReachable(t *testing.T) {\n"+
		"\tpool := factSourcePool(t)\n\tguarded := openGuardedPool(t)\n"+
		"\t_ = guarded\n\tcrossFileReset(pool)\n}\n")

	// ── 夹具 ⑱：同包另一个文件里有守卫，但这条腿**够不到它** ────────────────────
	write("r89unreachable_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func TestCrossFileUnreachable(t *testing.T) {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tif dsn == \"\" {\n\t\tt.Skip(\"unset\")\n\t}\n"+
		"\tpool, err := pgxpool.New(context.Background(), dsn)\n\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n"+
		"\tt.Cleanup(pool.Close)\n"+
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.other_table\")\n}\n")

	pkgs, gidx := r87BuildPkgFacts(t, dir, "example.com/x", exported)

	analyze := func(fn string) []r87Pair {
		p := filepath.Join(dir, fn)
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		// ★ 最后一个参数是**导入解析根目录**（跨包闸门要用），不是包目录
		//   ⇒⇒ 包目录由 `r87AnalyzeFile` 内部按 `filepath.Dir(path)` 推导。
		return r87AnalyzeFile(t, p, fn, string(raw), exported, pkgs, gidx, "example.com/x", dir)
	}

	// ⑰ 跨文件可达 ⇒ 零候选（**假阳性控制**：旧口径在这里会误报）。
	//
	// ★ 要分析的是**含破坏性语句的那个文件**（`r89destructive_test.go`）——
	//   分析测试文件是**瞄错对象**（那个文件根本没有破坏性语句 ⇒ 恒绿）。
	if got := analyze("r89destructive_test.go"); len(got) != 0 {
		t.Fatalf("夹具 ⑰（守卫与腿根都在同包的另两个文件里）被判成 %d 条候选（%v）⇒ "+
			"★ 跨文件可达性 / 包级腿根没接上 ⇒ 有守卫的腿被误报成没守卫 ⇒ 门会被加豁免 ⇒ 洞被固化",
			len(got), got)
	}

	// ⑱ 跨文件但不可达 ⇒ 恰好一条（**判据不能放宽到「同包任一函数有守卫就算」**）。
	got := analyze("r89unreachable_test.go")
	if len(got) != 1 {
		t.Fatalf("夹具 ⑱（守卫在同包另一个文件里但这条腿够不到它）应判出 1 条，实得 %d 条（%v）⇒ "+
			"★ 判据被放宽成「同包任一函数有守卫就算」⇒ 会把没守卫的腿放过去", len(got), got)
	}
}
