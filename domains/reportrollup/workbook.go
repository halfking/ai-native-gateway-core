// workbook.go —— 对账报表 Excel 工作簿布局：双 sheet（用量 / 模型质量与
// 错误分析），分节（总计 / 按供应商 / 按租户 / 按人员 / 按模型 / 按天）。
// 文本一律 inline string，数值一律 number 单元格（对帐双方可在 Excel 内
// 直接再聚合），表头/分节标题粗体。
package reportrollup

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SheetUsage / SheetQuality 是两个 sheet 的固定名称（≤31 字符，无 Excel
// 非法字符）。
const (
	SheetUsage   = "用量"
	SheetQuality = "模型质量与错误分析"
)

// providerWorkbook 供应商对帐工作簿（全流量、成本口径）。
func providerWorkbook(rep *RangeReport) []sheetSheet {
	return []sheetSheet{
		{name: SheetUsage, rows: usageRows(rep), widths: usageWidths()},
		{name: SheetQuality, rows: qualityRows(rep, true), widths: qualityWidths(rep, true)},
	}
}

// internalWorkbook 内部对帐工作簿（business 流量、积分/内部价口径）。
func internalWorkbook(rep *RangeReport) []sheetSheet {
	return []sheetSheet{
		{name: SheetUsage, rows: usageRows(rep), widths: usageWidths()},
		{name: SheetQuality, rows: qualityRows(rep, false), widths: qualityWidths(rep, false)},
	}
}

func usageWidths() []float64 {
	return []float64{14, 18, 22, 10, 10, 10, 12, 12, 12, 12, 12, 12, 12, 8, 12, 12, 12}
}

// usageRows 生成 sheet1（用量）。双视图同构：标题 + 口径行 + 总计 /
// 按供应商或按租户 / 按人员 / 按模型 / 按天 分节。
func usageRows(rep *RangeReport) [][]cell {
	rows := make([][]cell, 0, 64)

	title := "供应商对帐报表（用量）"
	if rep.View == ViewInternal {
		title = "内部对帐报表（用量）"
	}
	rows = append(rows,
		[]cell{boldStrCell(title)},
		[]cell{strCell("区间: " + rep.Start.Format("2006-01-02") + " ~ " + rep.End.Format("2006-01-02")),
			strCell("生成时间: " + time.Now().UTC().Format("2006-01-02 15:04:05 UTC")),
			strCell("口径: " + caliberText(rep.View))},
		[]cell{},
	)

	// ---- 总计 ----
	rows = append(rows, []cell{boldStrCell("总计")})
	rows = append(rows, usageHeader(rep.View))
	totalKey := "全部"
	if rep.View == ViewInternal {
		totalKey = "全部租户（业务流量）"
	}
	rows = append(rows, usageDataRow(rep.View, totalKey, "", "", rep.Totals))
	rows = append(rows, []cell{})

	if rep.View == ViewProvider {
		// ---- 按供应商 ----
		rows = append(rows, []cell{boldStrCell("按供应商")})
		rows = append(rows, usageHeader(rep.View))
		for _, p := range rep.Providers {
			rows = append(rows, usageDataRow(rep.View, fmt.Sprintf("%d", p.ProviderID), p.ProviderName, "", p.Totals))
		}
		rows = append(rows, []cell{})
	} else {
		// ---- 按租户 ----
		rows = append(rows, []cell{boldStrCell("按租户")})
		rows = append(rows, usageHeader(rep.View))
		for _, t := range rep.Tenants {
			rows = append(rows, usageDataRow(rep.View, t.TenantID, "", "", t.Totals))
		}
		rows = append(rows, []cell{})
		// ---- 按人员 ----
		rows = append(rows, []cell{boldStrCell("按人员")})
		rows = append(rows, usageHeader(rep.View))
		for _, p := range rep.Persons {
			rows = append(rows, usageDataRow(rep.View, p.TenantID, "", p.Person, p.Totals))
		}
		rows = append(rows, []cell{})
	}

	// ---- 按模型 ----
	rows = append(rows, []cell{boldStrCell("按模型")})
	rows = append(rows, usageHeader(rep.View))
	for _, m := range rep.Models {
		provider := ""
		pid := ""
		if m.ProviderID != nil {
			pid = fmt.Sprintf("%d", *m.ProviderID)
			provider = m.ProviderName
		}
		rows = append(rows, usageDataRow(rep.View, pid, provider, m.RawModelName, m.Totals))
	}
	rows = append(rows, []cell{})

	// ---- 按天 ----
	rows = append(rows, []cell{boldStrCell("按天")})
	rows = append(rows, usageHeader(rep.View))
	for _, d := range rep.Days {
		rows = append(rows, usageDataRow(rep.View, d.Date, "", "", d.Totals))
	}
	return rows
}

