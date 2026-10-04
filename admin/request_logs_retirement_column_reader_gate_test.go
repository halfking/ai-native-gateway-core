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
// 逐行 `from\s+request_logs` 正则「数调用点可以，归因到某一列不行」）——
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

// retirementColumnsWithoutReaders 登记「三张退役清单里、但生产代码中
// **没有任何 SQL 读方**」的列，每条必须写清机制。
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
	"quality_fix_actions": "只出现在 db/db.go 的 **DDL** 里：`ALTER TABLE request_logs … SET storage` " +
		"的列名清单（:2056）与 `ADD COLUMN IF NOT EXISTS quality_fix_actions JSONB`（:2158）。" +
		"两者都不是 SELECT。⇒ 它的降级不构成任何读方风险。",
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
		var cleaned []string
		fileHasSelect := false
		for _, lit := range goStringLiterals(t, path) {
			lit = sqlBlockCommentRe.ReplaceAllString(lit, " ")
			lit = sqlLineCommentRe.ReplaceAllString(lit, " ")
			cleaned = append(cleaned, lit)
			if selectRe.MatchString(lit) {
				fileHasSelect = true
			}
		}
		if !fileHasSelect {
			return nil // 这个文件里没有任何查询，不可能读任何列
		}
		// 逐字面量判定，**刻意不把整个文件的字面量合并**。
		// 合并过一次，失败得很具体：合并后产生「假语句」——
		// 真实查询的 `;` 不在字面量里，于是 `WHERE … $` 的读区一路吞到合并文本末尾，
		// 把后面的裸列名常量（canonicalColumnOrderV2）也算成读方。
		// ⇒ **拼接的正确解法不是合并文本，是识别「投影段」这种形状。**
		for i, re := range anchored {
			for _, lit := range cleaned {
				if sqlReadsColumn(lit, re) {
					hit[cols[i]] = true
					break
				}
				// 拼接兜底：像投影列清单的片段（SELECT 段与 FROM 段之间的那部分）。
				if looksLikeProjectionList(lit) && re.MatchString(lit) {
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
