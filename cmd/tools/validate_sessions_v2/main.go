package main

// validate_sessions_v2: Data validation tool for Sessions V2 storage
//
// Purpose:
//   Compare data between request_logs (V1) and sessions V2 tables to verify
//   dual-write integrity and identify discrepancies.
//
// Usage:
//   # Single session validation
//   go run ./cmd/tools/validate_sessions_v2 \
//     -dsn "postgres://user:pass@host:5432/gateway" \
//     -tenant-id "tenant_xxx" \
//     -session-id "gw_abc123" \
//     [-verbose] [-format json|text]
//
//   # Batch validation
//   go run ./cmd/tools/validate_sessions_v2 \
//     -dsn "postgres://user:pass@host:5432/gateway" \
//     -tenant-id "tenant_xxx" \
//     -start-date "2026-07-01" \
//     -end-date "2026-07-17" \
//     [-max-sessions 120] [-settle-window 30m] \

//     [-format json|text] [-verbose]
//
// Output:
//   - JSON format (default): machine-readable, suitable for automation
//   - Text format: human-readable with status indicators
//   - Exit code 0 only when at least 100 settled candidates load and validate
//     without loader errors or skipped sessions (warnings are allowed)
//   - Exit code 1 when the parity gate is not satisfied
//   - -repair/-apply is a separate write-capable repair flow and is never parity evidence

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// CLI flags
	dsn := flag.String("dsn", "", "PostgreSQL DSN (required)")
	tenantID := flag.String("tenant-id", "", "Tenant ID to validate (required)")
	sessionID := flag.String("session-id", "", "Specific session ID to validate (single-session mode)")
	startDate := flag.String("start-date", "", "Start date YYYY-MM-DD (batch mode)")
	endDate := flag.String("end-date", "", "End date YYYY-MM-DD (batch mode)")
	maxSessions := flag.Int("max-sessions", 120, "Maximum sessions to inspect in batch mode; at least 100 settled sessions must pass")
	settleWindow := flag.Duration("settle-window", 30*time.Minute, "Exclude sessions updated within this window (batch mode)")
	format := flag.String("format", "json", "Output format: json or text")
	verbose := flag.Bool("verbose", false, "Show detailed per-session output during batch validation")
	repair := flag.Bool("repair", false, "Enable repair mode (rebuild V2 from V1)")
	apply := flag.Bool("apply", false, "Apply changes (without this, dry-run only)")
	flag.Parse()

	// Validate required flags
	if *dsn == "" || *tenantID == "" {
		fmt.Fprintln(os.Stderr, "Error: -dsn and -tenant-id are required")
		flag.Usage()
		os.Exit(2)
	}

	// Validate repair constraints
	if *repair && *sessionID == "" {
		fmt.Fprintln(os.Stderr, "Error: -repair requires -session-id (single-session only)")
		os.Exit(2)
	}

	// Validate mode: single-session or batch
	isSingleSession := *sessionID != ""
	isBatch := *startDate != "" || *endDate != ""

	if isSingleSession && isBatch {
		fmt.Fprintln(os.Stderr, "Error: cannot use -session-id with -start-date/-end-date")
		os.Exit(2)
	}

	if !isSingleSession && !isBatch {
		fmt.Fprintln(os.Stderr, "Error: must specify either -session-id or -start-date/-end-date")
		flag.Usage()
		os.Exit(2)
	}

	// Validate format
	if *format != "json" && *format != "text" {
		fmt.Fprintln(os.Stderr, "Error: -format must be 'json' or 'text'")
		os.Exit(2)
	}

	ctx := context.Background()

	// Connect to database
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// Initialize modules
	loader := NewSessionLoader(pool)
	reportGen := NewReportGenerator()
	reconstructor := NewMessageReconstructor()

	if isSingleSession {
		if *repair {
			// Repair mode
			validator := NewSessionValidator(*tenantID, *sessionID)
			repairer := NewSessionRepairer(pool, loader, validator, reconstructor, reportGen)
			exitCode := repairSession(ctx, repairer, *tenantID, *sessionID, *apply, *format)
			os.Exit(exitCode)
		} else {
			// Single-session validation
			exitCode := validateSingleSession(ctx, loader, reportGen, reconstructor, *tenantID, *sessionID, *format)
			os.Exit(exitCode)
		}
	} else {
		// Batch validation
		var start, end time.Time
		if *startDate != "" {
			start, err = time.Parse("2006-01-02", *startDate)
			if err != nil {
				log.Fatalf("Invalid -start-date: %v", err)
			}
		}
		if *endDate != "" {
			end, err = time.Parse("2006-01-02", *endDate)
			if err != nil {
				log.Fatalf("Invalid -end-date: %v", err)
			}
		}

		exitCode := validateBatch(ctx, loader, reportGen, reconstructor, *tenantID, start, end, *maxSessions, *settleWindow, *format, *verbose)
		os.Exit(exitCode)
	}
}

