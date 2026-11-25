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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s -day YYYY-MM-DD -timezone IANA_NAME\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Set REQUEST_RECONCILIATION_SHADOW_DSN to an authorized platform evidence database. Prints a read-only JSON snapshot.")
		flag.PrintDefaults()
	}
	day := flag.String("day", "", "local calendar day")
	zone := flag.String("timezone", "", "IANA name or UTC")
	projected := flag.Bool("projected", false, "compare V2 terminal projection and original evidence in the same snapshot")
	timeout := flag.Duration("timeout", 5*time.Minute, "total database read timeout")
	flag.Parse()
	if flag.NArg() != 0 || *day == "" || *zone == "" || *timeout <= 0 {
		flag.Usage()
		os.Exit(2)
	}
	if _, _, err := reconciliation.NaturalDay(*day, *zone); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
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
	if err := capture(ctx, dsn, *day, *zone, *projected); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func capture(ctx context.Context, dsn, day, zone string, projected bool) error {
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
	read := reconciliation.ReadDailyShadow
	if projected {
		read = reconciliation.ReadProjectedDailyShadow
	}
	snapshot, err := read(ctx, pool, day, zone)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(snapshot)
}
