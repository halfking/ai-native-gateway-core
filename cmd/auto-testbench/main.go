// Command auto-testbench is the unified harness for the AUTO routing
// specialty tests (docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md §4.3, P0①).
//
// It orchestrates the two evaluation layers around the auto matching suites
// (owner: autoroute/testdata):
//
//	classification layer  — in-process heuristic regression over the suite
//	                        (same construction as the package-level
//	                        TestAutoMatchingSuiteHeuristic), macro-F1/accuracy
//	                        per task type, plus the GRRQ single-value metric
//	                        (metrics.go) and a baseline regression gate;
//	selection layer       — optional merge of an autoroute-e2e-audit JSONL
//	                        run (-e2e-report) into the same report so both
//	                        layers land in one artifact.
//
// A second mode generates suite *candidates* from live feedback data
// ("修正即测试" + stratified sampling) without ever touching prompt text:
//
//	auto-testbench -mode generate -source corrections -dsn ... -out candidates.jsonl
//
// Typical use (see scripts/auto-testbench.sh):
//
//	go run ./cmd/auto-testbench -report reports/auto-testbench
//	go run ./cmd/auto-testbench -gate cmd/auto-testbench/testdata/baseline.json
//	go run ./cmd/auto-testbench -write-baseline cmd/auto-testbench/testdata/baseline.json
//
// Exit codes: 0 = pass (or gate pass), 1 = regression gate failed, 2 = usage
// or internal error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

const defaultSuites = "autoroute/testdata/auto_matching_suite.jsonl," +
	"autoroute/testdata/auto_matching_suite_v2.jsonl," +
	"autoroute/testdata/auto_matching_suite_v3.jsonl"

const defaultBaseline = "cmd/auto-testbench/testdata/baseline.json"

func main() {
	mode := flag.String("mode", "regression", "regression | generate")
	suites := flag.String("suite", defaultSuites, "comma-separated suite JSONL files")
	report := flag.String("report", "", "report path prefix; writes <prefix>.json + <prefix>.md (empty = stdout summary only)")
	gate := flag.String("gate", "", "baseline JSON to enforce (exit 1 below threshold)")
	gateDefault := flag.Bool("gate-default", false, "use the checked-in default baseline ("+defaultBaseline+")")
	writeBaseline := flag.String("write-baseline", "", "write this run's metrics as a new baseline JSON")
	e2eReport := flag.String("e2e-report", "", "optional autoroute-e2e-audit result JSONL to merge (selection layer)")

	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "generate mode: PostgreSQL DSN (default $DATABASE_URL)")
	source := flag.String("source", "corrections", "generate mode: corrections | selections")
	genOut := flag.String("out", "", "generate mode: output path (default stdout)")
	genDays := flag.Int("days", 30, "generate mode: lookback window in days")
	genLimit := flag.Int("limit", 100, "generate mode: max rows (selections: per task type)")
	flag.Parse()

	switch *mode {
	case "generate":
		if *dsn == "" {
			fmt.Fprintln(os.Stderr, "generate mode requires -dsn or $DATABASE_URL")
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := runGenerate(ctx, *dsn, *source, *genOut, *genDays, *genLimit); err != nil {
			fmt.Fprintln(os.Stderr, "generate:", err)
			os.Exit(2)
		}
		return
	case "regression":
		// fall through
	default:
		fmt.Fprintf(os.Stderr, "unknown -mode %q\n", *mode)
		os.Exit(2)
	}

	suiteFiles := strings.Split(*suites, ",")
	cases, err := loadSuiteFiles(suiteFiles)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	classifier := autoroute.NewHeuristicClassifier(
		autoroute.DefaultHeuristicThresholds(), autoroute.DefaultKeywords())
	ctx := context.Background()

	cm := newClassificationMetrics()
	tm := &tierMetrics{TierDistribution: map[string]int{}}
	var rows []caseRow

	for _, tc := range cases {
		got, err := classifier.Classify(ctx, caseSignals(tc))
		if err != nil {
			fmt.Fprintf(os.Stderr, "classify %s: %v\n", tc.Name, err)
			os.Exit(2)
		}
		gold := tc.ExpectedTask
		pred := string(got.Primary)
		cm.add(gold, pred, tc, got.Confidence, got.Reason)
		tier := resolveTier(pred)
		tm.add(gold, tc, tier)
		rows = append(rows, caseRow{
			Name: tc.Name, Expected: gold, Got: pred,
			Pass: gold == pred && !tc.Generated, Confidence: got.Confidence,
			Reason: got.Reason, TierIntent: tier, Classifier: got.Classifier,
			Generated: tc.Generated,
		})
	}
	cm.finalize()
	tm.finalize()
	g := grrq(cm.Accuracy, tm.OverProvisionRate)

	e2e := loadE2EReport(*e2eReport)

	if *report != "" {
		if err := writeReports(*report, suiteFiles, cm, tm, g, rows, e2e); err != nil {
			fmt.Fprintln(os.Stderr, "report:", err)
			os.Exit(2)
		}
	}

	printSummary(suiteFiles, cm, tm, g, e2e)

	if *writeBaseline != "" {
		if err := writeBaselineFile(*writeBaseline, suiteFiles, cm, tm, g); err != nil {
			fmt.Fprintln(os.Stderr, "write-baseline:", err)
			os.Exit(2)
		}
		fmt.Printf("baseline written: %s\n", *writeBaseline)
	}

	gatePath := *gate
	if gatePath == "" && *gateDefault {
		gatePath = defaultBaseline
	}
	if gatePath != "" {
		b, err := loadBaseline(gatePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gate:", err)
			os.Exit(2)
		}
		v := evaluateGate(b, cm, g)
		fmt.Printf("\ngate vs %s\n  %s\n", gatePath, strings.Join(v.Lines, "\n  "))
		if !v.Passed {
			fmt.Println("GATE: FAIL")
			os.Exit(1)
		}
		fmt.Println("GATE: PASS")
	}
}

