package bg

// 836_supplier_errors_heap_reassert 的**复制品一致性**契约。
//
// # 为什么需要这条门
//
// 836 的载荷三段都是从别处**抄**进来的：
//
//	1) ensure_supplier_errors_partition  ← 813
//	2) columnar_healthcheck               ← sql/schema/01-schema.sql
//	3) 分区转换 DO 块                     ← 813 第 2 步
//
// 813 的文件头就是为此写的「三基线一致性契约测试钉同形」。理由对 836 完全
// 成立：**复制品之间会各自漂移**，而漂移的方向是「权威的那份改了、抄来的那份
// 没改」，于是修复在下一轮静默失效 —— 症状与本轮要修的缺陷**同形**。
//
// # 这条门在本轮抓到的真问题（不是假想）
//
// 生成 836 的第一版抽取只取了 `$$…$$` 函数体，把 `CREATE … FUNCTION` 签名漏掉
// 了，交付的是「裸 `$$` + 函数体」的语法死文件。而**当时的逐字自证是绿的** ——
// 因为它在两边抽的是同一个被截断的片段。
//
// ⇒ 派生量与真值同形时要去读定义。判据不能只断言「我抽的那段两边一样」，必须
//   额外断言「**整个 CREATE 语句在文件里**」，而且要在**剥掉注释后**数 ——
//   否则文件头里一句解释「为什么这里用 CREATE OR REPLACE」的散文就能把计数撑大。
//
// 本文件不读数据库，钉的是文本契约；转换行为由集成门（真实 fresh-install 路径）
// 与一次性副本上的实跑覆盖。

import (
	"os"
	"strings"
	"testing"
)

const (
	m836      = "../sql/migrations/startup/836_supplier_errors_heap_reassert.sql"
	m813      = "../sql/migrations/startup/813_supplier_errors_partitions_heap.sql"
	mBaseline = "../sql/schema/01-schema.sql"
)

func readMigrationFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// sqlStatement 抽出一个 `CREATE [OR REPLACE] FUNCTION` 的**整条语句**：从
// `CREATE` 起到紧随其后的那个 `$$` 之后（含尾部分号）。
//
// 两个坑都在这里，各自都真实发生过：
//   - 只抽到 `$$` 之前 ⇒ 丢掉签名，交付语法死文件（836 的第一版）。
//   - 只抽到 `$$` 为止   ⇒ 丢掉分号，下一条 CREATE 会被并进本条语句，
//     报错还指向**下一条** CREATE（`syntax error at or near "CREATE"`）。
func sqlStatement(t *testing.T, src, marker string) string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("marker not found in source: %s", marker)
	}
	a := strings.Index(src[i:], "$$")
	if a < 0 {
		t.Fatalf("no $$ body after %s", marker)
	}
	a += i
	b := strings.Index(src[a+2:], "$$")
	if b < 0 {
		t.Fatalf("unterminated $$ body after %s", marker)
	}
	b += a + 2
	// b 指向**收尾 $$ 的起点**，所以分号在 b+2 而不是 b。
	// （这个 off-by-two 本轮犯了第三次：生成器一次、本测试一次。写完必须真跑。）
	stmt := src[i:b]
	if src[b+2:b+3] != ";" {
		t.Fatalf("statement at %s is not semicolon-terminated right after $$ — the extraction "+
			"helper needs updating to match the source shape (got %q)", marker, src[b:b+3])
	}
	return stmt + ";"
}

// 本包已有 stripSQLComments（supplier_price_zero_wording_test.go），直接复用。
//
// ★ 与之相同的一课：同一天我已经因为「造了第二个同功能助手」（830 的迁移读取
//   助手）踩过一次，所以这里先查后写。它不是引号感知的，对本判据够用：本文件只
//   用来数 `CREATE OR REPLACE FUNCTION` 的条数并比较函数体，而那些语句都出现在
//   行首、不在任何字符串字面量里。

// normExec 归一后可执行文本：CREATE [OR REPLACE] 视为同一写法，空白全部折叠。
//
// 两处归一都有实测依据：813 写 CREATE OR REPLACE、基线写 CREATE（同一函数的
// 两种投递方式）；而基线把签名排在一行、813 排在两行 —— 换行不是语义差异。
func normExec(t *testing.T, stmt string) string {
	t.Helper()
	s := strings.ReplaceAll(stmt, "CREATE OR REPLACE FUNCTION", "CREATE FUNCTION")
	return strings.Join(strings.Fields(stripSQLComments(s)), " ")
}

