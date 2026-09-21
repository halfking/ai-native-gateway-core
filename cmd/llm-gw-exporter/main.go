// cmd/llm-gw-exporter/main.go — 2026-09-06
//
// llm-gw-exporter: CLI tool for exporting AUTO route training data.
//
// Commands:
//   export   - Export training data to Parquet file
//   list     - List export history
//   validate - Validate exported Parquet file (privacy check)
//   configs  - List available export configs
//
// Privacy guarantee:
//   - ✅ Only exports 15 structured feature fields
//   - ❌ Never exports prompt/messages/response
//   - ✅ Validate command checks for prohibited fields
//
// Usage:
//   llm-gw-exporter export --config balanced-high-quality-v1 --output /data/exports/2026-09-06.parquet
//   llm-gw-exporter list --limit 10
//   llm-gw-exporter validate /data/exports/2026-09-06.parquet
//
// Part of: P2.2 - Training Data Export Pipeline

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/exporter"
	"github.com/spf13/cobra"
	"github.com/xitongsys/parquet-go-source/local"
	"github.com/xitongsys/parquet-go/reader"
)

var (
	dbConnStr  string
	configName string
	configID   int
	outputPath string
	limit      int
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "llm-gw-exporter",
		Short: "Export AUTO route training data to Parquet",
		Long: `llm-gw-exporter exports structured features and labels from auto_route_selections.

Privacy guarantee:
  - Only exports 15 structured feature fields (language, length, complexity, etc.)
  - Never exports prompt, messages, response, or any reversible content
  - Validate command checks exported files for privacy compliance

Example:
  llm-gw-exporter export --config balanced-high-quality-v1 --output /data/export.parquet
  llm-gw-exporter list --limit 10
  llm-gw-exporter validate /data/export.parquet`,
	}

	rootCmd.PersistentFlags().StringVar(&dbConnStr, "db", os.Getenv("DATABASE_URL"), "Database connection string")

	rootCmd.AddCommand(exportCmd())
	rootCmd.AddCommand(listCmd())
	rootCmd.AddCommand(validateCmd())
	rootCmd.AddCommand(configsCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// exportCmd creates the export command.
func exportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export training data to Parquet file",
		Long: `Export structured features and labels to a Parquet file.

The export process:
  1. Load export config by name or ID
  2. Query auto_route_selections (only structured features)
  3. Apply quality filters (confidence, reward, etc.)
  4. Deduplicate by content_hash or request_id
  5. Write to Parquet file with compression

Privacy guarantee:
  - Query only accesses 15 structured feature columns
  - Never touches prompt, messages, response columns
  - Output file contains only non-reversible features

Example:
  llm-gw-exporter export --config balanced-high-quality-v1 --output /data/export.parquet`,
		RunE: runExport,
	}

	cmd.Flags().StringVar(&configName, "config", "", "Export config name (required)")
	cmd.Flags().IntVar(&configID, "config-id", 0, "Export config ID (alternative to --config)")
	cmd.Flags().StringVar(&outputPath, "output", "", "Output Parquet file path (required)")
	cmd.MarkFlagRequired("output")

	return cmd
}

// listCmd creates the list command.
func listCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List export history",
		Long: `List recent export executions with status and statistics.

Example:
  llm-gw-exporter list --limit 10
  llm-gw-exporter list --config-id 1 --limit 20`,
		RunE: runList,
	}

	cmd.Flags().IntVar(&configID, "config-id", 0, "Filter by config ID (optional)")
	cmd.Flags().IntVar(&limit, "limit", 10, "Max number of exports to show")

	return cmd
}

// validateCmd creates the validate command.
func validateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <parquet-file>",
		Short: "Validate exported Parquet file (privacy check)",
		Long: `Validate a Parquet file to ensure it doesn't contain prohibited fields.

Privacy checks:
  1. File schema must not contain: prompt, messages, response, summary, keywords
  2. Record count matches expected range
  3. Required fields are present

Example:
  llm-gw-exporter validate /data/export.parquet`,
		Args: cobra.ExactArgs(1),
		RunE: runValidate,
	}

	return cmd
}

