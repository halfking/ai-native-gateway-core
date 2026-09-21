// annotation/exporter_test.go — 2026-09-06
//
// P2.1导出器单元测试
// P2.2 Track D: 主动学习不确定性采样（配额分配纯函数、CSV header兼容）用例

package annotation

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDateRange(t *testing.T) {
	tests := []struct {
		name      string
		start     string
		end       string
		wantError bool
		checkFunc func(*testing.T, time.Time, time.Time)
	}{
		{
			name:      "valid date range",
			start:     "2026-08-01",
			end:       "2026-09-01",
			wantError: false,
			checkFunc: func(t *testing.T, start, end time.Time) {
				expectedStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
				expectedEnd := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC) // +1 day

				if !start.Equal(expectedStart) {
					t.Errorf("start = %v, want %v", start, expectedStart)
				}
				if !end.Equal(expectedEnd) {
					t.Errorf("end = %v, want %v", end, expectedEnd)
				}
			},
		},
		{
			name:      "invalid start date format",
			start:     "2026/08/01",
			end:       "2026-09-01",
			wantError: true,
		},
		{
			name:      "invalid end date format",
			start:     "2026-08-01",
			end:       "2026/09/01",
			wantError: true,
		},
		{
			name:      "start after end",
			start:     "2026-09-01",
			end:       "2026-08-01",
			wantError: true,
		},
		{
			name:      "start equals end",
			start:     "2026-08-01",
			end:       "2026-08-01",
			wantError: false, // 允许相等，表示一天的数据
			checkFunc: func(t *testing.T, start, end time.Time) {
				expectedStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
				expectedEnd := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC) // +1 day

				if !start.Equal(expectedStart) {
					t.Errorf("start = %v, want %v", start, expectedStart)
				}
				if !end.Equal(expectedEnd) {
					t.Errorf("end = %v, want %v", end, expectedEnd)
				}
			},
		},
		{
			name:      "empty start",
			start:     "",
			end:       "2026-09-01",
			wantError: true,
		},
		{
			name:      "empty end",
			start:     "2026-08-01",
			end:       "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, err := ParseDateRange(tt.start, tt.end)
			if (err != nil) != tt.wantError {
				t.Errorf("ParseDateRange() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if err == nil && tt.checkFunc != nil {
				tt.checkFunc(t, start, end)
			}
		})
	}
}

func TestExportConfig_Validation(t *testing.T) {
	// 测试导出配置的有效性
	tests := []struct {
		name   string
		config ExportConfig
		valid  bool
	}{
		{
			name: "valid config with min/max confidence",
			config: ExportConfig{
				StartDate:     time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
				EndDate:       time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				MinConfidence: ptrFloat64(0.0),
				MaxConfidence: ptrFloat64(0.7),
				Limit:         1000,
				OutputPath:    "/tmp/annotations.csv",
			},
			valid: true,
		},
		{
			name: "valid config without confidence filters",
			config: ExportConfig{
				StartDate:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
				EndDate:    time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				Limit:      1000,
				OutputPath: "/tmp/annotations.csv",
			},
			valid: true,
		},
		{
			name: "zero limit",
			config: ExportConfig{
				StartDate:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
				EndDate:    time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				Limit:      0,
				OutputPath: "/tmp/annotations.csv",
			},
			valid: true, // 0表示不限制
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 这里只是验证配置可以被创建，实际验证在导出时进行
			if tt.config.OutputPath == "" && tt.valid {
				t.Error("valid config should have output path")
			}
		})
	}
}

// ptrFloat64 返回float64的指针
func ptrFloat64(v float64) *float64 {
	return &v
}

// ---------------------------------------------------------------------------
// P2.2 Track D: 主动学习不确定性采样测试
// ---------------------------------------------------------------------------

// mkCand 构造配额分配纯函数的候选样本
func mkCand(id, taskType string, conf float64) SamplingCandidate {
	return SamplingCandidate{RequestID: id, TaskType: taskType, Confidence: conf}
}

// countByType 按task_type统计选中样本数
func countByType(cands []SamplingCandidate) map[string]int {
	counts := make(map[string]int)
	for _, c := range cands {
		counts[c.TaskType]++
	}
	return counts
}

