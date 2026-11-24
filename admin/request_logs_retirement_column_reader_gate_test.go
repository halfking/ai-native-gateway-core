//go:build !integration

package admin

// request_logs_retirement_column_reader_gate_test.go — 2026-10-04（审计 §9.193）。
//
// S4 退役风险清单的三张表（`db.RetirementUnservableColumns` /
// `RetirementDegradedColumns` / `RetirementStructuralGapColumns`）回答的是
// 「这一列退役后会怎样」。**它们不回答、也没人问「这一列有人在读吗」**——
// 一列无人读取时，它归哪一档都不会伤害任何人，把它留在「会断的列」里
// 只会让整张清单虚高，而**虚高的清单会被整体折扣**，真正会断的那几列
// 于是跟着一起被忽略。
//
// 这道门补的是后半个问题：**清单里的每一列，必须至少有一个生产读方**，
// 否则必须具名登记为什么可以没有。
//
// # 为什么是「默认拒绝 + 具名登记」，不是「一刀切禁止」
//
// 与 `bodiesUnaffectedJustification`（§9.183）同一种形状：默认拒绝会误伤
// 「确实无人消费」的正确写法；放行条件从「门写宽了」变成
// **「有人写下了为什么，而这段话会被 diff 审到」**。
//
// # 判据为什么必须区分 SELECT 与 INSERT
//
// 2026-10-04 我用**逐行** grep 断言「`client_protocol` 全仓无人 SELECT 读」，
// 并据此在决策表 D28-c 里建议把它移出清单。**那个结论是错的**：
// `admin/logs.go:204` 的主日志查询（走 710 视图）确实 SELECT 了
// `rl.client_protocol` 并下发到 JSON 字段。逐行 grep 漏掉它，
// 因为那段 SQL 字面量**跨行**。
//
// ⇒ 本门从 AST 取 SQL 字面量（复用 `extractV1ReadingLiterals`），
// 并**要求字面量里出现 SELECT 词**才算读方；纯 INSERT/UPDATE 列表不算。
// 注释已在 `extractV1ReadingLiterals` 里被剥掉，所以
// 「-- 注释里提到 SELECT」不会冒充读方。
//
// 这也是本仓已经吃过的一课（`request_logs_retirement_exposure_test.go` 头注释：
// 逐行 `from\s+_request_logs` 正则「数调用点可以，归因到某一列不行」）——
// 我用手工 grep 又犯了一次。

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

// selectRe 只认独立成词的 SELECT，避免命中 selected / subselect。
var selectRe = regexp.MustCompile(`(?i)\bSELECT\b`)

