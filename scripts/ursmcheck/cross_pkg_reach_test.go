package ursm_test

// R91 轮的门：**跨包可达性** —— 把 R90 轮留下的「可达性只到同包」这条边界补上。
//
// # 这道门补的是哪块缺口
//
// R87~R90 轮的可达性是**同包**的 ⇒⇒ 于是三件事判不出来：
//
//	① **跨包消费事实源 DSN**（★ **漏放**方向，数据事故级）
//	   —— 腿在包 A、DSN 在包 B 被读取并连上 ⇒⇒ 门**看不见**这条腿。
//	② **跨包链式闸门**：腿 → 包 B 的 helper → 包 C 的**声明闸门**。
//	③ **跨包 PG 连接打开点**：腿在包 A，连库在包 B。
//
// ★ R91 轮实测的仓内形态：跨包被调函数**自己读了事实源 DSN** 的有 **11** 处
//   （`config.Load` × 8；`pkg/identity.ShadowDatabase` × 3 读 `IDENTITY_SHADOW_DSN`）
//   ⇒⇒ 形态**真实存在**，只是当前没和破坏性语句落在同一条腿上。
//
// # ★★ 而「跨包内联判定」**故意不闭合**（本门最关键的一条）
//
// `internal/testguard.RequireDatabaseMatchesDSN` 的**体内**就有目标库判定证据，
// 但 R87 轮刻意**不**把它列入 `GuardFunctions` 名单：它只核对「连接落在 DSN 指的
// 那个库」，**不决定这条腿跑不跑** ⇒⇒ DSN 指产品库时它照样通过
// ⇒⇒ 破坏性 SQL 照样落在产品库上 ⇒⇒ ⇒ 它**不是**安全闸门。
//
// ⇒⇒ 所以跨包**只传播「调用了声明闸门」**，**不传播「体内写了判定」**。
//
// # 本文件的作用
//
// 三个夹具，**每个一个独立子目录**（= 独立包）★
//
//	㉗ 跨包**链式声明闸门**（腿 → P.helper → Q.Helper → `Q.Gate()`）⇒ 零候选
//	㉘ 跨包**内联判定**（对方包**没**声明名单）⇒ **必须仍是候选**（真腿不能被误判成有守卫）
//	㉙ 跨包**开 PG 连接**（DSN 在本包读、池在另一个包开）⇒ 一条候选
//
// ★ ㉘ 是本门**最关键**的一条：它钉住「跨包不传播内联判定」这条**安全判据**
//   ——⇒ 一旦有人把内联判定也跨包传播，真腿会被误判成「有守卫」⇒⇒ **漏放**。

import (
	"os"
	"path/filepath"
	"testing"
)

