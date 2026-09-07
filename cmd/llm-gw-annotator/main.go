// cmd/llm-gw-annotator/main.go — 2026-09-06
//
// llm-gw-annotator: CLI tool for human annotation workflow.
//
// Commands:
//   export   - Export low-confidence samples to CSV for annotation
//   import   - Import annotated CSV to database
//   validate - Validate CSV format before import
//   stats    - Show annotation statistics
//
// Privacy guarantee:
//   - ✅ Only exports structured feature fields
//   - ❌ Never exports prompt/messages/response
//   - ✅ Annotators only see non-reversible features
//
// Usage:
//   llm-gw-annotator export --start 2026-08-01 --end 2026-09-01 --max-confidence 0.7 --output annotations.csv
//   llm-gw-annotator validate annotations.csv
//   llm-gw-annotator import annotations.csv
//   llm-gw-annotator stats
//
// Part of: P2.1 - Human Annotation Workflow

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/annotation"
	"github.com/spf13/cobra"
)

var (
	dbConnStr     string
	startDate     string
	endDate       string
	minConfidence float64
	maxConfidence float64
	limit         int
	outputPath    string
	sampling      string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "llm-gw-annotator",
		Short: "Human annotation workflow for AUTO route training data",
		Long: `llm-gw-annotator manages human annotation workflow for low-confidence AUTO route samples.

Privacy guarantee:
  - Only exports structured feature fields (model, task, tokens, etc.)
  - Never exports prompt, messages, response, or any reversible content
  - Annotators only see non-reversible features

Workflow:
  1. Export low-confidence samples to CSV (llm-gw-annotator export)
  2. Human annotators review and fill in: human_provider, is_correct, reason
  3. Validate CSV format (llm-gw-annotator validate)
  4. Import annotations to database (llm-gw-annotator import)
  5. View statistics (llm-gw-annotator stats)

Example:
  llm-gw-annotator export --start 2026-08-01 --end 2026-09-01 --max-confidence 0.7 --output annotations.csv
  llm-gw-annotator export --start 2026-08-01 --end 2026-09-01 --sampling uncertain --limit 200 --output annotations.csv
  llm-gw-annotator validate annotations.csv
  llm-gw-annotator import annotations.csv
  llm-gw-annotator stats`,
	}

	rootCmd.PersistentFlags().StringVar(&dbConnStr, "db", os.Getenv("DATABASE_URL"), "Database connection string")

	rootCmd.AddCommand(exportCmd())
	rootCmd.AddCommand(importCmd())
	rootCmd.AddCommand(validateCmd())
	rootCmd.AddCommand(statsCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// exportCmd creates the export command.
func exportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export low-confidence samples to CSV for annotation",
		Long: `Export low-confidence AUTO route samples to CSV file for human annotation.

The export process:
  1. Query auto_route_selections (only structured features)
  2. Filter by confidence range and date range
  3. Exclude already-annotated samples
  4. Write to CSV with empty annotation columns

Sampling strategies (--sampling):
  all        Order strictly by confidence ascending, lowest first (default,
             identical to the P2.1 behavior)
  uncertain  Active-learning uncertainty sampling: prefer confidence in
             [0.4, 0.6] (model is least certain), backfill with confidence
             < 0.4 when insufficient, with a per-task-type floor quota to
             avoid one task type dominating the export. --start/--end,
             --min/--max-confidence still filter candidates and --limit
             still caps the total. CSV columns are identical in both modes.

Privacy guarantee:
  - Query only accesses structured feature columns
  - Never touches prompt, messages, response columns
  - Output CSV contains only non-reversible features

Example:
  llm-gw-annotator export --start 2026-08-01 --end 2026-09-01 --max-confidence 0.7 --limit 100 --output annotations.csv
  llm-gw-annotator export --start 2026-08-01 --end 2026-09-01 --sampling uncertain --limit 200 --output annotations.csv`,
		RunE: runExport,
	}

	cmd.Flags().StringVar(&startDate, "start", "", "Start date (YYYY-MM-DD, required)")
	cmd.Flags().StringVar(&endDate, "end", "", "End date (YYYY-MM-DD, required)")
	cmd.Flags().Float64Var(&minConfidence, "min-confidence", 0.0, "Minimum confidence (default: 0.0)")
	cmd.Flags().Float64Var(&maxConfidence, "max-confidence", 0.7, "Maximum confidence (default: 0.7)")
	cmd.Flags().IntVar(&limit, "limit", 1000, "Max number of samples to export (default: 1000)")
	cmd.Flags().StringVar(&outputPath, "output", "", "Output CSV file path (required)")
	cmd.Flags().StringVar(&sampling, "sampling", "all", "Sampling strategy: all (confidence ascending, default) or uncertain (active-learning uncertainty sampling with per-task-type quotas)")

	cmd.MarkFlagRequired("start")
	cmd.MarkFlagRequired("end")
	cmd.MarkFlagRequired("output")

	return cmd
}

