package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。

// query_index_coverage_gate_test.go —— R162 轮新增：**活查询 ⇒ 过滤/排序列 ⇒ 索引可用性**门。
//
// # 这道门补的是哪块缺口
//
// ★ 项目原始目标里有一条：「优化请求的列表、记录查询、写入过程的效率，
// ★ **不要有表扫描或慢 SQL** 的问题」。
//
// ★ R161/R162 两轮把「**数据库里真实存在哪些对象**」做成了机器可判的台账
//   （scripts/ursmcheck/db_object_census_gate_test.go + scripts/audit/asset/db-object-census.tsv）
// ★★ 但还缺一块：**「某条活的查询，它的过滤列/排序列有没有可用的索引」**
//   —— ★★ 这个判据**目前只存在于人的脑子里**（谁记得哪条列表查询慢？）。
//
// ★★ R162 轮的本地 EXPLAIN 实测（这就是缺口的证据）：
//
//	admin/turns_sessions.go:286 的
//	`ORDER BY s.updated_at DESC, s.session_id DESC`
//	⇒ 基线形态 = Index Scan + 【Sort】⇒ ★ 排序**未被索引覆盖**（Sort 是在内存里排的）
//	⇒ ★★★ 反事实对照实测（★ 禁 Seq Scan、★ 无候选索引）⇒ ★★ Sort **仍在**
//	  ⇒ ⇒ ★★ **「现有索引族没有一条能同时满足 tenant_id 过滤与这个排序」是【结构事实】**
//	  ⇒ ⇒ ★★ 与数据量无关 ⇒ ⇒ ★★ **这才是本门要判的那件事**（★ 而不是某个绝对计划形态）
//	⇒ ★★ 订正（★ R162 轮实测）：★ 「候选索引 ⇒ Sort 消失」在本地当前数据上【不成立】
//	  （★ 唯一有数据的分区退回 Seq Scan ⇒ 普通 Append 无法保序 ⇒ Sort 必然存在）
//	  ⇒ ⇒ 「Sort 消失」是【规划器选择】的结果，★ 不是【索引存在】的结果
//	⇒ ★ 候选索引的实验一律在事务内做并 ROLLBACK ⇒ ★ 零残留 ⇒ 底账里没有它
//
// ⇒ ⇒ ⇒ 本门把这条判据**从脑子搬进台账**：每条活查询登记它的过滤列/排序列，
// ⇒ ⇒ ⇒ 门每次运行**重算**「这些列有没有索引能同时覆盖过滤与排序」。
//
// # 两个事实源
//
//	① 底账：scripts/audit/asset/db-object-census.tsv（★ 17 列；★ 只有 env=local 参与判定）
//	② 清单：scripts/audit/asset/query-index-requirements.tsv（★ 7 列；★ 手工登记，★ 每行带源码定位）
//
// ★★ **只判 local**：本地是 CI 可跑的环境（★ 本门**不许连数据库** ⇒ 只读上面两个 TSV）。
//
// # 判定语义（★ 与「活查询 ⇒ 过滤/排序列 ⇒ 索引可用性」的物理事实对齐）
//
//	候选集：该表的【父级索引】（★ kind=idx ∧ parent_index 为空 ∧ parent_table == table）
//	       ★★ **部分索引（index_predicate 非空）一律排除** —— 只有查询带**同样谓词**
//	          才可用，而本门**不掌握查询谓词** ⇒ 保守排除（宁可判 gap，不谎称 covered）。
//	filter_ok：存在候选 ix，使 ix 的**前 len(filter_cols) 个列名**构成的**集合**
//	           == filter_cols 的集合（★ 都按小写比较；★ 是「前 N 列」不是「任意位置」
//	           ⇒ ★ btree 的**最左前缀**性质）。
//	order_ok ：存在候选 ix，使 ix 的列序列**恰好等于**
//	           【filter_cols 的某个排列】++ 【order_cols 去掉方向修饰后的序列】。
//	covered_by = filter_ok && order_ok。
//
// # ★★ 门的主要牙齿（★ 登记不是豁免）
//
//	★ covered_by == false ⇒ 该条**必须**登记为 known-gap，否则门红。
//	★ covered_by == true  ⇒ 登记成 covered 或 known-gap 都行（★ 登记保守不罚）。
//
// ★★ **绝对不要**因为「known-gap 已登记」就把门放松成永远绿：
// ★★ 登记项**只是被记账**，门**每次运行都重算**它到底覆没覆盖
// ★★ ⇒ 哪天索引补上了，那条登记就可以被删掉（★ 本门会把它 log 出来提示清理）。
//
// ★★ 另外：**清单为空 ⇒ 门红**（★ 读到 0 条需求 ⇒ 无未覆盖项 ⇒ 门恒绿 ⇒ 判据失效）。
// ★★ 这条与 scripts/ursmcheck/tsv_hollowout_inventory.tsv 的登记一致
// ★★ （★ 该清单已把本文件登记为 expect=red ⇒ 掏空本清单 ⇒ 本门必须红）。
//
// # ★ 诚实边界（★ 不声称本门测了它没测的东西）
//
//	① ★ 方向修饰（DESC/ASC）在 order 比较里**被忽略** ⇒ 本门判的是
//	   「有没有**列集合与顺序**对得上的索引」，不是「EXPLAIN 会不会走 Index Only Scan」。
//	② ★ INCLUDE 列按「去掉修饰、只留列名」处理 ⇒ 它**不是** btree 键列
//	   ⇒ 理论上不该参与最左前缀判定（★ 本轮清单里没有依赖 INCLUDE 的条目 ⇒ 无实际影响）。
//	③ ★ 表达式索引的列名可能是 `?column?` 之类的占位 ⇒ 参与比较但**不会**误命中真实列名。
//	④ ★ 本门**不**判断「索引是不是被规划器真的用上」（★ 那要 EXPLAIN，★ 本门不许连库）。

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	qicCensusRel = "scripts/audit/asset/db-object-census.tsv"
	qicReqRel    = "scripts/audit/asset/query-index-requirements.tsv"

	qicCensusCols = 17 // env + 16
	// ★★ R179 轮：台账扩到 11 列（★ 新增「实测计划形态」三列）。
	qicReqCols = 11 // query_id source table filter_cols order_cols status fresh_status plan_shape plan_form plan_env note

	// ★★ R179 轮新增的两个事实源：
	//
	// ★ qicObsRel：**实测计划形态观测台账**（★ 由 cmd/tools/query_plan_form 产出）
	//   ⇒ 它记录「每条活查询在目标环境里，★ 规划器实际走了哪条路」。
	// ★ qicExemptRel：**没有实测形态**的登记簿（★ 防止「没测」变成默认放行）。
	qicObsRel    = "scripts/audit/asset/query-plan-observations.tsv"
	qicExemptRel = "scripts/audit/asset/query-plan-exemptions.tsv"

	// ★★ plan_form 的取值域（★ 门会校验；★ 域外即红）。
	//
	//	seek      —— 每一处会话族扫描都是索引扫描，且没有行被扫描后过滤掉
	//	filter    —— 是索引扫描，但**有行被过滤掉**（★ 索引没接住那个谓词）
	//	seq-scan  —— 有一处会话族扫描是顺序扫描（★ 规划器没打算用索引）
	//	no-scan   —— 计划里没有会话族扫描（★ 答案来自别的关系，★ 例如 rollup 表）
	//	no-traffic—— ★ 生产上没跑到这条语句（★ ⇒ 必须有豁免登记）
	//	dynamic   —— SQL 含 fmt 占位（★ 静态不可知 ⇒ 必须先解析 helper）
	//	unmeasured—— ★ 没测（★ ⇒ 必须有豁免登记）
	//	error     —— ★ 实测本身失败了（★ ⇒ 必须有豁免登记）
	qicPlanFormSeek   = "seek"
	qicPlanFormFilter = "filter"
	qicPlanFormSeq    = "seq-scan"
	qicPlanFormNone   = "no-scan"
	qicPlanFormNoTrf  = "no-traffic"
	qicPlanFormDyn    = "dynamic"
	qicPlanFormUnmeas = "unmeasured"
	qicPlanFormError  = "error"

	// ★★ 「没有实测形态」的形态（★ 这几个必须挂豁免登记，★ 不许默认放行）。
	qicPlanFormNeedExempt = qicPlanFormNoTrf + "," + qicPlanFormUnmeas + "," + qicPlanFormError

	qicEnvLocal = "local"
	// ★★ qicEnvFresh = 「新装环境」：★ R163 轮用【安装器真实路径】（00/01/02 + 312 条 StartupFiles）
	// ★★ 在空库装出来的库 ⇒ ★★ 它是「多环境同一标准」的【新装侧】实测基准。
	qicEnvFresh = "fresh"

	// qicMaxPermFilter 是 filter_cols 全排列的**上限保护**。
	// ★ 清单很小（4 个元素以内）⇒ 全排列可枚举；★ 但一旦有人登记 9 个过滤列
	// ⇒ 9! = 362880 次比较 ⇒ 必须有上限 ⇒ 超限判 order 不成立并 log 出来。
	qicMaxPermFilter = 8

	// ★★ R179 轮：观测台账与豁免登记也各给一个「指向仓外副本」的开关
	//   ⇒ ★ 变异验证时**只改副本**（★ 不改编内文件 ⇒ ★ 门运行期零污染）。
	qicObsEnvOverride    = "URSM_QUERY_PLAN_OBSERVATIONS_TSV"
	qicExemptEnvOverride = "URSM_QUERY_PLAN_EXEMPTIONS_TSV"

	// qicReqEnvOverride 允许把**需求清单**指向仓外副本。
	// ★ 用途：**变异验证**（★ 证明门有牙齿时不能改仓内文件 ⇒ 改副本即可）。
	// ★ 默认行为**完全不变**：未设置该变量 ⇒ 读仓内原路径。
	qicReqEnvOverride = "URSM_QUERY_INDEX_REQUIREMENTS_TSV"
)

