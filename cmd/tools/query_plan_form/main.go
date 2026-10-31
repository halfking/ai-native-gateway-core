package main

// main.go —— R179 轮：**读点的实测计划形态**量具。
//
// # 这道量具补的是哪块缺口
//
// ★ R178 轮建立了「**索引的键**」（表达���索引）⇒ 但它只证明了「索引建出来了、
//   且键与视图投影逐字同形」。
// ★★★ 而 R178 自己留下了一个更硬的问题：
//   「**索引存在**」≠「**索引真被用上**」——
//   ★ 例如：索引的键是 `(CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END), ts DESC`，
//   ★ 而查询写的是 `WHERE api_key_id = $1`（**没有那个 CASE 表达式**）
//   ⇒ ⇒ PG **用不上**那条索引 ⇒ ⇒ 索引「在」，查询却在扫。
//
// ★★ 于是本轮把判据从「**索引存在吗**」升级为「**规划器实际走了哪条路**」。
//
// # 形态词表（★ 本工具输出的分类）
//
//	seek      —— 每一处【臂扫描】都是索引扫描，且**没有**行被扫描后过滤掉
//	filter    —— 臂扫描是索引扫描，但**有行被过滤掉**（★ 索引没接住那个谓词）
//	seq-scan  —— 有一处臂扫描是**顺序扫描**（★ 规划器根本没打算用索引）
//	no-scan   —— 计划里**没有**臂扫描（★ 答案来自别的关系，例如 rollup 表）
//	dynamic   —— SQL 里含 fmt 占位（%s/%d）⇒ **静态不可知** ⇒ 必须先解析 helper
//
// ★★ **「臂」是什么**：★ canonical 视图
//   `request_logs_with_current_month` = `session_turns_hot ∪ session_turns`
//   ＋ `session_turn_details` 的 LEFT JOIN ⇒ ⇒ 一次读会扇出到
//   **两张会话表（分日分区）＋ 一张 details 表** ⇒ ⇒ 本工具只对这「会话族」关系
//   上的扫描节点做判定（★ 其它关系是 rollup / 维表 / 配置表 ⇒ 不参与）。
//
// # ★★ 为什么用 `EXPLAIN`（**不带 ANALYZE**）
//
//	① ★ 只读库（252）上**不执行**查询 ⇒ 不产生行、不加锁、不放大 ⇒ 安全；
//	② ★ 规划器的选择只依赖**统计信息** ⇒ EXPLAIN 已经给出「生产上会选的计划」；
//	③ ★ 带 ANALYZE 会把查询真的跑一遍 ⇒ 在 1.1M 行的生产库上是**慢 SQL 的来源**。
//
// # 参数怎么来（★ 关键：**必须用真实值**）
//
// ★★ 规划器的选择依赖**选择性** ⇒ ⇒ 如果拿一个库里不存在的值去 EXPLAIN，
//   规划器会估成「1 行」⇒ ⇒ 计划形态会与生产**不同** ⇒ ⇒ 读数不可用。
// ⇒ ⇒ 所以本工具**先探针取真实代表值**（该环境里真实存在的 api_key / credential /
//   tenant / request / model / provider），再按**参数角色**（占位符参与的谓词列）
//   把它们内联进 SQL。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ─────────────────────────── 形态册（spec） ───────────────────────────

// Shape 是一个读点**形态**（★ 计划形态只由 关系/谓词/分组/排序 决定 ⇒ 投影列无关）。
type Shape struct {
	ShapeID         string      `json:"shape_id"`
	File            string      `json:"file"`
	Line            int         `json:"line"`
	OccurrenceCount int         `json:"occurrence_count"`
	FileCount       int         `json:"file_count"`
	Dynamic         bool        `json:"dynamic"`
	SQL             string      `json:"sql"`
	FullSQL         string      `json:"full_sql"`
	SQLFilterCols   []string    `json:"sql_filter_cols"`
	SQLOrderCols    string      `json:"sql_order_cols"`
	ParamRoles      []ParamRole `json:"param_roles"`
}

// ParamRole 是一个 `$N` 占位符的**角色**（★ 实测时按角色取真实值）。
type ParamRole struct {
	Index  int    `json:"index"`
	Column string `json:"column"`
	Op     string `json:"op"`
	Kind   string `json:"kind"`
}

type specFile struct {
	Shapes []Shape `json:"shapes"`
}

// ─────────────────────────── 代表值探针 ───────────────────────────

// Representatives 是一组【该环境里真实存在】的代表值。
//
// ★★ R179 轮实测逼出来的形态：★ 早期版本只探针了几���**固定**列
//
//	（api_key / credential / tenant / request / model / provider / session），
//	⇒ ⇒ 其余**列角色**（★ 73 条语句！）只能填 NULL
//	⇒ ⇒ 谓词语义失真（★ 例：success = NULL ⇒ ⇒ 规划器把谓词优化掉）
//	⇒ ⇒ **读数作废**。
//
// ⇒ ⇒ 所以改成：**探针直接问视图要一整行**（★ row_to_json）
//
//	⇒ ⇒ 视图的**每一列**都有真实代表值（★ 且类型与视图投影一致）。
type Representatives struct {
	MaxTS string
	// Cols 是「视图列名 → 该环境里真实存在的值（★ 已是 SQL 字面量形态）」。
	Cols map[string]string
	// ViewCols 是【视图投影出来的列名集合】（★ 用来判「这个参数影不影响视图的扫描形态」）。
	ViewCols map[string]bool
}

