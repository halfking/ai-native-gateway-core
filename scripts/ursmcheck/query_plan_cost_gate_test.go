package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。

// query_plan_cost_gate_test.go —— R180 轮：**生产代价轴**门。
//
// # 这道门补的是哪块缺口
//
// ★ 项目原始目标里有一条：「优化请求的列表、记录查询、写入过程的效率，
// ★ **不要有表扫描或慢 SQL 的问题**」。
//
// ★★ R179 轮建的「实测计划形态」轴答的是：**规划器实际走了哪条路**（★ seek / filter / seq-scan / no-scan）。
// ★★★ 而 R180 轮在 252 上实测出来的读数是：★★
//
//	形态 = seek（★ 真走索引）的 6 条 ⇒ 累计只有 **52 秒**（★ 占全部累计代价的 0.37%）
//	形态 = error（★ **测不出来**）的 72 条 ⇒ 累计 **10,987 秒（3.05 小时）**（★ 占 79%）
//	累计代价 Top 1 那条（★ 145 分钟 / 21240 次）恰恰落在 error 里
//
// ★★ ⇒ ⇒ **「形态」与「代价」几乎正交** ★★ ⇒ ⇒ **形态轴看不见最贵的那批读点**
// ⇒ ⇒ ⇒ **必须有第二条轴：★ 生产代价轴**（★ 数据在 pg_stat_statements 里，★ R179 采了却没进台账）。
//
// # 三个事实源
//
//	① 事实源：scripts/audit/asset/canonical-view-statements.tsv
//	   （★ 252 的 pg_stat_statements ⇒ calls / total_exec_time_ms / mean_exec_time_ms / rows / query）
//	② 代价台账：scripts/audit/asset/query-plan-cost-ledger.tsv
//	   （★ 由 scripts/audit/build-query-plan-cost-ledger.py 生成 ⇒ ★ 8 列）
//	③ 代价豁免登记：scripts/audit/asset/query-plan-cost-exemptions.tsv（★ 3 列：★ shape_id / category / reason）
//
// # 判据
//
//	① 代价台账非空（★ 空 ⇒ 门恒绿 ⇒ 判据失效 ⇒ 红）
//	② 台账里的读数必须与【事实源】逐字相等（★ ★ 读数不许手抄 ⇒ ★ 门自己就是那份对拍）
//	③ 台账必须【覆盖】事实源里的每一条语句（★ 反向覆盖 ⇒ 不许只登记便宜的）
//	④ 台账的 plan_form 必须与【实测形态观测台账】一致（★ 登记与事实相符）
//	⑤ ★★ **超阈值必须挂【代价豁免】登记**（★ category=slow，★ 写清为什么慢）★★
//	   —— ★ 单次慢：mean_ms >= 2000（★ **2 秒 = 用户原话**）
//	   —— ★ 累计慢：total_ms >= 300000（★ 累计 5 分钟；★ 抓【高频但单次不慢】的那批）
//	⑥ ★ 门**自己重算**阈值 ⇒ 必须与台账的 over_threshold 一致（★ 防「生成器把超阈的标成不超阈」）
//	⑦ ★ kind=non-app（★ 工具自己造成的慢）⇒ 必须有 category=non-app 的登记
//	   （★ ★ 这是【唯一的逃生舱】⇒ ★ 而它要求必须留下一条会被复核的理由 ⇒ ★ 逃不掉，只是要付账）
//	⑧ ★ 豁免登记里的每条都必须指向事实源里真实存在的语句（★ 防登记不存在的语句）
//
// # ★★ 诚实边界（★ 不声称本门测了它没测的东西）★★
//
//	① 本门只判【登记的完备性】与【登记与事实相符】⇒ ★ 它**不能**证明「这条读点不慢」，
//	   ★ 它只能保证「慢的那批都被登记了，且登记的理由是真的」⇒ ⇒ **发现问题的动作在人**。
//	② 阈值是【绝对值】而非分位（★ 分位会随数据漂移 ⇒ ⇒ 阈值就失去了判据的意义）。
//	③ 事实源是【采样窗口】里的统计（★ pg_stat_statements 的窗口）⇒ ⇒ 读数有窗口偏差，
//	   ★ 门不声称它是全量真相。
//	④ kind 由【人工登记】决定（★ 生成器不猜）⇒ ★ 本门只能校验「登记的 non-app 确实没参与判定」，
//	   ★ **不能**证明「它真的不是应用读点」⇒ ⇒ 那是【登记人的责任】。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	qpcCostRel   = "scripts/audit/asset/query-plan-cost-ledger.tsv"
	qpcCostExRel = "scripts/audit/asset/query-plan-cost-exemptions.tsv"
	qpcFactsRel  = "scripts/audit/asset/canonical-view-statements.tsv"
	qpcObsRel    = "scripts/audit/asset/query-plan-observations.tsv"

	// ★ 代价台账的列数：shape_id kind calls total_ms mean_ms rows plan_form over_threshold
	qpcCostCols = 8
	// ★ 豁免登记的列数：shape_id category reason
	qpcExemptCols = 3

	// ★★★ 两条硬阈值（★ 唯一的两个数字）★★★
	//
	// ★ mean_ms >= 2000（★ **2 秒 = 用户原话**「对于超过 2 秒打开的网页，
	// ★ 需要继续检查其慢的原因」）⇒ ⇒ 抓【单次就慢】的读点。
	qpcThreshMeanMs = 2000.0
	// ★ total_ms >= 300000（★ 累计 5 分钟）⇒ ⇒ 抓【高频但单次不慢】的读点
	// ★（★ 形态轴与单次耗时都抓不到它 ⇒ ★ 它只能被累计代价抓住）。
	qpcThreshTotalMs = 300000.0

	qpcKindApp    = "app"
	qpcKindNonApp = "non-app"

	qpcCatSlow    = "slow"
	qpcCatNonApp  = "non-app"

	// ★ 变异验证用：★ 允许把台账 / 豁免登记指向仓外副本（★ 默认行为完全不变）。
	qpcCostEnvOverride   = "URSM_QUERY_PLAN_COST_LEDGER_TSV"
	qpcCostExemptEnvOvrd = "URSM_QUERY_PLAN_COST_EXEMPTIONS_TSV"
)

