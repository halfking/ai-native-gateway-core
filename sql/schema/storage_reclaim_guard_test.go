// R89-DS（208 号）：`sql/fixes/2026-10-02-db-storage-reclaim.sql` 的三道守卫
// 在**活跃行**里原本都站不住，而这份脚本是要 DBA 在生产 252 上**手跑**的。
//
// 与 audit_script_test.go 同源：被审对象是**脚本/产物**而不是运行时代码，
// 所以按文本结构断言是合适的——它关心的正是「哪些守卫在里面」。
//
// 三处原始缺陷（都在 208 号坐实）：
//
//  1. `c.relname < 'usage_facts_default'` 被用来表达「只碰过去的日期分区」。
//     **它是空操作**：字典序下 `usage_facts_2026xxxx` 第 13 个字符是数字
//     （'2' = 0x32），`usage_facts_default` 是字母（'d' = 0x64）⇒ **所有**
//     日期分区（含脚本注释里明确写「保留」的 usage_facts_20261003）都排在
//     default **之前**，谓词恒为真。⇒ 用**名字的形状**冒充**时间先后**。
//  2. `TRUNCATE public.usage_facts_default` 是**唯一没有非空门禁**的破坏性
//     动作（同文件的另外两个都有）。而兜底分区恰恰是「时间越界写入」的落点，
//     最容易积累真实行。脚本头写「实测 0 行」——那是**另一台机器**某个时刻
//     的测量，不是对 252 / 本机的保证。
//  3. 判「空表」用 `c.reltuples = 0`，而 reltuples 是**规划器估算**不是事实。
//     脚本自己在 [可选 1] 写着「625 张表自 stats_reset 起从未 ANALYZE」
//     ⇒ 它已认定该字段不可信，却拿它当**删表门禁**。
//
// ⚠️ 注释先剥再判（audit_script_test.go:135-137 已记同一类坑）：这份脚本的
// 头部注释里**逐条描述了这三个 bug**（R89-DS 的更正横幅就在里面）⇒ 原始文本
// 搜索会被自己的说明满足，判据恒绿。
package schema

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const storageReclaimScript = "../fixes/2026-10-02-db-storage-reclaim.sql"

func storageReclaimSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(storageReclaimScript)
	if err != nil {
		t.Fatalf("read %s: %v", storageReclaimScript, err)
	}
	return string(b)
}