// importCmd creates the import command.
func importCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <csv-file>",
		Short: "Import annotated CSV to database",
		Long: `Import human annotations from CSV file to training_human_annotations table.

The import process:
  1. Read and validate CSV file
  2. Check for required fields (human_provider, is_correct, annotator)
  3. Skip already-annotated samples
  4. Insert new annotations

Example:
  llm-gw-annotator import annotations.csv`,
		Args: cobra.ExactArgs(1),
		RunE: runImport,
	}

	return cmd
}

// validateCmd creates the validate command.
func validateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <csv-file>",
		Short: "Validate CSV format before import",
		Long: `Validate CSV file to ensure it has correct format and required fields.

Validation checks:
  1. CSV header has required columns
  2. All rows have valid field values
  3. is_correct is TRUE or FALSE
  4. confidence is in [0, 1] range
  5. reason is one of: performance, cost, availability, quality, other, correct

Example:
  llm-gw-annotator validate annotations.csv`,
		Args: cobra.ExactArgs(1),
		RunE: runValidate,
	}

	return cmd
}

// statsCmd creates the stats command.
func statsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show annotation statistics",
		Long: `Show annotation statistics including:
  - Overall accuracy (ML prediction accuracy)
  - Per-provider accuracy
  - Per-annotator statistics
  - Annotation reason distribution

Example:
  llm-gw-annotator stats
  llm-gw-annotator stats --provider
  llm-gw-annotator stats --annotator`,
		RunE: runStats,
	}

	return cmd
}

// runExport executes the export command.
func runExport(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Connect to database
	db, err := pgxpool.New(ctx, dbConnStr)
	if err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer db.Close()

	// Validate sampling strategy
	switch sampling {
	case "all", "uncertain":
	default:
		return fmt.Errorf("invalid --sampling %q (must be \"all\" or \"uncertain\")", sampling)
	}

	// Parse date range
	start, end, err := annotation.ParseDateRange(startDate, endDate)
	if err != nil {
		return fmt.Errorf("parse date range failed: %w", err)
	}

	// Build export config
	config := annotation.ExportConfig{
		StartDate:  start,
		EndDate:    end,
		Limit:      limit,
		OutputPath: outputPath,
	}

	if minConfidence > 0.0 {
		config.MinConfidence = &minConfidence
	}

	if maxConfidence < 1.0 {
		config.MaxConfidence = &maxConfidence
	}

	// Get export stats first
	exporter := annotation.NewExporter(db)
	count, err := exporter.GetExportStats(ctx, config)
	if err != nil {
		return fmt.Errorf("get export stats failed: %w", err)
	}

	fmt.Printf("Found %d samples matching criteria (limit: %d)\n\n", count, limit)

	if count == 0 {
		fmt.Println("No samples to export.")
		return nil
	}

	// Execute export
	fmt.Printf("Exporting to: %s (sampling: %s)\n", outputPath, sampling)
	var exported int
	if sampling == "uncertain" {
		// Active-learning uncertainty sampling: confidence [0.4, 0.6] first,
		// backfill < 0.4, per-task-type floor quotas. --limit still caps total.
		exported, err = exporter.ExportUncertain(ctx, config, annotation.DefaultUncertainSamplingOptions())
		if err != nil {
			return fmt.Errorf("export failed: %w", err)
		}
	} else {
		if err := exporter.Export(ctx, config); err != nil {
			return fmt.Errorf("export failed: %w", err)
		}
		exported = min(count, limit)
	}

	fmt.Printf("\n✅ Export completed successfully\n")
	fmt.Printf("   Output: %s\n", outputPath)
	fmt.Printf("   Samples: %d\n\n", exported)
	fmt.Println("Next steps:")
	fmt.Println("  1. Open the CSV file in Excel/Google Sheets")
	fmt.Println("  2. Fill in: human_provider, is_correct, reason, annotator")
	fmt.Println("  3. Validate: llm-gw-annotator validate annotations.csv")
	fmt.Println("  4. Import: llm-gw-annotator import annotations.csv")
	fmt.Println()

	return nil
}

// runImport executes the import command.
func runImport(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	csvPath := args[0]

	fmt.Printf("Importing annotations from: %s\n\n", csvPath)

	// Connect to database
	db, err := pgxpool.New(ctx, dbConnStr)
	if err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer db.Close()

	// Execute import
	importer := annotation.NewImporter(db)
	result, err := importer.Import(ctx, csvPath)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}

	// Print result
	fmt.Printf("✅ Import completed in %d seconds\n\n", result.DurationSeconds)
	fmt.Printf("Total rows:    %d\n", result.TotalRows)
	fmt.Printf("Success:       %d\n", result.SuccessRows)
	fmt.Printf("Skipped:       %d (already annotated)\n", result.SkippedRows)
	fmt.Printf("Errors:        %d\n", result.ErrorRows)
	fmt.Println()

	// Print errors if any
	if len(result.Errors) > 0 {
		fmt.Println("Errors:")
		for i, err := range result.Errors {
			if i >= 10 {
				fmt.Printf("... and %d more errors\n", len(result.Errors)-10)
				break
			}
			fmt.Printf("  Row %d: %s\n", err.Row, err.Message)
		}
		fmt.Println()
	}

	if result.SuccessRows > 0 {
		fmt.Println("Next steps:")
		fmt.Println("  - View statistics: llm-gw-annotator stats")
		fmt.Println("  - Export training data with human labels")
		fmt.Println()
	}

	return nil
}

