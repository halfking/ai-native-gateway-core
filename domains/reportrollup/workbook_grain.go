// workbook_grain.go —— 多维对帐导出的工作簿布局（2026-09-29 对帐页多维筛选轮）。
//
// 与旧 workbook.go（双 sheet：用量 / 模型质量与错误分析）的差异：
//
//	旧：一个「用量」sheet 里用分节堆全部维度 + 一个按 error_kind 动态列的
//	    质量 sheet。对帐人要横向对比时只能在同一 sheet 里靠肉眼找分节，
//	    而且**没有逐日明细**——只有区间汇总与按天总计。
//	新：四个 sheet，按「粒度」切分而不是按「主题」切分：
//	  1. 汇总    —— 总计 + 六个维度各自的汇总（对帐人签字页）
//	  2. 按天     —— 每日一行的区间趋势
//	  3. 按天×X   —— 每天每个 X 的量（X 由 ?group= 决定，默认模型）。图表数据源
//	  4. 按天明细  —— tidy 长表（日期/维度/取值/度量），可在 Excel 里直接
//	              数据透视出任意组合；这是「多维的表」最通用的落地形态
//
// 未落定维度（provider/credential/api_key 为 NULL 的失败行）在表里显示为
// 「未落定」而不是被丢掉——丢了 Σ各分组就对不上总计。
package reportrollup

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 多维导出的 sheet 名（≤31 字符、无 Excel 非法字符）。
const (
	SheetGrainSummary = "汇总"
	SheetGrainDays    = "按天"
	SheetGrainDetail  = "按天明细"
)

// GrainGroup 「按天×X」sheet 里 X 取哪个维度。
//
// 为什么需要它：早先这个 sheet 写死按模型拆，对帐人按租户/供应商/apikey 交接
// 数据时只能去「按天明细」长表里自己做透视。四个 sheet 里有六维长表兜底，所以
// 这不是「不能导出多维」，而是**最常用的那张日粒度表不能跟着分组维度走**。
type GrainGroup string

const (
	GrainGroupModel      GrainGroup = "model"
	GrainGroupProvider   GrainGroup = "provider"
	GrainGroupCredential GrainGroup = "credential"
	GrainGroupTenant     GrainGroup = "tenant"
	GrainGroupPerson     GrainGroup = "person"
	GrainGroupAPIKey     GrainGroup = "apikey"
)

// grainGroupLabel 各维度在 sheet 表头里的中文名。
var grainGroupLabel = map[GrainGroup]string{
	GrainGroupModel:      "模型",
	GrainGroupProvider:   "供应商",
	GrainGroupCredential: "凭据",
	GrainGroupTenant:     "租户",
	GrainGroupPerson:     "用户",
	GrainGroupAPIKey:     "apikey",
}

// ParseGrainGroup 解析导出用的分组维度。空串按 model（保持旧行为），
// 非法值报错而不是静默回退——传错参数却拿到模型口径，比报错更难查。
func ParseGrainGroup(s string) (GrainGroup, error) {
	switch g := GrainGroup(strings.ToLower(strings.TrimSpace(s))); g {
	case "":
		return GrainGroupModel, nil
	case GrainGroupModel, GrainGroupProvider, GrainGroupCredential,
		GrainGroupTenant, GrainGroupPerson, GrainGroupAPIKey:
		return g, nil
	default:
		return "", fmt.Errorf("unknown group %q (want one of: model, provider, credential, tenant, person, apikey)", s)
	}
}

// SheetName 「按天×X」sheet 名。
func (g GrainGroup) SheetName() string {
	label, ok := grainGroupLabel[g]
	if !ok {
		label = "模型"
	}
	return "按天×" + label
}

// Label 维度中文名。
func (g GrainGroup) Label() string {
	if label, ok := grainGroupLabel[g]; ok {
		return label
	}
	return "模型"
}

// unassignedLabel 未落定维度的展示名。
const unassignedLabel = "未落定"

// idLabel 把哨兵/真实 id 折成展示文本。
func idLabel(id int64, name string) string {
	if id == UnassignedID {
		return unassignedLabel
	}
	if name != "" {
		return name
	}
	return strconv.FormatInt(id, 10)
}

// grainMetricHeader 统一的度量表头（双视角同序，内部口径的两列恒存在，
// 只是 provider 视角为 0——列对齐比"少两列"更利于对帐人横向复制）。
func grainMetricHeader() []cell {
	head := []cell{
		boldStrCell("请求数"), boldStrCell("成功数"), boldStrCell("失败数"), boldStrCell("失败率"),
		boldStrCell("质量评分"),
		boldStrCell("输入tokens"), boldStrCell("输出tokens"),
		boldStrCell("缓存读tokens"), boldStrCell("缓存写tokens"),
		boldStrCell("缓存率"),
		boldStrCell("供应商成本"), boldStrCell("币种"),
		boldStrCell("内部积分"), boldStrCell("内部金额"),
		boldStrCell("主要错误"),
	}
	return head
}

