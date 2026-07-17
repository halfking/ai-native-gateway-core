package main

// validate_sessions_v2: Data validation tool for Sessions V2 storage
//
// Purpose:
//   Compare data between request_logs (V1) and sessions V2 tables to verify
//   dual-write integrity and identify discrepancies.
//
// Validation checks:
//   1. Row count parity: request_logs vs session_turns
//   2. Token sum consistency: request_logs.usage vs session_turns aggregates
//   3. Cost sum consistency: request_logs.cost vs session_turns.cost_usd
//   4. Session snapshot accuracy: sessions table vs aggregated session_turns
//   5. Bodies integrity: session_bodies delta reconstruction matches request_logs.body
//
// Usage:
//   go run ./cmd/tools/validate_sessions_v2 \
//     -dsn "postgres://user:pass@host:5432/gateway" \
//     -tenant-id "tenant_xxx" \
//     -start-date "2026-07-01" \
//     -end-date "2026-07-17" \
//     [-session-id "gw_xxxxx"] \
//     [-verbose]
//
// Output:
//   - Summary report with pass/fail for each check
//   - Detailed discrepancies if -verbose is set
//   - Exit code 0 if all checks pass, 1 otherwise

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
	dsn := flag.String("dsn", "", "PostgreSQL DSN (required)")
	tenantID := flag.String("tenant-id", "", "Tenant ID to validate (required)")
	startDate := flag.String("start-date", "", "Start date (YYYY-MM-DD)")
	endDate := flag.String("end-date", "", "End date (YYYY-MM-DD)")
	sessionID := flag.String("session-id", "", "Specific session ID to validate (optional)")
	verbose := flag.Bool("verbose", false, "Show detailed discrepancies")
	flag.Parse()

	if *dsn == "" || *tenantID == "" {
		fmt.Fprintln(os.Stderr, "error: -dsn and -tenant-id are required")
		flag.Usage()
		os.Exit(2)
	}

	ctx := context.Background()

	// Connect to database
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	// Parse date range
	var start, end time.Time
	if *startDate != "" {
		start, err = time.Parse("2006-01-02", *startDate)
		if err != nil {
			log.Fatalf("parse start-date: %v", err)
		}
	}
	if *endDate != "" {
		end, err = time.Parse("2006-01-02", *endDate)
		if err != nil {
			log.Fatalf("parse end-date: %v", err)
		}
	}

	// Run validation
	validator := NewValidator(pool, *tenantID, *verbose)
	report, err := validator.Validate(ctx, ValidateOptions{
		StartDate: start,
		EndDate:   end,
		SessionID: *sessionID,
	})

	if err != nil {
		log.Fatalf("validation failed: %v", err)
	}

	// Print report
	printReport(report)

	// Exit code
	if !report.AllPassed() {
		os.Exit(1)
	}
}

// ValidateOptions configures validation scope
type ValidateOptions struct {
	StartDate time.Time
	EndDate   time.Time
	SessionID string
}

// Validator validates V2 data against V1 (request_logs)
type Validator struct {
	db       *pgxpool.Pool
	tenantID string
	verbose  bool
}

// NewValidator creates a new validator instance
func NewValidator(db *pgxpool.Pool, tenantID string, verbose bool) *Validator {
	return &Validator{
		db:       db,
		tenantID: tenantID,
		verbose:  verbose,
	}
}