// runValidate executes the validate command.
func runValidate(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	csvPath := args[0]

	fmt.Printf("Validating CSV file: %s\n\n", csvPath)

	// Connect to database
	db, err := pgxpool.New(ctx, dbConnStr)
	if err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer db.Close()

	// Execute validation
	importer := annotation.NewImporter(db)
	result, err := importer.Validate(ctx, csvPath)
	if err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Print result
	if result.IsValid {
		fmt.Printf("✅ Validation passed\n\n")
		fmt.Printf("Total rows:  %d\n", result.TotalRows)
		fmt.Printf("Valid rows:  %d\n", result.ValidRows)
		fmt.Println()
		fmt.Println("Ready to import:")
		fmt.Printf("  llm-gw-annotator import %s\n\n", csvPath)
	} else {
		fmt.Printf("❌ Validation failed\n\n")
		fmt.Printf("Total rows:    %d\n", result.TotalRows)
		fmt.Printf("Valid rows:    %d\n", result.ValidRows)
		fmt.Printf("Invalid rows:  %d\n", result.InvalidRows)
		fmt.Println()

		fmt.Println("Errors:")
		for i, err := range result.Errors {
			if i >= 20 {
				fmt.Printf("... and %d more errors\n", len(result.Errors)-20)
				break
			}
			if err.Field != "" {
				fmt.Printf("  Row %d, Field %s: %s\n", err.Row, err.Field, err.Message)
			} else {
				fmt.Printf("  Row %d: %s\n", err.Row, err.Message)
			}
		}
		fmt.Println()
	}

	return nil
}

// runStats executes the stats command.
func runStats(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Connect to database
	db, err := pgxpool.New(ctx, dbConnStr)
	if err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer db.Close()

	querier := annotation.NewStatsQuerier(db)

	// 1. Overall stats
	fmt.Println("=== Overall Statistics ===")
	fmt.Println()

	overall, err := querier.GetOverallStats(ctx)
	if err != nil {
		return fmt.Errorf("get overall stats failed: %w", err)
	}

	fmt.Printf("Total Annotations:  %d\n", overall.TotalAnnotations)
	fmt.Printf("Correct:            %d (%.2f%%)\n", overall.CorrectCount, overall.AccuracyPercent)
	fmt.Printf("Incorrect:          %d (%.2f%%)\n", overall.IncorrectCount, 100-overall.AccuracyPercent)
	fmt.Printf("Annotators:         %d\n", overall.NumAnnotators)
	fmt.Printf("First Annotation:   %s\n", overall.FirstAnnotationAt.Format("2006-01-02 15:04"))
	fmt.Printf("Last Annotation:    %s\n", overall.LastAnnotationAt.Format("2006-01-02 15:04"))
	fmt.Println()

	// 2. Per-provider accuracy
	fmt.Println("=== Accuracy by Provider ===")
	fmt.Println()

	providers, err := querier.GetProviderAccuracy(ctx)
	if err != nil {
		return fmt.Errorf("get provider accuracy failed: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tTOTAL\tCORRECT\tINCORRECT\tACCURACY\tAVG CONFIDENCE")
	fmt.Fprintln(w, "--------\t-----\t-------\t---------\t--------\t--------------")

	for _, p := range providers {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%.2f%%\t%.3f\n",
			p.Provider, p.TotalPredictions, p.CorrectPredictions,
			p.IncorrectPredictions, p.AccuracyPercent, p.AvgConfidence)
	}
	w.Flush()
	fmt.Println()

	// 3. Per-annotator stats
	fmt.Println("=== Statistics by Annotator ===")
	fmt.Println()

	annotators, err := querier.GetAnnotatorStats(ctx)
	if err != nil {
		return fmt.Errorf("get annotator stats failed: %w", err)
	}

	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ANNOTATOR\tTOTAL\tCORRECT\tINCORRECT\tACCURACY\tHOURS")
	fmt.Fprintln(w, "---------\t-----\t-------\t---------\t--------\t-----")

	for _, a := range annotators {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%.2f%%\t%.1f\n",
			a.Annotator, a.TotalAnnotations, a.CorrectCount,
			a.IncorrectCount, a.AccuracyPercent, a.HoursSpan)
	}
	w.Flush()
	fmt.Println()

	// 4. Reason distribution
	fmt.Println("=== Annotation Reason Distribution ===")
	fmt.Println()

	reasons, err := querier.GetReasonDistribution(ctx)
	if err != nil {
		return fmt.Errorf("get reason distribution failed: %w", err)
	}

	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REASON\tCOUNT\tPERCENT")
	fmt.Fprintln(w, "------\t-----\t-------")

	for _, r := range reasons {
		fmt.Fprintf(w, "%s\t%d\t%.2f%%\n", r.Reason, r.Count, r.Percent)
	}
	w.Flush()
	fmt.Println()

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func init() {
	// Setup logging
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
}