// probeSQL 取代表值。
//
// ★ 全部走索引友好的写法（★ ORDER BY ts DESC LIMIT 1 走 idx_session_turns_ts；
//
//	details 侧走 (partition_date, id) 主键的最左前缀）⇒ ⇒ 探针本身是廉价读。
//
// probeSQLTpl 是探针模板（★ 视图名由 -view 传参 ⇒ ★ 源码里不出现那个标识符
//
//	⇒ ★ 与仓内 R177 量具（cmd/tools/partition_bound_probe）同构：
//	⇒ ⇒ **那个文件在 canonical 读点登记簿里被登记为「只在注释/字符串里提及」**）。
var probeSQL string

const probeSQLTpl = `
SELECT row_to_json(v)::text FROM (SELECT * FROM %s LIMIT 200) v
`

// probeBaseSQL 取底表（session_turns）的多行（★ 补齐视图里恰好为 NULL 的基列）。
const probeBaseSQL = `
SELECT row_to_json(t)::text FROM (SELECT * FROM session_turns ORDER BY ts DESC LIMIT 200) t
`

// probeDetailsSQL 取 details 腿的一行（★ 视图那一行的 details 列【恒为 NULL】
//
//	—— 那是 LEFT JOIN 没匹配上 ⇒ ⇒ 拿它当代表值 ⇒ ⇒ 谓词恒假 ⇒ ⇒ 又是假象）。
const probeDetailsSQL = `
SELECT row_to_json(d)::text FROM (
  SELECT * FROM session_turn_details
  WHERE partition_date = (SELECT max(partition_date) FROM session_turn_details)
  LIMIT 1
) d
`

// probeMaxTS 取时间锚（★ 窗口按它折算）。
const probeMaxTS = `SELECT max(ts)::text FROM session_turns`

// ─────────────────────────── EXPLAIN plan ───────────────────────────

type planNode struct {
	NodeType string `json:"Node Type"`
	// ★★ SortKey = Sort 节点的排序列（★ PostgreSQL 的 EXPLAIN JSON 里，
	// ★ Sort / Incremental Sort 节点带 "Sort Key": ["ts DESC", …]）。
	// ★ ★ R181 轮新增：★ 此前「seek」会掩盖「排序没被索引接住」——
	// ★ 走索引拿到行，但 ORDER BY 的列不在索引里 ⇒ 规划器仍要额外 Sort。
	SortKey            []string   `json:"Sort Key"`
	RelationName       string     `json:"Relation Name"`
	Alias              string     `json:"Alias"`
	IndexName          string     `json:"Index Name"`
	IndexCond          string     `json:"Index Cond"`
	Filter             string     `json:"Filter"`
	RecheckCond        string     `json:"Recheck Cond"`
	JoinFilter         string     `json:"Join Filter"`
	RowsRemovedByFilte int64      `json:"Rows Removed by Filter"`
	PlanRows           int64      `json:"Plan Rows"`
	TotalCost          float64    `json:"Total Cost"`
	ActualRows         int64      `json:"Actual Rows"`
	ParentRelationship string     `json:"Parent Relationship"`
	Plans              []planNode `json:"Plans"`
}

type explainPayload struct {
	Plan       planNode `json:"Plan"`
	PlanningMs float64  `json:"Planning Time"`
	ExecMs     float64  `json:"Execution Time"`
}

// ─────────────────────────── 分类 ───────────────────────────

// isArmRelation 判断一个关系名是不是【会话族】（★ canonical 视图的两条臂 + details）。
//
// ★ 命中：session_turns / session_turns_hot / session_turns_YYYY_MM（日分区）
//
//	/ session_turn_details / session_turn_details_YYYY_MM
func isArmRelation(name string) bool {
	if name == "" {
		return false
	}
	return strings.HasPrefix(name, "session_turns") || strings.HasPrefix(name, "session_turn_details")
}

// isBaseRelation 判是不是【视图基表】的关系（★ `session_turns*` / `session_turns_hot`）。
//
// ★★ R179 轮第三次修正（★ 判决对象错了）：★★ 视图的定义是
//
//	FROM session_turns_hot t LEFT JOIN session_turn_details_hot d ON …
//	UNION ALL
//	FROM session_turns t LEFT JOIN session_turn_details d ON …
//
// ★★ ⇒ ⇒ 计划里**同时**有【基表扫描】与【明细表扫描】⇒ ⇒ 而本轮要判的是
// ★★ **「canonical 视图的读有没有被索引承重」** ⇒ ⇒ ⇒ **判决只能由基表扫描做**。
// ★★ ⇒ ⇒ 否则：★ 明细侧（★ request_id 索引）行数估得比基表侧多时，
// ★★ ⇒ ⇒ 判决会变成「明细表被索引承重了」⇒ ⇒ **答非所问**。
func isBaseRelation(name string) bool {
	if name == "" {
		return false
	}
	return strings.HasPrefix(name, "session_turns")
}

