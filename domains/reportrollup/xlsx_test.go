package reportrollup

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"
)

// TestWriteWorkbook_RoundTrip —— xlsx 回读测试：zip 解包 + XML 解析双 sheet、
// 单元格类型/样式、中文 sheet 名、特殊字符转义。writer 是手写的，此测试是
// 正确性的唯一机器守卫（无 excelize 可对照）。
func TestWriteWorkbook_RoundTrip(t *testing.T) {
	sheets := []sheetSheet{
		{
			name:   SheetUsage,
			widths: []float64{14, 20, 12},
			rows: [][]cell{
				{boldStrCell("用量标题 <A&B> \"引号\"")},
				{strCell("文本&<>\"'"), intCell(42), numCell(3.14)},
				{strCell(""), intCell(-7), numCell(0)},
			},
		},
		{
			name: SheetQuality,
			rows: [][]cell{
				{boldStrCell("质量"), strCell("x")},
			},
		},
	}
	raw, err := buildWorkbookBytes(sheets)
	if err != nil {
		t.Fatalf("buildWorkbookBytes: %v", err)
	}

	// ---- zip 结构 ----
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	byName := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		byName[f.Name] = data
	}
	for _, name := range []string{
		"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml",
		"xl/_rels/workbook.xml.rels", "xl/styles.xml",
		"xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml",
	} {
		if len(byName[name]) == 0 {
			t.Fatalf("missing zip member %s", name)
		}
	}

	// ---- workbook.xml：双 sheet 名 ----
	var workbook struct {
		Sheets []struct {
			Name    string `xml:"name,attr"`
			SheetID int    `xml:"sheetId,attr"`
			RID     string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(byName["xl/workbook.xml"], &workbook); err != nil {
		t.Fatalf("parse workbook.xml: %v", err)
	}
	if len(workbook.Sheets) != 2 {
		t.Fatalf("sheets = %d, want 2", len(workbook.Sheets))
	}
	if workbook.Sheets[0].Name != SheetUsage || workbook.Sheets[1].Name != SheetQuality {
		t.Errorf("sheet names = %q, %q; want %q, %q",
			workbook.Sheets[0].Name, workbook.Sheets[1].Name, SheetUsage, SheetQuality)
	}

	// ---- sheet1 内容：类型/数值/转义 ----
	var ws struct {
		Rows []struct {
			R int `xml:"r,attr"`
			C []struct {
				Ref   string `xml:"r,attr"`
				Style int    `xml:"s,attr"`
				Type  string `xml:"t,attr"`
				V     string `xml:"v"`
				Is    struct {
					T string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(byName["xl/worksheets/sheet1.xml"], &ws); err != nil {
		t.Fatalf("parse sheet1: %v", err)
	}
	if len(ws.Rows) != 3 {
		t.Fatalf("sheet1 rows = %d, want 3", len(ws.Rows))
	}
	// 行1 列A：粗体标题，转义后原文还原。
	h := ws.Rows[0].C[0]
	if h.Style != 1 || h.Is.T != "用量标题 <A&B> \"引号\"" {
		t.Errorf("header cell = style %d text %q; want style 1 with raw text preserved", h.Style, h.Is.T)
	}
	// 行2：inline string + 两个数值。
	r2 := ws.Rows[1].C
	if r2[0].Type != "inlineStr" || r2[0].Is.T != "文本&<>\"'" {
		t.Errorf("r2c0 = type %q text %q", r2[0].Type, r2[0].Is.T)
	}
	if r2[1].V != "42" || r2[1].Type != "" {
		t.Errorf("r2c1 = %q (type %q), want number 42", r2[1].V, r2[1].Type)
	}
	if r2[2].V != "3.14" {
		t.Errorf("r2c2 = %q, want 3.14", r2[2].V)
	}
	// 行3：负数与 0。
	if ws.Rows[2].C[1].V != "-7" || ws.Rows[2].C[2].V != "0" {
		t.Errorf("row3 numbers = %q, %q", ws.Rows[2].C[1].V, ws.Rows[2].C[2].V)
	}

	// ---- styles.xml：两个 cellXfs（默认+粗体）----
	styles := string(byName["xl/styles.xml"])
	if !strings.Contains(styles, `applyFont="1"`) {
		t.Errorf("styles.xml missing bold xf")
	}

	// ---- ContentTypes 声明两个 sheet ----
	ct := string(byName["[Content_Types].xml"])
	if !strings.Contains(ct, "sheet2.xml") {
		t.Errorf("[Content_Types].xml missing sheet2 override")
	}
}

// TestColName —— 0 起列号 → Excel 字母列名。
func TestColName(t *testing.T) {
	cases := map[int]string{0: "A", 1: "B", 25: "Z", 26: "AA", 27: "AB", 701: "ZZ", 702: "AAA"}
	for in, want := range cases {
		if got := colName(in); got != want {
			t.Errorf("colName(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestCacheHitRatio —— 分母 0 → nil；正常分式。
func TestCacheHitRatio(t *testing.T) {
	if cacheHitRatio(0, 0) != nil {
		t.Errorf("zero denom should return nil")
	}
	r := cacheHitRatio(25, 75)
	rf, ok := r.(float64)
	if !ok || rf != 0.25 {
		t.Errorf("cacheHitRatio(25,75) = %v, want 0.25", r)
	}
}

// TestFoldProviderBuckets —— provider×model 行折叠成 by_provider：
// 计数/token/成本/积分求和，error_kind 透视跨行合并，分位不折叠（置 0）。
func TestFoldProviderBuckets(t *testing.T) {
	pid := int64(7)
	modelRows := []Bucket{
		{
			Scope: ScopeDailyByModel, ScopeKey: "7", ProviderID: &pid,
			RawModelName: "gpt-x", RequestCount: 10, SuccessCount: 8,
			InputTokens: 100, OutputTokens: 200, CacheReadTokens: 50, CacheWriteTokens: 5,
			CreditsCharged: 30, CostCents: 99, Currency: "USD",
			LatencyP50Ms: 100, LatencyP95Ms: 900,
			ErrorKindBreakdown: map[string]int64{"timeout": 1, "auth": 1},
		},
		{
			Scope: ScopeDailyByModel, ScopeKey: "7", ProviderID: &pid,
			RawModelName: "gpt-y", RequestCount: 5, SuccessCount: 5,
			InputTokens: 10, OutputTokens: 20,
			CreditsCharged: 10, CostCents: 11, Currency: "USD",
			ErrorKindBreakdown: map[string]int64{"timeout": 0, "rate_limited": 2},
		},
	}
	folded := foldProviderBuckets(modelRows)
	if len(folded) != 1 {
		t.Fatalf("folded rows = %d, want 1", len(folded))
	}
	f := folded[0]
	if f.Scope != ScopeDailyByProvider || f.ScopeKey != "7" || f.ProviderID == nil || *f.ProviderID != 7 {
		t.Errorf("folded identity wrong: %+v", f)
	}
	if f.RequestCount != 15 || f.SuccessCount != 13 {
		t.Errorf("counts = %d/%d, want 15/13", f.RequestCount, f.SuccessCount)
	}
	if f.InputTokens != 110 || f.OutputTokens != 220 || f.CacheReadTokens != 50 || f.CacheWriteTokens != 5 {
		t.Errorf("token sums wrong: %+v", f)
	}
	if f.CostCents != 110 || f.CreditsCharged != 40 {
		t.Errorf("cost/credits = %d/%d, want 110/40", f.CostCents, f.CreditsCharged)
	}
	if f.ErrorKindBreakdown["timeout"] != 1 || f.ErrorKindBreakdown["rate_limited"] != 2 {
		t.Errorf("breakdown merge wrong: %v", f.ErrorKindBreakdown)
	}
	if f.LatencyP50Ms != 0 || f.LatencyP95Ms != 0 {
		t.Errorf("folded latency must be 0 (percentiles not foldable), got %d/%d", f.LatencyP50Ms, f.LatencyP95Ms)
	}
}

// TestInternalCents —— 快照冻结价折算：float64/int64 缺失值三态。
func TestInternalCents(t *testing.T) {
	s := Snapshot{CreditsCharged: 1000, PriceSnapshot: map[string]any{"cents_per_credit": 0.15}}
	if got := internalCents(s); got != 150 {
		t.Errorf("internalCents = %v, want 150", got)
	}
	s.PriceSnapshot = map[string]any{"cents_per_credit": int64(1)}
	if got := internalCents(s); got != 1000 {
		t.Errorf("internalCents int64 = %v, want 1000", got)
	}
	s.PriceSnapshot = map[string]any{}
	if got := internalCents(s); got != 0 {
		t.Errorf("missing price → 0, got %v", got)
	}
}

// TestExportFilename —— 文件名格式稳定（对帐双方邮件归档可读）。
func TestExportFilename(t *testing.T) {
	start := parseDay(t, "2026-09-01")
	end := parseDay(t, "2026-09-30")
	got := ExportFilename(ViewProvider, start, end)
	if got != "reconciliation_provider_20260901_20260930.xlsx" {
		t.Errorf("filename = %q", got)
	}
	got = ExportFilename(ViewInternal, start, end)
	if got != "reconciliation_internal_20260901_20260930.xlsx" {
		t.Errorf("internal filename = %q", got)
	}
}

func parseDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return d
}
