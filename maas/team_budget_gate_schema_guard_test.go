package maas

// TestTeamBudgetGate_SQLAlignsRealSchema —— 团队预算门 SQL 列名对齐真实 schema 的源码守卫。
//
// # 为什么要有这道门（背景：2026-11-23 12h 修订审计轮）
//
// `maas/team_budget_gate.go` 的团队花费聚合查询原先写成：
//
//	SELECT COALESCE(SUM(ccb.credits), 0)
//	  FROM maas_credit_consumption_buckets ccb
//	 INNER JOIN api_keys ak ON ccb.api_key_id = ak.id
//	 WHERE ak.team_id = $1
//	   AND ccb.period_start >= $2 AND ccb.period_start < $3
//
// 而 `maas_credit_consumption_buckets`（迁移 389）的真实列只有：
//
//	tenant_id / bucket_start / credits / request_count / updated_at
//	PRIMARY KEY (tenant_id, bucket_start)
//
// ⇒ `ccb.api_key_id` 与 `ccb.period_start` **两列都不存在**
// ⇒ 运行时必然 SQLSTATE 42703（column does not exist）。
//
// ★ 这个缺陷被**三层假绿**同时掩盖：
//
//	① **未接线**：CheckTeamMonthlyBudget 零调用点 ⇒ 查询永远不执行；
//	② **零测试**：连单测都没有 ⇒ 假 store 不会告诉你列不存在
//	   （这正是 admin/analytics_schema_guard_test.go 记录的那条教训：
//	    「单测的假 store 不会告诉你列不存在」）；
//	③ **自我声明**：文件头自称「口径锚点：与租户预算门同源 … 禁止另起聚合」
//	   ⇒ 声明与实现不符，复核者照着注释验收会验到一个不存在的保证。
//
// ★★ 还有一个更深的坑：**只把列名改对也是错的**。
// `maas_credit_consumption_buckets` 只有**租户粒度**（PK 是 tenant_id+bucket_start），
// 没有 api_key_id ⇒ 团队粒度根本不可从该表聚合；
// 若硬凑（例如按 tenant_id 关联），结果是「把整个租户的消费记到每个团队头上」
// ⇒ 比不实现更危险。团队真源只能是 usage_facts（见 team_budget_gate.go 的口径段）。
// 本门把这条结论也钉死：见 TestTeamBudgetGate_ConsumptionBucketsCannotServeTeamGranularity。
//
// # 本门怎么判
//
// 真源＝`sql/migrations/startup/*.sql`（1155 个迁移是唯一权威事实；
// `sql/schema/01-schema.sql` 与 installer 的合并 schema 是**快照**，不含
// usage_facts / teams / team_budgets 等新表 ⇒ 不能拿它当真源）。
// 本门从迁移里还原「表 → 列集合」，再从 team_budget_gate.go 的源码里抠出
// SQL 文本，对每条语句的 `别名.列` 引用逐个核对：
// 引用了目标表上不存在的列 ⇒ 红，并打印表名/列名/该表真实列清单。
//
// # 为什么用源码守卫而不是真库集成测试
//
// 真库集成测试需要 DSN 与迁移到最新，成本高且不是每次 CI 都跑；
// 而本类缺陷（照着「想象中的表结构」写 SQL）的本质是**源码与 schema 的漂移**，
// 源码级对照能在零依赖下把它当场抓住。真实库列名另有
// `make test-realdb-gate` 一层兜底。
import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ── schema 还原 ────────────────────────────────────────────────────────────