// ─────────────────────────── 读文件 ───────────────────────────

// qicRepoPath 按「仓库根」定位仓内文件（★ 沿用同目录 dbObjRepoPath 的做法，
// ★ 但**自己写一个等价的小函数** ⇒ 本文件不依赖那个文件的存活）。
func qicRepoPath(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join("..", "..", filepath.FromSlash(rel))
}

// qicReadFile 读整个文件（★ 刻意不叫 mustRead：仓内 scripts/ursmcheck 包里已有同名 helper）。
func qicReadFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// ─────────────────────────── 底账（census） ───────────────────────────

// qicIndex 是一条候选索引的结构化信息。
type qicIndex struct {
	name      string   // 索引名
	cols      []string // 结构化列序列（★ 方向/INCLUDE 修饰已剥掉；★ 小写）
	partial   bool     // ★ 是否部分索引（★ 部分索引一律不进候选集）
	predicate string   // 部分索引的谓词（★ 仅用于打印说明）
}

// qicStripModifiers 去掉一段列说明的尾部修饰（★ DESC / ASC / INCLUDE），只留列名。
// ★ INCLUDE 列不是 btree 键列（★ 本轮清单不依赖它 ⇒ 见文件头诚实边界 ②）。
func qicStripModifiers(seg string) string {
	s := strings.TrimSpace(seg)
	for _, m := range []string{" DESC", " ASC", " INCLUDE", " desc", " asc", " include"} {
		s = strings.TrimSuffix(s, m)
	}
	return strings.TrimSpace(s)
}

