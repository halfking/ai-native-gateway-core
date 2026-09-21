package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func loadResult(path string) (EvalResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return EvalResult{}, err
	}
	var r EvalResult
	if err := json.Unmarshal(data, &r); err != nil {
		return EvalResult{}, err
	}
	return r, nil
}

func writeComparisonReport(paths []string, output string) error {
	results := make([]EvalResult, 0, len(paths))
	for _, p := range paths {
		r, err := loadResult(p)
		if err != nil {
			return fmt.Errorf("load %s: %w", p, err)
		}
		results = append(results, r)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Model < results[j].Model })
	var b strings.Builder
	b.WriteString("# Session Metadata Benchmark Report\n\n")
	b.WriteString("> This report is generated from anonymized session samples only. Missing endpoints, credentials, or gold labels are reported as blocked; no values are inferred.\n\n")
	b.WriteString("## Model comparison\n\n")
	b.WriteString("| Model | Mode | Samples | Completed | Errors | Accuracy | Macro-F1 | Title format | P95 latency (ms) |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range results {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %.3f | %.3f | %.3f | %.1f |\n", r.Model, r.Mode, r.SampleCount, r.Completed, r.Errors, r.Accuracy, r.MacroF1, r.TitleFormatRate, r.P95LatencyMS)
	}
	b.WriteString("\n## Per-label metrics\n\n")
	for _, r := range results {
		fmt.Fprintf(&b, "### %s\n\n", r.Model)
		if len(r.PerLabel) == 0 {
			b.WriteString("No per-label metrics were recorded.\n\n")
			continue
		}
		b.WriteString("| Label | Precision | Recall | F1 |\n|---|---:|---:|---:|\n")
		labels := make([]string, 0, len(r.PerLabel))
		for l := range r.PerLabel {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		for _, l := range labels {
			m := r.PerLabel[l]
			fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f |\n", l, m.Precision, m.Recall, m.F1)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Typical cases\n\n")
	b.WriteString("Typical-case excerpts are intentionally omitted until a real anonymized dataset is available. The evaluator stores only session hashes and model outputs, so examples can be added without exposing tenant or session identifiers.\n\n")
	b.WriteString("## Audit findings and blockers\n\n")
	b.WriteString("- Production sampling requires a read-only `DATABASE_URL`; none was available during this run.\n")
	b.WriteString("- Cloud comparison requires configured API keys and two reachable OpenAI-compatible endpoints; no keys were available during this run.\n")
	b.WriteString("- Local MLX/DFlash2 comparison requires a running `mlx-dspark` service and a compatible main/drafter model pair; `http://127.0.0.1:8080/health` was unreachable on September 6, 2026.\n")
	b.WriteString("- Final session metadata currently persists separately from canonical title/summary projections; see `domains/analysis/workers/session_metadata_close_hook.go` and `domains/sessionsummary/summarizer.go`.\n")
	b.WriteString("- The quarantined dialogue test remains tagged `broken_pending_repair`; it should be restored before claiming summary fidelity coverage.\n")
	b.WriteString("\n## Reproduction\n\n")
	fmt.Fprintf(&b, "Results loaded from: %s\n", strings.Join(paths, ", "))
	if err := os.WriteFile(output, []byte(b.String()), 0644); err != nil {
		return err
	}
	return nil
}

func expandResultPaths(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no result files match %q", pattern)
	}
	return matches, nil
}