// caseRow is the per-case JSONL record in <prefix>.cases.jsonl.
type caseRow struct {
	Name       string  `json:"name"`
	Expected   string  `json:"expected_task"`
	Got        string  `json:"got_task"`
	Pass       bool    `json:"pass"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	TierIntent string  `json:"tier_intent"`
	Classifier string  `json:"classifier"`
	Generated  bool    `json:"generated,omitempty"`
}

// e2eRow is the subset of cmd/autoroute-e2e-audit's resultRow consumed here.
type e2eRow struct {
	Name        string `json:"name"`
	Bucket      string `json:"bucket"`
	Expected    string `json:"expected_task"`
	Got         string `json:"got_task"`
	Pass        bool   `json:"pass"`
	ServedModel string `json:"served_model"`
}

type e2eSummary struct {
	Path     string
	Total    int
	Pass     int
	Failures []e2eRow
	Models   map[string]int
}

func loadE2EReport(path string) *e2eSummary {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-report: %v (ignored)\n", err)
		return nil
	}
	s := &e2eSummary{Path: path, Models: map[string]int{}}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var r e2eRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		s.Total++
		if r.Pass {
			s.Pass++
		} else {
			s.Failures = append(s.Failures, r)
		}
		if r.ServedModel != "" {
			s.Models[r.ServedModel]++
		}
	}
	return s
}

func printSummary(suiteFiles []string, cm *classificationMetrics, tm *tierMetrics, g float64, e2e *e2eSummary) {
	fmt.Printf("auto-testbench regression\n")
	fmt.Printf("  suites: %s\n", strings.Join(suiteFiles, ", "))
	fmt.Printf("  cases: %d (generated-candidates skipped: %d, known_failure: %d)\n",
		cm.Total, cm.Generated, cm.KnownFails)
	fmt.Printf("  accuracy: %.4f   macro_f1: %.4f\n", cm.Accuracy, cm.MacroF1)
	fmt.Printf("  tier distribution: %s\n", formatDist(tm.TierDistribution))
	fmt.Printf("  over-provision: %d/%d simple cases → tier-a (rate=%.4f)\n",
		tm.OverProvisioned, tm.SimpleTotal, tm.OverProvisionRate)
	fmt.Printf("  GRRQ: %.2f  (= accuracy×100 − overprovision×30)\n", g)
	if len(cm.Failures) > 0 {
		fmt.Printf("  FAILURES (%d):\n", len(cm.Failures))
		for _, f := range cm.Failures {
			fmt.Printf("    %-40s want=%-20s got=%-20s conf=%.2f reason=%s\n",
				f.Name, f.Expected, f.Got, f.Confidence, f.Reason)
		}
	}
	if e2e != nil {
		rate := 0.0
		if e2e.Total > 0 {
			rate = float64(e2e.Pass) / float64(e2e.Total)
		}
		fmt.Printf("  e2e layer (%s): %d/%d pass (%.4f)\n", e2e.Path, e2e.Pass, e2e.Total, rate)
		for _, f := range e2e.Failures {
			fmt.Printf("    e2e FAIL %-40s want=%s got=%s served=%s\n", f.Name, f.Expected, f.Got, f.ServedModel)
		}
	}
}

func formatDist(d map[string]int) string {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, d[k]))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " ")
}

type reportJSON struct {
	GeneratedAt    string   `json:"generated_at"`
	Classifier     string   `json:"classifier"`
	Suites         []string `json:"suites"`
	Classification struct {
		Total     int                       `json:"total"`
		Correct   int                       `json:"correct"`
		Accuracy  float64                   `json:"accuracy"`
		MacroF1   float64                   `json:"macro_f1"`
		LabelF1   map[string]float64        `json:"label_f1"`
		Confusion map[string]map[string]int `json:"confusion"`
		Failures  []caseFailure             `json:"failures"`
		Generated int                       `json:"generated_candidates"`
	} `json:"classification"`
	Tier struct {
		Distribution      map[string]int `json:"distribution"`
		SimpleTotal       int            `json:"simple_total"`
		OverProvisioned   int            `json:"over_provisioned"`
		OverProvisionRate float64        `json:"overprovision_rate"`
	} `json:"tier"`
	GRRQ float64     `json:"grrq"`
	E2E  *e2eSummary `json:"e2e,omitempty"`
}

func writeReports(prefix string, suiteFiles []string, cm *classificationMetrics, tm *tierMetrics, g float64, rows []caseRow, e2e *e2eSummary) error {
	// 逐用例明细（JSONL，规划 §4.3.5 的报告形态之一）。
	var cl strings.Builder
	enc := json.NewEncoder(&cl)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	if err := os.WriteFile(prefix+".cases.jsonl", []byte(cl.String()), 0o644); err != nil {
		return err
	}

	var rj reportJSON
	rj.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	rj.Classifier = "heuristic:default"
	rj.Suites = suiteFiles
	rj.Classification.Total = cm.Total
	rj.Classification.Correct = cm.Correct
	rj.Classification.Accuracy = cm.Accuracy
	rj.Classification.MacroF1 = cm.MacroF1
	rj.Classification.LabelF1 = cm.LabelF1
	rj.Classification.Confusion = cm.Confusion
	rj.Classification.Failures = cm.Failures
	rj.Classification.Generated = cm.Generated
	rj.Tier.Distribution = tm.TierDistribution
	rj.Tier.SimpleTotal = tm.SimpleTotal
	rj.Tier.OverProvisioned = tm.OverProvisioned
	rj.Tier.OverProvisionRate = tm.OverProvisionRate
	rj.GRRQ = g
	rj.E2E = e2e

	jd, err := json.MarshalIndent(rj, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(prefix+".json", append(jd, '\n'), 0o644); err != nil {
		return err
	}

	var md strings.Builder
	md.WriteString("# auto-testbench 回归报告\n\n")
	md.WriteString(fmt.Sprintf("- 生成时间: %s\n- 分类器: %s\n- 套件: %s\n",
		rj.GeneratedAt, rj.Classifier, strings.Join(suiteFiles, ", ")))
	md.WriteString(fmt.Sprintf("\n## 指标\n\n| 指标 | 值 |\n|---|---|\n| 用例数 | %d（候选跳过 %d, known_failure %d） |\n| 分类正确率 | %.4f |\n| macro-F1 | %.4f |\n| tier 分布 | %s |\n| 过配率 | %.4f (%d/%d 简单类) |\n| **GRRQ** | **%.2f** |\n",
		cm.Total, cm.Generated, cm.KnownFails, cm.Accuracy, cm.MacroF1,
		formatDist(tm.TierDistribution), tm.OverProvisionRate, tm.OverProvisioned, tm.SimpleTotal, g))
	md.WriteString("\n## 分类层按任务类型\n\n| task_type | F1 | 判对 | 金标签数 |\n|---|---|---|---|\n")
	labels := make([]string, 0, len(cm.LabelF1))
	for l := range cm.LabelF1 {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		s := cm.PerLabel[l]
		md.WriteString(fmt.Sprintf("| %s | %.3f | %d | %d |\n", l, cm.LabelF1[l], s.tp, s.tp+s.fn))
	}
	if len(cm.Failures) > 0 {
		md.WriteString("\n## 失败用例\n\n| case | want | got | conf | reason |\n|---|---|---|---|---|\n")
		for _, f := range cm.Failures {
			md.WriteString(fmt.Sprintf("| %s | %s | %s | %.2f | %s |\n", f.Name, f.Expected, f.Got, f.Confidence, f.Reason))
		}
	}
	if e2e != nil {
		rate := 0.0
		if e2e.Total > 0 {
			rate = float64(e2e.Pass) / float64(e2e.Total)
		}
		md.WriteString(fmt.Sprintf("\n## E2E 层（%s）\n\n- %d/%d 通过（%.4f）\n", e2e.Path, e2e.Pass, e2e.Total, rate))
		if len(e2e.Failures) > 0 {
			md.WriteString("\n| case | want | got | served |\n|---|---|---|---|\n")
			for _, f := range e2e.Failures {
				md.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n", f.Name, f.Expected, f.Got, f.ServedModel))
			}
		}
	}
	return os.WriteFile(prefix+".md", []byte(md.String()), 0o644)
}

// writeBaselineFile adapts metrics.writeBaseline to the CLI name (kept
// separate so metrics.go stays free of I/O call sites beyond this wrapper).
func writeBaselineFile(path string, suiteFiles []string, cm *classificationMetrics, tm *tierMetrics, g float64) error {
	return writeBaseline(path, "heuristic:default", suiteFiles, cm, tm, g)
}