// grainMetricRow 一行度量。score < 0 表示该行不评质量——只有按天明细这类
// 「同维度多次出现」的行留空；各维度汇总行的评分公式与维度无关（成功率 ×
// 时效因子），一律填真实值。br 传该分组的错误分布，
// 「主要错误」列取次数最多的 error_kind（并列取字典序，保证多次导出稳定）。
func grainMetricRow(t Totals, score float64, br map[string]int64) []cell {
	row := []cell{
		intCell(t.RequestCount), intCell(t.SuccessCount), intCell(t.ErrorCount), numCell(t.ErrorRate),
	}
	if score >= 0 {
		row = append(row, numCell(score))
	} else {
		row = append(row, strCell(""))
	}
	row = append(row,
		intCell(t.InputTokens), intCell(t.OutputTokens),
		intCell(t.CacheReadTokens), intCell(t.CacheWriteTokens),
	)
	if t.CacheHitRatio != nil {
		row = append(row, numCell(*t.CacheHitRatio))
	} else {
		row = append(row, strCell(""))
	}
	row = append(row, numCell(centsToUnitsF(float64(t.EstimatedCostCents))), strCell(t.Currency))
	row = append(row, intCell(t.CreditsCharged), numCell(centsToUnitsF(t.InternalCostCents)))
	kind, _ := topErrorKind(br)
	if kind == "" {
		row = append(row, strCell(""))
	} else {
		row = append(row, strCell(kind))
	}
	return row
}

// grainSummaryRows 汇总 sheet：总计 + 六维分节。
func grainSummaryRows(rep *GrainReport) [][]cell {
	rows := make([][]cell, 0, 128)
	title := "供应商对帐报表（汇总）"
	if rep.View == ViewInternal {
		title = "内部对帐报表（汇总）"
	}
	rows = append(rows,
		[]cell{boldStrCell(title)},
		[]cell{
			strCell("区间: " + rep.Start.Format("2006-01-02") + " ~ " + rep.End.Format("2006-01-02")),
			strCell("生成时间: " + time.Now().UTC().Format("2006-01-02 15:04:05 UTC")),
			strCell("口径: " + caliberText(rep.View)),
			strCell("快照口径: " + rep.Source),
		},
		[]cell{},
	)

	emit := func(dim string, rowsOut *[][]cell) {
		*rowsOut = append(*rowsOut, []cell{boldStrCell(dim)})
		*rowsOut = append(*rowsOut, append([]cell{boldStrCell("维度值"), boldStrCell("名称")}, grainMetricHeader()...))
	}

	// 总计
	emit("总计", &rows)
	rows = append(rows, append([]cell{strCell("全部"), strCell("")},
		grainMetricRow(rep.Totals, ProviderQualityScore(rep.Totals), rep.ErrorBreakdown)...))

	// 按供应商（带质量评分 —— 只有供应商粒度的评分语义成立）。
	emit("按供应商", &rows)
	for _, p := range rep.Providers {
		label := strconv.FormatInt(p.ProviderID, 10)
		name := p.ProviderName
		if p.ProviderID == UnassignedID {
			label, name = unassignedLabel, ""
		}
		rows = append(rows, append([]cell{strCell(label), strCell(name)},
			grainMetricRow(p.Totals, p.QualityScore, p.ErrorBreakdown)...))
	}

	// 按凭据
	emit("按凭据", &rows)
	for _, c := range rep.Credentials {
		label := strconv.FormatInt(c.CredentialID, 10)
		name := c.CredentialName
		if c.CredentialID == UnassignedID {
			label, name = unassignedLabel, ""
		} else if c.ProviderID != nil {
			name = name + " @ " + idLabel(*c.ProviderID, c.ProviderName)
		}
		rows = append(rows, append([]cell{strCell(label), strCell(name)}, grainMetricRow(c.Totals, c.QualityScore, c.ErrorBreakdown)...))
	}

	// 「按模型」分节**故意不进导出**：对帐人明确要求整个模型的统计列表只作
	// 页面上的折叠菜单（辅助挑选模型名做筛选），原则上不导出——它有几百
	// 行且不需要签字。模型口径的逐日数据仍在「按天×模型」sheet 里，
	// 对帐真要用模型维度时从那里取。

	// 按租户
	emit("按租户", &rows)
	for _, t := range rep.Tenants {
		rows = append(rows, append([]cell{strCell(t.TenantID), strCell("")}, grainMetricRow(t.Totals, t.QualityScore, t.ErrorBreakdown)...))
	}

	// 按用户（租户 + 人员）
	emit("按用户", &rows)
	for _, p := range rep.Persons {
		rows = append(rows, append([]cell{strCell(p.Person), strCell(p.TenantID)}, grainMetricRow(p.Totals, p.QualityScore, p.ErrorBreakdown)...))
	}

	// 按 apikey
	emit("按apikey", &rows)
	for _, k := range rep.APIKeys {
		label := strconv.FormatInt(k.APIKeyID, 10)
		name := k.APIKeyName
		if k.APIKeyID == UnassignedID {
			label, name = unassignedLabel, ""
		}
		rows = append(rows, append([]cell{strCell(label), strCell(name)}, grainMetricRow(k.Totals, k.QualityScore, k.ErrorBreakdown)...))
	}
	return rows
}