// tierOfConfidence 与 SelectUncertainSamples 相同的三层分类（测试用独立实现）
func tierOfConfidence(conf, low, high float64) int {
	switch {
	case conf >= low && conf < high:
		return 0
	case conf < low:
		return 1
	default:
		return 2
	}
}

// TestSelectUncertainSamples_BasicAllocation 正常分配:
// 两类型样本充足，保底配额+全局回填后总量等于limit，各类型不低于保底。
func TestSelectUncertainSamples_BasicAllocation(t *testing.T) {
	var candidates []SamplingCandidate
	// chat: 距0.5由近到远
	for i, conf := range []float64{0.50, 0.49, 0.51, 0.48, 0.52, 0.47, 0.46, 0.45} {
		candidates = append(candidates, mkCand(fmt.Sprintf("A%d", i+1), "chat", conf))
	}
	// summarize: 距0.5由近到远
	for i, conf := range []float64{0.55, 0.56, 0.54, 0.57, 0.53, 0.58, 0.52, 0.59} {
		candidates = append(candidates, mkCand(fmt.Sprintf("B%d", i+1), "summarize", conf))
	}

	opts := UncertainSamplingOptions{UncertainLow: 0.4, UncertainHigh: 0.6, MinPerTaskType: 3}
	selected := SelectUncertainSamples(candidates, 10, opts)

	if len(selected) != 10 {
		t.Fatalf("len(selected) = %d, want 10", len(selected))
	}
	// 保底阶段每类型按"最不确定优先"出队: chat出A1,A2,A3; summarize出
	// B7(0.52),B5(0.53),B3(0.54)。剩余4个名额按全局优先级回填chat的
	// A4,A5,A6,A7 → chat=7, summarize=3, 双方均不低于保底3。
	counts := countByType(selected)
	if counts["chat"] != 7 {
		t.Errorf("chat count = %d, want 7", counts["chat"])
	}
	if counts["summarize"] != 3 {
		t.Errorf("summarize count = %d, want 3", counts["summarize"])
	}
	for ty, n := range counts {
		if n < 3 {
			t.Errorf("task_type %s got %d samples, below floor quota 3", ty, n)
		}
	}
	for _, c := range selected {
		if tierOfConfidence(c.Confidence, 0.4, 0.6) != 0 {
			t.Errorf("selected %s conf %.2f not in uncertain band [0.4,0.6)", c.RequestID, c.Confidence)
		}
	}
}

// TestSelectUncertainSamples_BackfillFromOtherTypes 某类型不足时回填其他类型:
// summarize只有2条（<保底5），缺口自然回流给chat，总量仍达到limit。
func TestSelectUncertainSamples_BackfillFromOtherTypes(t *testing.T) {
	var candidates []SamplingCandidate
	for i, conf := range []float64{0.50, 0.49, 0.51, 0.48, 0.52, 0.47, 0.46, 0.45, 0.44, 0.43} {
		candidates = append(candidates, mkCand(fmt.Sprintf("A%d", i+1), "chat", conf))
	}
	candidates = append(candidates, mkCand("S1", "summarize", 0.42))
	candidates = append(candidates, mkCand("S2", "summarize", 0.43))

	opts := UncertainSamplingOptions{UncertainLow: 0.4, UncertainHigh: 0.6, MinPerTaskType: 5}
	selected := SelectUncertainSamples(candidates, 8, opts)

	if len(selected) != 8 {
		t.Fatalf("len(selected) = %d, want 8 (budget must be filled by backfill)", len(selected))
	}
	counts := countByType(selected)
	if counts["summarize"] != 2 {
		t.Errorf("summarize count = %d, want 2 (all its samples)", counts["summarize"])
	}
	if counts["chat"] != 6 {
		t.Errorf("chat count = %d, want 6 (backfilled from chat)", counts["chat"])
	}

	ids := make(map[string]bool)
	for _, c := range selected {
		ids[c.RequestID] = true
	}
	for _, want := range []string{"S1", "S2"} {
		if !ids[want] {
			t.Errorf("expected minority-type sample %s in selected set", want)
		}
	}
}