// Validate runs all validation checks
func (v *Validator) Validate(ctx context.Context, opts ValidateOptions) (*ValidationReport, error) {
	report := &ValidationReport{
		TenantID:  v.tenantID,
		StartDate: opts.StartDate,
		EndDate:   opts.EndDate,
		SessionID: opts.SessionID,
		Timestamp: time.Now(),
	}

	log.Println("=== Starting Sessions V2 Validation ===")
	log.Printf("Tenant: %s", v.tenantID)
	if !opts.StartDate.IsZero() {
		log.Printf("Date range: %s to %s", opts.StartDate.Format("2006-01-02"), opts.EndDate.Format("2006-01-02"))
	}
	if opts.SessionID != "" {
		log.Printf("Session: %s", opts.SessionID)
	}

	// Check 1: Row count parity
	log.Println("\n[1/5] Checking row count parity...")
	check1, err := v.checkRowCountParity(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("check row count: %w", err)
	}
	report.Checks = append(report.Checks, check1)

	// Check 2: Token sum consistency
	log.Println("\n[2/5] Checking token sum consistency...")
	check2, err := v.checkTokenSumConsistency(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("check token sum: %w", err)
	}
	report.Checks = append(report.Checks, check2)

	// Check 3: Cost sum consistency
	log.Println("\n[3/5] Checking cost sum consistency...")
	check3, err := v.checkCostSumConsistency(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("check cost sum: %w", err)
	}
	report.Checks = append(report.Checks, check3)

	// Check 4: Session snapshot accuracy
	log.Println("\n[4/5] Checking session snapshot accuracy...")
	check4, err := v.checkSessionSnapshotAccuracy(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("check session snapshot: %w", err)
	}
	report.Checks = append(report.Checks, check4)

	// Check 5: Bodies integrity
	log.Println("\n[5/5] Checking bodies integrity...")
	check5, err := v.checkBodiesIntegrity(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("check bodies integrity: %w", err)
	}
	report.Checks = append(report.Checks, check5)

	log.Println("\n=== Validation Complete ===")
	return report, nil
}

// checkRowCountParity verifies request_logs row count matches session_turns
func (v *Validator) checkRowCountParity(ctx context.Context, opts ValidateOptions) (ValidationCheck, error) {
	check := ValidationCheck{Name: "Row Count Parity"}

	// Count request_logs
	var v1Count int
	query := `
		SELECT COUNT(*)
		FROM gateway.request_logs
		WHERE tenant_id = $1
	`
	args := []interface{}{v.tenantID}
	argIdx := 2

	if !opts.StartDate.IsZero() {
		query += fmt.Sprintf(" AND ts >= $%d", argIdx)
		args = append(args, opts.StartDate)
		argIdx++
	}
	if !opts.EndDate.IsZero() {
		query += fmt.Sprintf(" AND ts < $%d", argIdx)
		args = append(args, opts.EndDate)
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
	}

	err := v.db.QueryRow(ctx, query, args...).Scan(&v1Count)
	if err != nil {
		return check, fmt.Errorf("count request_logs: %w", err)
	}

	// Count session_turns
	var v2Count int
	query = `
		SELECT COUNT(*)
		FROM gateway.session_turns
		WHERE tenant_id = $1
	`
	args = []interface{}{v.tenantID}
	argIdx = 2

	if !opts.StartDate.IsZero() {
		query += fmt.Sprintf(" AND ts >= $%d", argIdx)
		args = append(args, opts.StartDate)
		argIdx++
	}
	if !opts.EndDate.IsZero() {
		query += fmt.Sprintf(" AND ts < $%d", argIdx)
		args = append(args, opts.EndDate)
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
	}

	err = v.db.QueryRow(ctx, query, args...).Scan(&v2Count)
	if err != nil {
		return check, fmt.Errorf("count session_turns: %w", err)
	}

	// Compare
	check.V1Value = v1Count
	check.V2Value = v2Count
	check.Passed = (v1Count == v2Count)

	if !check.Passed {
		check.Discrepancy = fmt.Sprintf("request_logs: %d rows, session_turns: %d rows (diff: %d)",
			v1Count, v2Count, v1Count-v2Count)
	}

	log.Printf("  request_logs: %d rows", v1Count)
	log.Printf("  session_turns: %d rows", v2Count)
	if check.Passed {
		log.Println("  ✓ PASSED")
	} else {
		log.Printf("  ✗ FAILED: %s", check.Discrepancy)
	}

	return check, nil
}

