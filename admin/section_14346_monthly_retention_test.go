package admin

// section_14346_monthly_retention_test.go —— §143.46 门。
//
// 背景（§143.46，2026-10-21）：本轮在两实例上**只读**普查月度分区族，
// 挖出两件事：
//
//	① 月度分区 DROP 机制的**策略表是空的**：
//	   迁移 841 建了配置表 llm_gateway_partition_retention（每族 retain_months）
//	   + llm_gateway_drop_expired_month_partitions()，且 bg/partition_manager.go
//	   **已经在调用**它（生产日志「monthly partition retention sweep dropped=0」）
//	   ⇒ 但配置表**两实例都是 0 行** ⇒ **机制空转**
//	   ⇒ 审计表 llm_gateway_partition_drop_log 也是 **0 行**
//	   ⇒ **从未删过任何月度分区**。
//	② 三类留存机制穷举之后，**会话原始数据族在 PG 路径上没有任何留存**：
//	   session_turns / session_turn_details / sessions / session_censors /
//	   session_memora / session_bodies / session_tools / auto_route_selections /
//	   usage_ledger / credit_ledger / tool_usage_stats
//	   ⇒ 252 上 9 月的 session_turns_2026_09 单分区就 **2.67 GB**。
//
// 本门守四件事：
//  1. 决策表自测（覆盖判定 + 过期判定 + 「当月/未来月永不过期」这条承重性质）
//  2. 登记册逐行被决策表**重算** + 绝对条数与绝对字节钉住
//  3. **机制就位**：841 的四件套 + Go 侧对 DROP 函数的调用不许被拆
//  4. ★ **三处口径对齐**：841 的 SQL 判据、判定脚本、Go 决策表
//     三者的「过期方向」必须一致 —— 本轮在这上面踩了两次（反向 + 跨月差恒 0）
//
// ★ 本门**不需要数据库**（纯静态 + 纯函数）⇒ 不会 SKIP 成假绿。
//
// ★ 本门**不钉「配置表必须为空」** —— 那会是一根不可翻转的钉（属主一旦配了策略就红）。
//   「配置表当前为空」是**登记册里的快照事实**，不是不变量；真正的守卫是第 3 条
//   （机制不许被拆）+ 第 4 条（口径不许漂）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	monthlyTsv14346   = "../scripts/audit/monthly_partition_retention.tsv"
	monthlySql14346   = "../scripts/audit/verify_monthly_partition_retention.sql"
	migration841      = "../sql/migrations/startup/841_monthly_partition_retention.sql"
	partitionMgr14346 = "../bg/partition_manager.go"
)

// monthlyRegistryRow 是登记册的一行。
type monthlyRegistryRow struct {
	Instance        string
	Scope           MonthlyRetentionScope
	Family          string
	NMonthParts     int
	TotalBytes      int64
	ReclaimKeep1    int64
	ReclaimKeep2    int64
	ReclaimKeep3    int64
	PolicyReg       bool
	TTLDeclaredDays int
	Mechanism       MonthlyMechanism
	Verdict         MonthlyRetentionVerdict
	Note            string
}

func parseMonthlyRegistry(t *testing.T, path string) []monthlyRegistryRow {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取登记册 %s：%v", path, err)
	}
	var rows []monthlyRegistryRow
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(line, "instance\t") {
			continue
		}
		f := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
		if len(f) != 13 {
			t.Fatalf("登记册第 %d 行列数 = %d，期望 13：%q", i+1, len(f), line)
		}
		atoi := func(s string) int { v, _ := strconv.Atoi(s); return v }
		atoi64 := func(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
		rows = append(rows, monthlyRegistryRow{
			Instance:        f[0],
			Scope:           MonthlyRetentionScope(f[1]),
			Family:          f[2],
			NMonthParts:     atoi(f[3]),
			TotalBytes:      atoi64(f[4]),
			ReclaimKeep1:    atoi64(f[5]),
			ReclaimKeep2:    atoi64(f[6]),
			ReclaimKeep3:    atoi64(f[7]),
			PolicyReg:       f[8] == "yes",
			TTLDeclaredDays: atoi(f[9]),
			Mechanism:       MonthlyMechanism(f[10]),
			Verdict:         MonthlyRetentionVerdict(f[11]),
			Note:            f[12],
		})
	}
	return rows
}

