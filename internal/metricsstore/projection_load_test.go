package metricsstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/domains/reportrollup"
)

// The opt-in load check runs against a local disposable restore after its
// migration channel has applied 983d. It writes only the rebuilt projection.
func TestRequestMetricsProjectionLoad(t *testing.T) {
	dsn := os.Getenv("REQUEST_METRICS_LOAD_DSN")
	if dsn == "" {
		t.Skip("set REQUEST_METRICS_LOAD_DSN to a migrated local disposable restore")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || cfg.ConnConfig.Host != "127.0.0.1" {
		t.Fatal("use a loopback PostgreSQL connection in the isolated environment")
	}
	end, err := time.Parse(time.RFC3339, os.Getenv("REQUEST_METRICS_LOAD_END"))
	if err != nil {
		t.Fatal("set REQUEST_METRICS_LOAD_END to the historical window end in RFC3339")
	}
	sampleCount := 20
	if raw := os.Getenv("REQUEST_METRICS_LOAD_SAMPLES"); raw != "" {
		sampleCount, err = strconv.Atoi(raw)
		if err != nil || sampleCount < 5 || sampleCount > 100 {
			t.Fatal("REQUEST_METRICS_LOAD_SAMPLES must be between 5 and 100")
		}
	}

	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	end = end.In(zone)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("could not open the fixture pool")
	}
	defer pool.Close()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	processor := NewProjectionProcessor(db)
	started := time.Now()
	var work int
	for {
		count, err := processor.ProcessBatch(ctx, DefaultConfig().BatchSize)
		if err != nil {
			t.Fatal(err)
		}
		work += count
		if count == 0 {
			break
		}
	}
	t.Logf("backfill work=%d elapsed=%s", work, time.Since(started))

	for _, days := range []int{1, 7, 30, 90} {
		filter := metrics.MetricsFilter{StartTime: end.AddDate(0, 0, -days), EndTime: end}
		check := httptest.NewRecorder()
		path := "/readyz/request-metrics?from=" + filter.StartTime.UTC().Format(time.RFC3339) +
			"&through=" + end.UTC().Format(time.RFC3339)
		ProjectionReadinessHandler(db)(check, httptest.NewRequest(http.MethodGet, path, nil))
		var ready projectionReadiness
		if err := json.Unmarshal(check.Body.Bytes(), &ready); err != nil ||
			check.Code != http.StatusOK || ready.Coverage == nil {
			t.Fatalf("window %d days coverage returned HTTP %d: %s (%v)", days, check.Code, check.Body, err)
		}
		t.Logf("%dd coverage=%d/%d session=%d provider=%d measured_tokens=%d currency=%d",
			days, ready.Coverage.Current, ready.Coverage.Expected, ready.Coverage.WithSession,
			ready.Coverage.WithProvider, ready.Coverage.WithMeasuredTokens, ready.Coverage.WithCurrency)
		coverage, err := reportrollup.ReadProjectionCoverage(ctx, pool, filter.StartTime, end, end.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		measure := func(name string, read func() error) {
			t.Helper()
			samples := make([]time.Duration, sampleCount)
			for i := range samples {
				start := time.Now()
				if err := read(); err != nil {
					t.Fatalf("%dd %s: %v", days, name, err)
				}
				samples[i] = time.Since(start)
			}
			slices.Sort(samples)
			t.Logf("%dd %s n=%d p50=%s p95=%s max=%s", days, name, len(samples), samples[(sampleCount-1)/2], samples[(95*sampleCount+99)/100-1], samples[sampleCount-1])
		}
		measure("summary", func() error {
			_, err := metrics.ReadRequestStats(ctx, pool, metrics.RequestStatsQuery{
				MetricsFilter: filter, Group: "summary", Timezone: "Asia/Shanghai",
			})
			return err
		})
		measure("board", func() error {
			_, err := metrics.ReadRequestBoard(ctx, pool, metrics.BoardQuery{MetricsFilter: filter, BucketMinutes: 60})
			return err
		})
		measure("daily-history", func() error {
			_, err := reportrollup.BuildProjectedReport(ctx, pool, filter.StartTime, end,
				reportrollup.ViewInternal, reportrollup.GrainFilter{},
				reportrollup.Names{}, true, coverage)
			return err
		})
	}
}

func TestRequestMetricsProjectionPollingDelay(t *testing.T) {
	dsn := os.Getenv("REQUEST_METRICS_POLL_TEST_DSN")
	if dsn == "" {
		t.Skip("set REQUEST_METRICS_POLL_TEST_DSN to an isolated migrated request_metrics_fixture_* database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || cfg.ConnConfig.Host != "127.0.0.1" ||
		!strings.HasPrefix(cfg.ConnConfig.Database, "request_metrics_fixture_") {
		t.Fatal("use an isolated loopback request_metrics_fixture_* database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("could not open the fixture pool")
	}
	defer pool.Close()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	pollCtx, stop := context.WithCancel(ctx)
	worker := NewProjectionProcessor(db)
	var previous time.Time
	if err := pool.QueryRow(ctx, `SELECT coalesce(updated_at,now()-interval '1 day')
		FROM public.request_metrics_projection_state WHERE singleton`).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		worker.Run(pollCtx, DefaultConfig())
	}()
	defer func() { stop(); <-workerDone }()
	if err := waitForProjectionPoll(ctx, pool, previous); err != nil {
		t.Fatal(err)
	}
	// A peak burst followed by spread-out arrivals is independently checked
	// against the queue timestamps returned by the actual write statements.
	var arrivals []time.Time
	batchID := "projection-latency-" + time.Now().UTC().Format("20060102T150405.000000000")
	for batch := range 6 {
		size := 12
		if batch == 0 {
			size = 60
		}
		rows, err := pool.Query(ctx, `INSERT INTO public.usage_facts
			(event_id,request_id,occurred_at,tenant_id,status,total_tokens)
			SELECT $1 || '-' || i, $1 || '-' || i, now()-interval '1 day','fixture-latency','success',12
			FROM generate_series($2::int, $3::int) AS g(i) RETURNING created_at`,
			batchID, len(arrivals), len(arrivals)+size-1)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var arrival time.Time
			if err := rows.Scan(&arrival); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			arrivals = append(arrivals, arrival)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if batch < 5 {
			time.Sleep(10 * time.Second)
		}
	}
	var current int
	var published time.Time
	for current < len(arrivals) {
		if err := pool.QueryRow(ctx, `SELECT count(*),clock_timestamp()
			FROM public.request_metrics_projection WHERE tenant_id='fixture-latency'
			AND starts_with(request_id,$1)`, batchID).Scan(&current, &published); err != nil {
			t.Fatal(err)
		}
		if current < len(arrivals) {
			time.Sleep(time.Second)
		}
	}
	// The first observation that can see every row bounds each arrival's
	// visibility delay, including the commit and the observation interval.
	delays := make([]time.Duration, len(arrivals))
	for i, arrival := range arrivals {
		delays[i] = published.Sub(arrival)
	}
	slices.Sort(delays)
	p95 := delays[(95*len(delays)+99)/100-1]
	t.Logf("poll=%s samples=%d upper-bound p95=%s max=%s", DefaultConfig().Interval, len(delays), p95, delays[len(delays)-1])
	if p95 > 5*time.Minute {
		t.Fatalf("projection delay exceeds five minutes: %s", p95)
	}
}

func waitForProjectionPoll(ctx context.Context, pool *pgxpool.Pool, previous time.Time) error {
	for {
		var updated time.Time
		err := pool.QueryRow(ctx, `SELECT coalesce(updated_at,now()-interval '1 day')
			FROM public.request_metrics_projection_state WHERE singleton`).Scan(&updated)
		if err != nil || updated.After(previous) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
