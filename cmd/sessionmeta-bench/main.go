// cmd/sessionmeta-bench — unified benchmark for session metadata quality:
// task classification, title extraction, and summary generation.
//
// Usage:
//
//	# Phase 1: Sample and anonymize production sessions
//	sessionmeta-bench sample \
//	  --dsn "postgres://..." \
//	  --output testdata/sessions.jsonl \
//	  --target 80 \
//	  --seed 42
//
//	# Phase 2: Generate gold labels with dual-model consensus
//	sessionmeta-bench annotate \
//	  --input testdata/sessions.jsonl \
//	  --output testdata/gold.jsonl \
//	  --review testdata/review.csv \
//	  --model1 gpt-4o-mini \
//	  --model2 gemini-2.0-flash-exp
//
//	# Phase 3: Evaluate models
//	sessionmeta-bench eval \
//	  --mode label \
//	  --gold testdata/gold.jsonl \
//	  --endpoint http://127.0.0.1:8080/v1 \
//	  --model mlx-community/Qwen3-8B-8bit \
//	  --output results/mlx-baseline.json
//
//	# Phase 4: Generate report
//	sessionmeta-bench report \
//	  --results results/*.json \
//	  --output report.md
//
// Environment variables (no secrets in code):
//   - DATABASE_URL: PostgreSQL DSN for sampling
//   - OPENAI_API_KEY: for cloud baselines
//   - GEMINI_API_KEY: for cloud baselines
//   - MLX_ENDPOINT: local mlx-dspark endpoint (default: http://127.0.0.1:8080/v1)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

const version = "0.1.0"

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	subcommand := os.Args[1]
	switch subcommand {
	case "sample":
		cmdSample(os.Args[2:])
	case "annotate":
		cmdAnnotate(os.Args[2:])
	case "eval":
		cmdEval(os.Args[2:])
	case "report":
		cmdReport(os.Args[2:])
	case "version":
		fmt.Printf("sessionmeta-bench %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", subcommand)
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `sessionmeta-bench - Session metadata quality benchmark

Usage:
  sessionmeta-bench sample    --dsn DSN --output FILE [--target N] [--seed N]
  sessionmeta-bench annotate  --input FILE --output FILE --review FILE --model1 M --model2 M
  sessionmeta-bench eval      --mode MODE --gold FILE --endpoint URL --model M --output FILE
  sessionmeta-bench report    --results GLOB --output FILE
  sessionmeta-bench version
  sessionmeta-bench help

Subcommands:
  sample    Sample and anonymize sessions from production database
  annotate  Generate gold labels with dual-model consensus
  eval      Evaluate a model endpoint against gold set
  report    Generate comparative report from multiple eval results
  version   Print version
  help      Print this help

Environment:
  DATABASE_URL     PostgreSQL DSN (for sample)
  OPENAI_API_KEY   OpenAI key (for annotate/eval)
  GEMINI_API_KEY   Gemini key (for annotate/eval)
  MLX_ENDPOINT     Local mlx-dspark endpoint (default: http://127.0.0.1:8080/v1)
`)
}