// TestSection14346DecisionTableSelfTest 是决策表的自测。
//
// ★★ 承重用例是「当月与未来月永不过期」：
//
//	841 的安全约束 5 就是它（预建的 _2026_11 绝不能被 DROP）
//	⇒ 一旦过期判据的方向被写反（或把 `<` 改成 `<=`），
//	  **当月与未来分区会被 DROP** ⇒ 写入路径直接报 23514。
//	⇒ 这条性质必须被门钉住，而不是靠人记得。
func TestSection14346DecisionTableSelfTest(t *testing.T) {
	// ── 覆盖判定 ────────────────────────────────────────────────────────
	cases := []struct {
		name string
		in   MonthlyRetentionInput
		want MonthlyRetentionVerdict
	}{
		{"穷举后无任何机制 ⇒ 无界", MonthlyRetentionInput{
			Scope: ScopeMonthlyFamily, Mechanism: MonthlyMechNone, TTLDeclaredDays: -1,
		}, MonthlyVerdictUnbounded},
		{"路径 A 覆盖（按天 TTL）", MonthlyRetentionInput{
			Scope: ScopeMonthlyFamily, Mechanism: MonthlyMechStateTableTTL, TTLDeclaredDays: 7,
		}, MonthlyVerdictCovered},
		{"路径 C：整月归档", MonthlyRetentionInput{
			Scope: ScopeMonthlyFamily, Mechanism: MonthlyMechArchive, TTLDeclaredDays: -1,
		}, MonthlyVerdictCovered},
		{"路径 C：只剪 hot（半覆盖）", MonthlyRetentionInput{
			Scope: ScopeMonthlyFamily, Mechanism: MonthlyMechHotTrim, TTLDeclaredDays: -1,
		}, MonthlyVerdictCovered},
		{"路径 B：配置表已有该族", MonthlyRetentionInput{
			Scope: ScopeMonthlyFamily, Mechanism: MonthlyMechPolicyTable, TTLDeclaredDays: -1,
		}, MonthlyVerdictCovered},
	}
	for _, tc := range cases {
		got, reason := ClassifyMonthlyRetention(tc.in)
		if got != tc.want {
			t.Errorf("%s：verdict = %q，期望 %q（理由：%s）", tc.name, got, tc.want, reason)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s：判定理由为空 —— 空理由等于没判", tc.name)
		}
	}

	// ★★ 承重：路径 A 的**实际**保留窗口是 TTL..TTL+30（每月 2 号才扫）。
	lo, hi := EffectiveRetentionDays(7)
	if lo != 7 || hi != 37 {
		t.Errorf("TTL=7 天的实际留存窗口 = %d..%d 天，期望 7..37（每月 2 号才扫 ⇒ 最长多活 30 天）", lo, hi)
	}

	// ── 过期判定：与 841 的语义逐字对齐 ─────────────────────────────────
	cur := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	sep := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	octPart := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	novPart := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	// retain=1 ⇒ 只保留当月 ⇒ 9 月及更早过期。
	if !IsMonthlyPartitionExpired(sep, cur, 1) {
		t.Error("retain=1 时 9 月分区必须已过期（841: cutoff = 当月月初 ⇒ 9 月 < 10 月）")
	}
	// retain=2 ⇒ 保留 9+10 月 ⇒ 9 月**不**过期。
	if IsMonthlyPartitionExpired(sep, cur, 2) {
		t.Error("retain=2 时 9 月分区必须仍保留（cutoff = 2026-09-01 ⇒ 9 月 < cutoff 为假）")
	}
	// retain=2 ⇒ 8 月过期。
	if !IsMonthlyPartitionExpired(aug, cur, 2) {
		t.Error("retain=2 时 8 月分区必须已过期")
	}
	// ★★ 承重性质：当月与未来月**永不过期**（任何 retain 值）。
	for _, retain := range []int{1, 2, 3, 6, 12} {
		if IsMonthlyPartitionExpired(octPart, cur, retain) {
			t.Errorf("retain=%d 时当月分区被判为过期 —— 预建的当月分区会被 DROP ⇒ 写入路径报 23514", retain)
		}
		if IsMonthlyPartitionExpired(novPart, cur, retain) {
			t.Errorf("retain=%d 时未来月分区被判为过期 —— 预建分区会被 DROP ⇒ 写入路径报 23514", retain)
		}
	}
	// 没有策略 ⇒ 恒不过期（841：配置表里没这个族就根本不进判定循环）。
	if IsMonthlyPartitionExpired(sep, cur, 0) {
		t.Error("retain=0（无策略）时不得判任何分区为过期")
	}

	// ── 跨月差：必须用月序号差 ─────────────────────────────────────────
	// ★ `timestamp - date` 得到的是以天计的 interval ⇒ EXTRACT(month) 恒 0
	//   ⇒ 「几个月前」会全算成 0 ⇒ 危险方向。门在这里钉住 Go 版的行为。
	for _, tc := range []struct {
		part, cur time.Time
		want      int
	}{
		{sep, cur, 1},
		{octPart, cur, 0},
		{novPart, cur, -1},
		{time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), cur, 3},
	} {
		if got := MonthlyMonthDistance(tc.part, tc.cur); got != tc.want {
			t.Errorf("MonthlyMonthDistance(%s, %s) = %d，期望 %d", tc.part, tc.cur, got, tc.want)
		}
	}
}