// 1) ensure 函数：可执行体必须与 813 一致（注释措辞允许不同）
func Test836EnsureFunctionMatchesThirteenExecutableBody(t *testing.T) {
	got := sqlStatement(t, readMigrationFile(t, m836),
		"CREATE OR REPLACE FUNCTION public.ensure_supplier_errors_partition")
	want := sqlStatement(t, readMigrationFile(t, m813),
		"CREATE OR REPLACE FUNCTION public.ensure_supplier_errors_partition")

	if normExec(t, got) != normExec(t, want) {
		t.Errorf("836's ensure_supplier_errors_partition executable body has drifted from 813's.\n" +
			"813 is the chain's last word on this function (intentional_function_chains registers\n" +
			"V371 -> 699 -> 813 -> 836), so a divergence means 836 re-asserts something other than\n" +
			"the intended final state. 813's own file is immutable by repo discipline, so fix 836.")
	}
	// 反向：836 不得把 ensure 又写回列存版（那正是本迁移要消除的漂移）。
	if strings.Contains(normExec(t, got), "USING columnar") {
		t.Error("836's ensure function still creates USING columnar partitions — that is the exact " +
			"drift this migration exists to remove, and it would push the family back to columnar " +
			"on the next ensure tick")
	}
}

// 2) columnar_healthcheck：函数体逐字等于基线（语句关键字有一处刻意差异）
func Test836HealthcheckBodyIsVerbatimFromBaseline(t *testing.T) {
	got := sqlStatement(t, readMigrationFile(t, m836),
		"CREATE OR REPLACE FUNCTION public.columnar_healthcheck")
	baseline := readMigrationFile(t, mBaseline)
	want := sqlStatement(t, baseline, "CREATE FUNCTION public.columnar_healthcheck")

	// 刻意差异：基线写 CREATE FUNCTION（全新安装时该函数不存在），836 跑在已经
	// 有它的库上，裸 CREATE 会以 "function already exists" 中止 —— 也就是迁移会
	// 在它专门要修的那些库上失败。所以语句是 CREATE OR REPLACE，**体必须逐字相同**。
	if strings.Replace(got, "CREATE OR REPLACE FUNCTION", "CREATE FUNCTION", 1) != want {
		t.Error("836's columnar_healthcheck body is not verbatim from sql/schema/01-schema.sql. " +
			"The CREATE-vs-CREATE-OR-REPLACE difference above is intended; anything else is drift")
	}
	// 载荷的**用意**：没有它，转换完成后这一族仍然报 expected='unknown'，也就是
	// 漂移仍不可见。这条断言钉住「修复看得见」这半边，而不只是「分区转了」。
	if !strings.Contains(normExec(t, got), "supplier_errors") {
		t.Error("836's columnar_healthcheck does not mention supplier_errors — the drift this " +
			"migration removes would still be invisible in the health surface")
	}
}

// 3) 分区转换 DO 块：逐字等于 813 第 2 步
func Test836ConversionBlockIsVerbatimFromThirteen(t *testing.T) {
	src813 := readMigrationFile(t, m813)
	const startMark = "DO $$\nDECLARE\n    part        record;"
	a := strings.Index(src813, startMark)
	if a < 0 {
		t.Fatalf("cannot find 813's conversion DO block (anchor %q)", startMark)
	}
	b := strings.Index(src813[a:], "END $$;")
	if b < 0 {
		t.Fatal("813's conversion DO block is unterminated")
	}
	want := src813[a : a+b+len("END $$;")]

	got := readMigrationFile(t, m836)
	if !strings.Contains(got, want) {
		t.Error("836's partition-conversion DO block is not verbatim from 813's step 2. This block " +
			"carries the data-carrying path (DETACH -> rename -> rebuild as heap -> copy back -> " +
			"row-count parity -> drop backup) that the live supplier_errors_2026_10 (3,222 rows) " +
			"needs; any rewrite must re-verify the parity check")
	}
}

// 4) 整条 CREATE 语句必须在（836 自己踩过的那个坑），且要**剥掉注释后**数
func Test836ContainsCompleteCreateStatements(t *testing.T) {
	src := readMigrationFile(t, m836)
	code := stripSQLComments(src)

	const want = 2
	if got := strings.Count(code, "CREATE OR REPLACE FUNCTION"); got != want {
		t.Errorf("836 has %d executable CREATE OR REPLACE FUNCTION statement(s), want %d. A count "+
			"below that means a payload lost its CREATE signature and the file is a syntax-dead "+
			"migration that still reads fine; a count above it means prose leaked into the count "+
			"(this count is deliberately taken on comment-stripped text for that reason)",
			got, want)
	}
	for _, marker := range []string{
		"CREATE OR REPLACE FUNCTION public.ensure_supplier_errors_partition",
		"CREATE OR REPLACE FUNCTION public.columnar_healthcheck",
	} {
		if !strings.Contains(code, marker) {
			t.Errorf("836 does not contain the executable statement %q", marker)
		}
	}
}

//  5. 自证段必须在：813 能「记账为已应用、活库却没变」的根因就是没有任何一步
//     会检查结果，只有断言能关掉这个漏洞。
func Test836SelfCheckIsPresent(t *testing.T) {
	src := readMigrationFile(t, m836)
	for _, want := range []string{
		"836 self-check failed",            // 三条断言各自的失败文案
		"are still not heap",               // (b) 分区访问方法
		"still creates USING columnar",     // (a) ensure 形态
		"does not mention supplier_errors", // (c) 健康面是否复明
	} {
		if !strings.Contains(src, want) {
			t.Errorf("836's self-check is missing the assertion wording %q — without it the "+
				"migration can apply successfully while the drift survives, which is precisely "+
				"the 813 failure mode", want)
		}
	}
}