// qpcRepoPath 按「仓库根」定位仓内文件（★ 自己写一个等价的小函数 ⇒ 本文件不依赖别的文件的存活）。
func qpcRepoPath(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join("..", "..", filepath.FromSlash(rel))
}

func qpcReadFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// qpcRows 读 TSV 的数据行（★ **显式跳过 '#' 注释行** —— ★ R179 实测踩过：
// ★ 台账的头部说明行恰好有制表符 ⇒ 被当成数据行 ⇒ 条数对不上）。
func qpcRows(t *testing.T, path string) [][]string {
	t.Helper()
	var out [][]string
	for _, line := range strings.Split(qpcReadFile(t, path), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, strings.Split(line, "\t"))
	}
	return out
}

// qpcShapeID 复刻事实源侧的语句指纹（★ sha256(sql)[:10]）⇒ ★ 用来把台账行对回事实源。
func qpcShapeID(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return "stmt-" + hex.EncodeToString(sum[:])[:10]
}

// qpcPlaceholderReasons 是「占位符理由」黑名单（★ 空理由等于没有理由 ⇒ 门不接受）。
var qpcPlaceholderReasons = map[string]bool{
	"":        true,
	"-":       true,
	"n/a":     true,
	"na":      true,
	"none":    true,
	"null":    true,
	"tbd":     true,
	"todo":    true,
	"unknown": true,
	"待查":      true,
	"未知":      true,
}

// qpcCostRow 是一条代价台账行。
type qpcCostRow struct {
	shapeID  string
	kind     string
	calls    string
	totalMs  string
	meanMs   string
	rows     string
	planForm string
	over     string
	line     int
}

func qpcLoadCost(t *testing.T, path string) []qpcCostRow {
	t.Helper()
	var out []qpcCostRow
	lineno := 0
	for _, line := range strings.Split(qpcReadFile(t, path), "\n") {
		lineno++
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != qpcCostCols {
			t.Fatalf("%s:%d: 代价台账行有 %d 个字段，期望 %d: %q", path, lineno, len(parts), qpcCostCols, line)
		}
		out = append(out, qpcCostRow{
			shapeID:  strings.TrimSpace(parts[0]),
			kind:     strings.TrimSpace(parts[1]),
			calls:    strings.TrimSpace(parts[2]),
			totalMs:  strings.TrimSpace(parts[3]),
			meanMs:   strings.TrimSpace(parts[4]),
			rows:     strings.TrimSpace(parts[5]),
			planForm: strings.TrimSpace(parts[6]),
			over:     strings.TrimSpace(parts[7]),
			line:     lineno,
		})
	}
	return out
}