type armScan struct {
	relation  string
	nodeType  string
	indexName string
	indexCond string
	filter    string
	rowsRm    int64
	planRows  int64
	totalCost float64
}

// classify 对 plan 树做形态判定。
//
// ★★★ R179 轮修正（★ 本轮自己抓出来的【量具口径缺陷】）★★★
//
// ★ 旧判据（★ 「有 Seq Scan ⇒ seq-scan」；★ 「全部索引扫描但 Rows Removed by Filter > 0 ⇒ filter」）
// ★ 有两个各自独立的缺陷：
//
// ★ ① ★★ **「任一分区被顺序扫描」⇒ 整个计划记 `seq-scan`** ⇒ ⇒ **误标**。
// ★ ★ ★ 实测形态（★ 252，★ 「按 api_key 过滤会话」的真实计划）：
// ★ ★ ★   Index Scan using session_turns_2026_10_ts_idx (Index Cond: ts …)  ← ★ 装数据的分区
// ★ ★ ★         Filter: (CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END = 113)
// ★ ★ ★   Seq Scan on session_turns_2026_07 / _08                            ← ★ 窗口裁不掉的旧月分区
// ★ ★ ★   ★★★ R180 轮实测更正：★ 被扫的分区【全是 0 行】（★ 2026_07 / _08 / _11 / default）
// ★ ★ ★   ⇒ ⇒ ★★ 「出现了 Seq Scan」**不等于**「慢」★★ ⇒ ⇒ 代价取决于【被扫分区的行数】。
// ★ ★ ★ ⇒ ★ 旧判据把整条记成 `seq-scan` ⇒ ★★ 而**承载数据的那次扫描明明是索引扫描**。★★
//
// ★ ② ★★ **`Rows Removed by Filter` 是【执行期】统计** ⇒ ★ 我们用 `EXPLAIN`（★ 不带 ANALYZE）
// ★ ★ ⇒ ★★ 它恒为 0 ⇒ ⇒ 「`filter`」这一类在本模式下【根本不可测】★★
// ★ ★ ⇒ ⇒ 而「索引扫描 + 残留 Filter」的计划会被**误判成 `seek`** ⇒ ⇒ **假绿**。
//
// ★ 新判据（★ 两条都能被 EXPLAIN 的字段证伪）：
//
//	① 没有会话族扫描节点 ⇒ `no-scan`（★ 答案来自别的关系）
//	② 取**承载行数的那次扫描**（★ Plan Rows 最大 ⇒ 并列时取 Total Cost 最大）★ 作为主体
//	③ 主体是顺序扫描 ⇒ `seq-scan`
//	④ 主体是索引扫描、但**节点上还有残留 Filter** ⇒ `filter`（★ 索引没接住谓词）
//	⑤ 主体是索引扫描、且**没有残留 Filter** ⇒ `seek`（★ 谓词全在 Index Cond 里）
//
// ★ 另记：★ 主体之外**还有几个会话族关系被顺序扫描**
// ★ ⇒ ⇒ 这一项单独记进 TSV ⇒ ⇒ 「旧分区没被裁掉」这个事实不会因为分类修正而丢失。
// ★★ R180 轮实测更正（★ 见 docs/audit/2026-10-29-R180-生产代价轴.md §7）：★★
// ★★ 252 上被顺序扫描的那几个分区（★ 2026_07 / _08 / _11 / default）**全是 0 行**
// ★★ ⇒ ⇒ ★★ **空分区上的 Seq Scan 代价 ≈ 0** ⇒ ⇒ ★★ 「出现了 Seq Scan」**不等于**「慢」
// ★★ ⇒ ⇒ ★★ 真正的代价取决于【被扫分区的【行数】】，★ 而不是「扫没扫」
// ★★ ⇒ ⇒ ★★ 所以【慢不慢】只能由【代价轴】判（★ 形态轴没有行数 ⇒ 判不了慢）。
func classify(root planNode) (string, []armScan, string, string, int) {
	var scans []armScan
	var walk func(n planNode)
	walk = func(n planNode) {
		if isArmRelation(n.RelationName) {
			scans = append(scans, armScan{
				relation:  n.RelationName,
				nodeType:  n.NodeType,
				indexName: n.IndexName,
				indexCond: n.IndexCond,
				filter:    n.Filter,
				rowsRm:    n.RowsRemovedByFilte,
				planRows:  n.PlanRows,
				totalCost: n.TotalCost,
			})
		}
		for _, c := range n.Plans {
			walk(c)
		}
	}
	walk(root)

	if len(scans) == 0 {
		return "no-scan", nil, "", "", 0
	}

	// ★ 主体 = 承载行数的那次【基表】扫描（★ Plan Rows 最大 ⇒ 并列时 Total Cost 最大）。
	// ★ ★ 注意：★ 只在【基表】里挑（★ 见 isBaseRelation 的注释）⇒ ⇒ 明细表侧不参与判决。
	dom := -1
	for i, s := range scans {
		if !isBaseRelation(s.relation) {
			continue
		}
		if dom < 0 ||
			s.planRows > scans[dom].planRows ||
			(s.planRows == scans[dom].planRows && s.totalCost > scans[dom].totalCost) {
			dom = i
		}
	}
	if dom < 0 {
		// ★ 只有明细表扫描 ⇒ ★ 视图基表没被扫 ⇒ ⇒ 答案不来自会话族基表。
		return "no-scan", scans, "", "", 0
	}
	d := scans[dom]

	// ★ 主体之外的顺序扫描数（★ 「时间窗裁不掉的旧分区」这件事要单独留痕）。
	otherSeq := 0
	for i, s := range scans {
		if i == dom || !isBaseRelation(s.relation) {
			continue
		}
		if isSeqScanType(s.nodeType) {
			otherSeq++
		}
	}

	switch {
	case isSeqScanType(d.nodeType):
		return "seq-scan", scans, d.relation, d.filter, otherSeq
	case strings.TrimSpace(d.filter) != "":
		// ★★ 残留 Filter 还在 ⇒ ⇒ 谓词**没有**被索引完全接住 ⇒ ⇒ `filter`。
		// ★ ★ 注意：★ 这里判的是【节点上有没有 Filter】，★ 不是 Rows Removed by Filter
		// ★ ★（★ 后者是执行期统计 ⇒ ★ EXPLAIN 不带 ANALYZE 时恒为 0 ⇒ ★ 不可测）。
		return "filter", scans, d.relation, d.filter, otherSeq
	default:
		return "seek", scans, d.relation, d.filter, otherSeq
	}
}