// TestSelectUncertainSamples_MidBandPriority 置信度0.4–0.6优先于<0.4;
// 且≥0.6（tier2）仅在两者不足时兜底。
func TestSelectUncertainSamples_MidBandPriority(t *testing.T) {
	candidates := []SamplingCandidate{
		mkCand("mid1", "chat", 0.50),
		mkCand("mid2", "chat", 0.49),
		mkCand("mid3", "chat", 0.51),
		mkCand("mid4", "chat", 0.48),
		mkCand("mid5", "chat", 0.52),
		mkCand("low1", "chat", 0.30),
		mkCand("low2", "chat", 0.20),
		mkCand("low3", "chat", 0.10),
		mkCand("low4", "chat", 0.05),
		mkCand("low5", "chat", 0.35),
		mkCand("high1", "chat", 0.70),
		mkCand("high2", "chat", 0.65),
		mkCand("high3", "chat", 0.80),
	}
	opts := UncertainSamplingOptions{UncertainLow: 0.4, UncertainHigh: 0.6, MinPerTaskType: 0}

	// limit=5: 只选0.4–0.6区间，低置信与高置信都不选
	selected := SelectUncertainSamples(candidates, 5, opts)
	if len(selected) != 5 {
		t.Fatalf("len(selected) = %d, want 5", len(selected))
	}
	for _, c := range selected {
		if c.Confidence < 0.4 || c.Confidence >= 0.6 {
			t.Errorf("tier1 sample %s conf %.2f selected before mid-band exhausted", c.RequestID, c.Confidence)
		}
	}

	// limit=7: 中区间5条选完后回填<0.4，且回填顺序按越接近0.5越优先
	selected = SelectUncertainSamples(candidates, 7, opts)
	gotIDs := []string{}
	for _, c := range selected {
		gotIDs = append(gotIDs, c.RequestID)
	}
	wantIDs := []string{"mid1", "mid2", "mid3", "mid4", "mid5", "low5", "low1"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("backfill order = %v, want %v", gotIDs, wantIDs)
	}

	// limit=11: tier0+tier1共10条全部入选后，最后1个名额才从tier2兜底
	selected = SelectUncertainSamples(candidates, 11, opts)
	counts := make(map[int]int)
	for _, c := range selected {
		counts[tierOfConfidence(c.Confidence, 0.4, 0.6)]++
	}
	if counts[0] != 5 || counts[1] != 5 || counts[2] != 1 {
		t.Errorf("limit=11 tier counts = %v, want map[0:5 1:5 2:1]", counts)
	}
	// 输出顺序中tier必须单调不减（tier2只在所有中低区间之后）
	lastTier := 0
	for _, c := range selected {
		tier := tierOfConfidence(c.Confidence, 0.4, 0.6)
		if tier < lastTier {
			t.Errorf("tier order violated: %s (tier %d) appears after tier %d", c.RequestID, tier, lastTier)
		}
		lastTier = tier
	}
}

// TestSelectUncertainSamples_ConfidenceBoundaries 区间边界语义:
// [0.4, 0.6)为tier0，0.6整属于tier2兜底。
func TestSelectUncertainSamples_ConfidenceBoundaries(t *testing.T) {
	candidates := []SamplingCandidate{
		mkCand("at_low", "chat", 0.40),
		mkCand("at_high", "chat", 0.60),
		mkCand("below", "chat", 0.39),
	}
	opts := UncertainSamplingOptions{UncertainLow: 0.4, UncertainHigh: 0.6, MinPerTaskType: 0}

	selected := SelectUncertainSamples(candidates, 1, opts)
	if len(selected) != 1 || selected[0].RequestID != "at_low" {
		t.Fatalf("limit=1 should select boundary 0.40 first, got %v", selected)
	}

	selected = SelectUncertainSamples(candidates, 2, opts)
	if len(selected) != 2 || selected[0].RequestID != "at_low" || selected[1].RequestID != "below" {
		t.Fatalf("limit=2 should order 0.40 (tier0) before 0.39 (tier1 backfill), got %v", selected)
	}

	// 0.60（tier2）仅在前两层耗尽后兜底入选
	selected = SelectUncertainSamples(candidates, 3, opts)
	if len(selected) != 3 || selected[2].RequestID != "at_high" {
		t.Fatalf("limit=3 should include 0.60 as last-resort backfill, got %v", selected)
	}
}