// qicParseIndexCols 把底账的 index_columns 解析成列序列。
// ★ 形如 `tenant_id,session_id,partition_date DESC`。
func qicParseIndexCols(raw string) []string {
	var out []string
	for _, seg := range strings.Split(raw, ",") {
		c := qicStripModifiers(seg)
		if c == "" {
			continue // ★ 空段跳过
		}
		out = append(out, strings.ToLower(c))
	}
	return out
}

// qicLoadLocalIndexes 从底账里取出 **env=local** 的全部**父级索引**（★ 按表分组）。
//
// ★★ 两个过滤条件：
//
//	① env == local      ⇒ 只有本地是 CI 可跑的环境
//	② parent_index 为空 ⇒ 排除**分区子索引**（★ 子索引的 parent_table 是分区名，
//	                          不是它所属的逻辑表 ⇒ 拿它当「该表的索引」是错的）
//
// ★★ 这里**不**排除部分索引 —— 排除发生在判定处（★ 打印失败详情时
// ★★ 还要把**被排除的**部分索引一起列出来 ⇒ 用户才知道门为什么这么判）。
func qicLoadEnvIndexes(t *testing.T, path string, env string) map[string][]qicIndex {
	t.Helper()
	out := map[string][]qicIndex{}
	lineno := 0
	for _, line := range strings.Split(qicReadFile(t, path), "\n") {
		lineno++
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != qicCensusCols {
			// ★ 坏行必被拒（★ 行式 TSV 被换行污染 ⇒ 这里必须硬失败，不是「跳过这行」）。
			t.Fatalf("%s:%d: census row has %d fields, expected %d",
				path, lineno, len(parts), qicCensusCols)
		}
		if parts[0] != env || parts[1] != "idx" {
			continue
		}
		if strings.TrimSpace(parts[14]) != "" {
			continue // ★ 分区子索引 ⇒ 排除
		}
		idx := qicIndex{
			name:      parts[4],
			cols:      qicParseIndexCols(parts[15]),
			partial:   strings.TrimSpace(parts[16]) != "",
			predicate: strings.TrimSpace(parts[16]),
		}
		out[parts[8]] = append(out[parts[8]], idx)
	}
	return out
}

// ─────────────────────────── 清单（requirements） ───────────────────────────

// qicRequirement 是一条登记的需求。
type qicRequirement struct {
	queryID    string
	source     string
	table      string
	filterCols []string
	orderCols  []string
	status     string
	// ★★ freshStatus：★ 「新装环境」侧的状态（★ R163 轮新增）
	// ★★ ★★ 为什么要有这一列：★ local 覆盖 ≠ 新装覆盖 ⇒ ★★ 「local 上有索引、新装上没有」
	// ★★ ⇒ ★★ 新装环境【必然】全表扫描 ⇒ ★★ 这是「多环境同一标准」的真实缺口
	// ★★ ⇒ ★★ 强制它被【显式登记】，★ 不许靠 local 侧的 covered 蒙混过去。
	freshStatus string
	// ★★ R179 轮：实测计划形态三列。
	//
	// ★ planShape：★ 实测形态的**键**（★ 与观测台账的 shape_id 对应）。
	// ★ planForm ：★ 实测出来的形态（★ 取值域见 qicPlanForm* 常量）。
	// ★ planEnv  ：★ 在哪个环境实测的（★ local / 252 / …）。
	planShape string
	planForm  string
	planEnv   string
	note      string
	line      int
}