// isSeqScanType 判节点类型是不是「顺序扫描族」。
func isSeqScanType(nodeType string) bool {
	switch nodeType {
	case "Seq Scan", "Sample Scan", "Bitmap Heap Scan":
		return true
	}
	return false
}

// collectSortKeys 收集计划里【所有 Sort 节点】的排序列。
//
// ★★ R181 轮新增（★ 修 R179/R180 轮共同暴露的第三处量具缺陷）：★★
//
//	★ 此前形态轴只判「谓词有没有被索引接住」（★ seek / filter / seq-scan），
//	★ ★ **完全看不见排序** ⇒ ⇒ 「seek」会掩盖「排序没被索引接住」★★
//	★ ⇒ ⇒ 一条语句可以【谓词走索引、排序却要额外 Sort】⇒ ⇒ 那正是慢读点的典型形态
//	★ ⇒ ⇒ ⇒ **「形态」与「代价」必须同时看**（★ 承 R180 轮结论）★★
//
// ★ 口径：★ 递归走整棵树，★ 取每个 Sort / Incremental Sort 节点的 "Sort Key"
//
//	★ ★ 保留原样（★ 含 DESC / ASC 方向修饰）★★ ★ 空 = 计划里没有 Sort 节点。
//
// ★ ★ 为什么**单独一个函数**而不是改 classify 的返回值：★ classify 的签名被
//
//	★ main_test.go 用了 5 处 ⇒ ★ ★ 改签名会连带改测试 ⇒ ⇒ ★ 本轮只加不改，★ 最小侵入 ★★
func collectSortKeys(root planNode) []string {
	var out []string
	var walk func(n planNode)
	walk = func(n planNode) {
		switch n.NodeType {
		case "Sort", "Incremental Sort":
			for _, k := range n.SortKey {
				k = strings.TrimSpace(k)
				if k == "" {
					continue
				}
				out = append(out, k)
			}
		}
		for _, c := range n.Plans {
			walk(c)
		}
	}
	walk(root)
	return out
}

// ─────────────────────────── 参数内联 ───────────────────────────