var (
	// CREATE TABLE [IF NOT EXISTS] [public.]<table> ( … \n) [PARTITION BY …];
	// ★ 闭合行不总是裸的 `);` —— 分区表的真实形态是
	//   `) PARTITION BY RANGE (occurred_at);`（usage_facts/537 就是这样），
	//   所以闭合行按 `^\)[^;]*;` 匹配，而不是写死 `^\);`。
	reCreateTable = regexp.MustCompile(`(?ims)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?(\w+)\s*\((.*?)^\)[^;]*;`)
	// ALTER TABLE [IF EXISTS] [public.]<table> ADD COLUMN [IF NOT EXISTS] <col>
	reAddColumn = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:public\.)?(\w+)\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)`)
	// 表定义里不是列的约束/子句
	nonColumnPrefixes = []string{
		"constraint", "primary", "unique", "check", "foreign", "exclude",
		"like", "partition", "period", "with", "tablespace",
	}
)

// schemaSources 是「真实 schema」的全部来源，缺一不可：
//
//   - ../sql/schema/01-schema.sql：**基线表**（api_keys 等）。★ 它的表**不在**
//     sql/migrations/startup/ 里 —— 只扫迁移会得到 api_keys 只有 team_id 一列
//     的残缺 schema（team_id 是 057 迁移 ADD COLUMN 加的）⇒ 别名 ak.id /
//     ak.tenant_id 会被误判成「不存在」⇒ **假红**。
//   - ../sql/migrations/startup/*.sql：**迁移新增的表与列**
//     （usage_facts/537、teams/055、user_teams/056、team_budgets/058 …）。
//     ★ 合并 schema 快照里**没有**这些新表 ⇒ 只扫快照会漏 ⇒ **假绿**。
//
// ⇒ 真源＝基线 ∪ 迁移。少任何一边都会得到一个「看起来在跑、实际判不了」的门。
var schemaSources = []string{
	filepath.Join("..", "sql", "schema", "01-schema.sql"),
	filepath.Join("..", "sql", "migrations", "startup"),
}

// realSchemaColumns 从「基线 ∪ 迁移」还原「表 → 列集合」。
func realSchemaColumns(t *testing.T) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	add := func(table, col string) {
		table = strings.ToLower(table)
		if out[table] == nil {
			out[table] = map[string]bool{}
		}
		out[table][strings.ToLower(col)] = true
	}
	for _, src := range schemaSources {
		info, err := os.Stat(src)
		if err != nil {
			t.Fatalf("stat schema source %s: %v", src, err)
		}
		if !info.IsDir() {
			raw, err := os.ReadFile(src)
			if err != nil {
				t.Fatalf("read %s: %v", src, err)
			}
			ingestSQL(string(raw), add)
			continue
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatalf("read migrations dir %s: %v", src, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
				continue
			}
			// .down.sql 是回滚脚本，不定义正向 schema
			if strings.HasSuffix(e.Name(), ".down.sql") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			ingestSQL(string(raw), add)
		}
	}
	return out
}

// ingestSQL 从一段 SQL 文本里抽出表定义与 ADD COLUMN，交给 add 回调。
func ingestSQL(body string, add func(table, col string)) {
	for _, m := range reCreateTable.FindAllStringSubmatch(body, -1) {
		for _, col := range parseColumns(m[2]) {
			add(m[1], col)
		}
	}
	for _, m := range reAddColumn.FindAllStringSubmatch(body, -1) {
		add(m[1], m[2])
	}
}

// parseColumns 从 CREATE TABLE 的括号体里抽出列名（跳过约束与子句）。
func parseColumns(body string) []string {
	var cols []string
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), ","))
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		skip := false
		for _, p := range nonColumnPrefixes {
			if strings.HasPrefix(low, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		cols = append(cols, strings.ToLower(strings.Trim(fields[0], `"`)))
	}
	return cols
}

// ── SQL 抽取与解析 ─────────────────────────────────────────────────────────

var (
	reRawSQL    = regexp.MustCompile("(?s)`([^`]+)`")
	reColumnRef = regexp.MustCompile(`\b(\w+)\.(\w+)\b`)
	sqlKeywords = map[string]bool{"on": true, "where": true, "join": true, "left": true, "right": true, "inner": true, "outer": true, "group": true, "order": true, "and": true, "or": true, "as": true, "set": true, "limit": true, "having": true, "cross": true, "full": true, "using": true, "select": true, "from": true, "returning": true, "insert": true, "update": true, "delete": true, "values": true, "into": true, "union": true, "all": true, "distinct": true}
	// ★ 别名必须**强制**吃一个 token（表名后必须有空白 + token），
	//   否则可选组会被跳过、别名一个都捕获不到 ⇒ 门会「查无对象、恒绿」。
	//   （这正是本门第一版的假绿根因：可选组 `(\w+)?` 实际总是匹配空，
	//     于是每个 `别名.列` 引用都因「别名不在表映射里」被跳过 ⇒ 一条都没判。
	//     ⇒ 负控纪律：门必须先证明自己能红，见 TestTeamBudgetGate_GuardIsEffective。）
	//   抓到的是 SQL 关键字（WHERE/LEFT/…）时 ⇒ 视作「该表无别名」，用表名当别名。
	reFromOrJoin = regexp.MustCompile(`(?is)\b(?:FROM|JOIN)\s+(?:public\.)?(\w+)\s+(?:AS\s+)?([A-Za-z_]\w*)`)
)