// TestSection14346RegistryIsRecomputable 让登记册**不可能自说自话**：
// 每行的 verdict 必须等于决策表重算出来的结果，且绝对条数与绝对字节钉死。
func TestSection14346RegistryIsRecomputable(t *testing.T) {
	rows := parseMonthlyRegistry(t, monthlyTsv14346)

	if len(rows) != 52 {
		t.Fatalf("登记册行数 = %d，期望 52（少一行 = 一个缺口被悄悄抹掉）", len(rows))
	}

	for _, row := range rows {
		got, reason := ClassifyMonthlyRetention(MonthlyRetentionInput{
			Scope:           row.Scope,
			Mechanism:       row.Mechanism,
			TTLDeclaredDays: row.TTLDeclaredDays,
		})
		if got != row.Verdict {
			t.Errorf("%s/%s：登记册 verdict = %q，决策表重算 = %q（%s）",
				row.Instance, row.Family, row.Verdict, got, reason)
		}
	}

	// ★★★ 钉住的是**可回收量**（reclaim_keep1），不是**族总量**。
	//
	//   为什么换钉法：族总量里包含**当月分区**，而当月分区每分钟都在长
	//   （252 的 session_turns_2026_10 就是活事实）⇒ 钉总量等于钉一个
	//   明天就不一样的数字 ⇒ 每次刷新登记册都要同步改常量 ⇒ 徒增漂移面。
	//   而 reclaim_keep1 统计的是**已冻结的旧月分区**（9 月及更早）⇒ 稳定
	//   ⇒ 它同时就是**属主做决策的那个数**（保留 1 个月能回收多少）。
	wantReclaim := map[string]int64{
		"252|session_turns":          2868215808, // 2.67 GB（9 月主体）
		"252|usage_ledger":           777576448,  // 741 MB
		"252|session_turn_details":   632389632,  // 603 MB
		"252|request_wal":            201908224,  // 193 MB
		"252|candidate_failure_logs": 154918912,  // 148 MB
		"252|routing_decision_log":   143810560,  // 137 MB
		"252|supplier_errors":        119177216,  // 114 MB
		"252|sessions":               95100928,   // 91 MB
		"252|credential_model_index": 88367104,   // 84 MB
		"252|session_memora":         55099392,   // 53 MB
		"252|session_censors":        29728768,   // 28 MB
		"252|session_bodies":         18489344,   // 18 MB
		"252|_request_logs":          15286272,   // 15 MB
		"252|auto_route_selections":  12779520,   // 12 MB
		"211|session_turns":          409600,
		"211|session_bodies":         163840,
		"211|usage_ledger":           98304,
	}
	seen := map[string]bool{}
	for _, row := range rows {
		key := row.Instance + "|" + row.Family
		if want, ok := wantReclaim[key]; ok {
			seen[key] = true
			if row.ReclaimKeep1 != want {
				t.Errorf("%s：reclaim_keep1 = %d，期望 %d", key, row.ReclaimKeep1, want)
			}
			if row.TotalBytes <= 0 {
				t.Errorf("%s：total_bytes = %d —— 族总量必须 > 0"+
					"（pg_total_relation_size 对分区父表返回 0）", key, row.TotalBytes)
			}
		}
	}
	for key := range wantReclaim {
		if !seen[key] {
			t.Errorf("登记册缺少 %s —— 它是本轮的关键读数，缺行等于缺证据", key)
		}
	}

	// ★★ 中心结论：**会话原始数据族在 PG 路径上无留存** ⇒ 必须在册。
	//   缺一条 ⇒ 这个结论就少了证据（而且这类结论很容易被悄悄删掉）。
	unboundedMust := []string{
		"session_turns", "session_turn_details", "sessions",
		"session_censors", "session_memora", "session_bodies",
		"auto_route_selections", "usage_ledger",
	}
	for _, fam := range unboundedMust {
		found := false
		for _, row := range rows {
			if row.Instance == "252" && row.Family == fam && row.Verdict == MonthlyVerdictUnbounded {
				found = true
			}
		}
		if !found {
			t.Errorf("252 的 %s 必须在册且 verdict=UNBOUNDED —— "+
				"它是本轮的中心结论（PG 路径上无任何留存机制）", fam)
		}
	}

	// ★ 反向：已被专门机制覆盖的族**不得**被写成 UNBOUNDED
	//   （本轮就把 request_wal/_request_logs 误判过一回 ⇒ 这条钉住纠错）。
	coveredMust := map[string]string{
		"request_wal":            "ARCHIVE",
		"_request_logs":          "ARCHIVE",
		"_request_logs_bodies":   "PARTITION_DROP",
		"credential_model_index": "STATE_TABLE_TTL",
		"supplier_errors":        "STATE_TABLE_TTL",
	}
	for fam, wantMech := range coveredMust {
		found := false
		for _, row := range rows {
			if row.Instance == "252" && row.Family == fam {
				found = true
				if string(row.Mechanism) != wantMech {
					t.Errorf("252 的 %s：mechanism = %q，期望 %q", fam, row.Mechanism, wantMech)
				}
				if row.Verdict == MonthlyVerdictUnbounded {
					t.Errorf("252 的 %s 被写成 UNBOUNDED —— 它有专门机制（%s）⇒ 那是误判",
						fam, wantMech)
				}
			}
		}
		if !found {
			t.Errorf("登记册缺少 252 的 %s —— 它是「穷举后确认有机制」的反向证据", fam)
		}
	}
}