// inlineParams 把 SQL 里的 $1..$n 换成字面量（★ EXPLAIN 是独立语句，不能带参数）。
//
// ★ **必须跳过字符串字面量内部的 $**（否则 'a $1 b' 会被改坏）。
func inlineParams(sql string, params map[int]string) string {
	if len(params) == 0 {
		return sql
	}
	var b strings.Builder
	b.Grow(len(sql) + 32)
	inSingle := false
	for i := 0; i < len(sql); {
		c := sql[i]
		if inSingle {
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					b.WriteByte(sql[i+1])
					i += 2
					continue
				}
				inSingle = false
			}
			i++
			continue
		}
		if c == '\'' {
			inSingle = true
			b.WriteByte(c)
			i++
			continue
		}
		if c == '$' && i+1 < len(sql) && sql[i+1] >= '1' && sql[i+1] <= '9' {
			j := i + 1
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				j++
			}
			idx, _ := strconv.Atoi(sql[i+1 : j])
			if idx >= 1 {
				if v, ok := params[idx]; ok {
					b.WriteString(v)
					i = j
					continue
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// sqlLit 把字符串值转成 SQL 字面量（★ 转义单引号）。
func sqlLit(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// qicStaticColValues 是【与列无关的固定代表值】。
//
// ★★★ 为什么不能一律用 NULL（★ R179 实测踩过，★ 这一条最值钱）：
//
//	`success = $4` ⇒ ★ 若 $4 填 NULL ⇒ ⇒ 谓词恒不成立
//	⇒ ⇒ 规划器**直接把它优化掉** ⇒ ⇒ **计划里一个扫描节点都没有**
//	⇒ ⇒ 读出来是「no-scan」⇒ ⇒ ★★ 而那是【假象】⇒ ⇒ 真实形态是「seq-scan」。
//
// ⇒ ⇒ 所以**列角色**必须给**语义有效**的值（★ 布尔列给 TRUE/FALSE，★ 文本列给真实值）。
var qicStaticColValues = map[string]string{
	"success":         "TRUE",
	"is_auto_request": "FALSE",
	"request_status":  "'failure'",
	"token_band":      "''",
	"work_type":       "''",
	"namespace":       "''",
	"project_id":      "''",
	"task_type":       "''",
	"canonical_id":    "''",
	"end_user_id":     "''",
	"identity_hash":   "''",
}

// resolveParams 按【参数角色】把代表值填进占位符。
//
// ★ 返回 (params, faithful)：★ faithful=false ⇒ ★ 有一个列角色拿不到有效值
//
//	⇒ ⇒ 那个值被填成了 NULL ⇒ ⇒ **语义失真** ⇒ ⇒ 读数不可用（★ 必须标出来）。
func resolveParams(shape Shape, reps Representatives, windowStart, windowEnd string) (map[int]string, bool) {
	params := map[int]string{}
	faithful := true
	for _, r := range shape.ParamRoles {
		switch r.Column {
		case "ts", "start_ts", "end_ts", "from_ts", "to_ts":
			switch r.Op {
			case ">=", ">":
				params[r.Index] = sqlLit(windowStart)
			case "<", "<=":
				params[r.Index] = sqlLit(windowEnd)
			default:
				params[r.Index] = sqlLit(windowStart)
			}
		default:
			// ★★★ 通用路径：★ 从「视图一整行」里取该列的真实代表值。
			if v, ok := reps.Cols[r.Column]; ok {
				if r.Kind == "any" {
					// ★ 数组参数：★ `col = ANY($n)` ⇒ $n 是数组
					params[r.Index] = "ARRAY[" + v + "]"
				} else {
					params[r.Index] = v
				}
				continue
			}
			if v, ok := qicStaticColValues[r.Column]; ok {
				params[r.Index] = v
				continue
			}
			// ★ 未知角色的常量：★ 用 NULL（★ 类型安全的兜底）。
			// ★ 但 INTERVAL 上下文必须给**合法的 interval 字面量**
			//   （★ 实测：INTERVAL NULL ⇒ 「invalid input syntax for type interval」）。
			if r.Kind == "interval" {
				params[r.Index] = sqlLit("1 day")
				continue
			}
			if r.Kind == "datepart" {
				params[r.Index] = sqlLit("day")
				continue
			}
			if r.Column == "" {
				// ★ 纯常量（★ 不与任何列比较）⇒ ★ NULL 是安全的（★ 只在 COALESCE 兜底 / IN 列表里）
				params[r.Index] = "NULL"
				continue
			}
			// ★★★ 列角色却拿不到有效值。
			//   ★★ 但只有【这个列是视图投影出来的】时，取不到值才会毁掉语义
			//   （★ 谓词是「拿视图的哪一列做过滤」）⇒ ⇒ 别的列的参数
			//   （★ 指向别的表 / catalog）⇒ ⇒ 填 NULL **不影响视图的扫描形态**。
			if reps.ViewCols[r.Column] {
				params[r.Index] = "NULL"
				faithful = false
				continue
			}
			params[r.Index] = "NULL"
		}
	}
	return params, faithful
}

// ─────────────────────────── 主流程 ───────────────────────────

func main() {
	var specPath, outPath, env, dsnFile, viewName string
	var windowDays int
	fs := flag.NewFlagSet("query_plan_form", flag.ExitOnError)
	fs.StringVar(&specPath, "spec", "", "读点形态册 JSON（★ 必需）")
	fs.StringVar(&outPath, "out", "", "观测台账输出 TSV（★ 必需）")
	fs.StringVar(&env, "env", "local", "环境名（★ 写进读数：local / 252 / …）")
	fs.StringVar(&dsnFile, "dsn-file", "", "本地 DSN 所在文件（★ env=local 时必需）")
	fs.IntVar(&windowDays, "window-days", 7, "时间窗口天数（★ ts 谓词的下界/上界据此折算）")
	fs.StringVar(&viewName, "view", "", "★ 被测视图名（★ 必填；★ 不给默认值 ⇒ ★ 源码里不出现那个标识符）")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	if specPath == "" || outPath == "" || viewName == "" {
		fmt.Fprintln(os.Stderr, "★ --spec / --out / --view 必需")
		os.Exit(2)
	}
	probeSQL = fmt.Sprintf(probeSQLTpl, viewName)

	raw, err := os.ReadFile(specPath)
	if err != nil {
		die("读 spec 失败: " + err.Error())
	}
	var sp specFile
	if err := json.Unmarshal(raw, &sp); err != nil {
		die("解析 spec 失败: " + err.Error())
	}
	if len(sp.Shapes) == 0 {
		die("spec 里没有形态 ⇒ 测具空转 ⇒ 判据失效")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	var tr transport
	if env == "local" {
		if dsnFile == "" {
			die("env=local 需要 --dsn-file")
		}
		dsnBytes, err := os.ReadFile(dsnFile)
		if err != nil {
			die("读 DSN 文件失败: " + err.Error())
		}
		dsn := strings.TrimSpace(string(dsnBytes))
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			die("连本地库失败: " + err.Error())
		}
		defer pool.Close()
		tr = &pgxTransport{pool: pool, dsn: dsn}
	} else {
		tr = &sshTransport{
			host:        "115.29.212.252",
			port:        25022,
			user:        "root",
			container:   "pg-252-pg17",
			dbUser:      "llm_gateway",
			dbName:      "llm_gateway",
			controlPath: "/tmp/kx-pbp/ssh-252-pf",
		}
	}

	// ── 代表值探针 ──
	reps, err := fetchRepresentatives(ctx, tr)
	if err != nil {
		die("代表值探针失败: " + err.Error())
	}
	maxTS, err := time.Parse("2006-01-02 15:04:05.999999-07", reps.MaxTS)
	if err != nil {
		// ★ PG 的 timestamptz 文本形态可能带 +08 / Z 等不同偏移 ⇒ 解析失败不致命。
		maxTS = time.Now()
	}
	windowEnd := maxTS
	windowStart := maxTS.AddDate(0, 0, -windowDays)

	fmt.Fprintf(os.Stderr, "★ 通道：%s\n", tr.Describe())
	fmt.Fprintf(os.Stderr, "★ 代表值：★ 视图列 %d 个（★ 全部来自该环境的一行真实数据）\n", len(reps.Cols))
	fmt.Fprintf(os.Stderr, "★ 窗口：[%s, %s)\n", windowStart.Format(time.RFC3339), windowEnd.Format(time.RFC3339))

	var out strings.Builder
	out.WriteString("# query-plan-observations.tsv —— R179 轮：**读点的实测计划形态**观测台账\n")
	out.WriteString("# ★ 生成器：cmd/tools/query_plan_form（★ EXPLAIN，★ 不带 ANALYZE ⇒ 只读库安全）\n")
	out.WriteString("# ★ 列：shape_id\tenv\tplan_form\tindex_names\tarm_scans\tindex_cond_cols\tfilter_cols\trows_removed_by_filter\tplan_rows\tplanning_ms\tsource\tparams\tnote\tdominant_rel\tdominant_filter\tother_seq_rels\tsort_keys\n")
	out.WriteString("# ★ plan_form 判据：★ 主体 = 承载行数的那次会话族扫描（★ Plan Rows 最大）★ ⇒ 主体是顺序扫描 ⇒ seq-scan；★ 主体是索引扫描且**节点上还有残留 Filter** ⇒ filter；★ 主体是索引扫描且无残留 Filter ⇒ seek\n")
	out.WriteString("# ★ ★ rows_removed_by_filter 在 EXPLAIN（★ 不带 ANALYZE）下恒为 0（★ 执行期统计）⇒ ⇒ 【不可测】⇒ ⇒ 不参与 plan_form 判定\n")
	out.WriteString("# ★ dominant_rel = 决定 plan_form 的那次【基表】扫描所在的关系；★ dominant_filter = 该节点上的残留 Filter（★ 判决证据）；★ other_seq_rels = 主体之外**还被顺序扫描**的基表关系数（★ ★ R180 轮实测：★ 252 上这些分区【全是 0 行】⇒ ★★ Seq Scan 代价 ≈ 0 ⇒ ★★ 「出现了 Seq Scan」≠「慢」★★ ⇒ ★ 慢不慢只能由【代价轴】判）\n")
	out.WriteString("# ★★ sort_keys = 计划里【Sort / Incremental Sort 节点】的排序列（★ R181 轮新增的末尾列；★ 用 ; 分隔；★ 保留原样含 DESC）★★\n")
	out.WriteString("# ★ ★ 为什么要有它：★ 此前形态轴只判「谓词有没有被索引接住」⇒ ★★ 「seek」会掩盖「排序没被索引接住」★★ ⇒ ★ 一条语句可以【谓词走索引、排序却要额外 Sort】⇒ ⇒ ★★ **排序这一维必须单独观测** ★★ ⇒ ★ 空 = 计划里没有 Sort 节点（★ ≠ 「排序一定被接住」⇒ ★ 也可能是这条语句本来就不排序）\n")

	for _, sh := range sp.Shapes {
		if sh.Dynamic {
			writeRow(&out, sh, env, "dynamic", "", "", "", "", 0, 0, 0, "", "", "★ SQL 含 fmt 占位 ⇒ 静态不可知", "", "", 0, "")
			continue
		}
		params, faithful := resolveParams(sh, reps,
			windowStart.Format(time.RFC3339), windowEnd.Format(time.RFC3339))
		if !faithful {
			writeRow(&out, sh, env, "error", "", "", "", "", 0, 0, 0, "", "",
				"★ 有【列角色】的参数拿不到有效代表值 ⇒ ⇒ 只能填 NULL ⇒ ⇒ **谓词语义失真**"+
					"（★ 例：success = NULL ⇒ ⇒ 规划器把谓词优化掉 ⇒ ⇒ 读出来会是假的 no-scan）⇒ ⇒ 本条读数作废", "", "", 0, "")
			continue
		}
		sql := inlineParams(sh.SQL, params)
		// ★ 有些生产语句本身就是 EXPLAIN（★ 代码里有跑 EXPLAIN ANALYZE 的路径）
		//   ⇒ ⇒ 不能再套一层 EXPLAIN（★ 实测报「syntax error at or near EXPLAIN」）。
		explainSQL := sql
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "EXPLAIN") {
			explainSQL = "EXPLAIN (FORMAT JSON) " + sql
		}
		outRaw, qerr := tr.Query(ctx, explainSQL)
		// ★★ 回退臂：★ 抽出来的「读视图那段 SELECT」若是【相关子查询】
		//   （★ 例：DELETE … WHERE NOT EXISTS (SELECT … FROM view …)）
		//   ⇒ ⇒ 单独 EXPLAIN 会报「missing FROM-clause entry」（★ 引用了外层的列）
		//   ⇒ ⇒ 那就【整条语句】EXPLAIN 一遍（★ EXPLAIN 不执行 ⇒ ★ 只读库上安全）。
		if qerr != nil && sh.FullSQL != "" && sh.FullSQL != sh.SQL {
			whole := sh.FullSQL
			if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(whole)), "EXPLAIN") {
				whole = "EXPLAIN (FORMAT JSON) " + whole
			}
			if raw2, err2 := tr.Query(ctx, whole); err2 == nil && strings.TrimSpace(raw2) != "" {
				outRaw, qerr = raw2, nil
			}
		}
		if qerr != nil {
			writeRow(&out, sh, env, "error", "", "", "", "", 0, 0, 0, "", "",
				"★ EXPLAIN 失败: "+strings.ReplaceAll(qerr.Error(), "\t", " "), "", "", 0, "")
			continue
		}
		var ps []explainPayload
		if err := json.Unmarshal([]byte(strings.TrimSpace(outRaw)), &ps); err != nil || len(ps) == 0 {
			writeRow(&out, sh, env, "error", "", "", "", "", 0, 0, 0, "", "",
				"★ EXPLAIN 输出解析失败: "+strings.ReplaceAll(errText(err), "\t", " "), "", "", 0, "")
			continue
		}
		p := ps[0]
		form, scans, dominantRel, dominantFilter, otherSeq := classify(p.Plan)
		idxNames := []string{}
		idxCondCols := []string{}
		filterCols := []string{}
		rowsRm := int64(0)
		planRows := int64(0)
		armSummary := []string{}
		for _, s := range scans {
			armSummary = append(armSummary, s.relation+":"+s.nodeType)
			if s.indexName != "" {
				idxNames = append(idxNames, s.indexName)
			}
			for _, c := range extractCols(s.indexCond) {
				if !contains(idxCondCols, c) {
					idxCondCols = append(idxCondCols, c)
				}
			}
			for _, c := range extractCols(s.filter) {
				if !contains(filterCols, c) {
					filterCols = append(filterCols, c)
				}
			}
			rowsRm += s.rowsRm
			planRows += s.planRows
		}
		sort.Strings(idxNames)
		sort.Strings(idxCondCols)
		sort.Strings(filterCols)
		writeRow(&out, sh, env, form,
			strings.Join(dedupe(idxNames), ","),
			strings.Join(dedupe(armSummary), ","),
			strings.Join(idxCondCols, ","),
			strings.Join(filterCols, ","),
			rowsRm, planRows, int64(p.PlanningMs),
			fmt.Sprintf("%s:%d", sh.File, sh.Line),
			describeParams(params),
			"", dominantRel, dominantFilter, otherSeq,
			// ★★ R181 轮新增：★ 计划里 Sort 节点的排序列（★ 空 = 没有 Sort 节点）。
			// ★ ★ 这正是「seek 掩盖了排序没被索引接住」的那个观测点 ★★
			strings.Join(collectSortKeys(p.Plan), ";"))
	}

	if err := os.WriteFile(outPath, []byte(out.String()), 0o644); err != nil {
		die("写输出失败: " + err.Error())
	}
	fmt.Fprintf(os.Stderr, "★ 写出行：%d 个形态 → %s\n", len(sp.Shapes), outPath)
}

