package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const DailyShadowVersion = "request-reconciliation-shadow-v1"
const ProjectedDailyShadowVersion = "request-reconciliation-shadow-v2"
const MaxDailyShadowEvidenceRows = 2000

type DailyShadow struct {
	Version         string                     `json:"version"`
	Day             string                     `json:"day"`
	Timezone        string                     `json:"timezone"`
	Start           time.Time                  `json:"start"`
	End             time.Time                  `json:"end"`
	ReadAt          time.Time                  `json:"read_at"`
	WindowState     string                     `json:"window_state"`
	Coverage        string                     `json:"coverage"`
	Projection      *ReportProjection          `json:"projection,omitempty"`
	Summaries       map[string]json.RawMessage `json:"summaries"`
	InternalTenants []json.RawMessage          `json:"internal_tenants"`
	SupplierBills   []DailyShadowBill          `json:"exact_day_supplier_bills"`
	CountChecks     []DailyShadowCountCheck    `json:"count_checks"`
	Evidence        *DailyShadowEvidence       `json:"row_evidence,omitempty"`
	FinancialPeriod *FinancialPeriod           `json:"financial_period,omitempty"`
}

type DailyShadowBill struct {
	Invoice json.RawMessage   `json:"invoice"`
	Summary json.RawMessage   `json:"summary"`
	Lines   []json.RawMessage `json:"lines,omitempty"`
}

type DailyShadowEvidence struct {
	Limit               int               `json:"limit"`
	Requests            []json.RawMessage `json:"requests"`
	Attempts            []json.RawMessage `json:"attempts"`
	Postings            []json.RawMessage `json:"postings"`
	OverlappingInvoices []json.RawMessage `json:"overlapping_supplier_invoices"`
}

type DailyShadowOptions struct {
	Projected    bool
	EvidenceRows int
	InvoiceIDs   []uuid.UUID
}

type DailyShadowCountCheck struct {
	Name   string `json:"name"`
	Report int64  `json:"report"`
	Source int64  `json:"source"`
	State  string `json:"state"`
}

// DailyShadowBatch contains reports from the same PostgreSQL snapshot.
type DailyShadowBatch struct {
	ReadAt                   time.Time       `json:"read_at"`
	Snapshot                 string          `json:"postgres_snapshot"`
	Isolation                string          `json:"isolation"`
	ReadOnly                 bool            `json:"read_only"`
	Days                     []DailyShadow   `json:"days"`
	SelectedSupplierInvoices []InvoiceReport `json:"selected_supplier_invoices,omitempty"`
}

// ReadDailyShadow returns evidence for one local calendar day in a single
// read-only repeatable-read transaction. Use with an authorized platform
// evidence connection. Bill summaries use the original exact day windows.
func ReadDailyShadow(ctx context.Context, pool *pgxpool.Pool, day, zone string) (DailyShadow, error) {
	return readDailyShadow(ctx, pool, day, zone, false)
}

// ReadProjectedDailyShadow compares V2 terminal usage, sends, supplier statements
// and internal postings within one read-only database snapshot.
func ReadProjectedDailyShadow(ctx context.Context, pool *pgxpool.Pool, day, zone string) (DailyShadow, error) {
	return readDailyShadow(ctx, pool, day, zone, true)
}

func readDailyShadow(ctx context.Context, pool *pgxpool.Pool, day, zone string, projected bool) (DailyShadow, error) {
	batch, err := ReadDailyShadowBatch(ctx, pool, []string{day}, zone, projected)
	if err != nil {
		return DailyShadow{}, err
	}
	return batch.Days[0], nil
}

// ReadDailyShadowBatch reads the requested calendar days in order from one
// read-only repeatable-read transaction. The window state of each day is
// evaluated at the shared transaction timestamp.
func ReadDailyShadowBatch(ctx context.Context, pool *pgxpool.Pool, days []string, zone string, projected bool) (DailyShadowBatch, error) {
	return ReadDailyShadowBatchWithOptions(ctx, pool, days, zone, DailyShadowOptions{Projected: projected})
}