// qicParseColList 解析逗号分隔的列清单（★ 可为空串 ⇒ 空切片）。
// ★ 方向修饰在这里一并剥掉（★ 比较只看列名与顺序）。
func qicParseColList(raw string) []string {
	var out []string
	for _, seg := range strings.Split(raw, ",") {
		c := qicStripModifiers(seg)
		if c == "" {
			continue
		}
		out = append(out, strings.ToLower(c))
	}
	return out
}

// qicLoadRequirements 读需求清单。
func qicLoadRequirements(t *testing.T, path string) []qicRequirement {
	t.Helper()
	var out []qicRequirement
	lineno := 0
	for _, line := range strings.Split(qicReadFile(t, path), "\n") {
		lineno++
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != qicReqCols {
			t.Fatalf("%s:%d: requirement row has %d fields, expected %d: %q",
				path, lineno, len(parts), qicReqCols, line)
		}
		out = append(out, qicRequirement{
			queryID:     strings.TrimSpace(parts[0]),
			source:      strings.TrimSpace(parts[1]),
			table:       strings.TrimSpace(parts[2]),
			filterCols:  qicParseColList(parts[3]),
			orderCols:   qicParseColList(parts[4]),
			status:      strings.TrimSpace(parts[5]),
			freshStatus: strings.TrimSpace(parts[6]),
			planShape:   strings.TrimSpace(parts[7]),
			planForm:    strings.TrimSpace(parts[8]),
			planEnv:     strings.TrimSpace(parts[9]),
			note:        strings.TrimSpace(parts[10]),
			line:        lineno,
		})
	}
	return out
}

// ─────────────────────────── 判定 ───────────────────────────

// qicPermutations 返回列序列的全排列（★ 去重 ⇒ 同名列不产生重复排列）。
func qicPermutations(cols []string) [][]string {
	if len(cols) == 0 {
		return [][]string{{}}
	}
	seen := map[string]bool{}
	var out [][]string
	var walk func(cur []string, rest []string)
	walk = func(cur []string, rest []string) {
		if len(rest) == 0 {
			key := strings.Join(cur, "\x00")
			if seen[key] {
				return
			}
			seen[key] = true
			out = append(out, append([]string(nil), cur...))
			return
		}
		for i := range rest {
			next := make([]string, 0, len(cur)+1)
			next = append(next, cur...)
			next = append(next, rest[i])
			newRest := make([]string, 0, len(rest)-1)
			newRest = append(newRest, rest[:i]...)
			newRest = append(newRest, rest[i+1:]...)
			walk(next, newRest)
		}
	}
	walk(nil, cols)
	return out
}

// qicSameSeq 判断两个列序列是否**逐项相等**（★ 长度也要相等）。
func qicSameSeq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// qicCandidates 返回该表的**候选集**：父级索引 ∧ 非部分索引。
// ★★ 部分索引一律排除（★ 文件头「候选集」一节）。
func qicCandidates(all []qicIndex) []qicIndex {
	var out []qicIndex
	for _, ix := range all {
		if ix.partial {
			continue
		}
		out = append(out, ix)
	}
	return out
}

// qicJudge 返回 (filter_ok, order_ok, 说明)。
func qicJudge(req qicRequirement, cands []qicIndex) (bool, bool, string) {
	// ── filter_ok ──
	filterOK := false
	if len(req.filterCols) == 0 {
		// ★ 过滤列为空 ⇒ 无过滤条件要覆盖 ⇒ 视为成立（★ 纯排序类查询的常见形态）。
		filterOK = true
	} else {
		want := map[string]bool{}
		for _, c := range req.filterCols {
			want[c] = true
		}
		for _, ix := range cands {
			if len(ix.cols) < len(req.filterCols) {
				continue
			}
			got := map[string]bool{}
			for _, c := range ix.cols[:len(req.filterCols)] {
				got[c] = true
			}
			if len(got) != len(want) {
				continue
			}
			same := true
			for c := range want {
				if !got[c] {
					same = false
					break
				}
			}
			if same {
				filterOK = true
				break
			}
		}
	}

	// ── order_ok ──
	orderOK := false
	switch {
	case len(req.orderCols) == 0:
		// ★ 没有排序列 ⇒ 不需要排序索引 ⇒ 视为成立。
		orderOK = true
	case len(req.filterCols) > qicMaxPermFilter:
		// ★ 上限保护：过滤列过多 ⇒ 不枚举排列（★ 否则 9! 次比较）⇒ 判不成立并说明。
		return filterOK, false, "过滤列超过上限 " + qicItoa(qicMaxPermFilter) +
			" ⇒ 不枚举排列 ⇒ 判排序未被覆盖"
	default:
		perms := qicPermutations(req.filterCols)
		for _, ix := range cands {
			for _, p := range perms {
				target := make([]string, 0, len(p)+len(req.orderCols))
				target = append(target, p...)
				target = append(target, req.orderCols...)
				if qicSameSeq(ix.cols, target) {
					orderOK = true
					break
				}
			}
			if orderOK {
				break
			}
		}
	}
	return filterOK, orderOK, ""
}