// TestSelectUncertainSamples_TotalShortageKeepsAll 总量不足全收
func TestSelectUncertainSamples_TotalShortageKeepsAll(t *testing.T) {
	candidates := []SamplingCandidate{
		mkCand("c1", "chat", 0.50),
		mkCand("c2", "chat", 0.20),
		mkCand("s1", "summarize", 0.65),
		mkCand("s2", "summarize", 0.45),
		mkCand("e1", "extract", 0.05),
		mkCand("e2", "extract", 0.55),
	}

	selected := SelectUncertainSamples(candidates, 100, DefaultUncertainSamplingOptions())
	if len(selected) != len(candidates) {
		t.Fatalf("len(selected) = %d, want %d (all kept when total < limit)", len(selected), len(candidates))
	}
	want := make(map[string]bool)
	for _, c := range candidates {
		want[c.RequestID] = true
	}
	for _, c := range selected {
		if !want[c.RequestID] {
			t.Errorf("unexpected sample %s in result", c.RequestID)
		}
		delete(want, c.RequestID)
	}
	if len(want) != 0 {
		t.Errorf("missing samples: %v", want)
	}
}

// TestSelectUncertainSamples_EmptyInput 空输入返回空集合且不panic
func TestSelectUncertainSamples_EmptyInput(t *testing.T) {
	opts := DefaultUncertainSamplingOptions()
	if got := SelectUncertainSamples(nil, 10, opts); len(got) != 0 {
		t.Errorf("nil input: got %v, want empty", got)
	}
	if got := SelectUncertainSamples([]SamplingCandidate{}, 10, opts); len(got) != 0 {
		t.Errorf("empty input: got %v, want empty", got)
	}
	if got := SelectUncertainSamples(nil, 0, opts); len(got) != 0 {
		t.Errorf("nil input, unlimited: got %v, want empty", got)
	}
}

// TestSelectUncertainSamples_SingleTypeNotSkewed 单一类型不偏斜:
// chat占绝对多数时，少数类型仍拿到保底配额，不被淹没。
func TestSelectUncertainSamples_SingleTypeNotSkewed(t *testing.T) {
	var candidates []SamplingCandidate
	for i := 0; i < 100; i++ {
		candidates = append(candidates, mkCand(fmt.Sprintf("a%03d", i+1), "chat", 0.50))
	}
	for i := 1; i <= 5; i++ {
		candidates = append(candidates, mkCand(fmt.Sprintf("s%02d", i), "summarize", 0.45))
	}
	for i := 1; i <= 5; i++ {
		candidates = append(candidates, mkCand(fmt.Sprintf("e%02d", i), "extract", 0.55))
	}

	opts := UncertainSamplingOptions{UncertainLow: 0.4, UncertainHigh: 0.6, MinPerTaskType: 5}
	selected := SelectUncertainSamples(candidates, 20, opts)

	if len(selected) != 20 {
		t.Fatalf("len(selected) = %d, want 20", len(selected))
	}
	counts := countByType(selected)
	if counts["chat"] != 10 {
		t.Errorf("chat count = %d, want 10 (majority type capped by budget)", counts["chat"])
	}
	if counts["summarize"] != 5 {
		t.Errorf("summarize count = %d, want 5 (floor quota honored)", counts["summarize"])
	}
	if counts["extract"] != 5 {
		t.Errorf("extract count = %d, want 5 (floor quota honored)", counts["extract"])
	}
}

// TestSelectUncertainSamples_UnlimitedReturnsAllTierOrdered limit<=0不限制:
// 全部候选按tier0→tier1→tier2排序返回。
func TestSelectUncertainSamples_UnlimitedReturnsAllTierOrdered(t *testing.T) {
	candidates := []SamplingCandidate{
		mkCand("low1", "chat", 0.30),
		mkCand("mid1", "chat", 0.50),
		mkCand("high1", "chat", 0.70),
		mkCand("low5", "chat", 0.35),
		mkCand("mid2", "chat", 0.49),
		mkCand("high2", "chat", 0.65),
		mkCand("low2", "chat", 0.20),
		mkCand("mid3", "chat", 0.51),
		mkCand("high3", "chat", 0.80),
		mkCand("low3", "chat", 0.10),
		mkCand("mid4", "chat", 0.48),
		mkCand("low4", "chat", 0.05),
		mkCand("mid5", "chat", 0.52),
	}
	opts := UncertainSamplingOptions{UncertainLow: 0.4, UncertainHigh: 0.6, MinPerTaskType: 0}

	for _, limit := range []int{0, -1} {
		selected := SelectUncertainSamples(candidates, limit, opts)
		if len(selected) != len(candidates) {
			t.Fatalf("limit=%d: len(selected) = %d, want %d", limit, len(selected), len(candidates))
		}
		gotIDs := []string{}
		for _, c := range selected {
			gotIDs = append(gotIDs, c.RequestID)
		}
		wantIDs := []string{
			"mid1", "mid2", "mid3", "mid4", "mid5", // tier0, 距0.5升序
			"low5", "low1", "low2", "low3", "low4", // tier1
			"high2", "high1", "high3", // tier2
		}
		if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
			t.Errorf("limit=%d: order = %v, want %v", limit, gotIDs, wantIDs)
		}
	}
}