// ReadDailyShadowBatchWithOptions can include the first report rows and exact-day
// invoice lines in the same snapshot as the summaries. The limit applies to each
// report section and to each invoice separately. Selected invoices retain their
// original billing periods and must intersect a requested calendar day.
func ReadDailyShadowBatchWithOptions(ctx context.Context, pool *pgxpool.Pool, days []string, zone string, options DailyShadowOptions) (DailyShadowBatch, error) {
	if len(days) == 0 {
		return DailyShadowBatch{}, fmt.Errorf("at least one day is required")
	}
	if options.EvidenceRows < 0 || options.EvidenceRows > MaxDailyShadowEvidenceRows {
		return DailyShadowBatch{}, fmt.Errorf("evidence rows must be between 0 and %d", MaxDailyShadowEvidenceRows)
	}
	batch := DailyShadowBatch{Days: make([]DailyShadow, len(days))}
	for i, day := range days {
		start, end, err := NaturalDay(day, zone)
		if err != nil {
			return DailyShadowBatch{}, err
		}
		batch.Days[i] = DailyShadow{Day: day, Timezone: zone, Start: start.UTC(), End: end.UTC()}
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return DailyShadowBatch{}, err
	}
	defer tx.Rollback(ctx)
	scope := ReportScope{Platform: true}
	if err := setReportScope(ctx, tx, scope); err != nil {
		return DailyShadowBatch{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp(), pg_current_snapshot()::text,
		current_setting('transaction_isolation'), current_setting('transaction_read_only') = 'on'`).
		Scan(&batch.ReadAt, &batch.Snapshot, &batch.Isolation, &batch.ReadOnly); err != nil {
		return DailyShadowBatch{}, err
	}
	batch.ReadAt = batch.ReadAt.UTC()
	calendar, _, err := ReadFinancialCalendar(ctx, tx)
	if err != nil {
		return DailyShadowBatch{}, err
	}
	version, coverage := DailyShadowVersion, reportSections["coverage"].coverage
	var projection *ReportProjection
	if options.Projected {
		projection, err = readTerminalProjection(ctx, tx, scope.TenantID)
		if err != nil {
			return DailyShadowBatch{}, err
		}
		version = ProjectedDailyShadowVersion
		coverage = projectedReportSection("coverage", reportSections["coverage"]).coverage
	}
	for i := range batch.Days {
		snapshot := &batch.Days[i]
		snapshot.Version, snapshot.Coverage = version, coverage
		snapshot.ReadAt, snapshot.Projection = batch.ReadAt, projection
		snapshot.FinancialPeriod = financialPeriod(calendar, snapshot.Day, zone, snapshot.End, batch.ReadAt)
		if err := populateDailyShadow(ctx, tx, scope, snapshot, options); err != nil {
			return DailyShadowBatch{}, fmt.Errorf("day %s: %w", snapshot.Day, err)
		}
	}
	if len(options.InvoiceIDs) > 0 {
		batch.SelectedSupplierInvoices, err = readSelectedShadowInvoices(ctx, tx, batch.Days, options.InvoiceIDs, options.EvidenceRows)
		if err != nil {
			return DailyShadowBatch{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return DailyShadowBatch{}, err
	}
	return batch, nil
}

func readSelectedShadowInvoices(ctx context.Context, tx pgx.Tx, days []DailyShadow, ids []uuid.UUID, limit int) ([]InvoiceReport, error) {
	starts := make([]time.Time, len(days))
	ends := make([]time.Time, len(days))
	for i, day := range days {
		starts[i], ends[i] = day.Start, day.End
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	reports := make([]InvoiceReport, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var overlaps bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM public.provider_invoices i
			JOIN unnest($2::timestamptz[], $3::timestamptz[]) AS d(start_at, end_at)
			  ON i.window_start < d.end_at AND i.window_end > d.start_at
			WHERE i.invoice_id=$1
		)`, id, starts, ends).Scan(&overlaps)
		if err != nil {
			return nil, fmt.Errorf("check selected invoice %s: %w", id, err)
		}
		if !overlaps {
			return nil, fmt.Errorf("selected invoice %s has no overlap with the requested days", id)
		}
		report, err := readInvoiceReport(ctx, tx, id, limit, 0)
		if err != nil {
			return nil, fmt.Errorf("read selected invoice %s: %w", id, err)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func populateDailyShadow(ctx context.Context, tx pgx.Tx, scope ReportScope, snapshot *DailyShadow, options DailyShadowOptions) error {
	switch {
	case snapshot.ReadAt.Before(snapshot.Start):
		snapshot.WindowState = "future"
	case snapshot.ReadAt.Before(snapshot.End):
		snapshot.WindowState = "open"
	default:
		snapshot.WindowState = "elapsed"
	}
	snapshot.Summaries = make(map[string]json.RawMessage)
	var evidenceRows map[string]*[]json.RawMessage
	if options.EvidenceRows > 0 {
		snapshot.Evidence = &DailyShadowEvidence{Limit: options.EvidenceRows}
		evidenceRows = map[string]*[]json.RawMessage{
			"requests": &snapshot.Evidence.Requests,
			"attempts": &snapshot.Evidence.Attempts,
			"postings": &snapshot.Evidence.Postings,
		}
	}
	query := ReportQuery{Start: snapshot.Start, End: snapshot.End, Day: snapshot.Day, Timezone: snapshot.Timezone}
	for _, name := range []string{"requests", "attempts", "sessions", "postings", "coverage", "internal"} {
		query.Section = name
		query.Limit = 0
		rows := evidenceRows[name]
		switch {
		case rows != nil:
			query.Limit = options.EvidenceRows
		case name == "internal":
			query.Limit = -1 // Whole-snapshot tenant comparisons, without request-level detail.
		}
		section := reportSections[name]
		if options.Projected {
			section = projectedReportSection(name, section)
		}
		report, err := readReportSection(ctx, tx, scope, query, section)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		snapshot.Summaries[name] = report.Summary
		if rows != nil {
			*rows = report.Rows
		}
		if name == "internal" {
			snapshot.InternalTenants = report.Rows
		}
	}

	bills, err := readDailyShadowBills(ctx, tx, snapshot.Start, snapshot.End, options.EvidenceRows)
	if err != nil {
		return fmt.Errorf("read supplier day statements: %w", err)
	}
	snapshot.SupplierBills = bills
	if snapshot.Evidence != nil {
		references, err := readOverlappingInvoiceReferences(ctx, tx, snapshot.Start, snapshot.End)
		if err != nil {
			return fmt.Errorf("read overlapping supplier invoice references: %w", err)
		}
		snapshot.Evidence.OverlappingInvoices = references
	}
	snapshot.CountChecks, err = dailyShadowCountChecks(snapshot.Summaries)
	return err
}

// An invoice with another window can be traced from the day, while its
// quantities, adjustments and payable amount stay in its original period.
func readOverlappingInvoiceReferences(ctx context.Context, tx pgx.Tx, start, end time.Time) ([]json.RawMessage, error) {
	var references []json.RawMessage
	err := tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(i) ORDER BY window_start, window_end, invoice_id), '[]'::jsonb)
		FROM (
			SELECT invoice_id, provider_id, account_ref, currency, invoice_ref, timezone, window_start, window_end
			FROM public.provider_invoices
			WHERE window_start < $2 AND window_end > $1
				AND (window_start, window_end) <> ($1, $2)
		) i`, start, end).Scan(&references)
	return references, err
}

func readDailyShadowBills(ctx context.Context, tx pgx.Tx, start, end time.Time, lineLimit int) ([]DailyShadowBill, error) {
	rows, err := tx.Query(ctx, `SELECT invoice_id FROM public.provider_invoices
		WHERE window_start=$1 AND window_end=$2 ORDER BY invoice_id`, start, end)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	bills := make([]DailyShadowBill, 0, len(ids))
	for _, id := range ids {
		report, err := readInvoiceReport(ctx, tx, id, lineLimit, 0)
		if err != nil {
			return nil, fmt.Errorf("invoice %s: %w", id, err)
		}
		bills = append(bills, DailyShadowBill{Invoice: report.Invoice, Summary: report.Summary, Lines: report.Lines})
	}
	return bills, nil
}

func dailyShadowCountChecks(summaries map[string]json.RawMessage) ([]DailyShadowCountCheck, error) {
	// Keep arbitrary-precision monetary fields raw. The count fields used
	// here are typed separately from the other numbers in each summary.
	var requests struct{ Requests *int64 }
	var attempts struct{ Sent *int64 }
	var postings struct{ Postings *int64 }
	var coverage struct {
		Receipts *int64 `json:"received_requests"`
		Sends    *int64 `json:"recorded_sends"`
		Postings *int64 `json:"posted_entries"`
	}
	for name, summary := range map[string]any{"requests": &requests, "attempts": &attempts, "postings": &postings, "coverage": &coverage} {
		if err := json.Unmarshal(summaries[name], summary); err != nil {
			return nil, fmt.Errorf("decode %s count checks: %w", name, err)
		}
	}
	var checks []DailyShadowCountCheck
	for _, count := range []struct {
		name           string
		report, source *int64
	}{
		{"client_receipts", requests.Requests, coverage.Receipts},
		{"confirmed_supplier_sends", attempts.Sent, coverage.Sends},
		{"request_posting_entries", postings.Postings, coverage.Postings},
	} {
		if count.report == nil || count.source == nil {
			return nil, fmt.Errorf("summary omitted count check %s", count.name)
		}
		state := "equal"
		switch {
		case *count.report != *count.source:
			state = "different"
		case *count.source == 0:
			state = "no_observations"
		}
		checks = append(checks, DailyShadowCountCheck{Name: count.name, Report: *count.report, Source: *count.source, State: state})
	}
	return checks, nil
}
