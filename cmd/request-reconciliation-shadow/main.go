package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s -day YYYY-MM-DD [-day YYYY-MM-DD ...] -timezone IANA_NAME\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Set REQUEST_RECONCILIATION_SHADOW_DSN to an authorized platform evidence database. Prints a read-only JSON snapshot.")
		flag.PrintDefaults()
	}
	var days []string
	flag.Func("day", "local calendar day (repeat for one shared snapshot)", func(day string) error {
		days = append(days, day)
		return nil
	})
	var invoiceIDs []uuid.UUID
	flag.Func("invoice-id", "include a supplier invoice in its original billing period (repeatable)", func(value string) error {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil {
			return fmt.Errorf("invoice ID must be a nonzero UUID")
		}
		invoiceIDs = append(invoiceIDs, id)
		return nil
	})
	zone := flag.String("timezone", "", "IANA name or UTC")
	timeout := flag.Duration("timeout", 5*time.Minute, "total database read timeout")
	projected := flag.Bool("projected", false, "use V2 terminal usage from request_metrics")
	evidenceRows := flag.Int("evidence-rows", 0, fmt.Sprintf("rows per request, attempt, posting section and invoice report, plus overlapping invoice references (0-%d)", reconciliation.MaxDailyShadowEvidenceRows))
	flag.Parse()
	if flag.NArg() != 0 || len(days) == 0 || *zone == "" || *timeout <= 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *evidenceRows < 0 || *evidenceRows > reconciliation.MaxDailyShadowEvidenceRows {
		fmt.Fprintf(os.Stderr, "evidence rows must be between 0 and %d\n", reconciliation.MaxDailyShadowEvidenceRows)
		os.Exit(2)
	}
	for _, day := range days {
		if _, _, err := reconciliation.NaturalDay(day, *zone); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	dsn := os.Getenv("REQUEST_RECONCILIATION_SHADOW_DSN")
	if strings.TrimSpace(dsn) == "" {
		fmt.Fprintln(os.Stderr, "REQUEST_RECONCILIATION_SHADOW_DSN must be set")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	options := reconciliation.DailyShadowOptions{Projected: *projected, EvidenceRows: *evidenceRows, InvoiceIDs: invoiceIDs}
	if err := capture(ctx, dsn, days, *zone, options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func capture(ctx context.Context, dsn string, days []string, zone string, options reconciliation.DailyShadowOptions) error {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || config.ConnConfig.Database == "" {
		return fmt.Errorf("REQUEST_RECONCILIATION_SHADOW_DSN must identify a valid database")
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("could not initialize the configured evidence connection")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("could not connect to the configured evidence database")
	}
	snapshot, err := reconciliation.ReadDailyShadowBatchWithOptions(ctx, pool, days, zone, options)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if len(snapshot.Days) == 1 && len(options.InvoiceIDs) == 0 {
		return encoder.Encode(snapshot.Days[0])
	}
	return encoder.Encode(snapshot)
}