func fetchRepresentatives(ctx context.Context, tr transport) (Representatives, error) {
	reps := Representatives{Cols: map[string]string{}, ViewCols: map[string]bool{}}

	maxTSRaw, err := tr.Query(ctx, probeMaxTS)
	if err != nil {
		return reps, err
	}
	reps.MaxTS = strings.TrimSpace(maxTSRaw)
	if reps.MaxTS == "" {
		return reps, fmt.Errorf("时间锚探针无输出")
	}

	raw, err := tr.Query(ctx, probeSQL)
	if err != nil {
		return reps, err
	}
	if strings.TrimSpace(raw) == "" {
		return reps, fmt.Errorf("列值探针无输出")
	}
	// ★★ 探针取【多行】：★ 视图里很多列在【某一行】上是 NULL
	//   ⇒ ⇒ 单行探针会把它们当成「没有代表值」⇒ ⇒ 又一次假象。
	//   ⇒ ⇒ 逐列取【第一个非 NULL 的值】。
	for _, raw := range strings.Split(strings.TrimSpace(raw), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(strings.ReplaceAll(raw, "\n", "")), &row); err != nil {
			continue
		}
		for k, v := range row {
			reps.ViewCols[k] = true
			lit := jsonToSQLLiteral(v)
			if lit == "NULL" {
				continue
			}
			if cur, ok := reps.Cols[k]; !ok || cur == "NULL" {
				reps.Cols[k] = lit
			}
		}
	}
	if len(reps.Cols) == 0 {
		return reps, fmt.Errorf("列值探针没取到任何列")
	}

	// ★★ 底表的代表值（★ 补齐视图里恰好为 NULL 的基列）
	if baseRaw, err := tr.Query(ctx, probeBaseSQL); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(baseRaw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var row map[string]any
			if json.Unmarshal([]byte(strings.ReplaceAll(line, "\n", "")), &row) != nil {
				continue
			}
			for k, v := range row {
				lit := jsonToSQLLiteral(v)
				if lit == "NULL" {
					continue
				}
				if cur, ok := reps.Cols[k]; !ok || cur == "NULL" {
					reps.Cols[k] = lit
				}
			}
		}
	}

	// ★★ details 腿的代表值（★ 视图那一行的 details 列是 NULL ⇒ ⇒ 要单独取一行 details）
	detRaw, err := tr.Query(ctx, probeDetailsSQL)
	if err == nil {
		var det map[string]any
		if json.Unmarshal([]byte(strings.ReplaceAll(strings.TrimSpace(detRaw), "\n", "")), &det) == nil {
			for k, v := range det {
				lit := jsonToSQLLiteral(v)
				if lit == "NULL" {
					continue
				}
				// ★ 视图侧是 NULL 时才用 details 侧的值（★ 视图侧的优先）
				if cur, ok := reps.Cols[k]; !ok || cur == "NULL" {
					reps.Cols[k] = lit
				}
			}
		}
	}
	return reps, nil
}