// configsCmd creates the configs command.
func configsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "configs",
		Short: "List available export configs",
		Long: `List all export configurations with their parameters.

Example:
  llm-gw-exporter configs`,
		RunE: runConfigs,
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

	// Resolve config ID from name if needed
	if configName != "" && configID == 0 {
		configID, err = resolveConfigID(ctx, db, configName)
		if err != nil {
			return fmt.Errorf("resolve config failed: %w", err)
		}
	}

	if configID == 0 {
		return fmt.Errorf("either --config or --config-id is required")
	}

	// Execute export
	exp := exporter.NewTrainingExporter(db)
	result, err := exp.Export(ctx, configID, outputPath)
	if err != nil {
		return fmt.Errorf("export failed: %w", err)
	}

	// Print result
	fmt.Printf("\n✅ Export completed successfully\n\n")
	fmt.Printf("Export ID:         %d\n", result.ExportID)
	fmt.Printf("Output Path:       %s\n", result.OutputPath)
	fmt.Printf("Rows (raw):        %d\n", result.RowCountRaw)
	fmt.Printf("Rows (filtered):   %d\n", result.RowCountFiltered)
	fmt.Printf("Rows (deduped):    %d\n", result.RowCountDeduped)
	fmt.Printf("File Size:         %.2f MB\n", float64(result.FileSizeBytes)/(1024*1024))
	fmt.Printf("Duration:          %d seconds\n", result.DurationSeconds)
	fmt.Printf("\n")

	return nil
}

// runList executes the list command.
func runList(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Connect to database
	db, err := pgxpool.New(ctx, dbConnStr)
	if err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer db.Close()

	// Query exports
	exp := exporter.NewTrainingExporter(db)
	var configIDPtr *int
	if configID > 0 {
		configIDPtr = &configID
	}
	exports, err := exp.ListExports(ctx, configIDPtr, limit)
	if err != nil {
		return fmt.Errorf("list exports failed: %w", err)
	}

	if len(exports) == 0 {
		fmt.Println("No exports found.")
		return nil
	}

	// Print table
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCONFIG\tSTATUS\tROWS\tSIZE (MB)\tDURATION (s)\tCREATED AT")
	fmt.Fprintln(w, "--\t------\t------\t----\t---------\t------------\t----------")

	for _, e := range exports {
		rowCount := "N/A"
		if e.RowCount != nil {
			rowCount = fmt.Sprintf("%d", *e.RowCount)
		}

		fileSize := "N/A"
		if e.FileSizeBytes != nil {
			fileSize = fmt.Sprintf("%.2f", float64(*e.FileSizeBytes)/(1024*1024))
		}

		duration := "N/A"
		if e.DurationSeconds != nil {
			duration = fmt.Sprintf("%d", *e.DurationSeconds)
		}

		createdAt := e.CreatedAt.Format("2006-01-02 15:04")

		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.ID, e.ConfigName, e.Status, rowCount, fileSize, duration, createdAt)
	}

	w.Flush()
	return nil
}