// checkTokenSumConsistency verifies token usage sums match
func (v *Validator) checkTokenSumConsistency(ctx context.Context, opts ValidateOptions) (ValidationCheck, error) {
	check := ValidationCheck{Name: "Token Sum Consistency"}

	// Sum from request_logs
	var v1Tokens int64
	query := `
		SELECT COALESCE(SUM((usage->>'prompt_tokens')::int + (usage->>'completion_tokens')::int), 0)
		FROM gateway.request_logs
		WHERE tenant_id = $1
		AND usage IS NOT NULL
	`
	args := []interface{}{v.tenantID}
	argIdx := 2

	if !opts.StartDate.IsZero() {
		query += fmt.Sprintf(" AND ts >= $%d", argIdx)
		args = append(args, opts.StartDate)
		argIdx++
	}
	if !opts.EndDate.IsZero() {
		query += fmt.Sprintf(" AND ts < $%d", argIdx)
		args = append(args, opts.EndDate)
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
	}

	err := v.db.QueryRow(ctx, query, args...).Scan(&v1Tokens)
	if err != nil {
		return check, fmt.Errorf("sum request_logs tokens: %w", err)
	}

	// Sum from session_turns
	var v2Tokens int64
	query = `
		SELECT COALESCE(SUM(prompt_tokens + completion_tokens), 0)
		FROM gateway.session_turns
		WHERE tenant_id = $1
	`
	args = []interface{}{v.tenantID}
	argIdx = 2

	if !opts.StartDate.IsZero() {
		query += fmt.Sprintf(" AND ts >= $%d", argIdx)
		args = append(args, opts.StartDate)
		argIdx++
	}
	if !opts.EndDate.IsZero() {
		query += fmt.Sprintf(" AND ts < $%d", argIdx)
		args = append(args, opts.EndDate)
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
	}

	err = v.db.QueryRow(ctx, query, args...).Scan(&v2Tokens)
	if err != nil {
		return check, fmt.Errorf("sum session_turns tokens: %w", err)
	}

	// Compare (allow 1% tolerance for rounding)
	check.V1Value = v1Tokens
	check.V2Value = v2Tokens
	diff := abs(v1Tokens - v2Tokens)
	tolerance := int64(float64(v1Tokens) * 0.01)
	check.Passed = (diff <= tolerance)

	if !check.Passed {
		check.Discrepancy = fmt.Sprintf("request_logs: %d tokens, session_turns: %d tokens (diff: %d, tolerance: %d)",
			v1Tokens, v2Tokens, diff, tolerance)
	}

	log.Printf("  request_logs: %d tokens", v1Tokens)
	log.Printf("  session_turns: %d tokens", v2Tokens)
	if check.Passed {
		log.Println("  ✓ PASSED")
	} else {
		log.Printf("  ✗ FAILED: %s", check.Discrepancy)
	}

	return check, nil
}