// extractSQLStrings 从 Go 源码里取出反引号包裹的 SQL 文本。
func extractSQLStrings(src string) []string {
	var out []string
	for _, m := range reRawSQL.FindAllStringSubmatch(src, -1) {
		if strings.Contains(strings.ToUpper(m[1]), "SELECT") {
			out = append(out, m[1])
		}
	}
	return out
}

// aliasToTable 解析一条语句里的「别名 → 表名」映射。
func aliasToTable(stmt string) map[string]string {
	out := map[string]string{}
	for _, m := range reFromOrJoin.FindAllStringSubmatch(stmt, -1) {
		table := strings.ToLower(m[1])
		alias := ""
		if m[2] != "" {
			cand := strings.ToLower(m[2])
			if !sqlKeywords[cand] {
				alias = cand
			}
		}
		if alias == "" {
			alias = table
		}
		out[alias] = table
	}
	return out
}

// ── 门 ─────────────────────────────────────────────────────────────────────

// checkSQLAgainstSchema 核对一条 SQL 里所有「别名.列」引用是否落在真实 schema 上。
// 返回违规描述列表（空 = 通过）。
func checkSQLAgainstSchema(stmt string, schema map[string]map[string]bool) []string {
	var violations []string
	alias2table := aliasToTable(stmt)
	for _, m := range reColumnRef.FindAllStringSubmatch(stmt, -1) {
		alias := strings.ToLower(m[1])
		col := strings.ToLower(m[2])
		table, ok := alias2table[alias]
		if !ok {
			// 不是 FROM/JOIN 出来的别名（如 `ms.cents_per_credit::float8`
			// 的 :: 转换、或 schema 限定名）—— 无法判定就跳过，不误伤。
			continue
		}
		cols, ok := schema[table]
		if !ok {
			violations = append(violations, fmt.Sprintf(
				"SQL 引用了表 %q（别名 %q），但它在「基线 ∪ 迁移」里没有 CREATE TABLE/ADD COLUMN 记录 ⇒ 列无法核对", table, alias))
			continue
		}
		if !cols[col] {
			violations = append(violations, fmt.Sprintf(
				"SQL 引用了 %s.%s，但表 %q 的真实 schema 里没有这一列 ⇒ 运行时必然 SQLSTATE 42703(column does not exist)\n"+
					"  %q 的真实列：%s\n"+
					"  ★ 修法：换到真源表（团队粒度只有 usage_facts 有），不要只改列名",
				alias, col, table, table, strings.Join(sortedKeys(cols), ", ")))
		}
	}
	return violations
}

// TestTeamBudgetGate_SQLAlignsRealSchema 是本文件的主门。
func TestTeamBudgetGate_SQLAlignsRealSchema(t *testing.T) {
	src, err := os.ReadFile("team_budget_gate.go")
	if err != nil {
		t.Fatalf("read team_budget_gate.go: %v", err)
	}
	schema := realSchemaColumns(t)

	stmts := extractSQLStrings(string(src))
	if len(stmts) == 0 {
		t.Fatal("team_budget_gate.go 里没有抠出任何 SQL —— 量具腐化（改文件结构时本门必须同步）")
	}

	for _, stmt := range stmts {
		for _, v := range checkSQLAgainstSchema(stmt, schema) {
			t.Error(v)
		}
	}
}