// TestSection14346MechanismIsInPlace 盯住**机制不许被拆**。
//
// ★ 为什么不钉「配置表必须为空」：那是一根**不可翻转**的钉
//
//	（属主一旦配了 retain_months 门就红 ⇒ 下一个会话只会去删门）。
//	⇒ 真正该守的是「机制还在」：配置表 / 两个函数 / 审计表 / Go 侧调用。
func TestSection14346MechanismIsInPlace(t *testing.T) {
	raw, err := os.ReadFile(migration841)
	if err != nil {
		t.Fatalf("读取 %s：%v", migration841, err)
	}
	// ★ 剥掉 SQL 注释再断言 ⇒ 判据不能被注释喂饱
	//   （841 的头注释里就写着这些名字 ⇒ 文本匹配会被喂饱）。
	body := stripSQLCommentsForReclaimGate(string(raw))

	for _, needle := range []string{
		"llm_gateway_partition_retention",
		"llm_gateway_expired_month_partitions",
		"llm_gateway_drop_expired_month_partitions",
		"llm_gateway_partition_drop_log",
		"retain_months",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("%s 里（剥注释后）没有 %q —— 841 的机制被拆了，"+
				"月度分区 DROP 将无法启用", migration841, needle)
		}
	}

	// ★★ **建表头必须逐字在**：上面的名字判据**证明不了「表还在」** ——
	// 841 的函数体里就有 5 处引用这个表名 ⇒ 把 CREATE TABLE 整段删掉，
	// 「名字还在」照样成立 ⇒ 门会绿 ⇒ **机制已被拆而门看不见**。
	// ⇒ 承 §143.45 的纪律「判据一旦能在别处被满足就不是判据」：
	//   这里必须钉**建表语句本身**。
	if !strings.Contains(body, "CREATE TABLE IF NOT EXISTS public.llm_gateway_partition_retention (") {
		t.Errorf("%s 里（剥注释后）没有 `CREATE TABLE IF NOT EXISTS public."+
			"llm_gateway_partition_retention (` —— 策略表的**建表语句**不在了 ⇒ "+
			"路径 B 的输入载体被拆（名字可能在函数体里还留着，但表没了）", migration841)
	}

	// Go 侧必须**真的在调用** DROP 那个函数（否则机制建好了也没人跑）。
	//
	// ★★ 这里**必须走 go/ast**，不能用文本匹配：函数名在 bg/partition_manager.go
	// 的**注释**里也出现过（`// `llm_gateway_drop_expired_month_partitions()`，由本文件…`）
	// ⇒ 只删掉真正的调用语句，文本匹配**照样绿** ⇒ 门会被注释喂饱。
	// ⇒ 判据要问的是「**有没有一个真的调用点**」，而 AST 才能回答这件事。
	pm, err := os.ReadFile(partitionMgr14346)
	if err != nil {
		t.Fatalf("读取 %s：%v", partitionMgr14346, err)
	}
	_ = pm // ★ 文本读一遍只为给出更好的报错；判据在 AST 上
	calls := monthlyDropCallSitesByAST(t, partitionMgr14346)
	if len(calls) == 0 {
		t.Errorf("%s 里没有**真正的调用点**（AST 上找不到对 "+
			"llm_gateway_drop_expired_month_partitions 的调用）—— "+
			"路径 B 不会被执行（机制建好但没人调用）", partitionMgr14346)
	}
}