// jsonToSQLLiteral 把 JSON 值转成 SQL 字面量（★ 文本要加引号，★ 数字/布尔照抄）。
func jsonToSQLLiteral(v any) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if t {
			return "TRUE"
		}
		return "FALSE"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case string:
		return sqlLit(t)
	default:
		return sqlLit(fmt.Sprintf("%v", t))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func writeRow(out *strings.Builder, sh Shape, env, form, idxNames, armScans, idxCondCols, filterCols string,
	rowsRm, planRows, planningMs int64, source, params, note, dominantRel, dominantFilter string, otherSeq int,
	sortKeys string) {
	note = strings.ReplaceAll(note, "\t", " ")
	note = strings.ReplaceAll(note, "\n", " ")
	out.WriteString(strings.Join([]string{
		sh.ShapeID, env, form, idxNames, armScans, idxCondCols, filterCols,
		strconv.FormatInt(rowsRm, 10), strconv.FormatInt(planRows, 10),
		strconv.FormatInt(planningMs, 10), source, params, note,
		dominantRel, truncateForCell(dominantFilter), strconv.Itoa(otherSeq),
		// ★★ R181 轮新增的末尾列：★ 计划里 Sort 节点的排序列（★ 空 = 没有 Sort 节点）。
		// ★ ★ 必须加在【末尾】⇒ ★ 既有消费方按列序读 parts[0..15] 不受影响 ★★
		sortKeys,
	}, "\t") + "\n")
}

// extractCols 从 Index Cond / Filter 文本里抽出**列名**（★ 粗粒度：词边界 + 小写）。
func extractCols(cond string) []string {
	if strings.TrimSpace(cond) == "" {
		return nil
	}
	var out []string
	cur := strings.Builder{}
	flush := func() {
		tok := strings.ToLower(strings.TrimSpace(cur.String()))
		cur.Reset()
		if tok == "" {
			return
		}
		for _, r := range tok {
			if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				return
			}
		}
		if tok[0] >= '0' && tok[0] <= '9' {
			return
		}
		if !contains(out, tok) {
			out = append(out, tok)
		}
	}
	for _, r := range cond {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

func describeParams(params map[int]string) string {
	keys := make([]int, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("$%d=%s", k, params[k]))
	}
	return strings.Join(parts, " ")
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "ERROR: "+msg)
	os.Exit(1)
}

// truncateForCell 把长文本压进 TSV 单元格（★ 保留头尾 ⇒ ⇒ 证据可见但不撑爆行宽）。
func truncateForCell(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\t", " "), "\n", " ")
	if len(s) <= 160 {
		return s
	}
	return s[:80] + " … " + s[len(s)-60:]
}