func caliberText(view View) string {
	if view == ViewProvider {
		return "全部流量（含探针/自检），供应商成本口径"
	}
	return "业务流量，内部计费口径（积分 × 快照冻结单价）"
}

// usageHeader 用量分节表头。双视图列差异：internal 增加积分 / 内部成本。
func usageHeader(view View) []cell {
	base := []cell{boldStrCell("维度"), boldStrCell("名称/ID"), boldStrCell("第二维度")}
	base = append(base,
		boldStrCell("请求数"), boldStrCell("成功"), boldStrCell("失败"), boldStrCell("失败率"),
		boldStrCell("输入tokens"), boldStrCell("输出tokens"), boldStrCell("缓存读tokens"),
		boldStrCell("缓存写tokens"), boldStrCell("总tokens"),
		boldStrCell("供应商成本(分)"), boldStrCell("供应商成本"), boldStrCell("币种"),
	)
	if view == ViewInternal {
		base = append(base, boldStrCell("内部积分"), boldStrCell("内部成本(分)"), boldStrCell("内部成本(元)"))
	}
	return base
}

// usageDataRow 一行用量数据。dims: key / 名称 / 第二维度（人员或模型名）。
func usageDataRow(view View, key, name, second string, t Totals) []cell {
	row := []cell{strCell(key), strCell(name), strCell(second)}
	row = append(row,
		intCell(t.RequestCount), intCell(t.SuccessCount), intCell(t.ErrorCount), numCell(t.ErrorRate),
		intCell(t.InputTokens), intCell(t.OutputTokens), intCell(t.CacheReadTokens),
		intCell(t.CacheWriteTokens), intCell(t.TotalTokens),
		intCell(t.EstimatedCostCents), numCell(centsToUnits(t.EstimatedCostCents)), strCell(t.Currency),
	)
	if view == ViewInternal {
		row = append(row,
			intCell(t.CreditsCharged),
			numCell(t.InternalCostCents),
			numCell(centsToUnitsF(t.InternalCostCents)),
		)
	}
	return row
}