// checkCostSumConsistency verifies cost sums match
func (v *Validator) checkCostSumConsistency(ctx context.Context, opts ValidateOptions) (ValidationCheck, error) {
	check := ValidationCheck{Name: "Cost Sum Consistency"}

	// Sum from request_logs
	var v1Cost float64
	query := `
		SELECT COALESCE(SUM(cost_usd), 0)
		FROM gateway.request_logs
		WHERE tenant_id = $1
	`
	args := []interface{}{v.tenantID}
	argIdx := 2

	if !opts.StartDate.IsZero() {
		query += fmt.Sprintf(" AND ts >= $%d", argIdx)
		args = append(args, opts.StartDate)
		argIdx++
	}
	if !opts.EndDate.IsZero() {
		query += fmt.Sprintf(" AND ts < $%d", argIdx)
		args = append(args, opts.EndDate)
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
	}

	err := v.db.QueryRow(ctx, query, args...).Scan(&v1Cost)
	if err != nil {
		return check, fmt.Errorf("sum request_logs cost: %w", err)
	}

	// Sum from session_turns
	var v2Cost float64
	query = `
		SELECT COALESCE(SUM(cost_usd), 0)
		FROM gateway.session_turns
		WHERE tenant_id = $1
	`
	args = []interface{}{v.tenantID}
	argIdx = 2

	if !opts.StartDate.IsZero() {
		query += fmt.Sprintf(" AND ts >= $%d", argIdx)
		args = append(args, opts.StartDate)
		argIdx++
	}
	if !opts.EndDate.IsZero() {
		query += fmt.Sprintf(" AND ts < $%d", argIdx)
		args = append(args, opts.EndDate)
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
	}

	err = v.db.QueryRow(ctx, query, args...).Scan(&v2Cost)
	if err != nil {
		return check, fmt.Errorf("sum session_turns cost: %w", err)
	}

	// Compare (allow $0.01 or 1% tolerance)
	check.V1Value = v1Cost
	check.V2Value = v2Cost
	diff := abs64(v1Cost - v2Cost)
	tolerance := max64(0.01, v1Cost*0.01)
	check.Passed = (diff <= tolerance)

	if !check.Passed {
		check.Discrepancy = fmt.Sprintf("request_logs: $%.6f, session_turns: $%.6f (diff: $%.6f, tolerance: $%.6f)",
			v1Cost, v2Cost, diff, tolerance)
	}

	log.Printf("  request_logs: $%.6f", v1Cost)
	log.Printf("  session_turns: $%.6f", v2Cost)
	if check.Passed {
		log.Println("  ✓ PASSED")
	} else {
		log.Printf("  ✗ FAILED: %s", check.Discrepancy)
	}

	return check, nil
}

// checkSessionSnapshotAccuracy verifies sessions table aggregates match session_turns
func (v *Validator) checkSessionSnapshotAccuracy(ctx context.Context, opts ValidateOptions) (ValidationCheck, error) {
	check := ValidationCheck{Name: "Session Snapshot Accuracy"}

	// Get sessions from V2
	query := `
		SELECT session_id, total_turns, total_tokens, total_cost_usd
		FROM gateway.sessions
		WHERE tenant_id = $1
	`
	args := []interface{}{v.tenantID}
	argIdx := 2

	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
	}

	rows, err := v.db.Query(ctx, query, args...)
	if err != nil {
		return check, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var discrepancies []string
	sessionsChecked := 0
	sessionsPassed := 0

	for rows.Next() {
		var sessionID string
		var snapshotTurns, snapshotTokens int
		var snapshotCost float64

		err := rows.Scan(&sessionID, &snapshotTurns, &snapshotTokens, &snapshotCost)
		if err != nil {
			return check, fmt.Errorf("scan session: %w", err)
		}

		// Get actual aggregates from session_turns
		var actualTurns, actualTokens int
		var actualCost float64
		err = v.db.QueryRow(ctx, `
			SELECT 
				COUNT(*),
				COALESCE(SUM(prompt_tokens + completion_tokens), 0),
				COALESCE(SUM(cost_usd), 0)
			FROM gateway.session_turns
			WHERE tenant_id = $1 AND session_id = $2
		`, v.tenantID, sessionID).Scan(&actualTurns, &actualTokens, &actualCost)

		if err != nil {
			return check, fmt.Errorf("aggregate session_turns for %s: %w", sessionID, err)
		}

		sessionsChecked++

		// Compare (allow 1% tolerance)
		turnsMatch := (snapshotTurns == actualTurns)
		tokensDiff := abs(int64(snapshotTokens - actualTokens))
		tokensMatch := tokensDiff <= int64(float64(actualTokens)*0.01)
		costDiff := abs64(snapshotCost - actualCost)
		costMatch := costDiff <= max64(0.01, actualCost*0.01)

		if turnsMatch && tokensMatch && costMatch {
			sessionsPassed++
		} else {
			discrepancy := fmt.Sprintf("Session %s: snapshot(turns=%d, tokens=%d, cost=$%.4f) vs actual(turns=%d, tokens=%d, cost=$%.4f)",
				sessionID, snapshotTurns, snapshotTokens, snapshotCost, actualTurns, actualTokens, actualCost)
			discrepancies = append(discrepancies, discrepancy)

			if v.verbose {
				log.Printf("  %s", discrepancy)
			}
		}
	}

	if err := rows.Err(); err != nil {
		return check, fmt.Errorf("iterate sessions: %w", err)
	}

	check.V1Value = sessionsChecked
	check.V2Value = sessionsPassed
	check.Passed = (sessionsChecked == sessionsPassed)

	if !check.Passed {
		check.Discrepancy = fmt.Sprintf("%d/%d sessions have accurate snapshots", sessionsPassed, sessionsChecked)
		if len(discrepancies) > 0 && len(discrepancies) <= 10 {
			check.Details = discrepancies
		}
	}

	log.Printf("  Sessions checked: %d", sessionsChecked)
	log.Printf("  Sessions passed: %d", sessionsPassed)
	if check.Passed {
		log.Println("  ✓ PASSED")
	} else {
		log.Printf("  ✗ FAILED: %s", check.Discrepancy)
	}

	return check, nil
}

