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
// or internal error (contradictory flag combos, invalid -days/-limit, an
// unreadable -e2e-report, or a semantically invalid baseline all exit 2 —
// R64: these used to be silent no-ops).
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

// defaultE2EBaseline holds the selection-layer threshold. It is a separate file
// from the offline classification baseline on purpose: collapse rate is only
// computable from a live E2E run (the decision header), so putting it in the
// offline baseline would create a threshold the offline gate can never
// evaluate — a gate that looks installed and silently does nothing (R76).
const defaultE2EBaseline = "cmd/auto-testbench/testdata/e2e_baseline.json"

// e2eBaseline is the selection-layer gate contract.
type e2eBaseline struct {
	GeneratedAt     string             `json:"generated_at"`
	Source          string             `json:"source"`
	MaxCollapseRate float64            `json:"max_collapse_rate"`
	Measured        map[string]float64 `json:"measured,omitempty"`
	Notes           string             `json:"notes,omitempty"`
}

// evaluateE2EGate compares the selection layer of this run against the E2E
// baseline. ok=false means "not evaluated" (no E2E report in this run) which
// must never turn into a failure — the offline path has no collapse rate.
func evaluateE2EGate(b *e2eBaseline, s *e2eSummary) (ok bool, line string, failed bool) {
	if s == nil || s.Decided == 0 {
		return false, "e2e layer not run in this invocation — collapse threshold not evaluated", false
	}
	got := s.CollapseRate()
	pass := got <= b.MaxCollapseRate
	verdict := "PASS"
	if !pass {
		verdict = "ABOVE"
	}
	line = fmt.Sprintf("collapse_rate    got=%.4f  max=%.4f  (%d/%d decided)  %s",
		got, b.MaxCollapseRate, s.FallbackCollapsed, s.Decided, verdict)
	return true, line, !pass
}

// validateE2EBaseline rejects a baseline that cannot gate anything or that
// would fail spuriously.
//
// Lower bound > 0 on purpose: fallback_used also fires when the candidate pool
// is legitimately empty (every model unavailable), which is a healthy
// outcome. A zero threshold would turn that into a build failure, so the
// floor is a small tolerance, not zero.
func validateE2EBaseline(b *e2eBaseline) error {
	switch {
	case b.MaxCollapseRate <= 0:
		return fmt.Errorf("e2e baseline max_collapse_rate = %v, want > 0 (zero fails on legitimate empty-pool fallbacks; set a small tolerance)", b.MaxCollapseRate)
	case b.MaxCollapseRate > 1:
		return fmt.Errorf("e2e baseline max_collapse_rate = %v, want <= 1", b.MaxCollapseRate)
	}
	return nil
}

func loadE2EBaseline(path string) (*e2eBaseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b e2eBaseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse e2e baseline %s: %w", path, err)
	}
	if err := validateE2EBaseline(&b); err != nil {
		return nil, err
	}
	return &b, nil
}