// validateSingleSession validates a single session and returns exit code
func validateSingleSession(
	ctx context.Context,
	loader *SessionLoader,
	reportGen *ReportGenerator,
	reconstructor *MessageReconstructor,
	tenantID, sessionID, format string,
) int {
	// Load data
	v1Turns, err := loader.LoadV1Turns(ctx, tenantID, sessionID)
	if err != nil {
		log.Fatalf("Failed to load V1 turns: %v", err)
	}

	v2Turns, err := loader.LoadV2Turns(ctx, tenantID, sessionID)
	if err != nil {
		log.Fatalf("Failed to load V2 turns: %v", err)
	}

	v2Bodies, err := loader.LoadV2Bodies(ctx, tenantID, sessionID)
	if err != nil {
		log.Fatalf("Failed to load V2 bodies: %v", err)
	}

	v2Session, err := loader.LoadV2Session(ctx, tenantID, sessionID)
	if err != nil {
		log.Fatalf("Failed to load V2 session: %v", err)
	}

	// Run validation
	validator := NewSessionValidator(tenantID, sessionID)
	checks := validator.ValidateSession(v1Turns, v2Turns, v2Bodies, v2Session)
	reconResults := reconstructor.ValidateReconstruction(v1Turns, v2Turns, v2Bodies)

	// Generate report
	report := reportGen.GenerateSessionReport(tenantID, sessionID, v1Turns, v2Turns, checks, reconResults)

	// Output
	if format == "json" {
		jsonBytes, err := reportGen.FormatJSON(report)
		if err != nil {
			log.Fatalf("Failed to format JSON: %v", err)
		}
		fmt.Println(string(jsonBytes))
	} else {
		fmt.Print(reportGen.FormatTextSession(report))
	}

	// Determine exit code
	if report.Status == "error" {
		return 1
	}
	return 0
}

// validateBatch validates multiple sessions and returns exit code
func validateBatch(
	ctx context.Context,
	loader *SessionLoader,
	reportGen *ReportGenerator,
	reconstructor *MessageReconstructor,
	tenantID string,
	startDate, endDate time.Time,
	maxSessions int,
	settleWindow time.Duration,
	format string,
	verbose bool,
) int {
	// Load session IDs
	sessionIDs, err := loader.LoadSessionsInRange(ctx, tenantID, startDate, endDate, settleWindow, maxSessions)
	if err != nil {
		log.Fatalf("Failed to load session IDs: %v", err)
	}

	minimumSessions := 100
	batchSummary := BatchSummary{Candidates: len(sessionIDs)}
	if len(sessionIDs) == 0 {
		log.Println("No settled sessions found in the specified range")
	} else {
		log.Printf("Found %d settled sessions to validate", len(sessionIDs))
	}

	// Validate each candidate. A failed loader is a gate failure, not a skipped
	// success, and is retained in the machine-readable batch summary.
	var sessionReports []SessionReport
	for i, sessionID := range sessionIDs {
		if verbose {
			log.Printf("[%d/%d] Validating session %s...", i+1, len(sessionIDs), sessionID)
		}

		v1Turns, err := loader.LoadV1Turns(ctx, tenantID, sessionID)
		if err != nil {
			batchSummary.LoaderErrors++
			log.Printf("Error: failed to load V1 turns for %s: %v", sessionID, err)
			continue
		}
		v2Turns, err := loader.LoadV2Turns(ctx, tenantID, sessionID)
		if err != nil {
			batchSummary.LoaderErrors++
			log.Printf("Error: failed to load V2 turns for %s: %v", sessionID, err)
			continue
		}
		v2Bodies, err := loader.LoadV2Bodies(ctx, tenantID, sessionID)
		if err != nil {
			batchSummary.LoaderErrors++
			log.Printf("Error: failed to load V2 bodies for %s: %v", sessionID, err)
			continue
		}
		v2Session, err := loader.LoadV2Session(ctx, tenantID, sessionID)
		if err != nil {
			batchSummary.LoaderErrors++
			log.Printf("Error: failed to load V2 session for %s: %v", sessionID, err)
			continue
		}

		validator := NewSessionValidator(tenantID, sessionID)
		checks := validator.ValidateSession(v1Turns, v2Turns, v2Bodies, v2Session)
		reconResults := reconstructor.ValidateReconstruction(v1Turns, v2Turns, v2Bodies)
		report := reportGen.GenerateSessionReport(tenantID, sessionID, v1Turns, v2Turns, checks, reconResults)
		sessionReports = append(sessionReports, *report)
		if verbose && report.Status != "ok" {
			log.Printf("  Status: %s (%d differences)", report.Status, len(report.Differences))
		}
	}

	batchReport := reportGen.GenerateBatchReport(tenantID, startDate, endDate, settleWindow, sessionReports)
	batchReport.Summary.Candidates = batchSummary.Candidates
	batchReport.Summary.LoaderErrors = batchSummary.LoaderErrors
	batchReport.Summary.Skipped = batchSummary.Skipped
	batchReport.GatePassed = BatchGatePassed(batchReport.Summary, minimumSessions)

	// Output
	if format == "json" {
		jsonBytes, err := reportGen.FormatJSON(batchReport)
		if err != nil {
			log.Fatalf("Failed to format JSON: %v", err)
		}
		fmt.Println(string(jsonBytes))
	} else {
		fmt.Print(reportGen.FormatTextBatch(batchReport))
	}

	// Determine exit code
	if !batchReport.GatePassed {
		return 1
	}
	return 0
}