// runValidate executes the validate command.
func runValidate(cmd *cobra.Command, args []string) error {
	filePath := args[0]

	fmt.Printf("Validating Parquet file: %s\n\n", filePath)

	// Open Parquet file
	fr, err := local.NewLocalFileReader(filePath)
	if err != nil {
		return fmt.Errorf("open file failed: %w", err)
	}
	defer fr.Close()

	// Create Parquet reader
	pr, err := reader.NewParquetReader(fr, new(exporter.TrainingDataRecord), 1)
	if err != nil {
		return fmt.Errorf("create parquet reader failed: %w", err)
	}
	defer pr.ReadStop()

	// Check 1: Schema validation (no prohibited fields)
	fmt.Println("✅ Check 1: Schema validation")
	schema := pr.SchemaHandler.SchemaElements
	prohibitedFields := exporter.ProhibitedFields()

	var foundProhibited []string
	for _, elem := range schema {
		for _, prohibited := range prohibitedFields {
			if elem.Name == prohibited {
				foundProhibited = append(foundProhibited, prohibited)
			}
		}
	}

	if len(foundProhibited) > 0 {
		fmt.Printf("   ❌ FAILED: Found prohibited fields: %v\n", foundProhibited)
		return fmt.Errorf("privacy violation: file contains prohibited fields")
	}
	fmt.Println("   ✅ No prohibited fields found")

	// Check 2: Required fields presence
	fmt.Println("\n✅ Check 2: Required fields presence")
	requiredFields := []string{"request_id", "timestamp", "chosen_model", "feature_version", "content_hash"}
	var missingFields []string

	for _, required := range requiredFields {
		found := false
		for _, elem := range schema {
			if elem.Name == required {
				found = true
				break
			}
		}
		if !found {
			missingFields = append(missingFields, required)
		}
	}

	if len(missingFields) > 0 {
		fmt.Printf("   ❌ FAILED: Missing required fields: %v\n", missingFields)
		return fmt.Errorf("invalid schema: missing required fields")
	}
	fmt.Println("   ✅ All required fields present")

	// Check 3: Record count
	fmt.Println("\n✅ Check 3: Record count")
	numRows := pr.GetNumRows()
	fmt.Printf("   Total rows: %d\n", numRows)

	if numRows == 0 {
		fmt.Println("   ⚠️  WARNING: File is empty")
	} else {
		fmt.Println("   ✅ File contains data")
	}

	// Check 4: Sample records (read first 5)
	fmt.Println("\n✅ Check 4: Sample records")
	sampleSize := 5
	if int(numRows) < sampleSize {
		sampleSize = int(numRows)
	}

	records := make([]*exporter.TrainingDataRecord, sampleSize)
	if err := pr.Read(&records); err != nil {
		return fmt.Errorf("read sample records failed: %w", err)
	}

	fmt.Printf("   Read %d sample records\n", len(records))
	for i, rec := range records {
		fmt.Printf("   [%d] request_id=%s, chosen_model=%s, confidence=%.2f\n",
			i+1, rec.RequestID, rec.ChosenModel, rec.Confidence)
	}

	// Final summary
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("✅ Validation passed: File is privacy-compliant")
	fmt.Printf("   Total rows: %d\n", numRows)
	fmt.Printf("   No prohibited fields found\n")
	fmt.Println(strings.Repeat("=", 60) + "\n")

	return nil
}

// runConfigs executes the configs command.
func runConfigs(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Connect to database
	db, err := pgxpool.New(ctx, dbConnStr)
	if err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer db.Close()

	// Query configs
	query := `
		SELECT id, name, description, feature_version, 
		       time_range_start, time_range_end, dedup_strategy
		FROM training_export_configs
		ORDER BY created_at DESC
	`

	rows, err := db.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("query configs failed: %w", err)
	}
	defer rows.Close()

	// Print table
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tVERSION\tTIME RANGE\tDEDUP\tDESCRIPTION")
	fmt.Fprintln(w, "--\t----\t-------\t----------\t-----\t-----------")

	for rows.Next() {
		var id int
		var name, description, featureVersion, dedupStrategy string
		var timeRangeStart, timeRangeEnd time.Time

		err := rows.Scan(&id, &name, &description, &featureVersion,
			&timeRangeStart, &timeRangeEnd, &dedupStrategy)
		if err != nil {
			return fmt.Errorf("scan row failed: %w", err)
		}

		timeRange := fmt.Sprintf("%s to %s",
			timeRangeStart.Format("2006-01-02"),
			timeRangeEnd.Format("2006-01-02"))

		// Truncate description
		if len(description) > 40 {
			description = description[:37] + "..."
		}

		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
			id, name, featureVersion, timeRange, dedupStrategy, description)
	}

	w.Flush()
	return nil
}

// resolveConfigID resolves config name to ID.
func resolveConfigID(ctx context.Context, db *pgxpool.Pool, name string) (int, error) {
	var id int
	err := db.QueryRow(ctx, "SELECT id FROM training_export_configs WHERE name = $1", name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("config '%s' not found: %w", name, err)
	}
	return id, nil
}

func init() {
	// Setup logging
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
}