// TestR91CrossPackageReachabilitySelfCheck 跨包可达性的夹具门。
func TestR91CrossPackageReachabilitySelfCheck(t *testing.T) {
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

	const destructiveBody = "package fx\n\nimport (\n\t\"context\"\n\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n" +
		"func crossFileReset(pool *pgxpool.Pool) {\n" +
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.some_table\")\n}\n"

	// ── ㉗：跨包**链式声明闸门** ⇒ 零候选 ───────────────────────────────────
	//
	// ★ 形状：腿 → `fx.helper` → `otherpkg.Helper` → `otherpkg.Gate()`
	//   （`otherpkg` **自己声明** `GuardFunctions = ["Gate"]`）
	// ⇒⇒ 闸门在**两个包之外** ⇒ 同包可达性判不出来。
	write("fx17", "otherpkg/other.go", "package otherpkg\n\n"+
		"var GuardFunctions = []string{\"Gate\"}\n\n"+
		"func Gate() string { return \"SELECT current_database()\" }\n\n"+
		"func Helper() string { return Gate() }\n")
	write("fx17", "helper_test.go", "package fx\n\nimport (\n\t\"testing\"\n\n"+
		"\"example.com/x/fx17/otherpkg\"\n)\n\n"+
		"func helper() string { return otherpkg.Helper() }\n")
	write("fx17", "leg_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func TestCrossPkgChainGate(t *testing.T) {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tpool, err := pgxpool.New(context.Background(), dsn)\n\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n"+
		"\tt.Cleanup(pool.Close)\n\t_ = helper()\n"+
		"\t_, _ = pool.Exec(context.Background(), \"TRUNCATE public.some_table\")\n}\n")

	// ── ㉘：跨包**内联判定**（对方包**没**声明名单）⇒ **必须仍是候选** ─────────
	//
	// ★★★ 本门**最关键**的一条：对方包里的函数**体内**有目标库判定证据，
	//   但那个包**没有**声明 `GuardFunctions` ⇒⇒ 「这个包恰好含有判定」
	//   **不是**「这个函数是闸门」⇒⇒ 腿**不能**因此被判成有守卫。
	//
	// ★ 旧口径（R87~R90）在这里是**漏放**方向：跨包调用进「有判定证据但没声明名单」
	//   的包 ⇒ 包级粗口径兜底 ⇒ 一律当成闸门调用 ⇒ 真腿被放过。
	write("fx18", "otherpkg/other.go", "package otherpkg\n\n"+
		"// Verify 只核对「连接落在哪个库」，**不决定这条腿跑不跑** ⇒⇒ 不是闸门。\n"+
		"func Verify() string { return \"SELECT current_database()\" }\n")
	write("fx18", "destructive_test.go", destructiveBody)
	write("fx18", "leg_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"example.com/x/fx18/otherpkg\"\n\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func TestCrossPkgInlineJudge(t *testing.T) {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tpool, err := pgxpool.New(context.Background(), dsn)\n\tif err != nil {\n\t\tt.Skip(\"no pg\")\n\t}\n"+
		"\tt.Cleanup(pool.Close)\n\t_ = otherpkg.Verify()\n\tcrossFileReset(pool)\n}\n")

	// ── ㉙：跨包**开 PG 连接**（DSN 在本包读、池在另一个包开）⇒ 一条候选 ───────
	write("fx19", "otherpkg/other.go", "package otherpkg\n\nimport (\n\t\"context\"\n\t\"os\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func OpenPool() *pgxpool.Pool {\n\tdsn := os.Getenv(\""+name+"\")\n"+
		"\tpool, _ := pgxpool.New(context.Background(), dsn)\n\treturn pool\n}\n")
	write("fx19", "destructive_test.go", destructiveBody)
	write("fx19", "leg_test.go", "package fx\n\nimport (\n\t\"testing\"\n\n"+
		"\"example.com/x/fx19/otherpkg\"\n)\n\n"+
		"func TestCrossPkgOpenPool(t *testing.T) {\n"+
		"\tpool := otherpkg.OpenPool()\n\tcrossFileReset(pool)\n}\n")

	pkgs, gidx := r87BuildPkgFacts(t, dir, "example.com/x", exported)

	analyze := func(fixture, fn string) []r87Pair {
		p := filepath.Join(dir, fixture, fn)
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		// ★ 最后一个参数是**导入解析根目录**（跨包闸门要用），不是包目录
		//   ⇒⇒ 包目录由 `r87AnalyzeFile` 内部按 `filepath.Dir(path)` 推导。
		//
		// ★★ 夹具的 **import 路径必须与目录布局一致**（`example.com/x/fxNN/otherpkg`
		//   对应 `<tmp>/fxNN/otherpkg`）⇒ 否则跨包边指向**不存在的包** ⇒ 边接不上
		//   ⇒ ⇒ 夹具**恒绿/恒红**但原因与被测形态无关（R91 轮实测踩过一次）。
		return r87AnalyzeFile(t, p, fn, string(raw), exported, pkgs, gidx, "example.com/x", dir)
	}

	// ㉗ 跨包链式声明闸门 ⇒ 零候选。
	if got := analyze("fx17", "leg_test.go"); len(got) != 0 {
		t.Fatalf("夹具 ㉗（腿 → 同包 helper → 另一个包的 helper → 另一个包的**声明闸门**）"+
			"被判成 %d 条候选（%v）⇒ ★ 跨包**链式**闸门没接上 ⇒ 有守卫的腿被误报成没守卫",
			len(got), got)
	}

	// ㉘ ★★ 跨包内联判定 ⇒ **必须仍是候选**（真腿不能被误判成有守卫）。
	if got := analyze("fx18", "destructive_test.go"); len(got) != 1 {
		t.Fatalf("夹具 ㉘（另一个包里的函数**体内**有目标库判定，但那个包**没**声明闸门名单）"+
			"应判出 1 条候选，实得 %d 条（%v）⇒ "+
			"★ 把「这个包**恰好**含有判定」当成了「这个函数是闸门」⇒ "+
			"⇒ 跨包传播内联判定 ⇒⇒ **真腿被放过**（漏放，数据事故级）", len(got), got)
	}

	// ㉙ 跨包开 PG 连接 ⇒ 一条候选（连库在别的包，DSN 也在别的包）。
	if got := analyze("fx19", "destructive_test.go"); len(got) != 1 {
		t.Fatalf("夹具 ㉙（DSN 与 PG 连接都在**另一个包**里）应判出 1 条候选，实得 %d 条（%v）⇒ "+
			"★ 跨包的「消费事实源 DSN」判不出来 ⇒⇒ 门看不见这条腿", len(got), got)
	}
}