func cmdSample(args []string) {
	fs := flag.NewFlagSet("sample", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "PostgreSQL DSN")
	output := fs.String("output", "", "Output JSONL file (required)")
	target := fs.Int("target", 80, "Target number of sessions")
	seed := fs.Int64("seed", 42, "Random seed for reproducibility")
	minTurns := fs.Int("min-turns", 1, "Minimum turns per session")
	maxTurns := fs.Int("max-turns", 20, "Maximum turns per session")
	days := fs.Int("days", 30, "Sample from last N days")

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	if *dsn == "" {
		log.Fatal("sample: --dsn or DATABASE_URL required")
	}
	if *output == "" {
		log.Fatal("sample: --output required")
	}

	cfg := SamplerConfig{
		DSN:             *dsn,
		TargetCount:     *target,
		MinTurns:        *minTurns,
		MaxTurns:        *maxTurns,
		TimeWindowStart: time.Now().UTC().Add(-time.Duration(*days) * 24 * time.Hour),
		TimeWindowEnd:   time.Now().UTC(),
		RandomSeed:      *seed,
		ExcludeActors: []string{
			"auto-title-generator",
			"quality-test",
			"self-check",
			"node-probe",
			"credential-selfcheck",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log.Printf("sample: starting (target=%d, seed=%d, days=%d)", *target, *seed, *days)
	samples, err := SampleSessions(ctx, cfg)
	if err != nil {
		log.Fatalf("sample: %v", err)
	}

	f, err := os.Create(*output)
	if err != nil {
		log.Fatalf("sample: create output: %v", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, s := range samples {
		if err := enc.Encode(s); err != nil {
			log.Fatalf("sample: encode: %v", err)
		}
	}

	log.Printf("sample: wrote %d sessions to %s", len(samples), *output)
}

func cmdAnnotate(args []string) {
	fs := flag.NewFlagSet("annotate", flag.ExitOnError)
	input := fs.String("input", "", "Input anonymized sessions JSONL (required)")
	output := fs.String("output", "", "Output consensus JSONL (required)")
	review := fs.String("review", "", "Output divergence review CSV (required)")
	endpoint1 := fs.String("endpoint1", "https://api.openai.com/v1", "First annotator endpoint")
	endpoint2 := fs.String("endpoint2", "https://generativelanguage.googleapis.com/v1beta/openai", "Second annotator endpoint")
	key1 := fs.String("api-key1", os.Getenv("OPENAI_API_KEY"), "First annotator API key")
	key2 := fs.String("api-key2", os.Getenv("GEMINI_API_KEY"), "Second annotator API key")
	model1 := fs.String("model1", "gpt-4o-mini", "First annotator model")
	model2 := fs.String("model2", "gemini-2.0-flash", "Second annotator model")
	timeout := fs.Duration("timeout", 30*time.Second, "Per-request timeout")

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *input == "" || *output == "" || *review == "" {
		log.Fatal("annotate: --input, --output, --review required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := runAnnotation(ctx, *input, *output, *review, *endpoint1, *key1, *model1, *endpoint2, *key2, *model2, *timeout); err != nil {
		log.Fatalf("annotate: %v", err)
	}
}
func cmdEval(args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	mode := fs.String("mode", "label", "Evaluation mode: label|title|summary|all")
	gold := fs.String("gold", "", "Gold standard JSONL (required)")
	endpoint := fs.String("endpoint", os.Getenv("MLX_ENDPOINT"), "Model endpoint base URL")
	model := fs.String("model", "", "Model name (required)")
	output := fs.String("output", "", "Output results JSON (required)")
	concurrency := fs.Int("c", 4, "Concurrent requests")
	timeout := fs.Duration("timeout", 30*time.Second, "Per-request timeout")

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *gold == "" || *endpoint == "" || *model == "" || *output == "" {
		log.Fatal("eval: --gold, --endpoint, --model, --output required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	result, err := evaluate(ctx, *gold, *endpoint, *model, *mode, *concurrency, *timeout)
	if err != nil {
		log.Fatalf("eval: %v", err)
	}
	f, err := os.Create(*output)
	if err != nil {
		log.Fatalf("eval: create output: %v", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(result); err != nil {
		log.Fatalf("eval: write output: %v", err)
	}
	log.Printf("eval: completed=%d errors=%d accuracy=%.3f macro_f1=%.3f p95=%.1fms", result.Completed, result.Errors, result.Accuracy, result.MacroF1, result.P95LatencyMS)
}

func cmdReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	results := fs.String("results", "", "Results JSON glob pattern (required)")
	output := fs.String("output", "", "Output markdown report (required)")

	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *results == "" || *output == "" {
		log.Fatal("report: --results, --output required")
	}
	paths, err := expandResultPaths(*results)
	if err != nil {
		log.Fatalf("report: expand paths: %v", err)
	}
	if err := writeComparisonReport(paths, *output); err != nil {
		log.Fatalf("report: %v", err)
	}
	log.Printf("report: wrote %d results to %s", len(paths), *output)
}