// repairSession repairs a single session and returns exit code
func repairSession(
	ctx context.Context,
	repairer *SessionRepairer,
	tenantID, sessionID string,
	apply bool,
	format string,
) int {
	// Generate repair plan
	plan, err := repairer.PlanRepair(ctx, tenantID, sessionID)
	if err != nil {
		log.Fatalf("Failed to generate repair plan: %v", err)
	}

	if !apply {
		// Dry-run mode: show plan without executing
		if format == "json" {
			jsonBytes, _ := json.MarshalIndent(plan, "", "  ")
			fmt.Println(string(jsonBytes))
		} else {
			fmt.Println(strings.Repeat("=", 80))
			fmt.Println("[DRY RUN] Repair plan for session", sessionID)
			fmt.Println(strings.Repeat("=", 80))
			fmt.Printf("Tenant:     %s\n", plan.TenantID)
			fmt.Printf("Session:    %s\n", plan.SessionID)
			fmt.Printf("V1 Source:  %d rows\n\n", plan.SourceRows)

			fmt.Println("Will DELETE:")
			for table, count := range plan.DeleteCounts {
				fmt.Printf("  - %-20s %d rows\n", table+":", count)
			}
			fmt.Println()

			fmt.Println("Will REBUILD:")
			for table, count := range plan.RebuildCounts {
				fmt.Printf("  - %-20s %d rows\n", table+":", count)
			}
			fmt.Println()

			fmt.Println("Run with --apply to execute this repair.")
			fmt.Println(strings.Repeat("=", 80))
		}
		return 0
	}

	// Execute repair
	log.Printf("Executing repair for session %s...", sessionID)
	result, err := repairer.ExecuteRepair(ctx, tenantID, sessionID)
	if err != nil || !result.Success {
		if format == "json" {
			jsonBytes, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(jsonBytes))
		} else {
			fmt.Println(strings.Repeat("=", 80))
			fmt.Println("✗ REPAIR FAILED")
			fmt.Println(strings.Repeat("=", 80))
			if result.Error != nil {
				fmt.Printf("Error: %v\n", result.Error)
			}
			fmt.Println(strings.Repeat("=", 80))
		}
		return 1
	}

	// Verify repair
	log.Printf("Verifying repair for session %s...", sessionID)
	verifyReport, err := repairer.VerifyRepair(ctx, tenantID, sessionID)
	if err != nil {
		log.Fatalf("Failed to verify repair: %v", err)
	}

	result.VerificationReport = verifyReport

	// Output results
	if format == "json" {
		jsonBytes, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(jsonBytes))
	} else {
		fmt.Println(strings.Repeat("=", 80))
		fmt.Println("REPAIR COMPLETED")
		fmt.Println(strings.Repeat("=", 80))
		fmt.Printf("Session:    %s\n", result.SessionID)
		fmt.Printf("Tenant:     %s\n\n", result.TenantID)

		fmt.Println("Deleted:")
		for table, count := range result.DeletedRows {
			fmt.Printf("  ✓ %-20s %d rows\n", table+":", count)
		}
		fmt.Println()

		fmt.Println("Rebuilt:")
		for table, count := range result.InsertedRows {
			fmt.Printf("  ✓ %-20s %d rows\n", table+":", count)
		}
		fmt.Println()

		fmt.Println("Verification:")
		if verifyReport.Status == "ok" {
			fmt.Println("  ✓ Re-validation passed (status: ok)")
		} else if verifyReport.Status == "warning" {
			fmt.Printf("  ⚠ Re-validation passed with warnings (status: %s)\n", verifyReport.Status)
			fmt.Printf("    %d warning(s) found\n", len(verifyReport.Differences))
		} else {
			fmt.Printf("  ✗ Re-validation failed (status: %s)\n", verifyReport.Status)
			fmt.Printf("    %d error(s) found\n", len(verifyReport.Differences))
		}

		fmt.Println(strings.Repeat("=", 80))
	}

	// Exit code based on verification
	if verifyReport.Status == "error" {
		return 1
	}
	return 0
}
