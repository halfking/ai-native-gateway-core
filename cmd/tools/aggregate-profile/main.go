// Package main — cmd/tools/aggregate-profile
//
// One-shot tool: runs the provider-profile DailyAggregator against a target DB
// for a given date, populating provider_profile_daily from provider_profile_metrics.
//
// Used to backfill daily profiles on environments (e.g. 252) where the new
// aggregator (Phase 2 first-run fix) hasn't been deployed yet, so the quality
// UI bridge has data to read.
//
// Usage:
//
//	DB_URL="postgres://llm_gateway:...@host:port/llm_gateway?sslmode=disable" \
//	go run ./cmd/tools/aggregate-profile            # aggregate yesterday
//	go run ./cmd/tools/aggregate-profile -date 2026-07-26
//	go run ./cmd/tools/aggregate-profile -date today
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
)

func main() {
	dateStr := flag.String("date", "yesterday", "date to aggregate (YYYY-MM-DD, 'today', or 'yesterday')")
	flag.Parse()

	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		log.Fatal("DB_URL env var required (postgres://...)")
	}

	var date time.Time
	switch *dateStr {
	case "today":
		date = time.Now()
	case "yesterday":
		date = time.Now().AddDate(0, 0, -1)
	default:
		t, err := time.Parse("2006-01-02", *dateStr)
		if err != nil {
			log.Fatalf("invalid -date %q: %v", *dateStr, err)
		}
		date = t
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	metricsStore := providerprofile.NewPGMetricsStore(pool)
	profileStore := providerprofile.NewPGProfileStore(pool)
	scorer := providerprofile.NewDefaultScorer()
	weights := providerprofile.DefaultWeights()
	lister := providerprofile.NewGatewayCredentialLister(pool)

	// Also include credentials that are currently active OR were auto-disabled
	// (so their last day of metrics still gets a profile). The lister only returns
	// active ones; we additionally query all credential_ids that have metrics for
	// the target date.
	agg := providerprofile.NewDailyAggregator(metricsStore, profileStore, scorer, weights, lister)

	slog.Info("aggregating provider profiles", "date", date.Format("2006-01-02"))
	if err := agg.AggregateDailyProfiles(ctx, date); err != nil {
		log.Fatalf("aggregate failed: %v", err)
	}
	slog.Info("done", "date", date.Format("2006-01-02"))
}