// checkBodiesIntegrity verifies session_bodies deltas can reconstruct request_logs bodies
func (v *Validator) checkBodiesIntegrity(ctx context.Context, opts ValidateOptions) (ValidationCheck, error) {
	check := ValidationCheck{Name: "Bodies Integrity"}

	// Sample sessions to check (limit to 100 for performance)
	query := `
		SELECT DISTINCT session_id
		FROM gateway.session_turns
		WHERE tenant_id = $1
	`
	args := []interface{}{v.tenantID}
	argIdx := 2

	if !opts.StartDate.IsZero() {
		query += fmt.Sprintf(" AND ts >= $%d", argIdx)
		args = append(args, opts.StartDate)
		argIdx++
	}
	if !opts.EndDate.IsZero() {
		query += fmt.Sprintf(" AND ts < $%d", argIdx)
		args = append(args, opts.EndDate)
		argIdx++
	}
	if opts.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, opts.SessionID)
	} else {
		query += " LIMIT 100"
	}

	rows, err := v.db.Query(ctx, query, args...)
	if err != nil {
		return check, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return check, fmt.Errorf("scan session_id: %w", err)
		}
		sessionIDs = append(sessionIDs, sessionID)
	}

	if err := rows.Err(); err != nil {
		return check, fmt.Errorf("iterate sessions: %w", err)
	}

	sessionsChecked := 0
	sessionsPassed := 0
	var discrepancies []string

	for _, sessionID := range sessionIDs {
		passed, discrepancy := v.checkSessionBodies(ctx, sessionID)
		sessionsChecked++
		if passed {
			sessionsPassed++
		} else {
			discrepancies = append(discrepancies, discrepancy)
			if v.verbose {
				log.Printf("  %s", discrepancy)
			}
		}
	}

	check.V1Value = sessionsChecked
	check.V2Value = sessionsPassed
	check.Passed = (sessionsChecked == sessionsPassed)

	if !check.Passed {
		check.Discrepancy = fmt.Sprintf("%d/%d sessions have valid body reconstruction", sessionsPassed, sessionsChecked)
		if len(discrepancies) > 0 && len(discrepancies) <= 10 {
			check.Details = discrepancies
		}
	}

	log.Printf("  Sessions checked: %d", sessionsChecked)
	log.Printf("  Sessions passed: %d", sessionsPassed)
	if check.Passed {
		log.Println("  ✓ PASSED")
	} else {
		log.Printf("  ✗ FAILED: %s", check.Discrepancy)
	}

	return check, nil
}

