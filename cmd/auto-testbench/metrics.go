package main

// metrics.go — auto-testbench 的双层评测指标与回归门禁。
//
// 分类层（macro-F1/正确率）复用 cmd/autoclass-bench/report.go 的口径
// （precision/recall/f1 per label，macro = 未加权平均，错误样本不进分母）；
// GRRQ 是 v2 规划 §4.3 定稿的单值质量指标（首版权衡）：
//
//	GRRQ = 分类正确率×100 − 过配率×30
//	过配率 = 简单类用例被判到 tier-a 的比例（简单类默认 {chat}）
//
// tier 解析：离线模式使用 L1→tier 临时映射表（l1TierDefaults）。该表对齐
// taskprofile V3 档案的成本档语义（架构/审计/调试=tier-a，编码/重构=tier-b，
// 文档/总结=tier-c），仅作 GRRQ 首版的可计算锚点；P2 TierSelector 接线后
// 由运行时解析链（task_type_tier_config > 档案默认）替换（届时本文件同步
// 收敛，见 docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md §4.6）。

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

// labelStats accumulates per-label (per task type) confusion counts.
type labelStats struct{ tp, fp, fn int }

func (s labelStats) precision() float64 {
	if s.tp+s.fp == 0 {
		return 0
	}
	return float64(s.tp) / float64(s.tp+s.fp)
}

func (s labelStats) recall() float64 {
	if s.tp+s.fn == 0 {
		return 0
	}
	return float64(s.tp) / float64(s.tp+s.fn)
}

