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

type DailyShadow struct {
	Version         string                     `json:"version"`
	Day             string                     `json:"day"`
	Timezone        string                     `json:"timezone"`
	Start           time.Time                  `json:"start"`
	End             time.Time                  `json:"end"`
	ReadAt          time.Time                  `json:"read_at"`
	WindowState     string                     `json:"window_state"`
	Coverage        string                     `json:"coverage"`
	Summaries       map[string]json.RawMessage `json:"summaries"`
	InternalTenants []json.RawMessage          `json:"internal_tenants"`
	SupplierBills   []DailyShadowBill          `json:"exact_day_supplier_bills"`
	CountChecks     []DailyShadowCountCheck    `json:"count_checks"`
}

type DailyShadowBill struct {
	Invoice json.RawMessage `json:"invoice"`
	Summary json.RawMessage `json:"summary"`
}

type DailyShadowCountCheck struct {
	Name   string `json:"name"`
	Report int64  `json:"report"`
	Source int64  `json:"source"`
	State  string `json:"state"`
}

// ReadDailyShadow returns evidence for one local calendar day in a single
// read-only repeatable-read transaction. Use with an authorized platform
// evidence connection. Bill summaries use the original exact day windows.
func ReadDailyShadow(ctx context.Context, pool *pgxpool.Pool, day, zone string) (DailyShadow, error) {
	start, end, err := NaturalDay(day, zone)
	if err != nil {
		return DailyShadow{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return DailyShadow{}, err
	}
	defer tx.Rollback(ctx)
	scope := ReportScope{Platform: true}
	if err := setReportScope(ctx, tx, scope); err != nil {
		return DailyShadow{}, err
	}
	snapshot := DailyShadow{
		Version: DailyShadowVersion, Day: day, Timezone: zone, Start: start.UTC(), End: end.UTC(),
		Coverage:  reportSections["coverage"].coverage,
		Summaries: make(map[string]json.RawMessage),
	}
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&snapshot.ReadAt); err != nil {
		return DailyShadow{}, err
	}
	snapshot.ReadAt = snapshot.ReadAt.UTC()
	switch {
	case snapshot.ReadAt.Before(start):
		snapshot.WindowState = "future"
	case snapshot.ReadAt.Before(end):
		snapshot.WindowState = "open"
	default:
		snapshot.WindowState = "elapsed"
	}
	query := ReportQuery{Start: start, End: end, Day: day, Timezone: zone}
	for _, name := range []string{"requests", "attempts", "sessions", "postings", "coverage", "internal"} {
		query.Section = name
		if name == "internal" {
			query.Limit = -1 // Whole-snapshot tenant comparisons, without request-level detail.
		}
		report, err := readReportSection(ctx, tx, scope, query, reportSections[name])
		if err != nil {
			return DailyShadow{}, fmt.Errorf("read %s: %w", name, err)
		}
		snapshot.Summaries[name] = report.Summary
		if name == "internal" {
			snapshot.InternalTenants = report.Rows
		}
	}

	snapshot.SupplierBills, err = readDailyShadowBills(ctx, tx, start, end)
	if err != nil {
		return DailyShadow{}, fmt.Errorf("read supplier day statements: %w", err)
	}
	snapshot.CountChecks, err = dailyShadowCountChecks(snapshot.Summaries)
	if err != nil {
		return DailyShadow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DailyShadow{}, err
	}
	return snapshot, nil
}

func readDailyShadowBills(ctx context.Context, tx pgx.Tx, start, end time.Time) ([]DailyShadowBill, error) {
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
		report, err := readInvoiceReport(ctx, tx, id, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("invoice %s: %w", id, err)
		}
		bills = append(bills, DailyShadowBill{Invoice: report.Invoice, Summary: report.Summary})
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