// TestTeamBudgetGate_GuardIsEffective 是本门的**负控**（本门自己必须能被审计）。
//
// ★ 为什么必须有它：本门第一版因为「别名正则的可选组永远匹配空」而
//
//	**一条引用都没判**（假绿）—— 只看「主门绿」完全看不出这一点。
//	⇒ 负控纪律（AGENTS.md）：守卫自己要能证明「它会红」。
//
// 做法：把**原始缺陷 SQL**（引用 ccb.api_key_id / ccb.period_start 的那版）
// 喂给同一个判定函数，断言它**确实报出违规**；再把当前修好的 SQL 喂进去，
// 断言它**不报**。⇒ 门红绿都来自同一段判定逻辑 ⇒ 门不会「恒绿」。
func TestTeamBudgetGate_GuardIsEffective(t *testing.T) {
	schema := realSchemaColumns(t)

	broken := `
		SELECT COALESCE(SUM(ccb.credits), 0)
		  FROM maas_credit_consumption_buckets ccb
		 INNER JOIN api_keys ak ON ccb.api_key_id = ak.id
		 WHERE ak.team_id = $1
		   AND ccb.period_start >= $2
		   AND ccb.period_start < $3
	`
	fixed := `
		SELECT COALESCE(SUM(f.credits_charged), 0)
		  FROM usage_facts f
		  JOIN api_keys ak ON ak.id = f.api_key_id
		  JOIN teams t     ON t.id = ak.team_id
		 WHERE t.id = $1
		   AND ak.tenant_id = t.tenant_id
		   AND f.occurred_at >= $2
		   AND f.occurred_at < $3
	`

	got := checkSQLAgainstSchema(broken, schema)
	if len(got) == 0 {
		t.Fatal("负控失败：原始缺陷 SQL（ccb.api_key_id / ccb.period_start）竟然没被门报出违规 ⇒ " +
			"本门是假绿（多半是别名/表映射又退化了）")
	}
	t.Logf("负控如期报红（%d 条违规）：%v", len(got), got[0])

	if v := checkSQLAgainstSchema(fixed, schema); len(v) != 0 {
		t.Fatalf("负控的对照面失败：修好的 SQL 反而被判违规 ⇒ %v", v)
	}

	// 还要确认主门真的判到了东西（不是「查无对象」的空转）：
	// 修好的 SQL 至少要解析出 usage_facts / api_keys / teams 三个别名。
	aliases := aliasToTable(fixed)
	for _, want := range []string{"usage_facts", "api_keys", "teams"} {
		found := false
		for _, tbl := range aliases {
			if tbl == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("负控：修好的 SQL 没解析出表 %q（别名表=%v）⇒ 别名解析退化，主门会空转", want, aliases)
		}
	}
}

// TestTeamBudgetGate_ConsumptionBucketsCannotServeTeamGranularity 把
// 「buckets 表没有 key 粒度」这条结论钉死——它是「只改列名也是错的」的依据。
func TestTeamBudgetGate_ConsumptionBucketsCannotServeTeamGranularity(t *testing.T) {
	schema := realSchemaColumns(t)
	buckets, ok := schema["maas_credit_consumption_buckets"]
	if !ok {
		t.Fatal("sql/migrations/startup/ 里找不到 maas_credit_consumption_buckets 的表定义 —— 迁移目录结构变了？")
	}
	for _, forbidden := range []string{"api_key_id", "period_start"} {
		if buckets[forbidden] {
			t.Errorf("maas_credit_consumption_buckets 现在**有** %q 列 ⇒ 团队粒度聚合的技术前提变了；"+
				"请重审 team_budget_gate.go 的真源选择并同步更新本门与注释", forbidden)
		}
	}
	for _, required := range []string{"tenant_id", "bucket_start", "credits"} {
		if !buckets[required] {
			t.Errorf("maas_credit_consumption_buckets 少了预期的列 %q —— 迁移被改动？", required)
		}
	}
}

// TestTeamBudgetGate_UsesUsageFactsAsTeamGrainSource 钉住「团队真源＝usage_facts」。
func TestTeamBudgetGate_UsesUsageFactsAsTeamGrainSource(t *testing.T) {
	src, err := os.ReadFile("team_budget_gate.go")
	if err != nil {
		t.Fatalf("read team_budget_gate.go: %v", err)
	}
	body := string(src)

	if strings.Contains(body, "maas_credit_consumption_buckets") {
		// 允许出现在**注释**里（口径说明会提到它），但不允许出现在 SQL 文本里。
		for _, stmt := range extractSQLStrings(body) {
			if strings.Contains(stmt, "maas_credit_consumption_buckets") {
				t.Errorf("团队花费聚合 SQL 又用回了 maas_credit_consumption_buckets ⇒ 该表只有租户粒度，"+
					"按团队聚合要么 42703、要么把整个租户的消费记到每个团队头上\n  SQL: %s", strings.TrimSpace(stmt))
			}
		}
	}

	schema := realSchemaColumns(t)
	uf, ok := schema["usage_facts"]
	if !ok {
		t.Fatal("sql/migrations/startup/ 里找不到 usage_facts 的表定义 —— 迁移目录结构变了？")
	}
	for _, required := range []string{"api_key_id", "credits_charged", "occurred_at"} {
		if !uf[required] {
			t.Errorf("usage_facts 缺少预期的列 %q —— 团队真源前提变了，请重审", required)
		}
	}
}

// sortedKeys 给错误信息用的稳定列序。
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