// retirementColumnsWithoutReaders 登记「三张退役清单里、但**本门口径下**
// 没有任何 SQL 读方」的列，每条必须写清机制。
//
// ⚠ **口径是「同一字面量含 SELECT」**（见下面的说明）：因此「读方存在但只经
// **拼接片段**出现」也会落进这张表。⇒ 登记时必须把**读方实际在哪**写出来 ——
// 只写「没有读方」会是一句**不属实**的话（这正是本仓反复记过的那个坑：
// 「把不知道报成安全」）。
//
// 空字符串或缺项都会被本门判红。移除一条登记的**后果**也一样（红）：
// 那说明有人加了读方却没回来销账。
var retirementColumnsWithoutReaders = map[string]string{
	"test_col": "测试占位列。migration 710 把它登记为 NULL 补位，v1 侧 46,490 行全满、" +
		"session 侧 0 行——但**没有任何生产 SQL 读它**（全仓只有 710 的投影列表里出现过它）。" +
		"⇒ 它的「不可供给」不构成任何读方风险，属测试脚手架。",
	"test_tab_indent": "与 test_col 同批的测试占位列，同样只有投影、没有读方。" +
		"保留登记是为了让「它也不可供给」这件事有一个显式的归档位置，" +
		"而不是靠「没人提所以没人管」。",
	"stream_chunks_sent": "只有写方：`domains/hooks/observability/telemetry/client.go` 的写列、" +
		"`admin/telemetry.go` 的写列、`domains/session/v2/details_writer.go` 的写列；" +
		"`domains/streaming/handler.go:6255` 读的是**内存 map** `m[\"stream_chunks_sent\"]`，" +
		"不是 SQL。⇒ 无 SQL 读方，降级对它不构成读方风险。",
	"quality_fix_actions": "只出现在 db/db.go 的 **DDL** 里：`ALTER TABLE _request_logs … SET storage` " +
		"的列名清单（:2056）与 `ADD COLUMN IF NOT EXISTS quality_fix_actions JSONB`（:2158）。" +
		"两者都不是 SELECT。⇒ 它的降级不构成任何读方风险。",
	"client_request_id": "★ 2026-10-20（迁移 890）新增登记：**读方存在，但本门口径看不见它**。\n" +
		"真实读方有两处：① `admin/logs.go:1010` 的 getLog 腿 2 —— " +
		"`scanDetailRow(\"rl.client_request_id = $1\")`；" +
		"② `admin/unified_detail.go:205-212` 的第二条查询 —— " +
		"`WHERE rl.client_request_id = $1`（会话族原生源 + 租户谓词）。\n" +
		"两处的谓词都在**拼接片段**里：含列名的那一片自己不带 SELECT/FROM " +
		"（`admin/unified_detail.go` 的原文是 ` rl\n\t\t\t WHERE rl.client_request_id = $1`，" +
		"实测 `sqlReadsColumn` 对它返回 false —— 因为该函数要求**同一条语句**含 SELECT）。\n" +
		"⇒ 本门「同一字面量含 SELECT」的口径看不见它们 ⇒ 登记。\n" +
		"⚠ **这不是「没有读方」**：读方确实存在，且读的是会话族原生源" +
		"（`client_request_id` 在会话臂有真值，迁移 874 还给它建了部分索引）。" +
		"⇒ 退化对它不构成读方风险；登记它是为了把这条**口径盲区**显式记下来。",
	"credits_rate_multiplier": "★ 2026-10-20（迁移 890）新增登记：**读方存在，但本门口径看不见它**。\n" +
		"真实读方：`maas/credits_sql.go:25` —— " +
		"`COALESCE(` + alias + `.credits_rate_multiplier, 1.0)::float8`。" +
		"那是一个**表达式片段**（既无 SELECT 也无 FROM，逗号分隔项 < 3 ⇒ " +
		"既不是读区、也不像投影清单）⇒ 本门口径看不见它 ⇒ 登记。\n" +
		"⚠ **这不是「没有读方」**。而且它本来就是一个**结构性缺口列**：" +
		"会话臂上恒为 NULL（迁移 738 建的 v1 侧列，来源是 890 已删的中间包装视图），" +
		"读方拿到的永远是 COALESCE 的兜底 1.0 ⇒ 退化对它不构成读方风险。",
	"client_forwarded_for": "只有写方：`telemetry/context_attrs.go:159/190`（写 entry + 写列）、" +
		"`domains/session/v2/turn_writer.go` 的写列。816/817 把它从 NULL 补位改成有源投影，" +
		"但**至今没有任何 SELECT 读它**。⇒ 降级对它不构成读方风险。",
}