// stripSQLComments 去掉 `--` 行注释。**保留行数**（逐行替换，不删行）——
// 分节标记是注释里的行号，删行会让它和剥注释后的正文错位。
func stripSQLComments(src string) string {
	out := make([]string, 0, 64)
	for _, ln := range strings.Split(src, "\n") {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i]
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// sectionHeaderRe 匹配脚本自带的分节标记（注释行，仅用于**定位**分段）。
var sectionHeaderRe = regexp.MustCompile(`(?i)^\s*--\s*-+\s*(2[a-z])\.`)

// reclaimFindings 是一份「这份脚本会做什么破坏性动作」的**结构化摘要**。
// 判据全部做在这上面，好让反向对照能直接喂构造文本。
type reclaimFindings struct {
	sections          int      // 认出的 `-- ---- 2x.` 分节数
	drops             int      // 破坏性 DROP TABLE 数
	truncates         int      // 破坏性 TRUNCATE TABLE 数
	unguardedSections []string // 含破坏性动作但本节没有真实空表探针的节名

	lexicographicNameCompare int // 活跃行里的 `relname <` / `relname >`
	reltuplesAsPredicate     int // 活跃行里的 `reltuples = 0`
	usesRealDate             bool
}

// orderingCompareRe 抓 `relname` 后面的**比较运算符**（连同一个后继字符，
// 因为 RE2 没有前瞻，只能带出来自己判）。
//
// ⚠️ 分支顺序要紧：双字符形式必须排在单字符前面。写成 `[<>]=?|[<>][^=]` 时
// `<>` 会先被 `<` 吃掉（`=?` 匹配空），导致 `<>` 被误判成排序比较 ——
// 本门第二版就踩了这个，负控 5 钉住。
var orderingCompareRe = regexp.MustCompile(`(?i)relname\s*([<>][>=]|[<>])`)

// countLexicographicCompares 数「用分区名做大小比较」的次数。
//
// ⚠️ 必须排除 `<>` / `<=` / `>=`：脚本里 `AND c.relname <> 'usage_facts_default'`
// 是**合法且必要**的排除条件，而本门第一版用 `strings.Contains(l, "relname <")`
// 把它误报了一次——**一道误报正确代码的门比没有门更坏**（负控 5 钉住这条）。
func countLexicographicCompares(lowerLine string) int {
	n := 0
	for _, m := range orderingCompareRe.FindAllStringSubmatch(lowerLine, -1) {
		op := m[1]
		if strings.HasPrefix(op, "<>") || strings.HasPrefix(op, "<=") || strings.HasPrefix(op, ">=") {
			continue
		}
		n++
	}
	return n
}

// analyzeReclaim 按**脚本自带的分节标记**切段，逐节问：这一节有破坏性动作吗？
// 有的话，本节里有真实空表探针吗？
//
// 为什么按分节、而不是按语句（`;` 边界）或按「离上一个动作的最近距离」：
//
//	① 按语句会**误判正确代码** —— 2c 合法地写成
//	   `IF NOT <探针> … THEN … CONTINUE; END IF;` 再往下 DROP，
//	   探针落在前一条语句里，按语句判就是「无门禁」。
//	   **一道误报正确代码的门比没有门更坏**（199 号 §96 / 200 号 §101 同一纪律）。
//	② 按「最近距离」会让**上一节的探针替这一节背书**，那正是 207 号 §118
//	   踩过的「A 的证据替 B 背书」。
//
// 分节标记是脚本自己的结构 ⇒ 判据与被测量对象**同源**；标记被删掉时
// sections == 0，主断言的覆盖下限会红。
func analyzeReclaim(raw string) reclaimFindings {
	var f reclaimFindings

	stripped := strings.Split(stripSQLComments(raw), "\n")
	rawLines := strings.Split(raw, "\n")

	type hdr struct {
		line int
		name string
	}
	var hdrs []hdr
	for i, ln := range rawLines {
		if m := sectionHeaderRe.FindStringSubmatch(ln); m != nil {
			hdrs = append(hdrs, hdr{line: i, name: m[1]})
		}
	}
	f.sections = len(hdrs)

	lower := make([]string, len(stripped))
	for i, ln := range stripped {
		l := strings.ToLower(ln)
		lower[i] = l
		if countLexicographicCompares(l) > 0 {
			f.lexicographicNameCompare += countLexicographicCompares(l)
		}
		if strings.Contains(l, "reltuples = 0") {
			f.reltuplesAsPredicate++
		}
	}
	f.usesRealDate = strings.Contains(strings.ToLower(strings.Join(stripped, "\n")), "::date < current_date")

	for hi, h := range hdrs {
		end := len(lower)
		if hi+1 < len(hdrs) {
			end = hdrs[hi+1].line
		}
		if h.line >= end {
			continue
		}
		body := strings.ToLower(strings.Join(lower[h.line:end], "\n"))
		drops := strings.Count(body, "drop table")
		truncs := strings.Count(body, "truncate table")
		f.drops += drops
		f.truncates += truncs
		if drops+truncs > 0 && !strings.Contains(body, "storage_reclaim_table_is_empty") {
			f.unguardedSections = append(f.unguardedSections, h.name)
		}
	}
	return f
}

// TestStorageReclaimDestructiveActionsAreGuarded 是本门的主断言。
func TestStorageReclaimDestructiveActionsAreGuarded(t *testing.T) {
	f := analyzeReclaim(storageReclaimSource(t))

	// 覆盖下限：先把「分节与动作都被数到了」坐实。抽取器坏掉时下面会全绿。
	if f.sections < 3 {
		t.Fatalf("只认出 %d 个 `-- ---- 2x.` 分节（下限 3）：分段器很可能已失效，"+
			"此时「每节都有门禁」会与「一节都没数到」同步为真。"+
			"若你重排了脚本，请同步更新本门的分段约定", f.sections)
	}
	if f.drops < 2 || f.truncates < 1 {
		t.Fatalf("只数到 %d 个 DROP / %d 个 TRUNCATE（下限 2 / 1）：动作抽取器很可能已失效",
			f.drops, f.truncates)
	}

	if len(f.unguardedSections) > 0 {
		t.Errorf("以下分节含破坏性动作（DROP/TRUNCATE）却没有真实空表探针"+
			"（storage_reclaim_table_is_empty）：%s\n"+
			"  删表不可回滚。守卫必须与动作**同节**——用别处的探针替它背书等于没门禁"+
			"（207 号 §118 的「A 的证据替 B 背书」）。",
			strings.Join(f.unguardedSections, ", "))
	}

	if f.lexicographicNameCompare > 0 {
		t.Errorf("活跃行里仍有 %d 处用 `relname <` / `relname >` 做分区名比较：\n"+
			"  字典序下 `usage_facts_2026xxxx`（数字 '2'=0x32）排在 "+
			"`usage_facts_default`（字母 'd'=0x64）**之前**，谓词恒为真、什么也护不住；"+
			"「过去」必须按分区名里的**真实日期**比 part_date < current_date",
			f.lexicographicNameCompare)
	}
	if f.reltuplesAsPredicate > 0 {
		t.Errorf("活跃行里仍有 %d 处把 `reltuples = 0` 当判据：\n"+
			"  reltuples 是规划器**估算**（PG14+ 从未 ANALYZE 过甚至是 -1）；"+
			"表在空时被 ANALYZE 过、之后写入的行不会让它变非 0 ⇒ 会放行一次有数据的删表。"+
			"请用 EXISTS (SELECT 1 … LIMIT 1) 真探针", f.reltuplesAsPredicate)
	}
	if !f.usesRealDate {
		t.Error("未找到 `::date < current_date` 形式的真实日期判据：" +
			"dry-run 报表与执行体必须用它，否则报表会把明确要保留的未来分区标成 DROP")
	}
}

// TestAnalyzeReclaimDiscriminates 是本门的鉴别力证明：喂**故意错误**的文本，
// 断言判据会把它们判成「不合规」。不依赖真实文件、不依赖临时改文件。
func TestAnalyzeReclaimDiscriminates(t *testing.T) {
	t.Run("负控1_未加门禁的节必须被报成无门禁", func(t *testing.T) {
		src := "-- ---- 2a. usage_facts_default ----\n" +
			"IF to_regclass('public.usage_facts_default') IS NOT NULL THEN\n" +
			"  EXECUTE 'TRUNCATE TABLE public.usage_facts_default';\n" +
			"END IF;\n"
		f := analyzeReclaim(src)
		if f.sections != 1 || f.truncates != 1 {
			t.Fatalf("负控失效：sections=%d truncates=%d（期望 1/1）", f.sections, f.truncates)
		}
		if len(f.unguardedSections) != 1 {
			t.Errorf("负控失效：无门禁的节未被报出（unguarded=%v）", f.unguardedSections)
		}
	})

	t.Run("负控2_字典序与统计估算守卫必须被检出", func(t *testing.T) {
		src := "-- ---- 2a. x ----\n" +
			"SELECT c.relname FROM pg_class c\n" +
			" WHERE c.relname < 'usage_facts_default' AND c.reltuples = 0;\n"
		f := analyzeReclaim(src)
		if f.lexicographicNameCompare == 0 {
			t.Error("负控失效：`relname <` 的字典序守卫未被检出")
		}
		if f.reltuplesAsPredicate == 0 {
			t.Error("负控失效：`reltuples = 0` 作为判据未被检出")
		}
	})

	t.Run("负控3_上一节的探针不得替本节背书", func(t *testing.T) {
		src := "-- ---- 2a. guarded ----\n" +
			"SELECT storage_reclaim_table_is_empty(x) FROM t;\n" +
			"-- ---- 2b. unguarded ----\n" +
			"FOR r IN SELECT 1 LOOP\n  EXECUTE format('DROP TABLE public.%I', r.x);\nEND LOOP;\n"
		f := analyzeReclaim(src)
		if f.drops != 1 {
			t.Fatalf("负控失效：未数到 DROP（drops=%d）", f.drops)
		}
		if len(f.unguardedSections) != 1 || f.unguardedSections[0] != "2b" {
			t.Errorf("负控失效：上一节的探针替 2b 背书了（unguarded=%v）", f.unguardedSections)
		}
	})

	t.Run("负控4_CONTINUE式合法守卫不得被误报", func(t *testing.T) {
		// 这一条是 208 号自己踩到的坑：按语句（`;`）切段会把这种**正确**写法
		// 判成「无门禁」。一道误报正确代码的门比没有门更坏，所以它必须留在门里。
		src := "-- ---- 2c. guarded-by-continue ----\n" +
			"FOR r IN SELECT 1 LOOP\n" +
			"  IF NOT storage_reclaim_table_is_empty(r.x) AND v_force <> 'on' THEN\n" +
			"    RAISE WARNING 'skip';\n    v_skipped := v_skipped + 1;\n    CONTINUE;\n" +
			"  END IF;\n" +
			"  EXECUTE format('DROP TABLE public.%I', r.x);\n" +
			"END LOOP;\n"
		f := analyzeReclaim(src)
		if len(f.unguardedSections) != 0 {
			t.Errorf("负控失效：CONTINUE 式合法守卫被误报为无门禁（%v）", f.unguardedSections)
		}
	})

	t.Run("负控5_不等于比较不得被当成排序守卫", func(t *testing.T) {
		// 本门第一版用 strings.Contains(l, "relname <") 数排序比较，把脚本里
		// 合法且必要的 `AND c.relname <> 'usage_facts_default'` 误报了一次。
		if n := countLexicographicCompares("and c.relname <> 'usage_facts_default'"); n != 0 {
			t.Errorf("负控失效：`<>` 被误判为排序比较（n=%d）", n)
		}
		if n := countLexicographicCompares("and c.relname <= 'x'"); n != 0 {
			t.Errorf("负控失效：`<=` 被误判为排序比较（n=%d）", n)
		}
		// 真排序比较必须仍然被抓到。
		for _, s := range []string{
			"and c.relname < 'usage_facts_default'",
			"and c.relname > 'usage_facts_default'",
			"and c.relname< 'x'",
		} {
			if n := countLexicographicCompares(s); n == 0 {
				t.Errorf("负控失效：真排序比较 %q 未被检出", s)
			}
		}
	})
}