// TestSelectUncertainSamples_DoesNotMutateInput 纯函数不得修改输入切片
func TestSelectUncertainSamples_DoesNotMutateInput(t *testing.T) {
	candidates := []SamplingCandidate{
		mkCand("x1", "chat", 0.30),
		mkCand("x2", "chat", 0.50),
		mkCand("x3", "summarize", 0.70),
		mkCand("x4", "summarize", 0.10),
	}
	orig := append([]SamplingCandidate(nil), candidates...)

	SelectUncertainSamples(candidates, 3, DefaultUncertainSamplingOptions())

	for i := range candidates {
		if candidates[i] != orig[i] {
			t.Errorf("input mutated at index %d: got %+v, want %+v", i, candidates[i], orig[i])
		}
	}
}

// TestSelectUncertainSamples_Deterministic 相同输入两次调用结果完全一致
func TestSelectUncertainSamples_Deterministic(t *testing.T) {
	candidates := []SamplingCandidate{
		mkCand("d1", "chat", 0.50),
		mkCand("d2", "summarize", 0.49),
		mkCand("d3", "chat", 0.20),
		mkCand("d4", "extract", 0.65),
		mkCand("d5", "extract", 0.44),
		mkCand("d6", "summarize", 0.33),
	}
	opts := DefaultUncertainSamplingOptions()

	first := SelectUncertainSamples(candidates, 4, opts)
	second := SelectUncertainSamples(candidates, 4, opts)
	if len(first) != len(second) {
		t.Fatalf("lengths differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("non-deterministic at %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// TestSelectUncertainSamples_ZeroOptionsFallBackToDefaults 零值opts安全回退默认区间
func TestSelectUncertainSamples_ZeroOptionsFallBackToDefaults(t *testing.T) {
	candidates := []SamplingCandidate{
		mkCand("mid", "chat", 0.50),
		mkCand("low", "chat", 0.20),
	}
	selected := SelectUncertainSamples(candidates, 1, UncertainSamplingOptions{})
	if len(selected) != 1 || selected[0].RequestID != "mid" {
		t.Fatalf("zero opts should fall back to band [0.4,0.6) and select 'mid', got %v", selected)
	}
}

// TestExportCSVHeader_Compatibility CSV header兼容（P2.1 importer契约）:
// 不确定性采样与现有模式共用同一写出路径，header逐字段一致、
// 含importer全部必需列、且绝不包含prompt/messages/response隐私列。
func TestExportCSVHeader_Compatibility(t *testing.T) {
	// 1. 与P2.1原实现逐字段一致（顺序+名称）
	expected := []string{
		"request_id", "model_name", "task_type", "prompt_tokens",
		"is_streaming", "has_vision", "region", "profile",
		"auto_provider", "confidence",
		"human_provider", "is_correct", "reason", "annotator",
	}
	if len(exportCSVHeader) != len(expected) {
		t.Fatalf("header has %d columns, want %d", len(exportCSVHeader), len(expected))
	}
	for i := range expected {
		if exportCSVHeader[i] != expected[i] {
			t.Errorf("header[%d] = %q, want %q", i, exportCSVHeader[i], expected[i])
		}
	}

	// 2. 满足importer的必需列校验
	if err := validateHeader(exportCSVHeader); err != nil {
		t.Fatalf("importer validateHeader failed on export header: %v", err)
	}

	// 3. 隐私红线: 不得出现prompt/messages/response内容列
	//    （prompt_tokens是P2.1冻结的合法特征列——token计数而非内容；
	//      步骤1的逐字节一致断言本身即保证列集合未被扩大）
	bannedColumns := map[string]bool{
		"prompt": true, "messages": true, "response": true, "completion": true,
	}
	for _, col := range exportCSVHeader {
		if bannedColumns[strings.ToLower(col)] {
			t.Errorf("privacy violation: header contains banned content column %q", col)
		}
	}

	// 4. 端到端: 不确定性采样选出的样本经writeAnnotationCSV写出后，
	//    人工补填标注列即可通过importer.Validate（header按列名定位）。
	rows := []CSVAnnotationRow{
		{RequestID: "req_001", ModelName: "gpt-4", TaskType: "chat", AutoProvider: "openai", Confidence: 0.45},
		{RequestID: "req_002", ModelName: "gpt-4", TaskType: "summarize", AutoProvider: "openai", Confidence: 0.52},
		{RequestID: "req_003", ModelName: "claude", TaskType: "extract", AutoProvider: "anthropic", Confidence: 0.38},
	}
	// 模拟uncertain模式: 从行构造候选→配额分配→回选
	var candidates []SamplingCandidate
	for _, r := range rows {
		candidates = append(candidates, SamplingCandidate{
			RequestID: r.RequestID, TaskType: r.TaskType, Confidence: r.Confidence,
		})
	}
	selected := SelectUncertainSamples(candidates, 2, DefaultUncertainSamplingOptions())
	byID := make(map[string]CSVAnnotationRow)
	for _, r := range rows {
		byID[r.RequestID] = r
	}
	selectedRows := make([]CSVAnnotationRow, 0, len(selected))
	for _, c := range selected {
		selectedRows = append(selectedRows, byID[c.RequestID])
	}

	exportPath := filepath.Join(t.TempDir(), "uncertain_export.csv")
	if err := writeAnnotationCSV(exportPath, selectedRows); err != nil {
		t.Fatalf("writeAnnotationCSV failed: %v", err)
	}

	// 读回文件，断言首行header与exportCSVHeader逐字节一致
	file, err := os.Open(exportPath)
	if err != nil {
		t.Fatalf("open exported csv failed: %v", err)
	}
	reader := csv.NewReader(file)
	record, err := reader.Read()
	file.Close()
	if err != nil {
		t.Fatalf("read header from exported csv failed: %v", err)
	}
	if len(record) != len(exportCSVHeader) {
		t.Fatalf("exported header has %d fields, want %d", len(record), len(exportCSVHeader))
	}
	for i := range exportCSVHeader {
		if record[i] != exportCSVHeader[i] {
			t.Errorf("exported header[%d] = %q, want %q", i, record[i], exportCSVHeader[i])
		}
	}

	// 模拟标注人员补填后应能通过importer校验
	filledPath := filepath.Join(t.TempDir(), "filled.csv")
	in, err := os.Open(exportPath)
	if err != nil {
		t.Fatalf("reopen exported csv failed: %v", err)
	}
	r := csv.NewReader(in)
	all, err := r.ReadAll()
	in.Close()
	if err != nil {
		t.Fatalf("read exported csv failed: %v", err)
	}
	for i := 1; i < len(all); i++ {
		all[i][10] = "anthropic" // human_provider
		all[i][11] = "TRUE"      // is_correct
		all[i][12] = "correct"   // reason
		all[i][13] = "alice"     // annotator
	}
	out, err := os.Create(filledPath)
	if err != nil {
		t.Fatalf("create filled csv failed: %v", err)
	}
	w := csv.NewWriter(out)
	if err := w.WriteAll(all); err != nil {
		out.Close()
		t.Fatalf("write filled csv failed: %v", err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		out.Close()
		t.Fatalf("flush filled csv failed: %v", err)
	}
	out.Close()

	importer := NewImporter(nil) // Validate不访问数据库
	result, err := importer.Validate(context.Background(), filledPath)
	if err != nil {
		t.Fatalf("importer.Validate failed: %v", err)
	}
	if !result.IsValid {
		for _, e := range result.Errors {
			t.Errorf("validation error: row %d: %s", e.Row, e.Message)
		}
		t.Fatalf("exported CSV rejected by P2.1 importer")
	}
	if result.ValidRows != len(selectedRows) {
		t.Errorf("ValidRows = %d, want %d", result.ValidRows, len(selectedRows))
	}
}