// grainDayRows 按天 sheet。
func grainDayRows(rep *GrainReport) [][]cell {
	title := "供应商对帐报表（按天）"
	if rep.View == ViewInternal {
		title = "内部对帐报表（按天）"
	}
	rows := [][]cell{
		{boldStrCell(title)},
		{strCell("区间: " + rep.Start.Format("2006-01-02") + " ~ " + rep.End.Format("2006-01-02"))},
		{},
		append([]cell{boldStrCell("日期")}, grainMetricHeader()...),
	}
	for _, d := range rep.Days {
		rows = append(rows, append([]cell{strCell(d.Date)}, grainMetricRow(d.Totals, ProviderQualityScore(d.Totals), d.ErrorBreakdown)...))
	}
	// 合计行：让不逐行相加的人一眼看到区间总计对不对。
	rows = append(rows, append([]cell{boldStrCell("合计")}, grainMetricRow(rep.Totals, ProviderQualityScore(rep.Totals), rep.ErrorBreakdown)...))
	return rows
}

// grainDayGroupRows 按天 × <group 维度> sheet（图表数据源）。
//
// 六个维度里有两种行类型：模型是 DailyModelRow（只有 RawModelName），其余是
// DailyGroupRow（有 Key + Name）。这里先归一成 (日期, 取值, 名称) 三元组再排，
// 免得两条几乎一样的循环各写一遍、排��规则还各自漂移。
func grainDayGroupRows(rep *GrainReport, group GrainGroup) [][]cell {
	label := group.Label()
	header := []cell{boldStrCell("日期"), boldStrCell(label)}
	if group != GrainGroupModel {
		// 模型行没有独立的展示名，其余维度有「取值 + 名称」两列，
		// 交接数据时对方需要能对上人看的名字而不只是内部 key。
		header = append(header, boldStrCell("名称"))
	}

	type dayKey = grainDayKey
	byKey, errByKey := grainDailyTotals(rep, group)

	sorted := make([]dayKey, 0, len(byKey))
	for k := range byKey {
		sorted = append(sorted, k)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].date != sorted[j].date {
			return sorted[i].date < sorted[j].date
		}
		return sorted[i].value < sorted[j].value
	})

	rows := [][]cell{
		{boldStrCell("按天 × " + label + "明细")},
		{strCell("区间: " + rep.Start.Format("2006-01-02") + " ~ " + rep.End.Format("2006-01-02"))},
		{},
		append(header, grainMetricHeader()...),
	}
	for _, k := range sorted {
		lead := []cell{strCell(k.date), strCell(k.value)}
		if group != GrainGroupModel {
			lead = append(lead, strCell(k.name))
		}
		rows = append(rows, append(lead, grainMetricRow(byKey[k], -1, errByKey[k])...))
	}
	return rows
}

// grainDailyTotals 按 group 维度抽 (日期,取值[,名称]) → 度量/错误分布。
func grainDailyTotals(rep *GrainReport, group GrainGroup) (map[grainDayKey]Totals, map[grainDayKey]map[string]int64) {
	totals := map[grainDayKey]Totals{}
	errs := map[grainDayKey]map[string]int64{}
	if group == GrainGroupModel {
		for _, m := range rep.DailyModels {
			k := grainDayKey{date: m.Date, value: m.RawModelName}
			totals[k] = m.Totals
			if len(m.ErrorBreakdown) > 0 {
				errs[k] = m.ErrorBreakdown
			}
		}
		return totals, errs
	}
	var rows []DailyGroupRow
	switch group {
	case GrainGroupProvider:
		rows = rep.DailyProviders
	case GrainGroupCredential:
		rows = rep.DailyCredentials
	case GrainGroupTenant:
		rows = rep.DailyTenants
	case GrainGroupPerson:
		rows = rep.DailyPersons
	case GrainGroupAPIKey:
		rows = rep.DailyAPIKeys
	}
	for _, g := range rows {
		k := grainDayKey{date: g.Date, value: g.Key, name: g.Name}
		totals[k] = g.Totals
		if len(g.ErrorBreakdown) > 0 {
			errs[k] = g.ErrorBreakdown
		}
	}
	return totals, errs
}