// qicDescribeIndexes 把某表的父级索引渲染成可读的一行行说明。
// ★★ **包括被排除的部分索引** ⇒ 用户才知道「为什么这些索引没被算上」。
func qicDescribeIndexes(all []qicIndex) string {
	var b strings.Builder
	sorted := append([]qicIndex(nil), all...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	if len(sorted) == 0 {
		b.WriteString("      （该表在 local 底账里没有任何父级索引）\n")
		return b.String()
	}
	for _, ix := range sorted {
		b.WriteString("      - ")
		b.WriteString(ix.name)
		b.WriteString(" cols=[")
		b.WriteString(strings.Join(ix.cols, ","))
		b.WriteString("]")
		if ix.partial {
			b.WriteString(" ★部分索引（谓词非空）⇒ 保守排除")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// qicItoa 把非负整数转成字符串（★ 刻意不叫 itoa：仓内 scripts/ursmcheck 包里已有同名 helper）。
func qicItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ─────────────────────── ★★ R179 轮：实测形态观测台账 ───────────────────────

// qicObservation 是一条**实测**记录（★ 某个读点形态在某个环境里的计划形态）。
type qicObservation struct {
	shapeID string
	env     string
	form    string
	idxName string
	line    int
}

// qicLoadObservations 读实测台账（★ 键 = (shape_id, env)）。
func qicLoadObservations(t *testing.T, path string) map[string]qicObservation {
	t.Helper()
	out := map[string]qicObservation{}
	lineno := 0
	for _, line := range strings.Split(qicReadFile(t, path), "\n") {
		lineno++
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			t.Fatalf("%s:%d: observation row has %d fields, expected >= 3: %q", path, lineno, len(parts), line)
		}
		o := qicObservation{
			shapeID: strings.TrimSpace(parts[0]),
			env:     strings.TrimSpace(parts[1]),
			form:    strings.TrimSpace(parts[2]),
			line:    lineno,
		}
		if len(parts) > 3 {
			o.idxName = strings.TrimSpace(parts[3])
		}
		out[o.shapeID+"@"+o.env] = o
	}
	return out
}

// qicLoadExemptions 读豁免登记（★ 键 = query_id ⇒ 理由）。
func qicLoadExemptions(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	lineno := 0
	for _, line := range strings.Split(qicReadFile(t, path), "\n") {
		lineno++
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			t.Fatalf("%s:%d: exemption row has %d fields, expected >= 2: %q", path, lineno, len(parts), line)
		}
		qid := strings.TrimSpace(parts[0])
		reason := strings.TrimSpace(parts[1])
		if reason == "" {
			t.Fatalf("%s:%d: 豁免登记 %q 的理由是空的 ⇒ ⇒ 空理由的登记等于没有登记", path, lineno, qid)
		}
		out[qid] = reason
	}
	return out
}

// qicLoadViewNames 从底账里取出**视图名集合**（★ env=local）。
//
// ★★ 为什么需要它：★「静态覆盖轴」对**视图**结构性不适用
//
//	（★ 视图自己没有索引 ⇒ ⇒ 索引在底表上 ⇒ ⇒ 静态轴答不了「覆盖没覆盖」）
//	⇒ ⇒ 所以 status 才引入了第三态 `measured-only`
//	⇒ ⇒ ★ 而这一态**只**允许用于视图行 ⇒ ⇒ 否则它就是个万能逃生舱 ⇒ ⇒ 必须守住。
func qicLoadViewNames(t *testing.T, path string, env string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, line := range strings.Split(qicReadFile(t, path), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != qicCensusCols {
			t.Fatalf("%s: census row has %d fields, expected %d", path, len(parts), qicCensusCols)
		}
		// ★★ census 里**视图的 kind 是 rel**（★ kind 列是「对象类别」：idx/rel/ext/srv）
		//   ★★ 而 relkind 列（parts[3]）才是 'r'/'p'/'v'/'m'/'S'/'f'
		//   ⇒ ⇒ 判视图要判 **relkind == "v"**（★ 实测踩过：★ 判 kind=="view" ⇒ 一条都读不到）
		if parts[0] == env && parts[1] == "rel" && parts[3] == "v" {
			out[parts[4]] = true
		}
	}
	return out
}

// qicPlanFormKnown 判断 plan_form 是否在取值域内。
func qicPlanFormKnown(form string) bool {
	switch form {
	case qicPlanFormSeek, qicPlanFormFilter, qicPlanFormSeq, qicPlanFormNone,
		qicPlanFormNoTrf, qicPlanFormDyn, qicPlanFormUnmeas, qicPlanFormError:
		return true
	}
	return false
}

// qicPlanFormNeedsExempt 判断该形态是否**必须**挂豁免登记。
func qicPlanFormNeedsExempt(form string) bool {
	return form == qicPlanFormNoTrf || form == qicPlanFormUnmeas || form == qicPlanFormError
}

// ─────────────────────────── 门 ───────────────────────────

// TestQueryIndexCoverageRequirements 是本门的主体。
//
// ★ 判据：
//
//	① 清单非空（★ 空清单 ⇒ 门恒绿 ⇒ 判据失效 ⇒ 红）
//	② 每条登记的 status 只能是 covered / known-gap
//	③ covered_by == false ⇒ **必须**登记为 known-gap（★ 主要牙齿）
//	④ covered_by == true  ⇒ 登记成哪种都行（★ 登记保守不罚）
//	★★ 门**每次运行都重算**（★ 登记只是被记账 ⇒ 索引补上后那条就能删）。
func TestQueryIndexCoverageRequirements(t *testing.T) {
	censusPath := qicRepoPath(t, qicCensusRel)

	// ★ 清单路径：默认仓内原路径；★ 变异验证时可用环境变量指向副本（★ 默认行为不变）。
	reqPath := qicRepoPath(t, qicReqRel)
	if override := strings.TrimSpace(os.Getenv(qicReqEnvOverride)); override != "" {
		reqPath = override
		t.Logf("★ 需求清单被环境变量 %s 指向 %s（★ 变异验证用法）", qicReqEnvOverride, reqPath)
	}

	// ★★ 两个环境都要判：★ local（★ 开发/验证环境）＋ fresh（★ **新装环境**）
	localIdx := qicLoadEnvIndexes(t, censusPath, qicEnvLocal)
	freshIdx := qicLoadEnvIndexes(t, censusPath, qicEnvFresh)
	reqs := qicLoadRequirements(t, reqPath)

	// ★★ R179 轮：实测形态观测台账 + 豁免登记 + 视图名集合。
	obsPath := qicRepoPath(t, qicObsRel)
	if override := strings.TrimSpace(os.Getenv(qicObsEnvOverride)); override != "" {
		obsPath = override
		t.Logf("★ 实测形态台账被环境变量 %s 指向 %s（★ 变异验证用法）", qicObsEnvOverride, obsPath)
	}
	obs := qicLoadObservations(t, obsPath)
	exemptPath := qicRepoPath(t, qicExemptRel)
	if override := strings.TrimSpace(os.Getenv(qicExemptEnvOverride)); override != "" {
		exemptPath = override
		t.Logf("★ 豁免登记被环境变量 %s 指向 %s（★ 变异验证用法）", qicExemptEnvOverride, exemptPath)
	}
	exempt := qicLoadExemptions(t, exemptPath)
	viewNames := qicLoadViewNames(t, censusPath, qicEnvLocal)

	// ★★ 观测台账被掏空 ⇒ 「实测轴」整条失效 ⇒ ⇒ 门红（★ 与「清单为空 ⇒ 门恒绿」同源）。
	if len(obs) == 0 {
		t.Fatalf("★ 实测形态台账 %s 没有数据行 ⇒ ⇒ 本门读到的「实测轴」全是空的 ⇒ ⇒ "+
			"★ 门会恒绿 ⇒ ⇒ 判据失效 ⇒ ⇒ 门红", obsPath)
	}

	// ① 底账里必须真的有这两个环境的父级索引行（★ 否则「没有索引 ⇒ 没覆盖」会变成恒真）。
	localTotal := 0
	for _, v := range localIdx {
		localTotal += len(v)
	}
	if localTotal == 0 {
		t.Fatalf("底账 %s 里 env=%s 的父级索引行数为 0 ⇒ 判定恒不成立 ⇒ 门会误报 ⇒ 判据失效",
			censusPath, qicEnvLocal)
	}
	freshTotal := 0
	for _, v := range freshIdx {
		freshTotal += len(v)
	}
	if freshTotal == 0 {
		t.Fatalf("底账 %s 里 env=%s 的父级索引行数为 0 ⇒ 新装侧判定恒不成立 ⇒ 门会误报 ⇒ 判据失效",
			censusPath, qicEnvFresh)
	}

	// ① 清单为空 ⇒ 门恒绿 ⇒ 判据失效 ⇒ 红。
	// ★ 这条与 scripts/ursmcheck/tsv_hollowout_inventory.tsv 的登记一致
	// ★（★ 该清单已把本门登记为 expect=red ⇒ 掏空需求清单 ⇒ 本门必须红）。
	if len(reqs) == 0 {
		t.Fatalf("需求清单 %s 没有数据行 ⇒ 本门读到 0 条需求 ⇒ 无未覆盖项 ⇒ 门恒绿 ⇒ 判据失效",
			reqPath)
	}

	localCovered, localGap := 0, 0
	freshCovered, freshGap := 0, 0
	newInstallGaps := 0

	for _, req := range reqs {
		// ② status 取值域（★ 两个环境各一列；★ R179 轮引入第三态 measured-only）。
		if req.status != "covered" && req.status != "known-gap" && req.status != "measured-only" {
			t.Errorf("%s:%d: query_id=%q 的 status=%q 非法（★ 只能是 covered / known-gap / measured-only）",
				reqPath, req.line, req.queryID, req.status)
			continue
		}
		if req.freshStatus != "covered" && req.freshStatus != "known-gap" && req.freshStatus != "measured-only" {
			t.Errorf("%s:%d: query_id=%q 的 fresh_status=%q 非法（★ 只能是 covered / known-gap / measured-only）",
				reqPath, req.line, req.queryID, req.freshStatus)
			continue
		}
		// ★★★ ②′ 第三态的**准入闸**（★ 不守住它，measured-only 就是万能逃生舱）：
		//   「静态覆盖轴」对**视图**结构性不适用（★ 视图自己没有索引）
		//   ⇒ ⇒ measured-only **只**允许用于「底账里确实是视图」的那张表。
		if req.status == "measured-only" && !viewNames[req.table] {
			t.Errorf("%s:%d: query_id=%q 的 status=measured-only，★ 但表 %q 在 %s 的底账里【不是视图】"+
				"⇒ ⇒ 「静态轴结构性不适用」这个理由【不成立】⇒ ⇒ 门红（★ 底表读点必须老老实实登记 covered / known-gap）",
				reqPath, req.line, req.queryID, req.table, qicCensusRel)
		}
		if req.freshStatus == "measured-only" && !viewNames[req.table] {
			t.Errorf("%s:%d: query_id=%q 的 fresh_status=measured-only，★ 但表 %q 在 %s 的底账里【不是视图】"+
				"⇒ ⇒ 门红（★ 同上）", reqPath, req.line, req.queryID, req.table, qicCensusRel)
		}

		// ── local 侧 ──
		localAll := localIdx[req.table]
		localCands := qicCandidates(localAll)
		lFilter, lOrder, lNote := qicJudge(req, localCands)
		localCoveredBy := lFilter && lOrder

		if localCoveredBy {
			localCovered++
			if req.status == "known-gap" {
				t.Logf("★ 登记可清理（local）：query_id=%q table=%s 现已被索引覆盖（filter_ok=%v order_ok=%v）⇒ 登记项可以删掉或改成 covered",
					req.queryID, req.table, lFilter, lOrder)
			}
		} else {
			localGap++
			// ③ ★★ 主要牙齿：没被覆盖却登记成 covered ⇒ 门红（★ 并打印全部上下文）。
			if req.status == "covered" {
				extra := ""
				if lNote != "" {
					extra = "  (" + lNote + ")\n"
				}
				t.Errorf("★ [local] 未覆盖却登记成 covered：query_id=%q table=%q filter_cols=[%s] order_cols=[%s] "+
					"（★ 来源 %s）\n   filter_ok=%v order_ok=%v%s   该表的父级索引：\n%s",
					req.queryID, req.table, strings.Join(req.filterCols, ","),
					strings.Join(req.orderCols, ","), req.source, lFilter, lOrder,
					extra, qicDescribeIndexes(localAll))
			}
		}

		// ── ★★ fresh（新装环境）侧 ── ★★ 这是「多环境同一标准」的新装侧判据
		freshAll := freshIdx[req.table]
		freshCands := qicCandidates(freshAll)
		fFilter, fOrder, fNote := qicJudge(req, freshCands)
		freshCoveredBy := fFilter && fOrder

		if freshCoveredBy {
			freshCovered++
			if req.freshStatus == "known-gap" {
				t.Logf("★ 登记可清理（fresh）：query_id=%q table=%s 现已被索引覆盖（filter_ok=%v order_ok=%v）",
					req.queryID, req.table, fFilter, fOrder)
			}
		} else {
			freshGap++
			if req.freshStatus == "covered" {
				extra := ""
				if fNote != "" {
					extra = "  (" + fNote + ")\n"
				}
				t.Errorf("★ [fresh/新装环境] 未覆盖却登记成 covered：query_id=%q table=%q filter_cols=[%s] order_cols=[%s] "+
					"（★ 来源 %s）\n   filter_ok=%v order_ok=%v%s   该表的父级索引：\n%s",
					req.queryID, req.table, strings.Join(req.filterCols, ","),
					strings.Join(req.orderCols, ","), req.source, fFilter, fOrder,
					extra, qicDescribeIndexes(freshAll))
			}
			// ★★★ ★★ local 覆盖、但 fresh 不覆盖 ⇒ ★★ **新装环境【必然】全表扫描**
			if localCoveredBy {
				newInstallGaps++
				t.Logf("★★★ [新装缺口] query_id=%q（★ 来源 %s）：★ local 上有索引可用，★ ★★ 但【新装环境】没有 ⇒ "+
					"★★ 任何按安装器路径装出来的新环境，这条查询【必然】全表扫描（★ 已登记为 fresh known-gap）",
					req.queryID, req.source)
			}
		}
	}

	// ★★★ R179 轮：**动态轴**（★ 实测计划形态）的牙齿 ★★★
	//
	// ★★ 这一段要抓的是**静态轴抓不到**的那一类：
	//   「静态说【有索引能覆盖】，★ 而规划器实际【没走索引】」
	//   ⇒ ★★ 也就是「**索引在，却静默退回 Filter / Seq Scan**」（★ R178 的 CASE 表达式正是这一类）。
	seekCount, filterCount, seqCount := 0, 0, 0
	for _, req := range reqs {
		// ⑤ plan_form 取值域
		if !qicPlanFormKnown(req.planForm) {
			t.Errorf("%s:%d: query_id=%q 的 plan_form=%q 不在取值域内（★ 合法值：%s）",
				reqPath, req.line, req.queryID, req.planForm,
				"seek / filter / seq-scan / no-scan / no-traffic / dynamic / unmeasured / error")
			continue
		}
		// ⑥ ★★★ **登记 covered 者必须实测走索引** ★★★（★ 本轮要的那颗牙）
		//    ★ 反过来说：★ 静态轴说「有索引覆盖」，★ 实测却是 filter / seq-scan
		//    ⇒ ⇒ 门红 ⇒ ⇒ **「索引在却静默退回」被抓出来**。
		//
		// ★★ 口径（★ R179 轮自纠）：★ **「没测」与「测了但不是 seek」必须分开判** ——
		//   ★ 「没测」（unmeasured / no-traffic / error）⇒ ⇒ 走【豁免机制】（★ 判据⑧）
		//     ⇒ ⇒ 那里必须写清「为什么测不出来」；
		//   ★ 「测了」⇒ ⇒ 才是这里的硬判据。
		//   ★ ⇒ 否则「一条从没被实测过的旧登记」会把门直接顶红，★ 而它并没有违反任何承诺。
		measured := req.planForm != qicPlanFormUnmeas && req.planForm != qicPlanFormNoTrf && req.planForm != qicPlanFormError
		switch req.status {
		case "covered":
			if measured && req.planForm != qicPlanFormSeek {
				t.Errorf("★ [实测轴] query_id=%q（★ 来源 %s）登记成 covered（★ 静态轴说【有索引覆盖】），"+
					"★ 但实测形态是 %q（★ 在 %s 环境实测）⇒ ⇒ **索引在却没被用上** ⇒ ⇒ 门红"+
					"（★ 要么改成 known-gap，★ 要么把索引修到真能用）",
					req.queryID, req.source, req.planForm, req.planEnv)
			}
		case "known-gap":
			if measured && req.planForm == qicPlanFormSeek {
				t.Errorf("★ [实测轴] query_id=%q（★ 来源 %s）登记成 known-gap，★ 但实测形态是 seek"+
					"（★ 在 %s 环境实测）⇒ ⇒ **登记已经过期**（★ 索引补上了）⇒ ⇒ 门红（★ 请把登记改成 covered）",
					req.queryID, req.source, req.planEnv)
			}
		}

		// ⑦ ★ 台账与观测台账**必须对得上**（★ 台账写的形态 = 观测台账记的形态）
		if req.planShape != "" {
			o, ok := obs[req.planShape+"@"+req.planEnv]
			if !ok {
				t.Errorf("★ [实测轴] query_id=%q 的 plan_shape=%q + plan_env=%q 在 %s 里【没有对应记录】"+
					"⇒ ⇒ 台账声称「实测过」但观测台账里没有 ⇒ ⇒ 门红（★ 登记与事实不符）",
					req.queryID, req.planShape, req.planEnv, qicObsRel)
			} else if o.form != req.planForm {
				t.Errorf("★ [实测轴] query_id=%q 登记的 plan_form=%q，★ 但观测台账记的是 %q"+
					"（★ %s:%d）⇒ ⇒ 两者必须一致 ⇒ ⇒ 门红",
					req.queryID, req.planForm, o.form, qicObsRel, o.line)
			}
		}

		// ⑧ ★ 「没有实测形态」必须挂豁免登记（★ 不许默认放行）
		if qicPlanFormNeedsExempt(req.planForm) {
			if _, ok := exempt[req.queryID]; !ok {
				t.Errorf("★ [实测轴] query_id=%q（★ 来源 %s）的 plan_form=%q（★ 没有实测形态）"+
					"⇒ ⇒ 但 %s 里【没有它的豁免登记】⇒ ⇒ 门红（★ 测不出来就要写清为什么）",
					req.queryID, req.source, req.planForm, qicExemptRel)
			}
		}

		switch req.planForm {
		case qicPlanFormSeek:
			seekCount++
		case qicPlanFormFilter:
			filterCount++
		case qicPlanFormSeq:
			seqCount++
		}
	}

	t.Logf("需求清单 %s：共 %d 条 ⇒ local（covered %d / 未覆盖 %d）｜ fresh 新装环境（covered %d / 未覆盖 %d）｜ "+
		"★ 其中「local 覆盖但新装不覆盖」= %d 条 ⇒ 读底账 %s",
		reqPath, len(reqs), localCovered, localGap, freshCovered, freshGap, newInstallGaps, censusPath)
	t.Logf("★ 实测轴（★ 读 %s，★ %d 条观测）：seek %d / filter %d / seq-scan %d ⇒ ★ 读 %s",
		obsPath, len(obs), seekCount, filterCount, seqCount, exemptPath)
}