func main() {
	mode := flag.String("mode", "regression", "regression | generate")
	suites := flag.String("suite", defaultSuites, "comma-separated suite JSONL files")
	report := flag.String("report", "", "report path prefix; writes <prefix>.json + <prefix>.md (empty = stdout summary only)")
	gate := flag.String("gate", "", "baseline JSON to enforce (exit 1 below threshold)")
	gateDefault := flag.Bool("gate-default", false, "use the checked-in default baseline ("+defaultBaseline+")")
	e2eBaselinePath := flag.String("e2e-baseline", "", "selection-layer baseline JSON; only consulted when -e2e-report is given (default "+defaultE2EBaseline+" when -e2e-report is set)")
	writeBaseline := flag.String("write-baseline", "", "write this run's metrics as a new baseline JSON")
	e2eReport := flag.String("e2e-report", "", "optional autoroute-e2e-audit result JSONL to merge (selection layer)")

	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "generate mode: PostgreSQL DSN (default $DATABASE_URL)")
	source := flag.String("source", "corrections", "generate mode: corrections | selections")
	genOut := flag.String("out", "", "generate mode: output path (default stdout)")
	genDays := flag.Int("days", 30, "generate mode: lookback window in days")
	genLimit := flag.Int("limit", 100, "generate mode: max rows (selections: per task type)")
	flag.Parse()

	// R64（P3）：显式收集用户给出的 flag，拒绝互斥/无效组合（此前均静默失效，
	// 照常 exit 0）。usage 错误统一 exit 2。
	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if err := validateFlags(*mode, explicit, *genDays, *genLimit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

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

	e2e, e2eErr := loadE2EReport(*e2eReport)
	if e2eErr != nil {
		fmt.Fprintln(os.Stderr, e2eErr)
		os.Exit(2)
	}

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
		// R64（P2）：JSON 可解析≠语义可用——零阈值门不住任何回归，
		// total_cases/suite_files 过期则是拿旧库存比新跑。不符一律
		// exit 2（usage 错误），绝不静默放行。
		if err := validateBaseline(b, suiteFiles, cm.Total); err != nil {
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

	// Selection-layer gate (R76). Consulted only when this run actually merged
	// an E2E report: the offline path has no decision headers, so no collapse
	// rate. Absence of an E2E run is a skip, never a silent pass and never a
	// failure — the classification gate above already decided that run.
	ebPath := *e2eBaselinePath
	if ebPath == "" && e2e != nil {
		ebPath = defaultE2EBaseline
	}
	if ebPath != "" && e2e != nil {
		eb, err := loadE2EBaseline(ebPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e gate:", err)
			os.Exit(2)
		}
		evaluated, line, failed := evaluateE2EGate(eb, e2e)
		fmt.Printf("\nselection gate vs %s\n  %s\n", ebPath, line)
		if evaluated && failed {
			fmt.Println("SELECTION GATE: FAIL")
			fmt.Println("GATE: FAIL")
			os.Exit(1)
		}
		if evaluated {
			fmt.Println("SELECTION GATE: PASS")
		}
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

// regressionOnlyFlags are the flags only -mode regression consumes; in
// generate mode they are silent no-ops, so explicitly setting one is a usage
// error (R64: they used to be ignored and the run still exited 0).
var regressionOnlyFlags = []string{"suite", "report", "gate", "gate-default", "write-baseline", "e2e-report", "e2e-baseline"}

// validateFlags rejects contradictory or no-op flag combinations (R64 P3):
//   - -mode generate with any regression-only flag → error (silent no-op);
//   - generate-mode -days < 0 or -limit < 1 → error (invalid window/rows);
//   - -gate together with -gate-default → error (the former silently won).
func validateFlags(mode string, explicit map[string]bool, days, limit int) error {
	if mode == "generate" {
		for _, f := range regressionOnlyFlags {
			if explicit[f] {
				return fmt.Errorf("-%s has no effect in -mode generate; drop it or run -mode regression", f)
			}
		}
		if days < 0 {
			return fmt.Errorf("-days must be >= 0, got %d", days)
		}
		if limit < 1 {
			return fmt.Errorf("-limit must be >= 1, got %d", limit)
		}
		return nil
	}
	if explicit["gate"] && explicit["gate-default"] {
		return fmt.Errorf("-gate and -gate-default are mutually exclusive; give exactly one")
	}
	return nil
}

// e2eRow is the subset of cmd/autoroute-e2e-audit's resultRow consumed here.
//
// Pass is a *bool because the audit emits `null` for it when the case errored
// upstream (429 / dispatch failure) rather than because the classification was
// wrong. Decoding that null into a plain bool yields false, which silently
// reclassified every upstream outage as a classification FAIL — a full-suite
// rate-limit blip then reports "0% pass" while the classification layer was in
// fact 240/240. The distinction is the whole reason this layer is worth reading.
//
// Decision carries the selection layer (2026-09-28). The offline regression
// only exercises the classifier, so the only place a selection defect such as
// "the whole candidate pool collapsed onto the 48h popularity fallback" is
// observable is the X-Gw-Auto-Decision header the audit already records.
type e2eRow struct {
	Name        string `json:"name"`
	Bucket      string `json:"bucket"`
	Expected    string `json:"expected_task"`
	Got         string `json:"got_task"`
	Pass        *bool  `json:"pass"`
	ServedModel string `json:"served_model"`
	Error       string `json:"error"`
	Decision    *struct {
		TaskType     string         `json:"task_type"`
		ChosenModel  string         `json:"chosen_model"`
		FallbackUsed bool           `json:"fallback_used"`
		Candidates   []e2eCandidate `json:"candidates_top3"`
	} `json:"decision"`
}

// e2eCandidate is one entry of the decision header's candidates_top3. Note it
// is one CREDENTIAL, not one model — the live pools put the same model in the
// list several times under different credentials. R77 D11-#8.
type e2eCandidate struct {
	Model     string  `json:"model"`
	RouteTier string  `json:"route_tier"`
	Match     float64 `json:"match_score"`
}

type e2eSummary struct {
	Path     string         `json:"path"`
	Total    int            `json:"total"`
	Pass     int            `json:"pass"`
	Failures []e2eRow       `json:"failures,omitempty"`
	Errors   int            `json:"upstream_errors"`
	Models   map[string]int `json:"served_models"`

	// Selection layer. All four are counted over rows that captured a
	// decision, which includes cases whose dispatch later errored — the
	// decision header is written before dispatch, so those rows still carry
	// a usable verdict.
	Decided           int            `json:"decided"`
	ClassCorrect      int            `json:"classification_correct"`
	FallbackCollapsed int            `json:"fallback_collapsed"`
	SingleCandidate   int            `json:"single_candidate_pools"`
	CollapseByTask    map[string]int `json:"collapse_by_task,omitempty"`
	TotalByTask       map[string]int `json:"decided_by_task,omitempty"`
	CollapseRateVal   float64        `json:"collapse_rate"`
	ClassAccuracyVal  float64        `json:"classification_accuracy"`

	// ModelMonotone counts decided cases whose candidate pool held exactly one
	// distinct MODEL, regardless of how many candidate entries it had.
	//
	// R77 D11-#8: SingleCandidate counts candidate ENTRIES, and the three
	// entries of candidates_top3 are three credentials of one model, not three
	// options. On the 2026-09-28 post-L-5 240-case run, 217/240 (90.4%) pools
	// were model-monotone while SingleCandidate read 0 — the metric reported a
	// healthy pool over a pool that had no model-level choice at all. Counting
	// distinct models is the layer that actually describes the failover ladder.
	//
	// Credential diversity is not upstream diversity either: the observed
	// failover chain cred 21 → cred 42 crossed credentials that both sit on
	// provider 14 / api.minimaxi.com, and every 429-side credential observed
	// (3/4/25/49) points at token.sensenova.cn across three different provider
	// rows. Whether routing should *require* model or upstream diversity is a
	// routing-policy decision and is deliberately NOT made here.
	ModelMonotone int `json:"model_monotone_pools"`
}

// CollapseRate is the fraction of decided cases whose candidate pool collapsed
// onto the 48h popularity fallback (fallback_used). A non-zero value means
// routing-by-task did not actually happen for those cases. 2026-09-28 measured
// baseline: 124/240 = 0.517 before the fail-open fix, 0/240 after.
func (s *e2eSummary) CollapseRate() float64 {
	if s == nil || s.Decided == 0 {
		return 0
	}
	return float64(s.FallbackCollapsed) / float64(s.Decided)
}

// ClassAccuracy is the classification layer measured over every case that
// produced a decision, errored or not. This is the number that held at
// 240/240 across the whole 2026-09-28 audit even while every dispatch 429'd.
func (s *e2eSummary) ClassAccuracy() float64 {
	if s == nil || s.Decided == 0 {
		return 0
	}
	return float64(s.ClassCorrect) / float64(s.Decided)
}

// DistinctModelRate is the share of decided cases whose candidate pool offered
// no model-level choice — one distinct model, however many credentials backed
// it. R77 D11-#8. CollapseRate() and this measure different failures: the
// former catches a pool that fell back to the unscored 48h popularity pick,
// this one catches a pool that scored normally but had nowhere to go.
func (s *e2eSummary) DistinctModelRate() float64 {
	if s == nil || s.Decided == 0 {
		return 0
	}
	return float64(s.ModelMonotone) / float64(s.Decided)
}

// loadE2EReport parses an autoroute-e2e-audit JSONL. An explicitly given but
// unreadable path is a usage error (R64: it used to be logged as "(ignored)"
// and the run still exited 0). Malformed lines stay skip-by-line tolerant —
// the merge contract accepts concatenated multi-suite outputs with
// comment/blank separators.
func loadE2EReport(path string) (*e2eSummary, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("e2e-report: %w", err)
	}
	s := &e2eSummary{Path: path, Models: map[string]int{},
		CollapseByTask: map[string]int{}, TotalByTask: map[string]int{}}
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
		// A row lands in Failures at most once. `pass` and the decision's
		// task_type are two views of the same verdict (the audit derives
		// got_task from the decision header), so recording both would
		// double-count every genuine miss.
		recorded := false
		switch {
		case r.Pass != nil && *r.Pass:
			s.Pass++
			// R73 订正（D11#5）：与下方 pass=false 分支对称置位。当前写端
			// 契约 pass := got==expected 使「pass=true 且 decision 错」不可达；
			// 若未来写端漂移，缺这行会让同一行既进 Pass 又经 !recorded 臂
			// 进 Failures，双计。
			recorded = true
		case r.Pass != nil:
			// A decided verdict of "wrong" — a genuine classification FAIL.
			s.Failures = append(s.Failures, r)
			recorded = true
		default:
			// pass == null: the case errored upstream. Not a classification FAIL.
			s.Errors++
		}
		if r.ServedModel != "" {
			s.Models[r.ServedModel]++
		}
		if r.Decision == nil {
			continue
		}
		s.Decided++
		s.TotalByTask[r.Expected]++
		if r.Decision.TaskType == r.Expected {
			s.ClassCorrect++
		} else if !recorded {
			// The audit left pass null (upstream error) yet the decision was
			// still wrong — that is a real miss the error masked.
			s.Failures = append(s.Failures, r)
		}
		if r.Decision.FallbackUsed {
			s.FallbackCollapsed++
			s.CollapseByTask[r.Expected]++
		}
		if len(r.Decision.Candidates) <= 1 {
			s.SingleCandidate++
		}
		// R77 D11-#8: count distinct MODELS, not candidate entries. The three
		// entries of candidates_top3 are three credentials of one model on the
		// live pools, so SingleCandidate read 0 on a pool that in fact offered
		// no model-level choice at all.
		if distinctCandidateModels(r.Decision.Candidates) <= 1 {
			s.ModelMonotone++
		}
	}
	// Materialise the derived rates once, so the JSON report carries the same
	// numbers the console prints (the accessors stay nil-safe for the
	// "e2e layer not run" case, where there is nothing to report).
	s.CollapseRateVal = s.CollapseRate()
	s.ClassAccuracyVal = s.ClassAccuracy()
	return s, nil
}

// distinctCandidateModels counts the distinct model names in a candidate pool.
// Empty model names are skipped: a blank entry is a malformed row, not a
// model, and letting it through would inflate the count and hide a
// model-monotone pool.
func distinctCandidateModels(cands []e2eCandidate) int {
	seen := make(map[string]struct{}, len(cands))
	for _, c := range cands {
		if c.Model == "" {
			continue
		}
		seen[c.Model] = struct{}{}
	}
	return len(seen)
}

func printSummary(suiteFiles []string, cm *classificationMetrics, tm *tierMetrics, g float64, e2e *e2eSummary) {
	fmt.Printf("auto-testbench regression\n")
	fmt.Printf("  suites: %s\n", strings.Join(suiteFiles, ", "))
	fmt.Printf("  cases: %d (generated-candidates skipped: %d, known_failure: %d [xpass: %d])\n",
		cm.Total, cm.Generated, cm.KnownFails, cm.KnownXPass)
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
		fmt.Printf("  e2e layer (%s): %d/%d pass (%.4f), %d upstream-error (not a classification verdict)\n",
			e2e.Path, e2e.Pass, e2e.Total, rate, e2e.Errors)
		// Selection layer. Reported over every decided case, so a run where
		// the upstream rate-limited out still yields a real number here
		// instead of an empty section.
		if e2e.Decided > 0 {
			fmt.Printf("  e2e classification (decided): %d/%d (%.4f)\n",
				e2e.ClassCorrect, e2e.Decided, e2e.ClassAccuracy())
			fmt.Printf("  e2e selection: collapse %d/%d (rate=%.4f), single-candidate pools %d/%d (counts entries=credentials)\n",
				e2e.FallbackCollapsed, e2e.Decided, e2e.CollapseRate(),
				e2e.SingleCandidate, e2e.Decided)
			fmt.Printf("  e2e model diversity: model-monotone %d/%d (rate=%.4f) — pool held 1 distinct model\n",
				e2e.ModelMonotone, e2e.Decided, e2e.DistinctModelRate())
			if e2e.FallbackCollapsed > 0 {
				fmt.Printf("    collapse by task (routing-by-task did not happen for these):\n")
				tasks := make([]string, 0, len(e2e.CollapseByTask))
				for t := range e2e.CollapseByTask {
					tasks = append(tasks, t)
				}
				sort.Strings(tasks)
				for _, t := range tasks {
					fmt.Printf("      %-24s %d/%d\n", t, e2e.CollapseByTask[t], e2e.TotalByTask[t])
				}
			}
		}
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
		Total         int                       `json:"total"`
		Correct       int                       `json:"correct"`
		Accuracy      float64                   `json:"accuracy"`
		MacroF1       float64                   `json:"macro_f1"`
		LabelF1       map[string]float64        `json:"label_f1"`
		Confusion     map[string]map[string]int `json:"confusion"`
		Failures      []caseFailure             `json:"failures"`
		Generated     int                       `json:"generated_candidates"`
		KnownFailures int                       `json:"known_failures"`
		KnownXPass    int                       `json:"known_failure_xpass"`
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
	rj.Classification.KnownFailures = cm.KnownFails
	rj.Classification.KnownXPass = cm.KnownXPass
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
	md.WriteString(fmt.Sprintf("\n## 指标\n\n| 指标 | 值 |\n|---|---|\n| 用例数 | %d（候选跳过 %d, known_failure %d [xpass %d]） |\n| 分类正确率 | %.4f |\n| macro-F1 | %.4f |\n| tier 分布 | %s |\n| 过配率 | %.4f (%d/%d 简单类) |\n| **GRRQ** | **%.2f** |\n",
		cm.Total, cm.Generated, cm.KnownFails, cm.KnownXPass, cm.Accuracy, cm.MacroF1,
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
		md.WriteString(e2eMarkdownSection(e2e))
	}
	return os.WriteFile(prefix+".md", []byte(md.String()), 0o644)
}

// e2eMarkdownSection renders the E2E layer of the human-facing report.
//
// The selection-layer numbers belong in here, not only in the console output
// and the JSON sidecar: reports/auto-testbench.md is the artifact a reviewer
// actually opens. Before this section carried them, a run where 124/240 cases
// had collapsed onto the 48h popularity fallback and a completely healthy run
// produced the same markdown — the two facts were indistinguishable to a human
// reader, which is the blind spot R75 flagged.
func e2eMarkdownSection(s *e2eSummary) string {
	var md strings.Builder
	rate := 0.0
	if s.Total > 0 {
		rate = float64(s.Pass) / float64(s.Total)
	}
	md.WriteString(fmt.Sprintf("\n## E2E 层（%s）\n\n- %d/%d 通过（%.4f）\n", s.Path, s.Pass, s.Total, rate))
	// Upstream errors are a dispatch fact, not a classification verdict; saying
	// so here keeps a 429-heavy run from reading as a routing regression.
	md.WriteString(fmt.Sprintf("- 上游报错 %d 例（派发失败，非分类判定）\n", s.Errors))
	if s.Decided > 0 {
		md.WriteString(fmt.Sprintf("- 分类层（取到决策的 %d 例）：%d/%d（%.4f）\n",
			s.Decided, s.ClassCorrect, s.Decided, s.ClassAccuracy()))
		md.WriteString(fmt.Sprintf("- 选型层（取到决策的 %d 例）：坍缩到 48h 热度兜底 %d（%.4f），单候选池 %d（按候选**条目**数，条目=凭据）\n",
			s.Decided, s.FallbackCollapsed, s.CollapseRate(), s.SingleCandidate))
		md.WriteString(fmt.Sprintf("- 模型级多样性（取到决策的 %d 例）：model-monotone %d/%d（%.4f）——候选池只有 1 个不同模型；"+
			"凭据数不等于可选项数，条目 3 常常是同一模型的 3 个凭据\n",
			s.Decided, s.ModelMonotone, s.Decided, s.DistinctModelRate()))
		if s.FallbackCollapsed > 0 {
			md.WriteString("\n### 坍缩分任务归因\n\n| task_type | 坍缩/该任务已判定 |\n|---|---|\n")
			tasks := make([]string, 0, len(s.CollapseByTask))
			for t := range s.CollapseByTask {
				tasks = append(tasks, t)
			}
			sort.Strings(tasks)
			for _, t := range tasks {
				md.WriteString(fmt.Sprintf("| %s | %d/%d |\n", t, s.CollapseByTask[t], s.TotalByTask[t]))
			}
		}
	}
	if len(s.Failures) > 0 {
		md.WriteString("\n| case | want | got | served |\n|---|---|---|---|\n")
		for _, f := range s.Failures {
			md.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n", f.Name, f.Expected, f.Got, f.ServedModel))
		}
	}
	return md.String()
}

// writeBaselineFile adapts metrics.writeBaseline to the CLI name (kept
// separate so metrics.go stays free of I/O call sites beyond this wrapper).
func writeBaselineFile(path string, suiteFiles []string, cm *classificationMetrics, tm *tierMetrics, g float64) error {
	return writeBaseline(path, "heuristic:default", suiteFiles, cm, tm, g)
}