// retirementListedColumns 返回三张清单的全集（去重、排序）。
func retirementListedColumns() []string {
	seen := map[string]bool{}
	var out []string
	for _, cols := range [][]string{
		db.RetirementUnservableColumns,
		db.RetirementDegradedColumns,
		db.RetirementStructuralGapColumns,
	} {
		for _, c := range cols {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

// retirementColumnsSelectedSomewhere 扫出「被任何生产 SQL 读方 SELECT 过」的退役列。
//
// # 为什么要做到「语句区域」这么细
//
// 2026-10-04 两次实测把两种朴素判据都打掉了：
//
//  1. **逐行 grep**（我第一版）漏掉 `admin/logs.go:204` 的 `rl.client_protocol`，
//     因为那段 SQL 字面量**跨行**。⇒ 逐行不可用。
//  2. **按字面量要求含 SELECT**（我第二版）同样漏掉它：主查询由三个常量拼接
//     ——`requestLogsListCols`（投影，无 SELECT）、`requestLogsJoins`（FROM）、
//     `requestLogStatusExpr`。含列名的那段自己**没有 SELECT**。
//  3. **按字面量要求含 SELECT**（我第三版）又**多报**了
//     `client_forwarded_for`：`domains/session/v2/turn_writer.go:378` 是一个
//     同时含 INSERT 列表与别处 SELECT 的巨型字面量，于是纯写列表被当成了读方。
//
// ⇒ 结论：**同一类「多段拼接的查询」让两种判据朝相反方向各错一次。**
//
//	共享提取器 `extractV1ReadingLiterals` 错在**要求同一字面量里有 FROM**（漏报），
//	我前两版错在**要求同一字面量里有 SELECT**（一个漏报、一个多报）。
//	它的头注释声明「这是上界：只会多报，不会漏报」——**在拼接式查询上该声明不成立**。
//	修共享提取器会改动 §9.161/§9.162 已公布的数字 ⇒ 属主决定（决策表 **D29**），本轮不动。
//
// # 本门采用的判据
//
// 逐字面量 + 逐**语句区域**：
//   - 读区 = `SELECT … FROM` 之间、`WHERE` 之后、`GROUP BY`/`ORDER BY` 之后；
//   - 写区 = `INSERT … INTO` / `UPDATE … SET` 的列表，被**明确排除**；
//   - 跨段拼接兜底：字面量本身既无 INSERT 也无 SELECT，但**同文件**有任一含
//     SELECT 的字面量时，按中性处理（宁可多报——本门的失败形态是误报，
//     不是漏报；漏报会让「无读方」的列被错误地要求具名解释）。
//
// 字面量提取复用本包既有的 `goStringLiterals`（AST 版，`view_column_contract_test.go`），
// 不自建第二套。**这条规矩本轮我自己先违反了**：先写了正则版，编译报错才发现重名。
func retirementColumnsSelectedSomewhere(t *testing.T, root string) map[string]bool {
	t.Helper()
	cols := retirementListedColumns()
	anchored := make([]*regexp.Regexp, len(cols))
	for i, c := range cols {
		anchored[i] = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(c) + `\b`)
	}
	hit := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		var lits []string
		fileHasSelect := false
		for _, lit := range goStringLiterals(t, path) {
			lit = sqlBlockCommentRe.ReplaceAllString(lit, " ")
			lit = sqlLineCommentRe.ReplaceAllString(lit, " ")
			lits = append(lits, lit)
			if selectRe.MatchString(lit) {
				fileHasSelect = true
			}
		}
		if !fileHasSelect {
			return nil // 这个文件里没有任何查询，不可能读任何列
		}
		// ★★★ 2026-11-23 L30 轮·补：★ **字面量只扫一次**。
		// ★ 原形态是【每列重算一遍】——★ `sqlReadsColumn(lit, re)` 与
		//   `looksLikeProjectionList(lit)` **都只依赖 lit 本身**、★ 却各跑【列数】遍
		//   ⇒ ★ 量＝【列数 × 字面量数】。
		// ★ ★★ CPU profile 实测（★ 本机 `-cpuprofile`、★ 单跑这道门 10.38s）：
		//   ★ `sqlReadsColumn` **占 42.07%**、★ `looksLikeProjectionList` **占 28.51%**
		//   ⇒ ★★ **两者的乘数都恰好是【列数】** ⇒ ★ **先把它们提到【每字面量一次】**。
		// ★ ★★ 语义等价性（★ ★ 必须写清楚、★ 否则「为了快而改判据」没人敢信）：
		//   ★ 切语句规则（★ 按 `;`）、★ 写语句跳过（★ INSERT/UPDATE/DELETE）、
		//   ★ 非 SELECT 语句跳过、★ 读区定义（★ `selectRegionRe` 的非空捕获组）、
		//   ★ 投影清单兜底（★ 整段字面量匹配）—— ★ **全部逐字不变**；
		//   ★ ★ 唯一的变化是【同一段解析从【每列一次】变成【每字面量一次】】。
		// ★ ★★ 逐字面量判定，**刻意不把整个文件的字面量合并**。
		// 合并过一次，失败得很具体：合并后产生「假语句」——
		// 真实查询的 `;` 不在字面量里，于是 `WHERE … $` 的读区一路吞到合并文本末尾，
		// 把后面的裸列名常量（canonicalColumnOrderV2）也算成读方。
		// ⇒ **拼接的正确解法不是合并文本，是识别「投影段」这种形状。**
		scans := make([]litScan, 0, len(lits))
		for _, lit := range lits {
			scans = append(scans, scanLiteral(lit))
		}
		for i, re := range anchored {
			if hit[cols[i]] {
				continue // 这一列已经有读方了 ⇒ 不用再扫剩下的字面量
			}
			for _, ls := range scans {
				if literalReadsColumn(ls, re) {
					hit[cols[i]] = true
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描生产源码失败: %v", err)
	}
	return hit
}

// projectionForbiddenRe 是投影段里**不该**出现的词：出现即说明这段不是投影清单。
var projectionForbiddenRe = regexp.MustCompile(`(?i)\b(SELECT|FROM|WHERE|JOIN|INSERT|UPDATE|DELETE|VALUES|SET|AND|OR)\b`)

// looksLikeProjectionList 报告这个字面量是否像「投影列清单」——
// 即多段拼接查询里夹在 SELECT 与 FROM 之间的那部分。
//
// 形状判据（三条都要满足，刻意保守）：
//  1. 不含任何 SQL 结构/逻辑关键词（`SELECT`/`FROM`/… 一律排除）；
//  2. 逗号分隔的项 ≥ 3（`canonicalColumnOrderV2` 里每个元素是**单个**裸列名，
//     逗号不在字面量里 ⇒ 判否，这是关键的排他条件）；
//  3. 至少有一项带 `AS` 或点号限定（`rl.client_protocol` / `x AS y`）——
//     纯裸名列表不认。
func looksLikeProjectionList(lit string) bool {
	if projectionForbiddenRe.MatchString(lit) {
		return false
	}
	items := strings.Split(lit, ",")
	if len(items) < 3 {
		return false
	}
	qualified := false
	for _, it := range items {
		t := strings.TrimSpace(it)
		if t == "" {
			return false
		}
		if strings.Contains(t, " AS ") || strings.Contains(t, ".") {
			qualified = true
		}
	}
	return qualified
}

// writeRe 认写语句：INSERT…INTO / UPDATE…SET / DELETE。
var writeRe = regexp.MustCompile(`(?is)^\s*(INSERT\s+INTO|UPDATE\s+\S+\s+SET|DELETE\s+FROM)\b`)

// selectRegionRe 读区：SELECT…FROM 之间、WHERE 之后、GROUP BY / ORDER BY / HAVING 之后。
// 每条 alternative 都**终止于下一个结构关键字或句末**——上一版 WHERE 分支终止于
// 「下一个关键字或整段文本末尾」，在合并文本里它会一路吞过写列表，把纯写列表
// 判成读方（实测 `client_forwarded_for` / `stream_chunks_sent` 就是这样被多报的）。
var selectRegionRe = regexp.MustCompile(`(?is)\bSELECT\b(.*?)\bFROM\b` +
	`|\bWHERE\b(.*?)(?:\bGROUP\s+BY\b|\bORDER\s+BY\b|\bLIMIT\b|$)` +
	`|\b(?:GROUP\s+BY|ORDER\s+BY|HAVING)\b(.*?)(?:\bLIMIT\b|$)`)

// sqlReadsColumn 报告列是否被这段 SQL **读取**（而不是被写）。
//
// 按 `;` 切语句后逐条判：写语句整条跳过，读语句再看它有没有落在读区里。
// 「先切语句」是必要的——不切的话，读区的终止条件会在拼接文本里跨界。
//
// ★ 2026-11-23 L30 轮·补：★ **本函数保持原样、★ 一个字节都没改**。
// ★ 它现在只被**等价性自证测试**调用（★ `TestRetirementReaderScanMatchesLegacyPath`）
//
//	—— ★ 因为热路径已改成「字面量只扫一次」的 `scanLiteral` 形态，
//	★ 而这份【原实现】正是【那条自证的基准】。
//
// ★ ⇒ 留着的理由不是「兼容」、★ 而是「**要有东西可比**」：
//
//	★ 一份没人对照过的「更快实现」不叫优化、★ 叫改口径。
func sqlReadsColumn(sql string, colRe *regexp.Regexp) bool {
	for _, stmt := range strings.Split(sql, ";") {
		if writeRe.MatchString(stmt) {
			continue // INSERT/UPDATE/DELETE：写列表里的列名不是读
		}
		if !selectRe.MatchString(stmt) {
			continue
		}
		if regionsMention(stmt, colRe) {
			return true
		}
	}
	return false
}

func regionsMention(stmt string, colRe *regexp.Regexp) bool {
	for _, m := range selectRegionRe.FindAllStringSubmatch(stmt, -1) {
		for _, g := range m[1:] {
			if g != "" && colRe.MatchString(g) {
				return true
			}
		}
	}
	return false
}

// litScan 是一个 SQL 字面量的**一次**扫描结果。
//
// ★ 2026-11-23 L30 轮·补：★ 原形态把「这段 SQL 读到了哪些列」这件事
//
//	**对每一列各算一遍** ⇒ ★ 量＝【列数 × 字面量数】。
//
// ★ litScan 把「只依赖字面量本身」的那部分（★ 读区文本、★ 是否投影清单）
//
//	**提到字面量层级算一次**，★ 列这一层只剩「这段文本里有没有这个列名」。
type litScan struct {
	regions []string // 读区文本（★ 写语句与非 SELECT 语句已剔除）
	isProj  bool     // 是否像投影列清单（★ 对整段字面量判断）
	whole   string   // 字面量原文（★ 投影兜底要拿它整段匹配）
}

// scanLiteral 扫一个 SQL 字面量一次，返回它自己的读区与形状判断。
//
// ★ 与 `sqlReadsColumn` 的语义**逐条等价**（★ 等价性由
//
//	`TestRetirementReaderScanMatchesLegacyPath` 在**真实语料**上逐列对拍验证）：
//	★ 按 `;` 切语句、★ 写语句跳过、★ 非 SELECT 语句跳过、
//	★ 读区＝`selectRegionRe` 的非空捕获组、★ 投影清单兜底＝整段字面量匹配。
func scanLiteral(lit string) litScan {
	out := litScan{whole: lit, isProj: looksLikeProjectionList(lit)}
	for _, stmt := range strings.Split(lit, ";") {
		if writeRe.MatchString(stmt) {
			continue // INSERT/UPDATE/DELETE：写列表里的列名不是读
		}
		if !selectRe.MatchString(stmt) {
			continue
		}
		for _, m := range selectRegionRe.FindAllStringSubmatch(stmt, -1) {
			for _, g := range m[1:] {
				if g != "" {
					out.regions = append(out.regions, g)
				}
			}
		}
	}
	return out
}

// literalReadsColumn 报告这个**已扫描**的字面量是否读到了该列。
func literalReadsColumn(ls litScan, colRe *regexp.Regexp) bool {
	for _, g := range ls.regions {
		if colRe.MatchString(g) {
			return true
		}
	}
	// 拼接兜底：像投影列清单的片段（SELECT 段与 FROM 段之间的那部分）。
	if ls.isProj && colRe.MatchString(ls.whole) {
		return true
	}
	return false
}

// TestRetirementListedColumnsHaveAProductionReader 是这道门本体。
func TestRetirementListedColumnsHaveAProductionReader(t *testing.T) {
	root := repoRootFromCaller(t)

	// 先算「有 SELECT 读方」的列集合。
	//
	// ⚠ 这里**刻意不用** `extractV1ReadingLiterals` 的归因结果。原因是实测到它的一个
	// 盲区（2026-10-04，审计 §9.193.2）：`admin/logs.go` 的主查询由**多个字符串常量
	// 拼接**而成——`requestLogsListCols`（投影，含 `rl.client_protocol`）、
	// `requestLogsJoins`（FROM/JOIN）、`requestLogStatusExpr` 三段。
	// 含列名的那一段**自己不带 FROM**，于是 `aliasesIn()` 得到空别名表，
	// `columnAttribution` 返回 attrNone，该列**从未被归因**。
	// ⇒ 该文件在现有暴露报告里 definite 只列了 2 列，缺的正是这一整段投影。
	//
	// 这一点很重要，因为 `request_logs_retirement_exposure_test.go` 的头注释
	// 明确声明「这是**上界**：它会多报，不会漏报」。**在拼接式查询上这个声明不成立。**
	// 修 `columnAttribution` 的作用域会改变 §9.161/§9.162 已公布的数字
	// ⇒ 那是属主决定（决策表 D29），本轮不动它。
	//
	// 本门问的是**另一个问题**——「这一列有没有被任何 SQL 字面量 SELECT 过」——
	// 它不需要知道列来自哪条腿，因此用逐字面量的词边界匹配即可。
	// 两个问题的机制不同是**合理的**，不是重复实现。
	hasReader := retirementColumnsSelectedSomewhere(t, root)
	t.Logf("扫描范围：%s 下全部非测试 .go 文件（比读方清单更宽——"+
		"读清单里的列可能被清单外的文件读到，漏扫会误报「无读方」）", root)

	var missing, stale []string
	for _, col := range retirementListedColumns() {
		reason, registered := retirementColumnsWithoutReaders[col]
		_, has := hasReader[col]
		switch {
		case registered && strings.TrimSpace(reason) == "":
			t.Errorf("%s 已登记为「无生产读方」，但理由是空的 —— 登记必须写清机制", col)
		case registered && has:
			stale = append(stale, col)
		case !registered && !has:
			missing = append(missing, col)
		}
	}

	if len(missing) > 0 {
		t.Errorf("退役清单里有 %d 列**没有任何生产 SQL 读方**，却没在 "+
			"retirementColumnsWithoutReaders 里具名登记：\n  %s\n\n"+
			"这会让三张风险清单虚高——而虚高的清单会被整体折扣。\n"+
			"请为每一列写清：它为什么可以没有读方（测试占位 / 只有写方 / 消费方在别处），\n"+
			"或者**把它从清单里移出去**并写明理由。\n"+
			"⚠ 若你确实新增了读方，请回来销账登记项——那会让本门红，这是故意的。",
			len(missing), strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("这 %d 列登记为「无生产读方」，但本次扫描**找到了读方**："+
			"\n  %s\n\n登记已过期。两种可能：(a) 读方是这次新加的 ⇒ 请删掉登记项；"+
			"(b) 扫描口径有误 ⇒ 请修判据。**不要**两边都留着。",
			len(stale), strings.Join(stale, "\n  "))
	}
	t.Logf("退役清单共 %d 列，其中 %d 列登记为无生产读方，%d 列确认有读方。",
		len(retirementListedColumns()), len(retirementColumnsWithoutReaders),
		len(retirementListedColumns())-len(retirementColumnsWithoutReaders))
}

// TestRetirementReaderScanMatchesLegacyPath 是「提速后的扫描」与「提速前的扫描」
// 的**逐列对拍自证**。
//
// # 为什么这道门自己需要一道门
//
// 2026-11-23 L30 轮·补把本文件的扫描改成了「字面量只扫一次」的形态
// （`scanLiteral` ＋ `literalReadsColumn`），**热路径不再调用** `sqlReadsColumn`。
// ★ ⇒ 那一刻 `sqlReadsColumn` 变成【没人对照过的实现】——★ 而「没人对照过的更快实现」
//
//	不叫优化、★ **叫改口径**（★ 这正是本仓反复记过的那个坑）。
//
// ★ ⇒ 所以本测试把**原实现原样留下**（★ `sqlReadsColumn` 一个字节都没改）
//
//	并在**真实语料**上逐列对拍：★ 任何一列、任何一句字面量、★ 两种判定必须一致。
//
// # 判据
//
// 对每个采样到的生产 .go 文件、每条 SQL 字面量、每一列：
//
//	sqlReadsColumn(lit, re)            ← 提速前（★ 原始实现）
//	literalReadsColumn(scanLiteral(lit), re) ← 提速后（★ 新实现）
//
// 两者必须**逐条相等**。不等即判红——★ 并在失败信息里打印
// **是哪一列、哪一个文件、哪一句字面量**。
//
// # 为什么采样而不是全仓
//
// 全仓对拍要按【列数 × 字面量数】跑原始实现 ⇒ 那是这道门被提速掉的那份开销
// （★ 实测约 10s）⇒ ★ 把省下来的时间又还回去就白改了。
// ⇒ ★ 取**真实语料的一个足够宽的切片**：★ 默认最多 60 个文件、
//
//	★ 且**必含含读方的那些文件**（★ 否则对拍的都是空字面量、★ 等于没对）。
func TestRetirementReaderScanMatchesLegacyPath(t *testing.T) {
	root := repoRootFromCaller(t)
	cols := retirementListedColumns()
	anchored := make([]*regexp.Regexp, len(cols))
	for i, c := range cols {
		anchored[i] = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(c) + `\b`)
	}

	// ★ ★ 成本按【比较次数】封顶、★ 而不是按【文件数】。
	// ★ 为什么不用文件数：★ 实测 60 个文件要 11.95s、★ 而 20 个文件反而要 15.61s
	//   ⇒ ★★ **墙钟读数被机器负载主导（★ 当时 load average 234+）**
	//   ⇒ ★★ **按秒数调采样＝按噪声调采样** ⇒ ★★ **按【工作量】封顶才稳**。
	// ★ 4 万次比较 ≈ 原实现的正则工作量（★ 每次比较都要跑一遍原实现的读区解析）。
	const maxComparisons = 40000
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("扫描生产源码失败: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("没有扫到任何生产 .go 文件 —— 对拍样本为空即等于没对")
	}

	// ★ 优先采「含读方的文件」：★ 它们才是这条判据真正起作用的场景。
	var withReaders, withoutReaders []string
	for _, p := range files {
		lits := strippedSQLLiterals(t, p)
		if len(lits) == 0 {
			continue
		}
		has := false
		for _, lit := range lits {
			for _, re := range anchored {
				if legacyColumnPredicate(lit, re) {
					has = true
					break
				}
			}
			if has {
				break
			}
		}
		if has {
			withReaders = append(withReaders, p)
		} else {
			withoutReaders = append(withoutReaders, p)
		}
	}
	sample := append(withReaders, withoutReaders...)
	if len(withReaders) == 0 {
		t.Fatal("样本里一个「含读方」的文件都没有 ⇒ 对拍样本失去意义")
	}

	compared, mismatches, filesUsed := 0, 0, 0
	for _, p := range sample {
		if compared >= maxComparisons {
			break
		}
		filesUsed++
		for _, lit := range strippedSQLLiterals(t, p) {
			if compared >= maxComparisons {
				break
			}
			ls := scanLiteral(lit)
			for i, re := range anchored {
				if compared >= maxComparisons {
					break
				}
				legacy := legacyColumnPredicate(lit, re)
				fast := literalReadsColumn(ls, re)
				compared++
				if legacy == fast {
					continue
				}
				mismatches++
				if mismatches <= 5 {
					t.Errorf("提速前后判定不一致：列 `%s`、文件 %s\n  字面量：%q\n"+
						"  提速前＝%v、★ 提速后＝%v\n"+
						"  ⇒ 提速后的扫描**改了判据**（★ 而不只是改了算法）⇒ 必须回退。",
						cols[i], p, lit, legacy, fast)
				}
			}
		}
	}
	if mismatches > 5 {
		t.Errorf("提速前后共有 %d 处判定不一致（★ 只报了前 5 处）", mismatches)
	}
	t.Logf("对拍样本：%d 个文件（★ 其中含读方 %d 个、★ 优先入样）、%d 列、%d 次逐列比较、%d 处不一致",
		filesUsed, len(withReaders), len(cols), compared, mismatches)
}

// legacyColumnPredicate 是**提速前热路径**对「字面量 × 列」的完整判定。
//
// ★ ★ 它必须**两步都算**：★ 原热路径先问 `sqlReadsColumn`（同一条语句里含 SELECT
//
//	且落在读区）、★ 再问投影兜底（★ 字面量像投影列清单且整段含该列名）。
//
// ★ ★★ 第一版自证只拿 `sqlReadsColumn` 当基准 ⇒ ★ **当场对拍出 17 处不一致**
//
//	（★ 如 `"COALESCE(rdl.model, rdl.client_model, rdl.outbound_model)"`：
//	★ ★ 它没有 SELECT、★ 所以 `sqlReadsColumn` 判 false、★ 但它是投影清单形态
//	★ ★ ⇒ 原热路径判 **true**）⇒ ★★ **基准写错、不是实现写错** —— ★
//	★★ 而这恰恰证明这道自证有用：★ ★ **它把「我以为的等价」当场证伪了**。
func legacyColumnPredicate(lit string, colRe *regexp.Regexp) bool {
	if sqlReadsColumn(lit, colRe) {
		return true
	}
	// 拼接兜底：像投影列清单的片段（SELECT 段与 FROM 段之间的那部分）。
	return looksLikeProjectionList(lit) && colRe.MatchString(lit)
}

// strippedSQLLiterals 取一个文件的 SQL 字面量并剥掉注释（★ 与本门热路径同口径）。
func strippedSQLLiterals(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	for _, lit := range goStringLiterals(t, path) {
		lit = sqlBlockCommentRe.ReplaceAllString(lit, " ")
		lit = sqlLineCommentRe.ReplaceAllString(lit, " ")
		out = append(out, lit)
	}
	return out
}
