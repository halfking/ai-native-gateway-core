package admin

// logs_search_predicate_test.go —— `/api/logs?q=` 谓词形态的判据（审计 G-5）。
//
// # 这组判据钉什么
//
// 1. **词形词走全文检索腿**（迁移 887 建的表达式索引承重的那条）。
// 2. **子串路径一条都不能丢**：无论哪种形态，ILIKE 腿都必须在。
//    （这是「并联 OR 而不是串联 AND」的可观测后果。）
// 3. **查询表达式与索引表达式逐字同形** —— 跨文件契约：
//    Go 侧发出的 `to_tsvector('simple', rl.search_text)` 与迁移 887 建的
//    `to_tsvector('simple', search_text)` 必须是同一个表达式（只差表别名）。
//    不同形 ⇒ planner **静默**不用那条索引（不报错、只是顺序扫描），
//    所以这道门是「索引白建」的唯一防线。
// 4. **端点真的用了这个纯函数** —— 不是「文件里存在这个函数」，
//    而是 admin/logs.go 的 `?q=` 分支真的走它。
//
// 判据纪律：每条断言都必须**能在被测对象坏掉时变红**。本文件的变异验证
// 见交付报告：把 classifySessionSearchTerm 的 FTS 分支改成不可达 ⇒
// 第 1 条与第 3 条变红；把 logs.go 的 `?q=` 分支改回内联 ILIKE ⇒
// 第 4 条变红。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stripSearchSQLComments 剥掉 SQL 注释（行注释 `--` 与块注释 `/* */`），
// 只留**可执行语句**。
//
// ★ 为什么需要它：迁移文件头里正当地**引用**了它要避免的写法
// （「不能加生成列，因为会报 cannot insert into a generated column」）。
// 按整份文件正文查，会把**理由**当成**行为** ⇒ 判据恒红 ⇒ 门失效。
func stripSearchSQLComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	out := b.String()
	for {
		start := strings.Index(out, "/*")
		if start < 0 {
			return out
		}
		end := strings.Index(out[start:], "*/")
		if end < 0 {
			return out[:start]
		}
		out = out[:start] + out[start+end+2:]
	}
}

// ftsIndexExpression 是迁移 887 建的**索引表达式**（逐字）。
const ftsIndexExpression = "to_tsvector('simple', search_text)"

// ftsQueryExpressionRe 抓 Go 侧发出的全文检索谓词。
var ftsQueryExpressionRe = regexp.MustCompile(`to_tsvector\('simple',\s*([a-z_]+\.)?search_text\)`)

// 仓库根定位复用 admin 包内既有的 repoRoot（admin/retry_classifier_contract_test.go）：
// 它按 go.mod 的 module 路径向上找，而不是按「猜目录深度」——后者在仓库被挪进
// 另一个项目目录时会静默扫到**别的仓库**（已记的假绿形态）。

// TestSessionSearchPredicateUsesFTSForWordShapedInput 是第 1 条：
// 词形词必须走全文检索腿，且该腿的表达式必须逐字正确。
func TestSessionSearchPredicateUsesFTSForWordShapedInput(t *testing.T) {
	for _, term := range []string{
		"deepseek",
		"deepseek chat",
		"gpt-4",
		"glm-4.6",
		"session_id",
		"UPPER CASE",
	} {
		clause, args := buildSessionSearchPredicate(term, 3)
		if !strings.Contains(clause, "@@ plainto_tsquery('simple'") {
			t.Errorf("词形词 %q 没有走全文检索腿（谓词里没有 @@ plainto_tsquery）：\n%s", term, clause)
			continue
		}
		if !strings.Contains(clause, "to_tsvector('simple', rl.search_text)") {
			t.Errorf("词形词 %q 的全文腿表达式与索引表达式不同形：\n%s\n"+
				"必须是 %s（rl. 是表别名，pushdown 后就是 search_text）",
				term, clause, ftsIndexExpression)
		}
		if !strings.Contains(clause, "rl.search_text ILIKE $3") {
			t.Errorf("词形词 %q 的子串腿不见了（谓词里没有 ILIKE）：\n%s\n"+
				"并联（OR）必须保留子串腿，否则就是破坏子串检索", term, clause)
		}
		if len(args) != 2 {
			t.Errorf("词形词 %q 绑定了 %d 个参数（应为 2：ILIKE 模式 + 原词）：%v", term, len(args), args)
			continue
		}
		if got, ok := args[0].(string); !ok || got != "%"+strings.TrimSpace(term)+"%" {
			t.Errorf("词形词 %q 的 ILIKE 参数 = %v（应为 %q）", term, args[0], "%"+strings.TrimSpace(term)+"%")
		}
		if got, ok := args[1].(string); !ok || got != strings.TrimSpace(term) {
			t.Errorf("词形词 %q 的 FTS 参数 = %v（应为原词 %q，plainto_tsquery 自己切分）",
				term, args[1], strings.TrimSpace(term))
		}
	}
}