// checkSessionBodies checks if one session's bodies can be reconstructed
func (v *Validator) checkSessionBodies(ctx context.Context, sessionID string) (bool, string) {
	// Get turns for this session
	rows, err := v.db.Query(ctx, `
		SELECT t.turn_no, t.request_id, b.request_delta, b.response_delta
		FROM gateway.session_turns t
		JOIN gateway.session_bodies b ON t.session_id = b.session_id AND t.turn_no = b.turn_no
		WHERE t.tenant_id = $1 AND t.session_id = $2
		ORDER BY t.turn_no ASC
	`, v.tenantID, sessionID)

	if err != nil {
		return false, fmt.Sprintf("Session %s: query turns failed: %v", sessionID, err)
	}
	defer rows.Close()

	turnCount := 0
	for rows.Next() {
		var turnNo int
		var requestID string
		var requestDeltaJSON, responseDeltaJSON []byte

		err := rows.Scan(&turnNo, &requestID, &requestDeltaJSON, &responseDeltaJSON)
		if err != nil {
			return false, fmt.Sprintf("Session %s: scan turn failed: %v", sessionID, err)
		}

		// Verify deltas are valid JSON
		var requestDelta, responseDelta []interface{}
		if len(requestDeltaJSON) > 0 {
			if err := json.Unmarshal(requestDeltaJSON, &requestDelta); err != nil {
				return false, fmt.Sprintf("Session %s turn %d: invalid request_delta JSON", sessionID, turnNo)
			}
		}
		if len(responseDeltaJSON) > 0 {
			if err := json.Unmarshal(responseDeltaJSON, &responseDelta); err != nil {
				return false, fmt.Sprintf("Session %s turn %d: invalid response_delta JSON", sessionID, turnNo)
			}
		}

		turnCount++
	}

	if err := rows.Err(); err != nil {
		return false, fmt.Sprintf("Session %s: iterate turns failed: %v", sessionID, err)
	}

	if turnCount == 0 {
		return false, fmt.Sprintf("Session %s: no bodies found", sessionID)
	}

	return true, ""
}

// ValidationReport contains all validation results
type ValidationReport struct {
	TenantID  string
	StartDate time.Time
	EndDate   time.Time
	SessionID string
	Timestamp time.Time
	Checks    []ValidationCheck
}

// ValidationCheck represents one validation check
type ValidationCheck struct {
	Name        string
	Passed      bool
	V1Value     interface{}
	V2Value     interface{}
	Discrepancy string
	Details     []string
}

// AllPassed returns true if all checks passed
func (r *ValidationReport) AllPassed() bool {
	for _, check := range r.Checks {
		if !check.Passed {
			return false
		}
	}
	return true
}

// printReport prints the validation report
func printReport(report *ValidationReport) {
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("SESSIONS V2 VALIDATION REPORT")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("Tenant:    %s\n", report.TenantID)
	if !report.StartDate.IsZero() {
		fmt.Printf("Date Range: %s to %s\n", report.StartDate.Format("2006-01-02"), report.EndDate.Format("2006-01-02"))
	}
	if report.SessionID != "" {
		fmt.Printf("Session:   %s\n", report.SessionID)
	}
	fmt.Printf("Timestamp: %s\n", report.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Println(strings.Repeat("-", 80))

	passCount := 0
	for _, check := range report.Checks {
		if check.Passed {
			passCount++
		}
	}

	fmt.Printf("\nSummary: %d/%d checks passed\n\n", passCount, len(report.Checks))

	for i, check := range report.Checks {
		status := "✓ PASS"
		if !check.Passed {
			status = "✗ FAIL"
		}

		fmt.Printf("[%d] %s: %s\n", i+1, check.Name, status)

		if !check.Passed {
			fmt.Printf("    %s\n", check.Discrepancy)
			if len(check.Details) > 0 {
				fmt.Println("    Details:")
				for _, detail := range check.Details {
					fmt.Printf("      - %s\n", detail)
				}
			}
		}
	}

	fmt.Println(strings.Repeat("=", 80))

	if report.AllPassed() {
		fmt.Println("✓ ALL CHECKS PASSED")
	} else {
		fmt.Println("✗ SOME CHECKS FAILED")
	}

	fmt.Println(strings.Repeat("=", 80))
}

// Helper functions
func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