// grainDayKey 「按天×维度」的行标识。name 参与比较只为稳定排序，不影响归并。
type grainDayKey struct {
	date  string
	value string
	name  string
}

// grainDetailRows 按天明细 tidy 长表：日期/维度/取值/名称/度量。六个维度
// 纵向堆叠，Excel 里可直接数据透视。
func grainDetailRows(rep *GrainReport) [][]cell {
	rows := [][]cell{
		{boldStrCell("按天明细（长表）")},
		{strCell("区间: " + rep.Start.Format("2006-01-02") + " ~ " + rep.End.Format("2006-01-02"))},
		{},
		append([]cell{boldStrCell("日期"), boldStrCell("维度"), boldStrCell("取值"), boldStrCell("名称")}, grainMetricHeader()...),
	}
	emitGroups := func(dim string, groups []DailyGroupRow) {
		sorted := append([]DailyGroupRow(nil), groups...)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].Date != sorted[j].Date {
				return sorted[i].Date < sorted[j].Date
			}
			return sorted[i].Key < sorted[j].Key
		})
		for _, g := range sorted {
			rows = append(rows, append([]cell{
				strCell(g.Date), strCell(dim), strCell(g.Key), strCell(g.Name),
			}, grainMetricRow(g.Totals, -1, g.ErrorBreakdown)...))
		}
	}
	emitGroups("供应商", rep.DailyProviders)
	emitGroups("凭据", rep.DailyCredentials)
	emitGroups("租户", rep.DailyTenants)
	emitGroups("用户", rep.DailyPersons)
	emitGroups("apikey", rep.DailyAPIKeys)
	// 模型逐日明细直接由 DailyModels 派生（它已是「按天×模型」口径）。
	models := append([]DailyModelRow(nil), rep.DailyModels...)
	sort.Slice(models, func(i, j int) bool {
		if models[i].Date != models[j].Date {
			return models[i].Date < models[j].Date
		}
		return models[i].RawModelName < models[j].RawModelName
	})
	for _, m := range models {
		rows = append(rows, append([]cell{
			strCell(m.Date), strCell("模型"), strCell(m.RawModelName), strCell(""),
		}, grainMetricRow(m.Totals, -1, nil)...))
	}
	return rows
}

// grainWidths 汇总/按天 sheet 的列宽。
func grainWidths(withDim bool) []float64 {
	w := []float64{}
	if withDim {
		w = append(w, 18, 26)
	}
	return append(w,
		10, 10, 10, 10, 10,
		14, 14, 14, 14, 10,
		14, 8, 12, 12, 18,
	)
}

// BuildGrainWorkbookBytes 按多维报表渲染 xlsx（admin 导出端点入口）。
//
// detail=false 时**只出「汇总」一张表**，不给按天三张。需求原话是「报表及导出
// 的输出，可以只看汇总，也可以看到详细的每天的数据」——导出也在这句话里，
// 早先这里恒定出 4 张、把界面上的「汇总 ⇄ 按天明细」开关对导出做成摆设。
//
// 早先的理由是「导出永远取明细口径：多导一层的成本远小于对账人拿到汇总却
// 没法下钻的代价」。那个理由在**缺省**口径下依然成立，所以 detail 缺省为
// true、老调用方拿到的还是全量；这里补的是**显式要汇总**的那条通路。
func BuildGrainWorkbookBytes(rep *GrainReport, group GrainGroup, detail bool) ([]byte, error) {
	summaryWidths := grainWidths(true)
	dayWidths := grainWidths(false)
	// 「按天×X」有 2 列（日期/取值）或 3 列（多一个名称）两种形态。
	groupWidths := append([]float64{12, 22}, dayWidths...)
	if group != GrainGroupModel {
		groupWidths = append([]float64{12, 22, 26}, dayWidths...)
	}
	detailWidths := append([]float64{12, 10, 22, 26}, dayWidths...)
	sheets := []sheetSheet{
		{name: SheetGrainSummary, rows: grainSummaryRows(rep), widths: summaryWidths},
	}
	if detail {
		sheets = append(sheets,
			sheetSheet{name: SheetGrainDays, rows: grainDayRows(rep), widths: dayWidths},
			sheetSheet{name: group.SheetName(), rows: grainDayGroupRows(rep, group), widths: groupWidths},
			sheetSheet{name: SheetGrainDetail, rows: grainDetailRows(rep), widths: detailWidths},
		)
	}
	return buildWorkbookBytes(sheets)
}