// monthlyDropCallSitesByAST 用 go/ast 找出「**有人真的把调用 DROP 函数的 SQL 传下去**」的所在函数。
//
// ★ 为什么必须走 AST（而不是 strings.Contains）：
//
//	① 该函数名在 bg/partition_manager.go 的**注释**里也出现 ⇒ 文本匹配会被注释喂饱；
//	② AST 只会给出**代码里的字符串字面量**，注释永远进不来 ⇒ 判据与意图一致。
//
// ★★ 注意实现形态：Go 侧**不是**直接调那个 SQL 函数，而是把 SQL 当**字符串**传给
//
//	pm.db.QueryRow(ctx, "SELECT llm_gateway_drop_expired_month_partitions()")
//
// ⇒ 所以判据必须找「**某个调用的实参里有含该函数的 SQL 字符串**」，
// 而不是找 Go 的函数调用节点（那会恒空 ⇒ 变成恒红的假阳性）。
func monthlyDropCallSitesByAST(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s：%v", path, err)
	}
	var sites []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				if strings.Contains(s, "llm_gateway_drop_expired_month_partitions(") {
					sites = append(sites, fn.Name.Name)
				}
			}
			return true
		})
	}
	return sites
}

// TestSection14346ExpirySemanticsAlignAcrossThreePlaces 是本轮**最承重**的一道门。
//
// ★★ 同一个「过期」语义在**三个地方**各写了一遍：
//
//	① 迁移 841 的 SQL：cutoff := cur_month - ((retain_months - 1) || ' months')::interval
//	                   过期 ⇔ mth < cutoff
//	② 判定脚本 verify_monthly_partition_retention.sql：months_old >= n_keep_months
//	③ Go 决策表 admin/monthly_retention_verdict.go：IsMonthlyPartitionExpired
//
//	本轮在②上连踩两次（过期方向写反 / 跨月差算成 0）⇒ 三者必须**逐字对齐**。
//	⇒ 任一处漂移 ⇒ 属主拿到的回收数字就是错的（而且方向可能危险）。
func TestSection14346ExpirySemanticsAlignAcrossThreePlaces(t *testing.T) {
	// ① 841：cutoff 的月算 + 严格小于
	raw, err := os.ReadFile(migration841)
	if err != nil {
		t.Fatalf("读取 %s：%v", migration841, err)
	}
	body := stripSQLCommentsForReclaimGate(string(raw))
	if !strings.Contains(body, "cur_month - ((cfg.retain_months - 1) || ' months')::interval") {
		t.Error("841 的 cutoff 算式变了 —— 过期线不再是「当月月初往前推 N-1 个月」")
	}
	if !strings.Contains(body, "IF mth < cutoff THEN") {
		t.Error("841 的过期比较不再是严格小于 —— 当月/未来月可能被判为过期")
	}
	// ⚠ 反向形态必须**不**存在
	if strings.Contains(body, "IF mth >= cutoff THEN") {
		t.Error("841 的过期比较被改成 >= —— 当月与预建的未来分区会被 DROP ⇒ 写入路径报 23514")
	}

	// ② 判定脚本：过期口径必须是 months_old >= n_keep_months
	sraw, err := os.ReadFile(monthlySql14346)
	if err != nil {
		t.Fatalf("读取 %s：%v", monthlySql14346, err)
	}
	sbody := stripSQLCommentsForReclaimGate(string(sraw))
	if !strings.Contains(sbody, "months_old >= n_keep_months") {
		t.Error("判定脚本里没有 `months_old >= n_keep_months` —— 过期方向可能反了")
	}
	// ⚠ 反向形态（把 < N 当成过期）必须不作为**过期条件**出现
	if strings.Contains(sbody, "WHERE months_old < n_keep_months) AS droppable_parts") {
		t.Error("判定脚本把 `months_old < n_keep_months` 当成过期条件 —— 方向反了，" +
			"数字会变成「保留越多回收越多」")
	}
	// ★ 跨月差必须用月序号差（EXTRACT(month ...) 恒 0 那个坑）
	if strings.Contains(sbody, "EXTRACT(month FROM (date_trunc") {
		t.Error("判定脚本用 `EXTRACT(month FROM (timestamp - date))` 算跨月差 —— " +
			"timestamp-date 得到的是以天计的 interval ⇒ EXTRACT(month) 恒 0 ⇒ 「几个月前」全算成 0")
	}

	// ③ Go 决策表：与 841 语义逐字对拍（用一组语料）
	cur := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for month := 1; month <= 14; month++ {
		part := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, month-1, 0)
		for _, retain := range []int{1, 2, 3, 6, 12} {
			// 按 841 的公式手算
			cutoff := cur.AddDate(0, -(retain - 1), 0)
			want := part.Before(cutoff)
			got := IsMonthlyPartitionExpired(part, cur, retain)
			if got != want {
				t.Errorf("过期判定与 841 不一致：分区月=%s retain=%d ⇒ Go=%v，841 公式=%v",
					part.Format("2006-01"), retain, got, want)
			}
		}
	}
}