// TestSessionSearchPredicateKeepsSubstringPathForNonWordShapedInput 是第 2 条
// 的一半：非词形词（中文、过短、路径形态）必须留在 trigram/ILIKE 路径上。
//
// ★ 为什么中文不进 FTS 腿：'simple' 的分词器对 CJK 的切分形状与英文不同，
//
//	把它塞进 tsquery 的召回不可控；而 trigram 对 CJK 子串（≥3 字符）
//	本来就好使 ⇒ 让中文留在它已经好使的那条路上。
func TestSessionSearchPredicateKeepsSubstringPathForNonWordShapedInput(t *testing.T) {
	for _, term := range []string{
		"会话治理",        // CJK
		"会话",          // CJK，两字
		"ai",          // 2 字符：trigram 抽不出 trigram，词元也无选择性
		"a",           // 1 字符
		"src/main.go", // 路径形态
		"foo@bar.com", // 邮箱形态
		"a b",         // 两个 1 字符词元
	} {
		clause, args := buildSessionSearchPredicate(term, 3)
		if strings.Contains(clause, "@@") {
			t.Errorf("非词形词 %q 不该走全文检索腿，却发出了：\n%s", term, clause)
		}
		if !strings.Contains(clause, "rl.search_text ILIKE $3") {
			t.Errorf("非词形词 %q 必须留在子串路径上，谓词却是：\n%s", term, clause)
		}
		if len(args) != 1 {
			t.Errorf("非词形词 %q 绑定了 %d 个参数（应为 1）：%v", term, len(args), args)
			continue
		}
		if got, ok := args[0].(string); !ok || got != "%"+strings.TrimSpace(term)+"%" {
			t.Errorf("非词形词 %q 的 ILIKE 参数 = %v（应为 %q）", term, args[0], "%"+strings.TrimSpace(term)+"%")
		}
	}
}

// TestSessionSearchPredicateAlwaysKeepsTheSubstringArm 是第 2 条的另一半：
// **任何**形态的谓词都必须含 ILIKE 腿。
//
// 这条是「子串路径一条都不能丢」的可观测断言：形态决策函数若被改成
// 「FTS 形态下不发 ILIKE」，本条立刻红。
func TestSessionSearchPredicateAlwaysKeepsTheSubstringArm(t *testing.T) {
	terms := []string{
		"deepseek", "deepseek chat", "会话治理", "会话", "ai", "a",
		"src/main.go", "foo@bar.com", "", "   ", "gpt-4", "x", "…",
	}
	for _, term := range terms {
		clause, _ := buildSessionSearchPredicate(term, 1)
		if !strings.Contains(clause, "rl.search_text ILIKE") {
			t.Errorf("输入 %q 的谓词丢掉了子串腿：\n%s\n"+
				"子串腿是「不破坏子串检索」的唯一保证，任何形态都必须保留它", term, clause)
		}
	}
}

