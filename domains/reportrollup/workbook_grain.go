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
	return appendGrainMetrics(nil, t, score, br)
}

func appendGrainMetrics(row []cell, t Totals, score float64, br map[string]int64) []cell {
	row = append(row,
		intCell(t.RequestCount), intCell(t.SuccessCount), intCell(t.ErrorCount), numCell(t.ErrorRate),
	)
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
	if t.USDMeasuredRequests != nil && *t.USDMeasuredRequests == 0 && t.RequestCount > 0 {
		row = append(row, strCell(""), strCell(""))
	} else {
		row = append(row, numCell(centsToUnitsF(t.EstimatedCostCents)), strCell(t.Currency))
	}
	row = append(row, intCell(t.CreditsCharged))
	if t.USDMeasuredRequests != nil {
		row = append(row, strCell(""))
	} else {
		row = append(row, numCell(centsToUnitsF(t.InternalCostCents)))
	}
	kind, _ := topErrorKind(br)
	if kind == "" {
		row = append(row, strCell(""))
	} else {
		row = append(row, strCell(kind))
	}
	return row
}

// grainSummaryHeader carries the coverage notes and the same generated
// timestamp on each continuation worksheet.
func grainSummaryHeader(rep *GrainReport) [][]cell {
	title := "供应商对帐报表（汇总）"
	caliber := caliberText(rep.View)
	sourceLabel := "快照口径"
	if rep.View == ViewInternal {
		title = "内部对帐报表（汇总）"
	}
	if rep.Projection != nil {
		sourceLabel = "统计来源"
		title = "历史请求统计（汇总）"
		caliber = "全部请求流量，含探测和自检"
		if rep.View == ViewInternal || rep.View == ViewKey {
			caliber = "业务请求流量，积分为请求归属"
		} else if rep.Filter.BusinessOnly {
			caliber = "业务请求流量"
		}
	}
	rows := [][]cell{
		{boldStrCell(title)},
		{
			strCell("区间: " + rep.Start.Format("2006-01-02") + " ~ " + rep.End.Format("2006-01-02")),
			strCell("生成时间: " + time.Now().UTC().Format("2006-01-02 15:04:05 UTC")),
			strCell("口径: " + caliber),
			strCell(sourceLabel + ": " + rep.Source),
		},
		{},
	}
	if p := rep.Projection; p != nil {
		rows = append(rows, []cell{strCell("请求统计，实际过账见积分账本，账单及逐次发送见日对账。"),
			strCell(fmt.Sprintf("自然日时区 %s，数据截止 %s（不含该时刻）", p.Timezone, p.Through.Format(time.RFC3339)))})
		if p.PendingSince != nil {
			rows = append(rows, []cell{strCell("所选日期有尚未投影的记录。")})
		}
		if n := rep.Totals.USDUnmeasuredRequests; n != nil && *n > 0 {
			rows = append(rows, []cell{strCell(fmt.Sprintf("%d 条请求缺少 USD 金额或使用其他币种。", *n))})
		}
	}
	return rows
}

type grainSummarySection struct {
	label   string
	count   int
	dataRow func(int) []cell
}

func grainSummarySections(rep *GrainReport) []grainSummarySection {
	cells := make([]cell, 0, 2+len(grainMetricHeader()))
	metricRow := func(label, name string, totals Totals, score float64, errors map[string]int64) []cell {
		return appendGrainMetrics(append(cells[:0], strCell(label), strCell(name)), totals, score, errors)
	}
	return []grainSummarySection{
		{"总计", 1, func(_ int) []cell {
			return metricRow("全部", "", rep.Totals, ProviderQualityScore(rep.Totals), rep.ErrorBreakdown)
		}},
		{"按供应商", len(rep.Providers), func(i int) []cell {
			p := rep.Providers[i]
			label, name := strconv.FormatInt(p.ProviderID, 10), p.ProviderName
			if p.ProviderID == UnassignedID {
				label, name = unassignedLabel, ""
			}
			return metricRow(label, name, p.Totals, p.QualityScore, p.ErrorBreakdown)
		}},
		{"按凭据", len(rep.Credentials), func(i int) []cell {
			c := rep.Credentials[i]
			label, name := strconv.FormatInt(c.CredentialID, 10), c.CredentialName
			if c.CredentialID == UnassignedID {
				label, name = unassignedLabel, ""
			} else if c.ProviderID != nil {
				name += " @ " + idLabel(*c.ProviderID, c.ProviderName)
			}
			return metricRow(label, name, c.Totals, c.QualityScore, c.ErrorBreakdown)
		}},
		// Models are exported in daily sheets, as kept by the summary contract.
		{"按租户", len(rep.Tenants), func(i int) []cell {
			t := rep.Tenants[i]
			return metricRow(t.TenantID, "", t.Totals, t.QualityScore, t.ErrorBreakdown)
		}},
		{"按用户", len(rep.Persons), func(i int) []cell {
			p := rep.Persons[i]
			return metricRow(p.Person, p.TenantID, p.Totals, p.QualityScore, p.ErrorBreakdown)
		}},
		{"按apikey", len(rep.APIKeys), func(i int) []cell {
			k := rep.APIKeys[i]
			label, name := strconv.FormatInt(k.APIKeyID, 10), k.APIKeyName
			if k.APIKeyID == UnassignedID {
				label, name = unassignedLabel, ""
			}
			return metricRow(label, name, k.Totals, k.QualityScore, k.ErrorBreakdown)
		}},
	}
}

