package metricsstore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/domains/reportrollup"
)

func requestMetricsFixtureConfig(t *testing.T, dsn string) *pgxpool.Config {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || cfg.ConnConfig.Host != "127.0.0.1" ||
		!strings.HasPrefix(cfg.ConnConfig.Database, "request_metrics_fixture_") {
		t.Fatal("use an isolated loopback request_metrics_fixture_* database")
	}
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		if fallback.Host != "127.0.0.1" {
			t.Fatal("requires loopback-only fallback hosts")
		}
	}
	return cfg
}

// The opt-in load check runs against a local disposable restore after its
// migration channel has applied through 983o. It writes only derived statistics.
func TestRequestMetricsProjectionLoad(t *testing.T) {
	dsn := os.Getenv("REQUEST_METRICS_LOAD_DSN")
	if dsn == "" {
		t.Skip("set REQUEST_METRICS_LOAD_DSN to a migrated loopback request_metrics_fixture_* restore")
	}
	cfg := requestMetricsFixtureConfig(t, dsn)
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

	for _, job := range []struct {
		name    string
		refresh func(context.Context) (bool, error)
	}{
		{"summary", processor.RefreshDailySummary},
		{"board", processor.RefreshDailyBoard},
		{"history", processor.RefreshDailyHistory},
	} {
		started = time.Now()
		var rebuilt int
		for {
			built, err := job.refresh(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !built {
				break
			}
			rebuilt++
		}
		t.Logf("daily %s days=%d elapsed=%s", job.name, rebuilt, time.Since(started))
	}

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
		measure("history-totals", func() error {
			return measureHistorySummary(ctx, pool, filter.StartTime, end, reportrollup.ViewInternal, reportrollup.ProjectedSummaryTotals, false)
		})
		measure("history-provider-distribution", func() error {
			return measureHistorySummary(ctx, pool, filter.StartTime, end, reportrollup.ViewProvider, reportrollup.ProjectedSummaryDistribution, false)
		})
		if days == 90 {
			measure("history-provider-distribution-one-day-invalidated", func() error {
				return measureHistorySummary(ctx, pool, filter.StartTime, end, reportrollup.ViewProvider, reportrollup.ProjectedSummaryDistribution, true)
			})
		}
	}
}

func measureHistorySummary(ctx context.Context, pool *pgxpool.Pool, start, end time.Time, view reportrollup.View, scope reportrollup.ProjectedSummaryScope, invalidateDay bool) error {
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if invalidateDay {
		opts.AccessMode = pgx.ReadWrite
	}
	tx, err := pool.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL jit=off"); err != nil {
		return err
	}
	if invalidateDay {
		// Exercise the actual invalidation trigger. Rollback keeps the next
		// measurement independent and leaves the isolated restore published.
		tag, err := tx.Exec(ctx, `UPDATE public.request_metrics_daily_state SET dirty=true WHERE day=(
			SELECT day FROM public.request_metrics_history_days
			WHERE day>=$1::date AND day<$2::date ORDER BY day DESC LIMIT 1)`,
			start.Format("2006-01-02"), end.Format("2006-01-02"))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("expected one published history day in the load window")
		}
	}
	coverage, err := reportrollup.ReadProjectionCoverage(ctx, tx, start, end.AddDate(0, 0, -1), end)
	if err != nil {
		return err
	}
	report, err := reportrollup.BuildProjectedSummary(ctx, tx, start, end.AddDate(0, 0, -1), view, reportrollup.GrainFilter{}, reportrollup.Names{}, coverage, scope)
	if err != nil {
		return err
	}
	_, err = json.Marshal(report)
	return err
}

func TestRequestMetricsProjectionPollingDelay(t *testing.T) {
	dsn := os.Getenv("REQUEST_METRICS_POLL_TEST_DSN")
	if dsn == "" {
		t.Skip("set REQUEST_METRICS_POLL_TEST_DSN to an isolated migrated request_metrics_fixture_* database")
	}
	cfg := requestMetricsFixtureConfig(t, dsn)
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
		FROM public.request_metrics_repair_state WHERE singleton`).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	projectionDone, dailyDone, boardDone, historyDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(projectionDone)
		worker.Run(pollCtx, DefaultConfig())
	}()
	go func() {
		defer close(dailyDone)
		worker.RunDailySummary(pollCtx, DefaultConfig())
	}()
	go func() {
		defer close(boardDone)
		worker.RunDailyBoard(pollCtx, DefaultConfig())
	}()
	go func() {
		defer close(historyDone)
		worker.RunDailyHistory(pollCtx, DefaultConfig())
	}()
	defer func() {
		stop()
		<-projectionDone
		<-dailyDone
		<-boardDone
		<-historyDone
	}()
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
	for {
		var ready bool
		var observed time.Time
		if err := pool.QueryRow(ctx, `SELECT bool_and(h.day IS NOT NULL AND th.day IS NOT NULL AND pr.day IS NOT NULL), clock_timestamp()
			FROM public.request_metrics_projection p
			LEFT JOIN public.request_metrics_history_days h ON h.day=(p.occurred_at AT TIME ZONE 'Asia/Shanghai')::date
			LEFT JOIN public.request_metrics_tenant_history_days th ON th.day=h.day
			LEFT JOIN public.request_metrics_history_detail_days pr ON pr.day=h.day
			WHERE p.tenant_id='fixture-latency' AND starts_with(p.request_id,$1)`, batchID).Scan(&ready, &observed); err != nil {
			t.Fatal(err)
		}
		// Every sample is visible in the projection before checking publication.
		// Reuse the same arrival order for both latency upper bounds.
		historyP95 := p95 + observed.Sub(published)
		if historyP95 > 5*time.Minute {
			t.Fatalf("history and person rank publication delay exceeds five minutes: %s", historyP95)
		}
		if ready {
			t.Logf("history and person rank published samples=%d upper-bound p95=%s", len(arrivals), historyP95)
			break
		}
		time.Sleep(time.Second)
	}
}

func waitForProjectionPoll(ctx context.Context, pool *pgxpool.Pool, previous time.Time) error {
	for {
		var updated time.Time
		err := pool.QueryRow(ctx, `SELECT coalesce(updated_at,now()-interval '1 day')
			FROM public.request_metrics_repair_state WHERE singleton`).Scan(&updated)
		if err != nil || updated.After(previous) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