// TestSessionSearchPredicateMatchesTheIndexExpression 是第 3 条（跨文件契约）。
//
// 查询表达式与索引表达式必须**逐字同形**（只差表别名）。不同形的后果是
// **静默**的：planner 不用那条索引，搜索退化成顺序扫描，没有任何报错。
// ⇒ 这是「索引白建」的唯一防线。
func TestSessionSearchPredicateMatchesTheIndexExpression(t *testing.T) {
	migPath := filepath.Join(repoRoot(t), "sql", "migrations", "startup", "887_session_turns_search_text_fts.sql")
	raw, err := os.ReadFile(migPath)
	if err != nil {
		t.Fatalf("read migration 887: %v", err)
	}
	mig := string(raw)

	if !strings.Contains(mig, ftsIndexExpression) {
		t.Fatalf("迁移 887 里找不到索引表达式 %s —— 迁移的索引形态漂移了：\n%s", ftsIndexExpression, mig)
	}
	// 生成器里的形态（format 串里单引号要写两遍）。
	if !strings.Contains(mig, "to_tsvector(''simple'', search_text)") {
		t.Errorf("迁移 887 的分区索引生成器没有用 %q 的转义形态 —— "+
			"分区上的索引表达式会与父表不同形", "to_tsvector(''simple'', search_text)")
	}
	// ★ 硬约束：不得引入 tsvector 生成列（会让 promote 当场失败）。
	//
	// ⚠ 必须**剥掉 SQL 注释**再查：迁移的文件头里正当地**引用**了这句话
	//   （「加 tsvector 生成列会让 promote 报 cannot insert into a generated
	//   column」正是本迁移存在的理由），按整份文件正文查会把**理由**当成
	//   **行为** ⇒ 恒红。判据查的是**可执行语句**。
	if regexp.MustCompile(`(?i)GENERATED\s+ALWAYS\s+AS`).MatchString(stripSearchSQLComments(mig)) {
		t.Errorf("迁移 887 的**可执行语句**里出现了 GENERATED ALWAYS AS —— " +
			"session_turns 不能加生成列：promote_session_turns_hot_to_partition " +
			"从 hot 目录派生列清单后做 INSERT INTO public.session_turns (<cols>)，" +
			"生成列会让它报 cannot insert into a generated column ⇒ 热表永远排不进月分区")
	}

	// Go 侧发出的表达式：把表别名剥掉后必须与索引表达式逐字相同。
	clause, _ := buildSessionSearchPredicate("deepseek chat", 1)
	m := ftsQueryExpressionRe.FindStringSubmatch(clause)
	if m == nil {
		t.Fatalf("Go 侧谓词里没有 to_tsvector('simple', ...search_text) 形态：\n%s", clause)
	}
	qualified := m[0]
	unqualified := strings.Replace(qualified, "rl.", "", 1)
	if unqualified != ftsIndexExpression {
		t.Errorf("查询表达式 %q 与索引表达式 %q 不同形 —— planner 会**静默**不用那条索引。\n"+
			"（表达式索引只在查询表达式语法同形时才会被用上；不同形不报错，只是顺序扫描）",
			unqualified, ftsIndexExpression)
	}
}

// TestLogsHandlerUsesTheSearchPredicateBuilder 是第 4 条：
// admin/logs.go 的 `?q=` 分支真的走这个纯函数。
//
// 这不是「文件里存在这个函数」的断言 —— 它钉的是**端点**：
// logs.go 里若把 `?q=` 改回内联 ILIKE，索引就没人用了。
func TestLogsHandlerUsesTheSearchPredicateBuilder(t *testing.T) {
	path := filepath.Join(repoRoot(t), "admin", "logs.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read admin/logs.go: %v", err)
	}
	body := string(raw)

	if !strings.Contains(body, "buildSessionSearchPredicate(") {
		t.Errorf("admin/logs.go 的 ?q= 分支没有调用 buildSessionSearchPredicate —— " +
			"全文检索腿（迁移 887 的索引所承重的那条）不会被发出去")
	}
	// 反向钉：旧的**内联** ILIKE 字面量不应再出现在 logs.go 里
	// （它意味着有人把形态决策旁路掉了）。
	if strings.Contains(body, `addFilter("rl.search_text ILIKE $%d"`) {
		t.Errorf("admin/logs.go 里仍有内联的 %q —— ?q= 被旁路成纯子串路径，"+
			"迁移 887 的全文检索索引将没有查询承重", `addFilter("rl.search_text ILIKE $%d"`)
	}
}