// qualityRows 生成 sheet2（模型质量与错误分析）：模型粒度成功/失败、
// 延迟分位、缓存命中 + 错误按 error_kind 动态列透视。
func qualityRows(rep *RangeReport, withProvider bool) [][]cell {
	rows := make([][]cell, 0, 32)
	title := "模型质量与错误分析（内部口径）"
	if withProvider {
		title = "模型质量与错误分析（供应商口径）"
	}
	rows = append(rows,
		[]cell{boldStrCell(title)},
		[]cell{strCell("区间: " + rep.Start.Format("2006-01-02") + " ~ " + rep.End.Format("2006-01-02"))},
		[]cell{},
	)

	// 收集区间内出现过的 error_kind，按总次数降序作动态列。
	kindTotals := map[string]int64{}
	var allBreakdowns []map[string]int64
	allBreakdowns = append(allBreakdowns, breakdownsOfModels(rep.Models)...)
	allBreakdowns = append(allBreakdowns, breakdownsOfProviders(rep.Providers)...)
	allBreakdowns = append(allBreakdowns, breakdownsOfTenants(rep.Tenants)...)
	for _, bd := range allBreakdowns {
		for k, v := range bd {
			kindTotals[k] += v
		}
	}
	kinds := make([]string, 0, len(kindTotals))
	for k := range kindTotals {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kindTotals[kinds[i]] > kindTotals[kinds[j]] })

	header := []cell{boldStrCell("维度"), boldStrCell("名称"), boldStrCell("模型")}
	header = append(header,
		boldStrCell("请求数"), boldStrCell("成功"), boldStrCell("失败"), boldStrCell("失败率"),
		boldStrCell("P50延迟(ms)"), boldStrCell("P95延迟(ms)"), boldStrCell("缓存命中率"),
		boldStrCell("错误合计"),
	)
	for _, k := range kinds {
		header = append(header, boldStrCell("错误:"+k))
	}
	rows = append(rows, header)

	emit := func(dim, name, model string, t Totals, br map[string]int64) {
		row := []cell{strCell(dim), strCell(name), strCell(model)}
		row = append(row,
			intCell(t.RequestCount), intCell(t.SuccessCount), intCell(t.ErrorCount), numCell(t.ErrorRate),
			numCell(t.LatencyP50Ms), numCell(t.LatencyP95Ms),
		)
		if t.CacheHitRatio != nil {
			row = append(row, numCell(*t.CacheHitRatio))
		} else {
			row = append(row, strCell(""))
		}
		errTotal := int64(0)
		for _, v := range br {
			errTotal += v
		}
		row = append(row, intCell(errTotal))
		for _, k := range kinds {
			row = append(row, intCell(br[k]))
		}
		rows = append(rows, row)
	}

	// 总计行 + 模型行；供应商视角补供应商维度。
	if withProvider {
		emit("总计", "全部", "", rep.Totals, rep.ErrorBreakdown)
		for _, m := range rep.Models {
			name := m.ProviderName
			if name == "" && m.ProviderID != nil {
				name = fmt.Sprintf("%d", *m.ProviderID)
			}
			emit("供应商×模型", name, m.RawModelName, m.Totals, m.ErrorBreakdown)
		}
	} else {
		emit("总计", "全部租户（业务流量）", "", rep.Totals, rep.ErrorBreakdown)
		for _, t := range rep.Tenants {
			emit("租户", t.TenantID, "", t.Totals, t.ErrorBreakdown)
		}
		for _, m := range rep.Models {
			emit("模型", "", m.RawModelName, m.Totals, m.ErrorBreakdown)
		}
		for _, p := range rep.Persons {
			emit("人员", p.Person, "", p.Totals, p.ErrorBreakdown)
		}
	}
	return rows
}

func qualityWidths(rep *RangeReport, withProvider bool) []float64 {
	w := []float64{14, 20, 24, 10, 10, 10, 10, 12, 12, 12, 10}
	kinds := 0
	for _, m := range rep.Models {
		if len(m.ErrorBreakdown) > kinds {
			kinds = len(m.ErrorBreakdown)
		}
	}
	for i := 0; i < kinds; i++ {
		w = append(w, 12)
	}
	_ = withProvider
	return w
}

// BuildWorkbookBytes 按报表视角渲染 xlsx 字节流（admin 导出端点入口）。
func BuildWorkbookBytes(rep *RangeReport) ([]byte, error) {
	var sheets []sheetSheet
	if rep.View == ViewInternal {
		sheets = internalWorkbook(rep)
	} else {
		sheets = providerWorkbook(rep)
	}
	return buildWorkbookBytes(sheets)
}

func breakdownsOfModels(ms []ModelRow) []map[string]int64 {
	out := make([]map[string]int64, 0, len(ms))
	for i := range ms {
		out = append(out, ms[i].ErrorBreakdown)
	}
	return out
}

func breakdownsOfProviders(ps []ProviderRow) []map[string]int64 {
	out := make([]map[string]int64, 0, len(ps))
	for i := range ps {
		out = append(out, ps[i].ErrorBreakdown)
	}
	return out
}

func breakdownsOfTenants(ts []TenantRow) []map[string]int64 {
	out := make([]map[string]int64, 0, len(ts))
	for i := range ts {
		out = append(out, ts[i].ErrorBreakdown)
	}
	return out
}

// centsToUnits 分 → 元/美元主单位（两位小数语义，float 显示）。
func centsToUnits(cents int64) float64 {
	if cents == 0 {
		return 0
	}
	return float64(cents) / 100
}

func centsToUnitsF(cents float64) float64 {
	return cents / 100
}

// ExportFilename 导出文件名（不含路径）。
func ExportFilename(view View, start, end time.Time) string {
	prefix := "reconciliation_provider"
	if view == ViewInternal {
		prefix = "reconciliation_internal"
	}
	return fmt.Sprintf("%s_%s_%s.xlsx", prefix,
		strings.ReplaceAll(start.Format("2006-01-02"), "-", ""),
		strings.ReplaceAll(end.Format("2006-01-02"), "-", ""))
}