// qpcExempt 是一条代价豁免登记。
type qpcExempt struct {
	shapeID  string
	category string
	reason   string
	line     int
}

func qpcLoadExemptions(t *testing.T, path string) []qpcExempt {
	t.Helper()
	var out []qpcExempt
	if _, err := os.Stat(path); err != nil {
		// ★ 豁免登记文件不存在 ⇒ 当成【空登记】⇒ ⇒ 超阈的行会红（★ 这正是本门要的）。
		return out
	}
	lineno := 0
	for _, line := range strings.Split(qpcReadFile(t, path), "\n") {
		lineno++
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != qpcExemptCols {
			t.Fatalf("%s:%d: 豁免登记行有 %d 个字段，期望 %d: %q", path, lineno, len(parts), qpcExemptCols, line)
		}
		reason := strings.TrimSpace(parts[2])
		if reason == "" {
			t.Fatalf("%s:%d: 豁免登记 %q 的理由是空的 ⇒ ⇒ 空理由的登记等于没有登记", path, lineno, parts[0])
		}
		out = append(out, qpcExempt{
			shapeID:  strings.TrimSpace(parts[0]),
			category: strings.TrimSpace(parts[1]),
			reason:   reason,
			line:     lineno,
		})
	}
	return out
}

// qpcOverThreshold 门**自己**重算「这条是否超阈值」（★ 与生成器同口径 ⇒ ★ 两边不一致 ⇒ 门红）。
func qpcOverThreshold(meanMs, totalMs float64) string {
	if meanMs >= qpcThreshMeanMs {
		return "mean>=2s"
	}
	if totalMs >= qpcThreshTotalMs {
		return "total>=5min"
	}
	return ""
}