type grainSummarySegment struct {
	section grainSummarySection
	title   []cell
	start   int
	count   int
}

func grainSummarySheets(rep *GrainReport, widths []float64, limit int) []sheetSheet {
	head := grainSummaryHeader(rep)
	columns := append([]cell{boldStrCell("维度值"), boldStrCell("名称")}, grainMetricHeader()...)
	capacity := limit - len(head)
	var sheets []sheetSheet
	var payload []grainSummarySegment
	used := 0
	finish := func() {
		segments := payload
		part := sheetSheet{name: grainSheetPartName(SheetGrainSummary, len(sheets)+1), rows: head, widths: widths, dataCount: used}
		part.dataRow = func(i int) []cell {
			for _, seg := range segments {
				if i < seg.count+2 {
					switch i {
					case 0:
						return seg.title
					case 1:
						return columns
					default:
						return seg.section.dataRow(seg.start + i - 2)
					}
				}
				i -= seg.count + 2
			}
			return nil
		}
		sheets = append(sheets, part)
		payload, used = nil, 0
	}
	for _, section := range grainSummarySections(rep) {
		title := []cell{boldStrCell(section.label)}
		for offset := 0; ; {
			if capacity-used < 2+min(1, section.count-offset) {
				finish()
			}
			count := min(section.count-offset, capacity-used-2)
			payload = append(payload, grainSummarySegment{section: section, title: title, start: offset, count: count})
			used += count + 2
			offset += count
			if offset == section.count {
				break
			}
		}
	}
	finish()
	return sheets
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

type grainWorkbookDimension struct {
	group GrainGroup
	rows  []DailyGroupRow
}

func grainWorkbookDimensions(rep *GrainReport) ([]grainWorkbookDimension, []DailyModelRow) {
	groups := []grainWorkbookDimension{
		{GrainGroupProvider, rep.DailyProviders},
		{GrainGroupCredential, rep.DailyCredentials},
		{GrainGroupTenant, rep.DailyTenants},
		{GrainGroupPerson, rep.DailyPersons},
		{GrainGroupAPIKey, rep.DailyAPIKeys},
	}
	for i, group := range groups {
		groups[i].rows = orderedWorkbookRows(group.rows, func(a, b DailyGroupRow) bool {
			return a.Date < b.Date || (a.Date == b.Date && a.Key < b.Key)
		})
	}
	models := orderedWorkbookRows(rep.DailyModels, func(a, b DailyModelRow) bool {
		return a.Date < b.Date || (a.Date == b.Date && a.RawModelName < b.RawModelName)
	})
	return groups, models
}

// Projected reports already return ordered rows. Keep the caller's slices
// unchanged when exporting a report built by another caller.
func orderedWorkbookRows[T any](rows []T, less func(T, T) bool) []T {
	if !sort.SliceIsSorted(rows, func(i, j int) bool { return less(rows[i], rows[j]) }) {
		rows = append([]T(nil), rows...)
		sort.SliceStable(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
	}
	return rows
}

func grainDayGroupSheet(group GrainGroup, groups []grainWorkbookDimension, models []DailyModelRow, dateRange []cell, widths []float64) sheetSheet {
	label := group.Label()
	header := []cell{boldStrCell("日期"), boldStrCell(label)}
	if group != GrainGroupModel {
		header = append(header, boldStrCell("名称"))
	}
	s := sheetSheet{
		name: group.SheetName(), widths: widths,
		rows: [][]cell{{boldStrCell("按天 × " + label + "明细")}, dateRange, {}, append(header, grainMetricHeader()...)},
	}
	cells := make([]cell, 0, len(header)+len(grainMetricHeader()))
	if group == GrainGroupModel {
		s.dataCount = len(models)
		s.dataRow = func(i int) []cell {
			row := models[i]
			return appendGrainMetrics(append(cells[:0], strCell(row.Date), strCell(row.RawModelName)), row.Totals, -1, row.ErrorBreakdown)
		}
		return s
	}
	for _, dim := range groups {
		if dim.group == group {
			s.dataCount = len(dim.rows)
			s.dataRow = func(i int) []cell {
				row := dim.rows[i]
				return appendGrainMetrics(append(cells[:0], strCell(row.Date), strCell(row.Key), strCell(row.Name)), row.Totals, -1, row.ErrorBreakdown)
			}
			break
		}
	}
	return s
}

func grainDetailSheet(groups []grainWorkbookDimension, models []DailyModelRow, dateRange []cell, widths []float64) sheetSheet {
	head := []cell{boldStrCell("日期"), boldStrCell("维度"), boldStrCell("取值"), boldStrCell("名称")}
	s := sheetSheet{
		name: SheetGrainDetail, widths: widths,
		rows:      [][]cell{{boldStrCell("按天明细（长表）")}, dateRange, {}, append(head, grainMetricHeader()...)},
		dataCount: len(models),
	}
	for _, dim := range groups {
		s.dataCount += len(dim.rows)
	}
	cells := make([]cell, 0, len(head)+len(grainMetricHeader()))
	s.dataRow = func(i int) []cell {
		for _, dim := range groups {
			if i < len(dim.rows) {
				row := dim.rows[i]
				lead := append(cells[:0], strCell(row.Date), strCell(dim.group.Label()), strCell(row.Key), strCell(row.Name))
				return appendGrainMetrics(lead, row.Totals, -1, row.ErrorBreakdown)
			}
			i -= len(dim.rows)
		}
		row := models[i]
		lead := append(cells[:0], strCell(row.Date), strCell(grainGroupLabel[GrainGroupModel]), strCell(row.RawModelName), strCell(""))
		return appendGrainMetrics(lead, row.Totals, -1, nil)
	}
	return s
}

// Every part has the original heading rows and a disjoint slice of the data.
func appendGrainDetailSheets(sheets []sheetSheet, sheet sheetSheet, limit int) []sheetSheet {
	perSheet := limit - len(sheet.rows)
	if sheet.dataCount <= perSheet {
		return append(sheets, sheet)
	}
	for offset := 0; offset < sheet.dataCount; offset += perSheet {
		part := sheet
		part.name = grainSheetPartName(sheet.name, offset/perSheet+1)
		part.dataCount = min(perSheet, sheet.dataCount-offset)
		part.dataRow = func(i int) []cell { return sheet.dataRow(offset + i) }
		sheets = append(sheets, part)
	}
	return sheets
}

func grainSheetPartName(name string, part int) string {
	if part == 1 {
		return name
	}
	return fmt.Sprintf("%s (%d)", name, part)
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
	return buildWorkbookBytes(grainWorkbookSheets(rep, group, detail, maxWorksheetRows))
}

func grainWorkbookSheets(rep *GrainReport, group GrainGroup, detail bool, limit int) []sheetSheet {
	sheets := grainSummarySheets(rep, grainWidths(true), limit)
	if !detail {
		return sheets
	}
	dayWidths := grainWidths(false)
	groupWidths := []float64{12, 22}
	if group != GrainGroupModel {
		groupWidths = append(groupWidths, 26)
	}
	groupWidths = append(groupWidths, dayWidths...)
	detailWidths := append([]float64{12, 10, 22, 26}, dayWidths...)
	sheets = append(sheets, sheetSheet{name: SheetGrainDays, rows: grainDayRows(rep), widths: dayWidths})
	dateRange := []cell{strCell("区间: " + rep.Start.Format("2006-01-02") + " ~ " + rep.End.Format("2006-01-02"))}
	dimensions, models := grainWorkbookDimensions(rep)
	sheets = appendGrainDetailSheets(sheets, grainDayGroupSheet(group, dimensions, models, dateRange, groupWidths), limit)
	return appendGrainDetailSheets(sheets, grainDetailSheet(dimensions, models, dateRange, detailWidths), limit)
}