func (s labelStats) f1() float64 {
	p, r := s.precision(), s.recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// classificationMetrics is the classification-layer scoreboard.
type classificationMetrics struct {
	Total      int
	Correct    int
	PerLabel   map[string]labelStats // key: gold task type
	Confusion  map[string]map[string]int
	Accuracy   float64
	MacroF1    float64
	LabelF1    map[string]float64
	Failures   []caseFailure
	Generated  int // generated=true 候选行数（不进任何指标）
	KnownFails int
}

type caseFailure struct {
	Name, Expected, Got, Reason string
	Confidence                  float64
}

func newClassificationMetrics() *classificationMetrics {
	return &classificationMetrics{
		PerLabel:  map[string]labelStats{},
		Confusion: map[string]map[string]int{},
		LabelF1:   map[string]float64{},
	}
}

// add records one judged case. Gold labels drive the per-label key set so
// macro-F1 is computed over the classes the suite actually covers.
func (m *classificationMetrics) add(gold, got string, c suiteCase, conf float64, reason string) {
	if c.Generated {
		m.Generated++
		return
	}
	if c.KnownFailure {
		m.KnownFails++
	}
	m.Total++
	if gold == got {
		m.Correct++
		s := m.PerLabel[gold]
		s.tp++
		m.PerLabel[gold] = s
	} else {
		s := m.PerLabel[gold]
		s.fn++
		m.PerLabel[gold] = s
		p := m.PerLabel[got]
		p.fp++
		m.PerLabel[got] = p
		m.Failures = append(m.Failures, caseFailure{
			Name: c.Name, Expected: gold, Got: got,
			Confidence: conf, Reason: reason,
		})
	}
	if m.Confusion[gold] == nil {
		m.Confusion[gold] = map[string]int{}
	}
	m.Confusion[gold][got]++
}

// finalize derives Accuracy/MacroF1/LabelF1. Must be called after all add()s.
func (m *classificationMetrics) finalize() {
	if m.Total > 0 {
		m.Accuracy = float64(m.Correct) / float64(m.Total)
	}
	labels := make([]string, 0, len(m.PerLabel))
	for l := range m.PerLabel {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	sum := 0.0
	for _, l := range labels {
		f := m.PerLabel[l].f1()
		m.LabelF1[l] = f
		sum += f
	}
	// macro-F1 对"金标签 ∪ 被预测"的类集合求平均，与 autoclass-bench
	// report.macroF1（遍历 stats 全键）口径一致。
	if n := len(m.PerLabel); n > 0 {
		m.MacroF1 = sum / float64(n)
	}
}

// ─── tier 层与 GRRQ ──────────────────────────────────────────────────────

// l1TierDefaults is the provisional L1 → cost-tier anchor used by the
// offline GRRQ v1 (see file header). Values follow the taskprofile tier
// vocabulary (tier-a high-performance / tier-b standard / tier-c economy).
var l1TierDefaults = map[string]string{
	"reasoning":             "tier-a",
	"code_audit":            "tier-a",
	"planning":              "tier-a",
	"agent":                 "tier-a",
	"vision":                "tier-a",
	"code":                  "tier-b",
	"long_context":          "tier-b",
	"creative":              "tier-b",
	"function_call":         "tier-b",
	"intent_classification": "tier-c",
	"chat":                  "tier-c",
}

// resolveTier maps a (predicted) L1 task type to its tier intent. Unknown
// types resolve to tier-a — 保守方向：未知类不产生"过配"豁免。
func resolveTier(taskType string) string {
	if t, ok := l1TierDefaults[taskType]; ok {
		return t
	}
	return "tier-a"
}

// tierMetrics is the selection-layer (tier intent) scoreboard.
type tierMetrics struct {
	SimpleTotal       int
	OverProvisioned   int     // 简单类用例被判到 tier-a
	OverProvisionRate float64 // OverProvisioned / SimpleTotal
	TierDistribution  map[string]int
}

func (t *tierMetrics) add(gold string, c suiteCase, predictedTier string) {
	if c.Generated {
		return
	}
	t.TierDistribution[predictedTier]++
	if !isSimpleClass(gold) {
		return
	}
	t.SimpleTotal++
	if predictedTier == "tier-a" {
		t.OverProvisioned++
	}
}

func (t *tierMetrics) finalize() {
	if t.SimpleTotal > 0 {
		t.OverProvisionRate = float64(t.OverProvisioned) / float64(t.SimpleTotal)
	}
}

// simpleClassesDefault: 简单类 = chat（规划 §4.3 "过配率=简单任务落 tier-a
// 占比" 的首版口径；function_call 因 tools 存在按 OmniRoute 经验走硬升级，
// 不算简单类）。
var simpleClassesDefault = map[string]bool{"chat": true}

func isSimpleClass(taskType string) bool {
	return simpleClassesDefault[taskType]
}

// grrq computes the single-value quality metric:
//
//	GRRQ = accuracy×100 − overProvisionRate×30
//
// 权重 30 是 v2 规划 §4.3 定稿的首版惩罚系数（一个简单任务被顶到 tier-a
// 等价于 0.3 个分类错误的质量损失）。
func grrq(accuracy, overProvisionRate float64) float64 {
	return accuracy*100 - overProvisionRate*30
}

// ─── 基线与门禁 ──────────────────────────────────────────────────────────

// GateBaseline is the checked-in regression gate contract. Metrics are the
// observed values at write time; Thresholds are what future runs must meet.
type GateBaseline struct {
	GeneratedAt string   `json:"generated_at"`
	Classifier  string   `json:"classifier"`
	SuiteFiles  []string `json:"suite_files"`
	TotalCases  int      `json:"total_cases"`
	Metrics     struct {
		Accuracy          float64 `json:"accuracy"`
		MacroF1           float64 `json:"macro_f1"`
		GRRQ              float64 `json:"grrq"`
		OverProvisionRate float64 `json:"overprovision_rate"`
	} `json:"metrics"`
	Thresholds struct {
		MinAccuracy float64 `json:"min_accuracy"`
		MinMacroF1  float64 `json:"min_macro_f1"`
		MinGRRQ     float64 `json:"min_grrq"`
	} `json:"thresholds"`
}

// writeBaseline snapshots the current run as the gate floor. Thresholds are
// floored at 2 decimals so a later identical run can never fail its own
// baseline due to float formatting.
func writeBaseline(path, classifier string, suiteFiles []string, cm *classificationMetrics, tm *tierMetrics, g float64) error {
	b := GateBaseline{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Classifier:  classifier,
		SuiteFiles:  suiteFiles,
		TotalCases:  cm.Total,
	}
	b.Metrics.Accuracy = round3(cm.Accuracy)
	b.Metrics.MacroF1 = round3(cm.MacroF1)
	b.Metrics.GRRQ = round3(g)
	b.Metrics.OverProvisionRate = round3(tm.OverProvisionRate)
	b.Thresholds.MinAccuracy = floor2(cm.Accuracy)
	b.Thresholds.MinMacroF1 = floor2(cm.MacroF1)
	b.Thresholds.MinGRRQ = floor2(g)

	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// gateVerdict compares the current run against a baseline file.
type gateVerdict struct {
	Passed bool
	Lines  []string
}

func evaluateGate(b *GateBaseline, cm *classificationMetrics, g float64) gateVerdict {
	v := gateVerdict{Passed: true}
	check := func(name string, got, min float64) {
		line := fmt.Sprintf("%-14s got=%.4f  min=%.4f", name, got, min)
		if got < min {
			v.Passed = false
			line += "  ← BELOW"
		}
		v.Lines = append(v.Lines, line)
	}
	check("accuracy", cm.Accuracy, b.Thresholds.MinAccuracy)
	check("macro_f1", cm.MacroF1, b.Thresholds.MinMacroF1)
	check("grrq", g, b.Thresholds.MinGRRQ)
	return v
}

func loadBaseline(path string) (*GateBaseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b GateBaseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse baseline %s: %w", path, err)
	}
	return &b, nil
}

// validateBaseline enforces the baseline's semantic contract on top of the
// JSON syntax check (R64 P2): a syntactically-valid file with zeroed
// thresholds would gate nothing, and a stale case inventory (total_cases /
// suite_files) would compare apples to oranges. Any violation is a usage
// error (exit 2), not a gate failure.
func validateBaseline(b *GateBaseline, suiteFiles []string, totalCases int) error {
	t := b.Thresholds
	if t.MinAccuracy <= 0 {
		return fmt.Errorf("baseline thresholds.min_accuracy = %v, want > 0 (zero threshold gates nothing; refresh with -write-baseline)", t.MinAccuracy)
	}
	if t.MinMacroF1 <= 0 {
		return fmt.Errorf("baseline thresholds.min_macro_f1 = %v, want > 0 (zero threshold gates nothing; refresh with -write-baseline)", t.MinMacroF1)
	}
	if t.MinGRRQ <= 0 {
		return fmt.Errorf("baseline thresholds.min_grrq = %v, want > 0 (zero threshold gates nothing; refresh with -write-baseline)", t.MinGRRQ)
	}
	if b.TotalCases != totalCases {
		return fmt.Errorf("baseline total_cases = %d, current run has %d — suite inventory changed; refresh the baseline with -write-baseline", b.TotalCases, totalCases)
	}
	if !equalStringSets(b.SuiteFiles, suiteFiles) {
		return fmt.Errorf("baseline suite_files = %v, current -suite = %v — refresh the baseline with -write-baseline", b.SuiteFiles, suiteFiles)
	}
	return nil
}

// equalStringSets compares two string slices order-insensitively (the -suite
// flag is a comma-separated list; a different order is the same suite set).
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]string(nil), a...)
	sb := append([]string(nil), b...)
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// round3 rounds half away from zero via math.Round. The legacy
// int64(v*1000+0.5) truncated toward zero after the +0.5 bias, which bent
// negative values (GRRQ goes negative under heavy over-provision) upward.
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// floor2 floors to 2 decimals via math.Floor, so a written threshold can
// never exceed the metric that produced it — an identical re-run always
// passes its own baseline. The legacy int64 truncation moved toward zero,
// which RAISED negative GRRQ thresholds (tightened the gate silently).
func floor2(v float64) float64 { return math.Floor(v*100) / 100 }