// TestQueryPlanCostLedgerMatchesFacts 是 R180 轮新增：**生产代价轴**门。
//
// ★ 判据见文件头。
func TestQueryPlanCostLedgerMatchesFacts(t *testing.T) {
	costPath := qpcRepoPath(t, qpcCostRel)
	if override := strings.TrimSpace(os.Getenv(qpcCostEnvOverride)); override != "" {
		costPath = override
		t.Logf("★ 代价台账被环境变量 %s 指向 %s（★ 变异验证用法）", qpcCostEnvOverride, costPath)
	}
	exemptPath := qpcRepoPath(t, qpcCostExRel)
	if override := strings.TrimSpace(os.Getenv(qpcCostExemptEnvOvrd)); override != "" {
		exemptPath = override
		t.Logf("★ 代价豁免登记被环境变量 %s 指向 %s（★ 变异验证用法）", qpcCostExemptEnvOvrd, exemptPath)
	}
	factsPath := qpcRepoPath(t, qpcFactsRel)
	obsPath := qpcRepoPath(t, qpcObsRel)

	// ① 代价台账非空（★ 空 ⇒ 恒绿 ⇒ 判据失效 ⇒ 红）。
	cost := qpcLoadCost(t, costPath)
	if len(cost) == 0 {
		t.Fatalf("★ 代价台账 %s 没有数据行 ⇒ ⇒ 本门读到的「代价轴」全是空的 ⇒ ⇒ 门会恒绿 ⇒ ⇒ 判据失效 ⇒ ⇒ 门红", costPath)
	}

	// 事实源 ⇒ 语句指纹 ⇒ 它的 calls / total_ms / mean_ms / rows。
	type fact struct {
		calls   string
		totalMs string
		meanMs  string
		rows    string
	}
	facts := map[string]fact{}
	for _, parts := range qpcRows(t, factsPath) {
		if len(parts) < 5 {
			continue
		}
		sql := strings.TrimSpace(parts[4])
		if sql == "" {
			continue
		}
		facts[qpcShapeID(sql)] = fact{
			calls:   strings.TrimSpace(parts[0]),
			totalMs: strings.TrimSpace(parts[1]),
			meanMs:  strings.TrimSpace(parts[2]),
			rows:    strings.TrimSpace(parts[3]),
		}
	}
	if len(facts) == 0 {
		t.Fatalf("★ 生产语句事实源 %s 没有数据行 ⇒ ⇒ 事实源没了 ⇒ ⇒ 判据失效 ⇒ ⇒ 门红", factsPath)
	}

	// 实测形态观测台账（★ 键 = shape_id ⇒ 形态）。
	obs := map[string]string{}
	for _, parts := range qpcRows(t, obsPath) {
		if len(parts) < 3 {
			continue
		}
		obs[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[2])
	}

	exempts := qpcLoadExemptions(t, exemptPath)
	exemptByShape := map[string]qpcExempt{}
	for _, e := range exempts {
		if _, dup := exemptByShape[e.shapeID]; dup {
			t.Errorf("★ 豁免登记里 %s 有重复条目（★ %s:%d）⇒ ⇒ 门红", e.shapeID, exemptPath, e.line)
		}
		exemptByShape[e.shapeID] = e
		// ⑧ ★ 豁免登记必须指向事实源里真实存在的语句。
		if _, ok := facts[e.shapeID]; !ok {
			t.Errorf("★ 豁免登记 %s:%d 的 shape_id=%q 在事实源 %s 里【不存在】⇒ ⇒ 门红（★ 防登记不存在的语句）",
				exemptPath, e.line, e.shapeID, qpcFactsRel)
		}
		if e.category != qpcCatSlow && e.category != qpcCatNonApp {
			t.Errorf("★ 豁免登记 %s:%d 的 category=%q 不在 {%s, %s} 里 ⇒ ⇒ 门红",
				exemptPath, e.line, e.category, qpcCatSlow, qpcCatNonApp)
		}
		if qpcPlaceholderReasons[strings.ToLower(e.reason)] {
			t.Errorf("★ 豁免登记 %s:%d 的理由是占位符 %q ⇒ ⇒ 等于没登记 ⇒ ⇒ 门红",
				exemptPath, e.line, e.reason)
		}
	}

	seen := map[string]bool{}
	overCount := 0
	nonAppCount := 0
	appCount := 0
	var appTotalMs float64

	for _, r := range cost {
		seen[r.shapeID] = true

		// ② ★ 台账里的读数必须与事实源逐字相等（★ 读数不许手抄 ⇒ 门自己就是对拍）。
		f, ok := facts[r.shapeID]
		if !ok {
			t.Errorf("★ 代价台账 %s:%d 的 shape_id=%q 在事实源 %s 里【找不到】⇒ ⇒ 台账登记了一条事实源里没有的语句 ⇒ ⇒ 门红",
				costPath, r.line, r.shapeID, qpcFactsRel)
			continue
		}
		if r.calls != f.calls || r.totalMs != f.totalMs || r.meanMs != f.meanMs || r.rows != f.rows {
			t.Errorf("★ 代价台账 %s:%d 的读数与事实源对不上（★ 登记与事实不符 ⇒ ⇒ 门红）\n"+
				"   台账：calls=%s total_ms=%s mean_ms=%s rows=%s\n"+
				"   事实：calls=%s total_ms=%s mean_ms=%s rows=%s",
				costPath, r.line, r.calls, r.totalMs, r.meanMs, r.rows,
				f.calls, f.totalMs, f.meanMs, f.rows)
		}

		// ④ ★ plan_form 必须与观测台账一致（★ 登记与事实相符）。
		if got, ok := obs[r.shapeID]; ok && got != r.planForm {
			t.Errorf("★ 代价台账 %s:%d 登记的 plan_form=%q，★ 但实测观测台账记的是 %q ⇒ ⇒ 门红",
				costPath, r.line, r.planForm, got)
		}

		meanMs, errM := strconv.ParseFloat(r.meanMs, 64)
		totalMs, errT := strconv.ParseFloat(r.totalMs, 64)
		if errM != nil || errT != nil {
			t.Errorf("★ 代价台账 %s:%d 的耗时字段不是数字（★ mean_ms=%q total_ms=%q）⇒ ⇒ 门红",
				costPath, r.line, r.meanMs, r.totalMs)
			continue
		}

		// ★ kind 取值域。
		if r.kind != qpcKindApp && r.kind != qpcKindNonApp {
			t.Errorf("★ 代价台账 %s:%d 的 kind=%q 不在 {%s, %s} 里 ⇒ ⇒ 门红",
				costPath, r.line, r.kind, qpcKindApp, qpcKindNonApp)
			continue
		}

		e, hasExempt := exemptByShape[r.shapeID]

		if r.kind == qpcKindNonApp {
			nonAppCount++
			// ⑦ ★ non-app 必须有 category=non-app 的登记（★ 唯一的逃生舱 ⇒ 必须付账）。
			if !hasExempt || e.category != qpcCatNonApp {
				t.Errorf("★ 代价台账 %s:%d 把 %q 标成 kind=non-app（★ 不参与阈值判定）"+
					"⇒ ⇒ 但 %s 里【没有它的 category=non-app 登记】⇒ ⇒ 门红（★ 逃不掉，★ 只是要付账）",
					costPath, r.line, r.shapeID, qpcCostExRel)
			}
			// ★ non-app ⇒ 不参与阈值判定 ⇒ over_threshold 必须为空。
			if r.over != "" {
				t.Errorf("★ 代价台账 %s:%d 的 kind=non-app ⇒ 不参与阈值判定 ⇒ ⇒ over_threshold 必须为空，★ 但它是 %q ⇒ ⇒ 门红",
					costPath, r.line, r.over)
			}
			continue
		}

		appCount++
		appTotalMs += totalMs

		// ⑥ ★ 门自己重算阈值 ⇒ 必须与台账一致（★ 防「生成器把超阈的标成不超阈」）。
		wantOver := qpcOverThreshold(meanMs, totalMs)
		if wantOver != r.over {
			t.Errorf("★ 代价台账 %s:%d 的 over_threshold=%q，★ 但门自己重算（★ mean_ms=%s total_ms=%s ⇒ 阈值 %.0fms / %.0fms）得 %q"+
				"⇒ ⇒ 门红（★ 台账与门对阈值口径不一致 ⇒ ⇒ 必有一方算错了）",
				costPath, r.line, r.over, r.meanMs, r.totalMs, qpcThreshMeanMs, qpcThreshTotalMs, wantOver)
		}

		// ⑤ ★★ 超阈值必须挂 category=slow 的登记 ★★
		if wantOver != "" {
			overCount++
			if !hasExempt || e.category != qpcCatSlow {
				t.Errorf("★ [代价轴] shape_id=%q（★ %s:%d）单次/累计耗时超阈值（★ mean_ms=%s total_ms=%s ⇒ %s）"+
					"⇒ ⇒ 但 %s 里【没有它的 category=slow 登记】⇒ ⇒ 门红（★ 慢的读点必须写清为什么慢）",
					r.shapeID, costPath, r.line, r.meanMs, r.totalMs, wantOver, qpcCostExRel)
			}
		} else if hasExempt && e.category == qpcCatSlow {
			// ★ 没超阈却挂了 slow 登记 ⇒ 登记与事实不符（★ 可能是阈值算错了）⇒ ⇒ 门红。
			t.Errorf("★ [代价轴] shape_id=%q（★ %s:%d）没有超阈值（★ mean_ms=%s total_ms=%s）"+
				"⇒ ⇒ 却挂了 category=slow 的登记 %q ⇒ ⇒ 登记与事实不符 ⇒ ⇒ 门红",
				r.shapeID, costPath, r.line, r.meanMs, r.totalMs, e.reason)
		}
	}

	// ③ ★ 反向覆盖：★ 事实源里的每一条语句都必须出现在代价台账里（★ 不许只登记便宜的）。
	missing := 0
	for sid := range facts {
		if !seen[sid] {
			missing++
			t.Errorf("★ [代价轴] 事实源里的语句 %q 在代价台账 %s 里【没有登记】⇒ ⇒ 门红（★ 台账必须覆盖全部生产语句）",
				sid, qpcCostRel)
		}
	}

	t.Logf("★ 代价轴：台账 %s 共 %d 行（★ app %d / non-app %d）⇒ ★ 超阈值 %d 条（★ app 侧累计 %.1f 秒）"+
		"⇒ ★ 事实源 %s 共 %d 条（★ 未登记 %d 条）⇒ ★ 豁免登记 %s 共 %d 条",
		costPath, len(cost), appCount, nonAppCount, overCount, appTotalMs/1000.0,
		qpcFactsRel, len(facts), missing, exemptPath, len(exempts))
}
