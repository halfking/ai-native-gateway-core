package metricsstore

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/kaixuan/llm-gateway-go/admin/dashboardapi"
	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/domains/reportrollup"
)

type historyQueryProbe struct {
	reportrollup.Querier
	mode            string
	tenantMode      string
	tenantModelMode string
	packedRank      bool
}

func (p *historyQueryProbe) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	mode := "published"
	switch {
	case strings.Contains(query, "FROM gaps"):
		mode = "fragmented"
	case strings.Contains(query, "UNION ALL SELECT * FROM raw_cells"):
		mode = "contiguous"
	}
	switch {
	case strings.Contains(query, "FROM public.request_metrics_daily_history"):
		p.mode = mode
	case strings.Contains(query, "FROM public.request_metrics_daily_tenant_history"):
		p.tenantMode = mode
	case strings.Contains(query, "FROM public.request_metrics_daily_tenant_model_history"):
		p.tenantModelMode = mode
	case strings.Contains(query, "FROM public.request_metrics_person_rank_packs"):
		p.packedRank = true
	}
	return p.Querier.Query(ctx, query, args...)
}

func TestRequestMetricsProjectionPostgres(t *testing.T) {
	dsn := os.Getenv("REQUEST_METRICS_TEST_DSN")
	if dsn == "" {
		t.Skip("set REQUEST_METRICS_TEST_DSN to an isolated empty request_metrics_fixture_* database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("invalid fixture database configuration")
	}
	defer pool.Close()
	var database string
	var tables int
	if err := pool.QueryRow(ctx, "SELECT current_database(), count(*) FROM information_schema.tables WHERE table_schema='public'").Scan(&database, &tables); err != nil ||
		!strings.HasPrefix(database, "request_metrics_fixture_") || tables != 0 {
		t.Fatalf("expected an empty owned fixture, database=%q tables=%d error=%v", database, tables, err)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`
CREATE TABLE public.schema_migrations (version text PRIMARY KEY, description text);
CREATE TABLE public.models_canonical (id bigint PRIMARY KEY, canonical_name text);
CREATE TABLE public.providers (id bigint PRIMARY KEY, code text, display_name text);
CREATE TABLE public.credentials (id bigint PRIMARY KEY, provider_id bigint, label text);
CREATE TABLE public.api_keys (id bigint PRIMARY KEY, tenant_id text, key_prefix text, owner_user text);
CREATE TABLE public.users (id bigint PRIMARY KEY, tenant_id text, username text,
 updated_at timestamptz DEFAULT now(), UNIQUE (tenant_id, username));
CREATE TABLE public.session_summaries (session_key text PRIMARY KEY, tenant_id text, user_intent text);
CREATE TABLE public.request_context_attrs (request_id text, tenant_id text, ts timestamptz,
 gw_session_id text, gw_task_id text, virtual_client_id text, owner_user text, application_code text,
 agent_name text, client_ip inet, identity_hash text, egress_protocol text, work_type text, compression_strategy text,
 api_key_owner_user text, stream_interrupted boolean, failure_detail_code text, error_kind text);
CREATE TABLE public.session_turns_hot (request_id text, tenant_id text, ts timestamptz);
CREATE VIEW public.request_logs_with_current_month AS
 SELECT request_id,tenant_id,ts,gw_session_id,gw_task_id,0 AS attachment_count,
 'chat'::text AS client_protocol,egress_protocol,
 'client-request-model'::text AS client_model, 'classification'::text AS task_type, agent_name, client_ip, identity_hash, work_type, compression_strategy,
 api_key_owner_user, stream_interrupted, failure_detail_code, error_kind FROM public.request_context_attrs;
INSERT INTO public.models_canonical VALUES (7,'canonical-test');
INSERT INTO public.providers VALUES (9,'supplier','Supplier');`)
	apply := func(name string) {
		t.Helper()
		body, err := os.ReadFile("../../sql/migrations/startup/" + name)
		if err != nil {
			t.Fatal(err)
		}
		exec(strings.ReplaceAll(string(body), `\set ON_ERROR_STOP on`, ""))
	}
	apply("537_usage_facts.sql")
	apply("983_request_metrics.sql")
	apply("983b_add_raw_model_name.sql")
	exec(`
INSERT INTO public.request_context_attrs VALUES
 ('r1','tenant-a','2026-11-23 00:10Z','shared-session','task','client','owner','app','test-agent','192.0.2.1','identity-a'),
 ('r1','tenant-c','2026-11-23 00:20Z','wrong-tenant',NULL,NULL,NULL,NULL,NULL,NULL,NULL),
 ('r1','tenant-b','2026-11-23 00:10Z','shared-session',NULL,NULL,'owner',NULL,NULL,NULL,NULL);
INSERT INTO public.users (id,tenant_id,username) VALUES (1,'tenant-a','owner'), (2,'tenant-b','owner');
INSERT INTO public.request_metrics
 (request_id,tenant_id,occurred_at,occurred_date,billing_date,total_tokens,status,source)
VALUES
 ('r1','tenant-a','2026-11-23 00:10Z','2026-11-23','2026-11-23',7,'success','async_processor');
INSERT INTO public.usage_facts (event_id,request_id,revision,occurred_at,tenant_id,status,provider_id,canonical_id,raw_model_name,
 prompt_tokens,total_tokens,cost_amount,cost_currency,credits_charged)
VALUES
 ('e1','r1',1,'2026-11-23 00:10Z','tenant-a','success',9,7,'raw-test',10,10,0.003,'USD',2),
 ('e2','r1',1,'2026-11-23 00:10Z','tenant-b','success',9,7,'raw-test',4,4,0.9,'CNY',3),
 ('end','at-end',1,'2026-11-24 00:00Z','tenant-a','success',9,7,'raw-test',99,99,1,'USD',99);`)
	apply("973_request_metrics_projection.sql")
	apply("983j_request_metrics_973_repair.sql")
	apply("983j_request_metrics_973_repair.sql")
	apply("983k_request_metrics_daily_summary.sql")
	apply("983k_request_metrics_daily_summary.sql")
	apply("983l_request_metrics_daily_board.sql")
	apply("983l_request_metrics_daily_board.sql")
	apply("983m_request_metrics_daily_history.sql")
	apply("983m_request_metrics_daily_history.sql")

	t.Run("legacy_watermark_ignores_973_projected_identity", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `INSERT INTO public.request_metrics
			(request_id,fact_request_id,source_fact_id,tenant_id,occurred_at,occurred_date,billing_date,status)
			VALUES (chr(1)||jsonb_build_array('tenant-a','newest')::text, 'newest', 999999, 'tenant-a',
			'2026-11-26 00:00Z','2026-11-26','2026-11-26','success')`); err != nil {
			t.Fatal(err)
		}
		var at time.Time
		var requestID string
		if err := tx.QueryRow(ctx, watermarkSQL).Scan(&at, &requestID); err != nil {
			t.Fatal(err)
		}
		if requestID != "r1" || !at.Equal(time.Date(2026, 11, 23, 0, 10, 0, 0, time.UTC)) {
			t.Fatalf("legacy cursor moved to %q at %v", requestID, at)
		}
	})

	// Seed published report cases through the real partitioned table so its
	// day invalidation triggers run. The read view has no production writer.
	exec(`
CREATE FUNCTION public.insert_projection_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    values_json jsonb;
    columns text;
BEGIN
    values_json := jsonb_strip_nulls(to_jsonb(NEW)) ||
        jsonb_build_object('request_id', chr(1)||jsonb_build_array(NEW.tenant_id, NEW.request_id)::text,
                           'fact_request_id', NEW.request_id);
    SELECT string_agg(quote_ident(key), ', ' ORDER BY key) INTO columns
    FROM jsonb_object_keys(values_json) AS keys(key);
    EXECUTE format('INSERT INTO public.request_metrics (%s) SELECT %s FROM jsonb_populate_record(NULL::public.request_metrics, $1)',
                   columns, columns) USING values_json;
    RETURN NEW;
END $$;
CREATE TRIGGER insert_projection_fixture INSTEAD OF INSERT ON public.request_metrics_projection
FOR EACH ROW EXECUTE FUNCTION public.insert_projection_fixture();`)

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	processor := NewProjectionProcessor(db)
	readProjectionWatermark := func(wantStatus int) projectionReadiness {
		t.Helper()
		recorder := httptest.NewRecorder()
		ProjectionReadinessHandler(db).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz/request-metrics", nil))
		var state projectionReadiness
		if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil ||
			recorder.Code != wantStatus || state.Source != "request_metrics" || state.CheckedAt == nil {
			t.Fatalf("projection readiness HTTP %d, want %d: %s (%v)", recorder.Code, wantStatus, recorder.Body, err)
		}
		return state
	}
	if state := readProjectionWatermark(http.StatusServiceUnavailable); state.BackfillComplete || state.UpdatedAt != nil {
		t.Fatalf("projection watermark prematurely ready: %+v", state)
	}
	query := metrics.RequestStatsQuery{
		MetricsFilter: metrics.MetricsFilter{
			StartTime: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC),
		},
		Group: "summary", Timezone: "Asia/Shanghai",
	}
	checkpoint, err := metrics.ReadRequestStats(ctx, pool, query)
	if err != nil || checkpoint.Ready || len(checkpoint.Items) != 0 {
		t.Fatalf("early result must signal backfill: %+v %v", checkpoint, err)
	}
	if _, err := reportrollup.ReadProjectionCoverage(ctx, pool, query.StartTime, query.StartTime, query.EndTime); !errors.Is(err, reportrollup.ErrProjectionBackfill) {
		t.Fatalf("a historical report before backfill should be unavailable, got %v", err)
	}
	for range 4 {
		if _, err := processor.ProcessBatch(ctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	var projectedRows, historyTokens int
	if err := pool.QueryRow(ctx, `SELECT count(*), sum(total_tokens) FROM public.request_metrics_projection
		WHERE request_id='r1' AND source_fact_id IS NOT NULL`).Scan(&projectedRows, &historyTokens); err != nil ||
		projectedRows != 2 || historyTokens != 14 {
		t.Fatalf("upgrade did not project both tenants: rows=%d tokens=%d err=%v",
			projectedRows, historyTokens, err)
	}
	// An old slot must still be able to write the original key, while a new
	// slot must ignore that row in all V2 aggregates.
	legacyWriter := NewWriter(db)
	occurredAt := time.Date(2026, 11, 23, 0, 10, 0, 0, time.UTC)
	if err := legacyWriter.UpsertRequestMetric(ctx, &RequestMetric{
		RequestID: "r1", TenantID: "tenant-a", OccurredAt: occurredAt,
		OccurredDate: occurredAt, BillingDate: occurredAt, TotalTokens: 777,
		Status: "success", TrafficClass: "business", Source: "async_processor",
	}); err != nil {
		t.Fatalf("legacy writer could not share the upgraded table: %v", err)
	}
	var legacyTokens, canonicalTokens int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT total_tokens FROM public.request_metrics WHERE request_id='r1'),
		(SELECT sum(total_tokens) FROM public.request_metrics_projection WHERE request_id='r1')`).Scan(&legacyTokens, &canonicalTokens); err != nil ||
		legacyTokens != 777 || canonicalTokens != 14 {
		t.Fatalf("legacy write modified V2 evidence: legacy=%d projected=%d err=%v", legacyTokens, canonicalTokens, err)
	}
	// Revisions and newly inserted facts with old occurrence times enter the queue.
	exec(`
INSERT INTO public.usage_facts (event_id,request_id,revision,occurred_at,tenant_id,status,provider_id,canonical_id,raw_model_name,
 prompt_tokens,total_tokens,cost_amount,cost_currency,credits_charged)
VALUES
 ('e1','r1',2,'2026-11-23 00:10Z','tenant-a','success',9,7,'raw-test',12,12,0.004,'USD',2),
 ('e1-old','r1',1,'2026-11-23 00:10Z','tenant-a','failed',9,7,'raw-test',99,99,9,'USD',99),
 ('late','r-late',1,'2026-11-23 00:01Z','tenant-a','success',9,7,'raw-test',1,1,0.002,'USD',1);`)
	if state := readProjectionWatermark(http.StatusOK); !state.BackfillComplete || state.PendingSince == nil {
		t.Fatalf("projection watermark hid the new backlog: %+v", state)
	}
	for range 3 {
		if _, err := processor.ProcessBatch(ctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	if state := readProjectionWatermark(http.StatusOK); state.PendingSince != nil || state.UpdatedAt == nil {
		t.Fatalf("projection watermark did not advance after processing: %+v", state)
	}
	t.Run("rolls_back_and_retries_under_two_slots", func(t *testing.T) {
		exec(`INSERT INTO public.usage_facts
			(event_id,request_id,occurred_at,tenant_id,status,total_tokens)
			VALUES ('retry-1','must-retry','2026-11-22 22:00Z','tenant-recovery','success',3000000000);
			ALTER TABLE public.request_metrics ADD CONSTRAINT fixture_reject_publish
			CHECK (fact_request_id <> 'must-retry');`)
		var before time.Time
		if err := pool.QueryRow(ctx, `SELECT updated_at FROM public.request_metrics_repair_state WHERE singleton`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if _, err := processor.ProcessBatch(ctx, 20); err == nil {
			t.Fatal("expected the PostgreSQL projection write to fail")
		}
		var after time.Time
		var pending, projected int
		if err := pool.QueryRow(ctx, `SELECT updated_at,
			(SELECT count(*) FROM public.request_metrics_refresh_pending WHERE request_id='must-retry'),
			(SELECT count(*) FROM public.request_metrics_projection WHERE request_id='must-retry')
			FROM public.request_metrics_repair_state WHERE singleton`).Scan(&after, &pending, &projected); err != nil || !after.Equal(before) || pending != 1 || projected != 0 {
			t.Fatalf("partial batch commit: before=%v after=%v pending=%d projected=%d err=%v", before, after, pending, projected, err)
		}
		exec(`ALTER TABLE public.request_metrics DROP CONSTRAINT fixture_reject_publish`)
		firstSlot, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer firstSlot.Rollback()
		if _, err := firstSlot.ExecContext(ctx, lockProjectionSQL); err != nil {
			t.Fatal(err)
		}
		secondSlot := NewProjectionProcessor(db)
		if n, err := secondSlot.ProcessBatch(ctx, 20); n != 0 || err != nil {
			t.Fatalf("second slot should yield the claimed checkpoint: n=%d err=%v", n, err)
		}
		if err := firstSlot.Rollback(); err != nil {
			t.Fatal(err)
		}
		if n, err := secondSlot.ProcessBatch(ctx, 20); n != 1 || err != nil {
			t.Fatalf("restarted slot should publish the retained queue item: n=%d err=%v", n, err)
		}
		var tokens int64
		if err := pool.QueryRow(ctx, `SELECT total_tokens FROM public.request_metrics_projection WHERE request_id='must-retry'`).Scan(&tokens); err != nil || tokens != 3000000000 {
			t.Fatalf("lost durable or 64-bit usage: tokens=%d err=%v", tokens, err)
		}
	})

	t.Run("late_dimensions_and_in_place_fact_repair", func(t *testing.T) {
		// Use a tenant outside the visible reporting fixture totals.
		exec(`UPDATE public.usage_facts SET total_tokens=13 WHERE event_id='retry-1';
			INSERT INTO public.request_context_attrs (request_id,tenant_id,ts,gw_session_id) VALUES
			('must-retry','tenant-recovery','2026-11-23 00:00Z','late-session'),
			('must-retry','different-tenant','2026-11-22 22:00Z','foreign-session');`)
		if n, err := processor.ProcessBatch(ctx, 20); n != 3 || err != nil {
			t.Fatalf("expected the fact and both dimension notifications: n=%d err=%v", n, err)
		}
		var session string
		var tokens int
		if err := pool.QueryRow(ctx, `SELECT session_id,total_tokens FROM public.request_metrics_projection
			WHERE request_id='must-retry' AND tenant_id='tenant-recovery'`).Scan(&session, &tokens); err != nil || session != "late-session" || tokens != 13 {
			t.Fatalf("late dimension or repaired fact missing: session=%q tokens=%d err=%v", session, tokens, err)
		}
	})

	assertStats := func(query metrics.RequestStatsQuery, fragments ...string) {
		t.Helper()
		result, err := metrics.ReadRequestStats(ctx, pool, query)
		if err != nil || !result.Ready || result.PendingSince != nil || len(result.Items) != 1 {
			t.Fatalf("unexpected projection: %+v, %v", result, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(result.Items[0]), fragment) {
				t.Fatalf("missing %s in %s", fragment, result.Items[0])
			}
		}
	}
	assertStats(query, `"total_requests": 3`, `"active_sessions": 2`, `"total_tokens": 17`,
		`"total_cost_usd": 0.006`, `"cost_rows_outside_usd": 1`, `"total_credits_charged": 6`)
	t.Run("original_currency_totals", func(t *testing.T) {
		byCurrency := query
		byCurrency.Group = "currency"
		result, err := metrics.ReadRequestStats(ctx, pool, byCurrency)
		if err != nil || len(result.Items) != 2 {
			t.Fatalf("expected separate original currencies: %+v, %v", result, err)
		}
		usd, cny := string(result.Items[0]), string(result.Items[1])
		if !strings.Contains(usd, `"key": "USD"`) || !strings.Contains(usd, `"original_amount": 0.006`) ||
			!strings.Contains(cny, `"key": "CNY"`) || !strings.Contains(cny, `"original_amount": 0.900`) {
			t.Fatalf("cross-currency total: %s %s", usd, cny)
		}
	})
	t.Run("period_comparison", func(t *testing.T) {
		filter := query.MetricsFilter
		result, err := metrics.ReadRequestPeriodComparison(ctx, pool, filter, filter.EndTime, filter.EndTime.AddDate(0, 0, 1), "")
		if err != nil || !result.Ready || len(result.Periods) != 2 {
			t.Fatalf("compare projection windows: %+v %v", result, err)
		}
		current, next := result.Periods[0], result.Periods[1]
		if current.Requests != 3 || current.Tokens != 17 || current.CostUSD != 0.006 || current.Credits != 6 || current.NonUSDRows != 1 ||
			current.ModelCostsUSD["canonical-test"] != 0.006 || next.Requests != 1 || next.Tokens != 99 || next.CostUSD != 1 {
			t.Fatalf("comparison differs from independent projection totals: %+v", result.Periods)
		}
		filter.TenantID = "tenant-a"
		result, err = metrics.ReadRequestPeriodComparison(ctx, pool, filter, filter.EndTime, filter.EndTime.AddDate(0, 0, 1), "owner")
		if err != nil || len(result.Periods) != 2 || result.Periods[0].Requests != 1 || result.Periods[1].Requests != 0 {
			t.Fatalf("comparison did not retain scope in both windows: %+v %v", result, err)
		}
	})
	query.TenantID = "tenant-a"
	assertStats(query, `"total_requests": 2`, `"active_sessions": 1`, `"total_tokens": 13`)
	query.Group = "model"
	assertStats(query, `"model": "canonical-test"`, `"provider_code": "supplier"`, `"total_requests": 2`)
	query.Group = "session"
	assertStats(query, `"key": "shared-session"`, `"tenant_id": "tenant-a"`, `"total_requests": 1`)
	query.Group = "day"
	assertStats(query, `"key": "2026-11-22T16:00:00Z"`, `"total_requests": 2`, `"total_tokens": 13`)
	query.Group = "day-model"
	assertStats(query, `"key": "2026-11-23"`, `"model": "canonical-test"`, `"total_requests": 2`)
	query.Group = "provider"
	assertStats(query, `"provider_id": 9`, `"provider_name": "Supplier"`, `"total_requests": 2`)
	query.Group, query.BucketMinutes = "trend-bucket", 15
	query.Models, query.AppCode = []string{"canonical-test"}, "app"
	assertStats(query, `"key": "2026-11-23T00:00:00Z"`, `"total_requests": 1`, `"total_tokens": 12`)
	query.Group, query.Limit = "top-models", 5
	query.Owner = "owner"
	assertStats(query, `"key": "canonical-test"`, `"total_requests": 1`, `"total_tokens": 12`)
	t.Run("owner_directory_and_models", func(t *testing.T) {
		directory := query
		directory.Group, directory.Limit, directory.OwnerSearch = "owner-directory", 1, "OWN"
		assertStats(directory, `"total": 1`, `"key": "owner"`, `"unique_models": 1`, `"total_requests": 1`, `"total_credits_charged": 2`)
		directory.Offset = 1
		assertStats(directory, `"total": 1`, `"users": []`)
		directory.Offset, directory.OwnerSearch = 0, "%"
		assertStats(directory, `"total": 0`, `"users": []`)
		directory.TenantID, directory.OwnerSearch, directory.Limit = "", "owner", 10
		directory.AppCode = ""
		assertStats(directory, `"total": 2`, `"tenant_id": "tenant-a"`, `"tenant_id": "tenant-b"`,
			`"usd_measured_requests": 1`, `"usd_measured_requests": 0`)
		models := query
		models.Group = "owner-models"
		assertStats(models, `"key": "canonical-test"`, `"owner_user": "owner"`, `"provider": "supplier"`, `"total_requests": 1`, `"total_credits_charged": 2`)
	})
	t.Run("account_activity_preserves_tenant_identity", func(t *testing.T) {
		filter := query.MetricsFilter
		filter.TenantID = ""
		accounts, err := metrics.ReadProjectedAccounts(ctx, pool, filter)
		if err != nil || !accounts.Ready {
			t.Fatalf("account usage failed: %+v %v", accounts, err)
		}
		var list []struct {
			TenantID string `json:"tenant_id"`
			Username string `json:"username"`
			Tokens   int64  `json:"tokens"`
		}
		if err := json.Unmarshal(accounts.Payload, &list); err != nil || len(list) != 2 ||
			list[0].TenantID != "tenant-a" || list[1].TenantID != "tenant-b" || list[0].Username != list[1].Username ||
			list[0].Tokens != 12 || list[1].Tokens != 4 {
			t.Fatalf("account usage merged distinct registered users: %+v %v", list, err)
		}
		filter.TenantID = "tenant-a"
		activity, err := metrics.ReadProjectedAccountActivity(ctx, pool, filter, "owner", "Asia/Shanghai")
		if err != nil || !activity.Ready {
			t.Fatalf("account activity failed: %+v %v", activity, err)
		}
		for _, want := range []string{`"requests": 1`, `"tokens": 12`, `"cost_usd": 0.004`, `"date": "2026-11-23"`,
			`"request_id": "r1"`, `"name": "canonical-test"`, `"name": "app"`, `"top_keys": []`} {
			if !strings.Contains(string(activity.Payload), want) {
				t.Fatalf("missing %s in %s", want, activity.Payload)
			}
		}
		tenantActivity, err := metrics.ReadProjectedTenantActivity(ctx, pool, filter, "Asia/Shanghai")
		if err != nil || !tenantActivity.Ready || !strings.Contains(string(tenantActivity.Payload), `"requests": 2`) ||
			!strings.Contains(string(tenantActivity.Payload), `"tokens": 13`) || !strings.Contains(string(tenantActivity.Payload), `"cost_usd": 0.006`) ||
			!strings.Contains(string(tenantActivity.Payload), `"recent_requests": []`) {
			t.Fatalf("tenant activity failed to include unowned requests in the same scope: %+v %v", tenantActivity, err)
		}
		filter.TenantID = "tenant-b"
		activity, err = metrics.ReadProjectedAccountActivity(ctx, pool, filter, "owner", "Asia/Shanghai")
		if err != nil || !strings.Contains(string(activity.Payload), `"cost_rows_outside_usd": 1`) ||
			!strings.Contains(string(activity.Payload), `"cost_usd": 0`) || strings.Contains(string(activity.Payload), `"tokens": 12`) {
			t.Fatalf("tenant B activity did not retain its own currency scope: %+v %v", activity, err)
		}
	})

	t.Run("board_and_error_drill", func(t *testing.T) {
		exec(`
INSERT INTO public.usage_facts (event_id,request_id,occurred_at,tenant_id,traffic_class,status,provider_id,canonical_id,
 prompt_tokens,total_tokens,cost_amount,cost_currency,error_kind,latency_ms)
VALUES
 ('board-error','board-error','2026-11-23 00:16Z','tenant-a','business','timeout',9,7,3,3,0.002,'USD','upstream_timeout',250),
 ('board-probe','board-probe','2026-11-23 00:17Z','tenant-a','probe','success',9,7,40,40,1,'USD',NULL,25),
 ('board-other','board-other','2026-11-23 00:20Z','tenant-b','business','timeout',9,7,100,100,4,'USD','upstream_timeout',500);`)
		if _, err := processor.ProcessBatch(ctx, 10); err != nil {
			t.Fatal(err)
		}
		boardQuery := metrics.BoardQuery{MetricsFilter: query.MetricsFilter, BucketMinutes: 15}
		assertBoard := func(wantRequests, wantTokens int64) {
			t.Helper()
			board, err := metrics.ReadRequestBoard(ctx, pool, boardQuery)
			if err != nil || !board.Ready || board.PendingSince != nil {
				t.Fatalf("board state %+v, error %v", board, err)
			}
			var summary struct {
				Requests int64 `json:"total_requests"`
				Tokens   int64 `json:"total_tokens"`
				Failures int64 `json:"failed_count"`
			}
			if err := json.Unmarshal(board.Summary, &summary); err != nil {
				t.Fatal(err)
			}
			if summary.Requests != wantRequests || summary.Tokens != wantTokens || summary.Failures != 1 {
				t.Fatalf("summary %+v, want %d requests and %d tokens", summary, wantRequests, wantTokens)
			}
			var trend, series int64
			for _, point := range board.Trends {
				trend += point.Requests
			}
			for _, point := range board.ModelPoints {
				series += point.Requests
			}
			if trend != wantRequests || series != wantRequests {
				t.Fatalf("trend=%d model series=%d, want %d", trend, series, wantRequests)
			}
			for _, dimension := range []string{"clients", "client_ips", "identity_hashes", "errors"} {
				if items := board.Pies[dimension]; len(items) != 1 || items[0].Requests != 1 {
					t.Fatalf("%s distribution %+v", dimension, items)
				}
			}
			if providers := board.Pies["providers"]; len(providers) != 1 || providers[0].Requests != wantRequests {
				t.Fatalf("provider distribution %+v", providers)
			}
			if len(board.ProviderRows) != 1 || !strings.Contains(string(board.ProviderRows[0]), `"provider_id": 9`) {
				t.Fatalf("provider rows %s", board.ProviderRows)
			}
			items, ready, err := metrics.ReadBoardErrorDrill(ctx, pool, boardQuery, "upstream_timeout", "provider")
			if err != nil || !ready || len(items) != 1 || items[0].Key != "supplier" || items[0].Requests != 1 {
				t.Fatalf("unexpected drill %+v ready=%v err=%v", items, ready, err)
			}
		}
		assertBoard(3, 16)
		boardQuery.IncludeProbes = true
		assertBoard(4, 56)
		boardQuery.Model, boardQuery.ProviderCode, boardQuery.BucketMinutes = "canonical-test", "supplier", 360
		board, err := metrics.ReadRequestBoard(ctx, pool, boardQuery)
		if err != nil || len(board.Trends) != 1 || board.Trends[0].Requests != 4 || board.Trends[0].Success != 3 || board.Trends[0].AvgLatencyMS != 137.5 {
			t.Fatalf("analytics trend did not preserve exact scope and latency: %+v %v", board.Trends, err)
		}
		boardQuery.Agent = "test-agent"
		board, err = metrics.ReadRequestBoard(ctx, pool, boardQuery)
		if err != nil || len(board.Trends) != 1 || board.Trends[0].Requests != 1 || len(board.Pies["errors"]) != 0 {
			t.Fatalf("analytics agent filters were not applied before aggregation: %+v %+v %v", board.Trends, board.Pies, err)
		}
	})
	t.Run("board_model_series_use_window_rank", func(t *testing.T) {
		exec(`
INSERT INTO public.usage_facts (event_id,request_id,occurred_at,tenant_id,traffic_class,status,raw_model_name,
 total_tokens,credits_charged,cost_amount,cost_currency,latency_ms)
SELECT 'board-rank-'||n, 'board-rank-'||n, timestamptz '2026-11-30 03:00Z' + (n/9)*interval '15 min',
 'rank-tenant','business',CASE WHEN n=8 THEN 'timeout' ELSE 'success' END,
 'model-'||(CASE WHEN n=9 THEN 8 ELSE n END), n, n, n*0.01, 'USD', n*100
FROM generate_series(1,9) n;`)
		if _, err := processor.ProcessBatch(ctx, 10); err != nil {
			t.Fatal(err)
		}
		filter := query.MetricsFilter
		filter.TenantID = "rank-tenant"
		filter.StartTime = time.Date(2026, 11, 30, 3, 0, 0, 0, time.UTC)
		filter.EndTime = filter.StartTime.Add(time.Hour)
		board, err := metrics.ReadRequestBoard(ctx, pool, metrics.BoardQuery{MetricsFilter: filter, BucketMinutes: 15})
		if err != nil || len(board.Trends) != 2 || len(board.ModelPoints) != 8 {
			t.Fatalf("unexpected chart: %+v, %v", board, err)
		}
		first := board.Trends[0]
		if first.Requests != 8 || first.Success != 7 || first.AvgLatencyMS != 450 || first.Tokens != 36 {
			t.Fatalf("unexpected first bucket: %+v", first)
		}
		last := board.ModelPoints[len(board.ModelPoints)-1]
		if last.Model != "model-8" || last.Tokens != 9 || !last.Bucket.Equal(board.Trends[1].Bucket) {
			t.Fatalf("window-wide leader not retained in its second bucket: %+v", last)
		}
		others := board.ModelPoints[0]
		if others.Model != "__others__" || others.Requests != 2 || others.Tokens != 13 || others.Credits != 13 || others.CostUSD != 0.13 {
			t.Fatalf("unexpected combined models: %+v", others)
		}
	})
	t.Run("projected_cost_and_cache_economics", func(t *testing.T) {
		exec(`
INSERT INTO public.session_summaries VALUES ('economic-a','tenant-a','review'), ('economic-other','tenant-b','other tenant');
INSERT INTO public.request_context_attrs (request_id,tenant_id,ts,gw_session_id,egress_protocol,work_type,compression_strategy) VALUES
 ('cache-oa','tenant-a','2026-11-24 02:01Z','economic-a','openai-completions','code_review','summary'),
 ('cache-an','tenant-a','2026-11-24 02:02Z','economic-other','anthropic-messages','general_chat',''),
 ('cache-invalid','tenant-a','2026-11-24 02:05Z','economic-a','openai-responses','code_review','summary'),
 ('cache-gemini','tenant-a','2026-11-24 02:07Z',NULL,'gemini-generate',NULL,NULL);
INSERT INTO public.usage_facts (event_id,request_id,occurred_at,tenant_id,traffic_class,status,provider_id,canonical_id,
 prompt_tokens,cache_read_tokens,cache_write_tokens,total_tokens,cost_amount,cost_currency,credits_charged,usage_source) VALUES
 ('cache-oa','cache-oa','2026-11-24 02:01Z','tenant-a','business','success',9,7,100,25,0,100,0.003,'USD',2,'llm'),
 ('cache-an','cache-an','2026-11-24 02:02Z','tenant-a','business','success',9,7,80,40,10,130,1,'CNY',3,'corrected'),
 ('cache-untyped','cache-untyped','2026-11-24 02:03Z','tenant-a','business','success',9,7,30,20,0,30,0.002,'USD',1,'llm'),
 ('cache-none','cache-none','2026-11-24 02:04Z','tenant-a','business','failed',9,7,15,0,0,15,0,'USD',0,'llm'),
 ('cache-invalid','cache-invalid','2026-11-24 02:05Z','tenant-a','business','success',9,7,50,55,0,50,0.001,'USD',2,'llm'),
 ('cache-probe','cache-probe','2026-11-24 02:06Z','tenant-a','probe','success',9,7,500,100,0,500,8,'USD',99,'llm'),
 ('cache-gemini','cache-gemini','2026-11-24 02:07Z','tenant-a','business','success',9,7,80,30,0,80,0,'USD',0,'llm'),
 ('cache-estimate','cache-estimate','2026-11-24 02:08Z','tenant-a','business','success',9,7,500,0,0,500,0,'USD',0,'estimated');
`)
		if _, err := processor.ProcessBatch(ctx, 20); err != nil {
			t.Fatal(err)
		}
		q := metrics.RequestStatsQuery{MetricsFilter: metrics.MetricsFilter{TenantID: "tenant-a",
			StartTime: time.Date(2026, 11, 24, 2, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)},
			Group: "cache-economics", Timezone: "Asia/Shanghai"}
		assertStats(q, `"total_requests": 7`, `"cache_read_tokens": 170`, `"cache_write_tokens": 10`, `"cache_input_tokens": 325`,
			`"eligible_cache_read_tokens": 95`, `"cache_scope_requests": 4`, `"compressed_requests": 2`, `"compression_scope_requests": 4`,
			`"total_cost_usd": 0.006`, `"cost_rows_outside_usd": 1`, `"total_credits_charged": 8`)
		q.Group = "cost-provider"
		assertStats(q, `"key": "supplier"`, `"total_requests": 7`, `"total_cost_usd": 0.006`, `"total_credits_charged": 8`, `"failed_count": 1`)
		q.Group, q.Limit = "cost-intent", 1
		assertStats(q, `"key": "review"`, `"total_requests": 2`, `"total_cost_usd": 0.004`)
		q.Group = "cost-work_type"
		assertStats(q, `"key": "code_review"`, `"total_requests": 2`, `"total_cost_usd": 0.004`)
	})
	t.Run("maas_shared_scope_and_evidence", func(t *testing.T) {
		exec(`
INSERT INTO public.credentials VALUES (17,9,'supplier-account');
INSERT INTO public.api_keys VALUES (31,'tenant-maas','maas-key','key-owner'), (32,'tenant-maas','other-key','other-owner');
INSERT INTO public.request_context_attrs (request_id,tenant_id,ts,owner_user,api_key_owner_user,stream_interrupted,failure_detail_code,error_kind) VALUES
 ('cancelled','tenant-maas','2026-11-24 03:00Z','session-owner','key-owner',true,'','client_cancel');
INSERT INTO public.usage_facts (event_id,request_id,occurred_at,tenant_id,traffic_class,status,provider_id,credential_id,canonical_id,
 raw_model_name,api_key_id,prompt_tokens,total_tokens,cost_amount,cost_currency,credits_charged) VALUES
 ('maas-1','cancelled','2026-11-24 03:00Z','tenant-maas','business','failed',9,17,7,'raw-maas',32,5,5,0.002,'USD',2),
 ('maas-2','unknown','2026-11-24 03:05Z','tenant-maas','business','success',9,17,7,'raw-maas',31,6,6,0.01,'CNY',3),
 ('maas-3','other-owner','2026-11-24 03:10Z','tenant-maas','business','success',9,17,7,'raw-maas',32,50,50,5,'USD',50),
 ('maas-4','same-owner','2026-11-24 03:15Z','tenant-a','business','success',9,17,7,'raw-maas',31,60,60,6,'USD',60),
 ('maas-5','probe','2026-11-24 03:20Z','tenant-maas','probe','success',9,17,7,'raw-maas',31,70,70,7,'USD',70),
 ('maas-6','at-end','2026-11-24 04:00Z','tenant-maas','business','success',9,17,7,'raw-maas',31,80,80,8,'USD',80);
UPDATE public.usage_facts SET latency_ms=240 WHERE event_id='maas-1';`)
		if _, err := processor.ProcessBatch(ctx, 20); err != nil {
			t.Fatal(err)
		}
		q := metrics.MaaSQuery{Tenant: "tenant-maas", Owner: "key-owner", Timezone: "Asia/Shanghai",
			Start: time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 24, 4, 0, 0, 0, time.UTC),
			Limit: 1, IncludeUpstreamCost: true}
		read := func(section string, fragments ...string) string {
			t.Helper()
			result, err := metrics.ReadMaaSUsage(ctx, pool, q, section)
			if err != nil || !result.Ready || result.PendingSince != nil {
				t.Fatalf("%s state %+v, error %v", section, result, err)
			}
			payload := string(result.Payload)
			for _, fragment := range fragments {
				if !strings.Contains(payload, fragment) {
					t.Fatalf("%s payload %s missing %s", section, payload, fragment)
				}
			}
			return payload
		}
		read("summary", `"total_requests": 2`, `"total_credits": 5`, `"unique_models": 1`, `"success_count": 1`, `"avg_latency_ms": 240`,
			`"date": "2026-11-24"`, `"total_cost_usd": 0.002`, `"cost_rows_outside_usd": 1`)
		read("detail", `"owner_user": "key-owner"`, `"credential_label": "supplier-account"`,
			`"upstream_cost_usd": 0.002`, `"cost_rows_without_usd_evidence": 1`, `"cancelled_billed_requests": 1`,
			`"billed_without_cancellation_evidence": 1`)
		q.Owner = ""
		read("summary", `"total_requests": 3`, `"total_credits": 55`, `"total_cost_usd": 5.002`)
		assertStats(metrics.RequestStatsQuery{MetricsFilter: metrics.MetricsFilter{
			TenantID: q.Tenant, StartTime: q.Start, EndTime: q.End,
		}, Group: "summary", Timezone: q.Timezone}, `"total_requests": 3`, `"total_credits_charged": 55`, `"total_cost_usd": 5.002`)
		q.Owner = "key-owner"
		q.IncludeUpstreamCost = false
		if payload := read("summary", `"total_requests": 2`, `"total_credits": 5`); strings.Contains(payload, "cost_") {
			t.Fatalf("tenant summary includes platform cost: %s", payload)
		}
		q.Model, q.MatchModelAliases = "raw-maas", true
		read("requests", `"total": 2`, `"request_id": "unknown"`, `"credits_charged": 5`, `"cost_usd": null`)
		q.Offset = 2
		read("requests", `"total": 2`, `"rows": []`, `"credits_charged": 5`)
		q.Model, q.MatchModelAliases, q.Offset, q.Day = "canonical-test", false, 0, "2026-11-23"
		read("requests", `"total": 0`, `"rows": []`)
		q.IncludeUpstreamCost = true
		q.Start = q.Start.Add(time.Minute)
		if payload := read("summary", `"total_requests": 1`, `"cost_rows_without_usd_evidence": 1`); strings.Contains(payload, `"total_cost_usd"`) {
			t.Fatalf("missing USD evidence appears as a cost: %s", payload)
		}
	})
	t.Run("usage_revision_provenance", func(t *testing.T) {
		exec(`INSERT INTO public.usage_facts (event_id,request_id,revision,occurred_at,finalized_at,tenant_id,status,total_tokens,usage_source)
VALUES ('version-new','revision-key',2,'2026-11-25 17:00Z','2026-11-25 18:00Z','tenant-versions','success',90,'corrected');`)
		if _, err := processor.ProcessBatch(ctx, 20); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO public.usage_facts (event_id,request_id,revision,occurred_at,finalized_at,tenant_id,status,total_tokens,usage_source)
VALUES ('version-old','revision-key',2,'2026-11-25 17:00Z','2026-11-25 17:30Z','tenant-versions','success',1,'estimated');`)
		if _, err := processor.ProcessBatch(ctx, 20); err != nil {
			t.Fatal(err)
		}
		var source string
		var finalized time.Time
		var tokens int
		err := pool.QueryRow(ctx, `SELECT usage_source,source_finalized_at,total_tokens FROM public.request_metrics_projection
WHERE tenant_id='tenant-versions' AND request_id='revision-key' AND source_fact_id IS NOT NULL`).Scan(&source, &finalized, &tokens)
		if err != nil || source != "corrected" || tokens != 90 || finalized.UTC().Format(time.RFC3339) != "2026-11-25T18:00:00Z" {
			t.Fatalf("older same-version record replaced finalized evidence: source=%s finalized=%s tokens=%d error=%v", source, finalized, tokens, err)
		}
	})

	t.Run("closed_natural_day_report", func(t *testing.T) {
		exec(`
ALTER TABLE public.providers ADD COLUMN deleted_at timestamptz;
INSERT INTO public.request_context_attrs (request_id,tenant_id,ts,egress_protocol) VALUES
 ('report-usd','tenant-report','2026-11-24 16:00Z','openai-completions');
INSERT INTO public.usage_facts (event_id,request_id,occurred_at,tenant_id,traffic_class,status,provider_id,credential_id,canonical_id,
 raw_model_name,prompt_tokens,completion_tokens,cache_read_tokens,total_tokens,cost_amount,cost_currency,credits_charged,usage_source,latency_ms,error_kind) VALUES
 ('report-1','report-usd','2026-11-24 16:00Z','tenant-report','business','success',9,17,7,'raw-report',100,20,25,120,0.00123,'USD',2,'llm',100,NULL),
 ('report-2','report-cny','2026-11-25 05:01Z','tenant-report','business','success',9,17,7,'raw-report',40,10,2,50,1,'CNY',3,'llm',300,NULL),
 ('report-3','report-fail','2026-11-25 05:02Z','tenant-report','business','failed',9,NULL,7,'raw-report',0,0,0,0,0.00001,'USD',0,'estimated',50,'upstream_timeout'),
 ('report-4','report-probe','2026-11-25 05:03Z','tenant-report','probe','success',9,17,7,'raw-report',10,2,0,12,0.006,'USD',0,'llm',500,NULL),
 ('report-5','report-new-day','2026-11-25 16:00Z','tenant-report','business','success',9,17,7,'raw-report',900,10,0,910,1,'USD',1,'llm',900,NULL);`)
		if _, err := processor.ProcessBatch(ctx, 20); err != nil {
			t.Fatal(err)
		}
		loc, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, 11, 25, 0, 0, 0, 0, loc)
		today := start.AddDate(0, 0, 1)
		coverage, err := reportrollup.ReadProjectionCoverage(ctx, pool, start, today, today.Add(time.Hour))
		if err != nil || !coverage.From.Equal(start) || !coverage.Through.Equal(today) || coverage.PendingSince != nil {
			t.Fatalf("unexpected projected day coverage %+v: %v", coverage, err)
		}
		read := func(view reportrollup.View, filter reportrollup.GrainFilter) *reportrollup.GrainReport {
			t.Helper()
			out, err := reportrollup.BuildProjectedReport(ctx, pool, start, today, view, filter, reportrollup.Names{}, true, coverage)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		filter := reportrollup.GrainFilter{TenantID: "tenant-report", Model: "canonical-test"}
		out := read(reportrollup.ViewInternal, filter)
		totals := out.Totals
		if out.Source != reportrollup.ProjectedSource || totals.RequestCount != 3 || totals.TotalTokens != 170 ||
			math.Abs(totals.EstimatedCostCents-0.124) > 1e-9 || *totals.USDUnmeasuredRequests != 1 || totals.CreditsCharged != 5 ||
			totals.InternalCostCents != 0 || totals.CacheHitRatio == nil || *totals.CacheHitRatio != 0.25 ||
			totals.LatencyP50Ms != 200 || totals.LatencyP95Ms != 290 || out.ErrorBreakdown["upstream_timeout"] != 1 {
			t.Fatalf("incorrect closed-day request totals %+v, errors %v", totals, out.ErrorBreakdown)
		}
		if len(out.SnapshotDates) != 1 || out.SnapshotDates[0] != "2026-11-25" || len(out.DailyModels) != 1 || len(out.DailyCredentials) != 2 {
			t.Fatalf("incorrect observed days or breakdowns: %+v", out)
		}
		payload, err := json.Marshal(out.Totals)
		if err != nil || !strings.Contains(string(payload), `"internal_cost_cents":null`) || !strings.Contains(string(payload), `"latency_p95_ms":290`) {
			t.Fatalf("invalid wire totals %s: %v", payload, err)
		}
		if provider := read(reportrollup.ViewProvider, filter); provider.Totals.RequestCount != 4 || provider.Totals.TotalTokens != 182 {
			t.Fatalf("provider request statistics should include probes: %+v", provider.Totals)
		}
		business := filter
		business.BusinessOnly = true
		if provider := read(reportrollup.ViewProvider, business); provider.Totals.RequestCount != totals.RequestCount ||
			provider.Totals.TotalTokens != totals.TotalTokens || provider.Totals.EstimatedCostCents != totals.EstimatedCostCents {
			t.Fatalf("provider business scope does not match the same projected day: %+v", provider.Totals)
		}
		for _, view := range []reportrollup.View{reportrollup.ViewProvider, reportrollup.ViewInternal, reportrollup.ViewCredential, reportrollup.ViewKey} {
			for _, scope := range []reportrollup.ProjectedSummaryScope{reportrollup.ProjectedSummaryTotals, reportrollup.ProjectedSummaryDistribution} {
				want, err := reportrollup.BuildProjectedReport(ctx, pool, start, today, view, filter, reportrollup.Names{}, false, coverage)
				if err != nil {
					t.Fatal(err)
				}
				want.Models = []reportrollup.ModelRow{}
				want.DailyModels, want.Credentials, want.APIKeys = nil, nil, nil
				if scope == reportrollup.ProjectedSummaryTotals {
					want.ModelTotals = nil
				}
				providerView := view == reportrollup.ViewProvider || view == reportrollup.ViewCredential
				if scope == reportrollup.ProjectedSummaryTotals || !providerView {
					want.Providers = nil
				}
				if scope == reportrollup.ProjectedSummaryTotals || providerView {
					want.Tenants, want.Persons = nil, nil
				}
				got, err := reportrollup.BuildProjectedSummary(ctx, pool, start, today, view, filter, reportrollup.Names{}, coverage, scope)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s %s: scoped report differs from the full report: got %+v, want %+v, err %v", view, scope, got, want, err)
				}
			}
		}
		rankingQuery := metrics.SessionRequestRankingQuery{Start: start, End: today, Tenant: "tenant-report"}
		for _, dimension := range []string{"models", "providers"} {
			rankingQuery.Dimension = dimension
			ranks, err := metrics.ReadSessionRequestRanking(ctx, pool, rankingQuery)
			if err != nil || !ranks.Ready {
				t.Fatalf("request rankings unavailable: %+v %v", ranks, err)
			}
			var rows []map[string]any
			if err := json.Unmarshal(ranks.Payload, &rows); err != nil || len(rows) != 1 {
				t.Fatalf("unexpected ranking response: %s %v", ranks.Payload, err)
			}
			row := rows[0]
			if row["provider"] != "supplier" || row["request_count"] != 3.0 || row["total_tokens"] != 170.0 ||
				row["credits_charged"] != 5.0 || row["total_cost"] != 0.00124 || row["usd_unmeasured_requests"] != 1.0 {
				t.Fatalf("ranking disagrees with the business request total: %s", ranks.Payload)
			}
			if dimension == "providers" && row["model_count"] != 1.0 {
				t.Fatalf("provider ranking repeated the model: %s", ranks.Payload)
			}
			if dimension == "models" && (row["model"] != "canonical-test" || row["cache_hit_count"] != 2.0) {
				t.Fatalf("model ranking disagrees with request usage: %s", ranks.Payload)
			}
		}
		unassignedID := reportrollup.UnassignedID
		filter.CredentialID = &unassignedID
		if unassigned := read(reportrollup.ViewInternal, filter); unassigned.Totals.RequestCount != 1 {
			t.Fatalf("unassigned credential filter is inconsistent: %+v", unassigned.Totals)
		}
		unmeasured := filter
		unmeasured.CredentialID = nil
		unmeasured.ProviderID = &unassignedID
		if empty := read(reportrollup.ViewInternal, unmeasured); empty.Totals.RequestCount != 0 || empty.Models == nil {
			t.Fatalf("empty projection must retain the response arrays: %+v", empty)
		}
		options, err := reportrollup.LoadProjectedDimensionOptions(ctx, pool, reportrollup.ViewProvider, business, coverage)
		if err != nil || len(options.Models) != 1 || options.Models[0].Key != "raw-report" || options.Models[0].Requests != 3 {
			t.Fatalf("filter options disagree with the report: %+v %v", options, err)
		}
		bytes, err := reportrollup.BuildGrainWorkbookBytes(out, reportrollup.GrainGroupTenant, true)
		if err != nil || len(bytes) < 2 || string(bytes[:2]) != "PK" {
			t.Fatalf("could not export projected report: %v", err)
		}
		exec(`INSERT INTO request_metrics_refresh_pending (kind,tenant_id,request_id,occurred_at) VALUES
 ('fact','tenant-report','pending-in-window','2026-11-25 05:04Z'), ('fact','tenant-report','pending-next-day','2026-11-25 16:00Z');`)
		pending, err := reportrollup.ReadProjectionCoverage(ctx, pool, start, today, today.Add(time.Hour))
		if err != nil || pending.PendingSince == nil {
			t.Fatalf("pending in-window facts should be visible: %+v %v", pending, err)
		}
	})

	t.Run("session_dashboard_calendar", func(t *testing.T) {
		exec(`
CREATE TABLE public.session_dim (tenant_id text, gw_session_id text, owner_user text,
 first_request_at timestamptz, closed_at timestamptz, client_id text, last_active_at timestamptz,
 project_id text, application_code text,
 PRIMARY KEY (tenant_id, gw_session_id));
ALTER TABLE public.users ADD COLUMN display_name text, ADD COLUMN email text;
ALTER TABLE public.api_keys ADD COLUMN key_alias text;
INSERT INTO public.api_keys (id,tenant_id,key_prefix,owner_user,key_alias) VALUES
 (71,'session-a','redacted-prefix','alice','work-key');
INSERT INTO public.users (id,tenant_id,username,display_name,email) VALUES
 (101,'session-a','alice','Alice A','a@example.test'),(102,'session-b','alice','Alice B','b@example.test');
ALTER TABLE public.session_summaries ADD COLUMN health_grade text, ADD COLUMN health_score integer,
 ADD COLUMN compliance_status text, ADD COLUMN prompt_injection_detected boolean,
 ADD COLUMN pii_detected boolean, ADD COLUMN toxic_output_detected boolean, ADD COLUMN last_health_at timestamp;
INSERT INTO public.session_dim (tenant_id,gw_session_id,owner_user,first_request_at,closed_at) VALUES
 ('session-a','same-id','alice','2026-11-26 16:00Z',NULL),
 ('session-a','closing','alice','2026-11-25 17:00Z','2026-11-27 16:00Z'),
 ('session-a','probe-id','alice','2026-11-26 17:00Z',NULL),
 ('session-a','other-owner','bob','2026-11-27 17:00Z',NULL),
 ('session-b','same-id','alice','2026-11-26 16:00Z',NULL);
UPDATE public.session_dim SET last_active_at='2026-11-27 16:00Z',project_id='project/a',application_code='fallback'
 WHERE tenant_id='session-a' AND gw_session_id='same-id';
UPDATE public.session_dim SET application_code='bench' WHERE tenant_id='session-b' AND gw_session_id='same-id';
INSERT INTO public.session_summaries (session_key,tenant_id,health_grade,health_score,compliance_status,last_health_at)
 VALUES ('same-id','session-a','A',88,'compliant','2026-11-27 16:01Z'::timestamptz::timestamp),
 ('other-owner','session-a',NULL,NULL,'violation',NULL);
INSERT INTO public.request_context_attrs
 (request_id,tenant_id,ts,gw_session_id,gw_task_id,virtual_client_id,owner_user) VALUES
 ('session-east-1','session-a','2026-11-26 16:00Z','same-id','t1','c1','alice'),
 ('session-east-2','session-a','2026-11-27 16:00Z','same-id','t1','c1','alice'),
 ('session-prev','session-a','2026-11-25 17:00Z','closing','t2','c2','alice'),
 ('session-probe','session-a','2026-11-26 17:00Z','probe-id',NULL,NULL,'alice'),
 ('session-other','session-a','2026-11-27 17:00Z','other-owner',NULL,NULL,'bob'),
 ('session-east-1','session-b','2026-11-26 16:00Z','same-id',NULL,NULL,'alice');
UPDATE public.request_context_attrs SET agent_name='fixture-agent'
 WHERE tenant_id='session-a' AND request_id IN ('session-east-1','session-east-2');
INSERT INTO public.usage_facts
 (event_id,request_id,occurred_at,tenant_id,traffic_class,status,canonical_id,total_tokens,cost_amount,cost_currency,credits_charged)
 VALUES
 ('session-e1','session-east-1','2026-11-26 16:00Z','session-a','business','success',7,30,0.002,'USD',2),
 ('session-e2','session-east-2','2026-11-27 16:00Z','session-a','business','success',7,20,1,'CNY',3),
 ('session-e3','session-prev','2026-11-25 17:00Z','session-a','business','success',7,10,0.001,'USD',1),
 ('session-e4','session-probe','2026-11-26 17:00Z','session-a','probe','success',7,90,0,'USD',0),
 ('session-e5','session-other','2026-11-27 17:00Z','session-a','business','failed',7,0,0,'USD',0),
 ('session-e6','session-no-dim','2026-11-27 17:00Z','session-a','business','success',7,15,0.003,'USD',1),
 ('session-e7','session-east-1','2026-11-26 16:00Z','session-b','business','success',7,50,0.005,'USD',10),
 ('session-e8','session-end','2026-11-28 16:00Z','session-a','business','success',7,200,1,'USD',20);
UPDATE public.usage_facts SET api_key_id=71,provider_id=9,end_user_id='fixture-person'
 WHERE tenant_id='session-a' AND event_id IN ('session-e1','session-e2');
UPDATE public.usage_facts SET canonical_id=NULL,raw_model_name='bob-model'
 WHERE tenant_id='session-a' AND event_id='session-e5';`)
		if _, err := processor.ProcessBatch(ctx, 100); err != nil {
			t.Fatal(err)
		}
		loc := time.FixedZone("Asia/Shanghai", 8*60*60)
		start := time.Date(2026, 11, 27, 0, 0, 0, 0, loc)
		end := start.AddDate(0, 0, 2)
		migration, err := os.ReadFile("../../sql/migrations/startup/762_session_project_backfill_chain.sql")
		if err != nil {
			t.Fatal(err)
		}
		_, resolver, ok := strings.Cut(string(migration), "CREATE OR REPLACE FUNCTION public.gw_resolve_project_ref(")
		body, _, terminated := strings.Cut(resolver, "$$;")
		if !ok || !terminated {
			t.Fatal("project resolver unavailable in migration 762")
		}
		exec("CREATE OR REPLACE FUNCTION public.gw_resolve_project_ref(" + body + "$$;")
		personnel := metrics.SessionPeopleQuery{Start: start, End: end, PreviousStart: start,
			Timezone: "Asia/Shanghai", Section: "ranking", Limit: 10, SortBy: "tokens"}
		readPeople := func(query metrics.SessionPeopleQuery) (metrics.SessionPeopleResult, map[string]any) {
			t.Helper()
			result, err := metrics.ReadSessionPeople(ctx, pool, query)
			if err != nil || !result.Ready {
				t.Fatalf("personnel projection unavailable for %s: %v", query.Section, err)
			}
			var data map[string]any
			if err := json.Unmarshal(result.Payload, &data); err != nil {
				t.Fatal(err)
			}
			return result, data
		}
		_, ranking := readPeople(personnel)
		var aliceTenants []any
		for _, item := range ranking["users"].([]any) {
			row := item.(map[string]any)
			if row["owner"] == "alice" {
				aliceTenants = append(aliceTenants, row["tenant_id"])
			}
		}
		if len(aliceTenants) != 2 || aliceTenants[0] != "session-a" || aliceTenants[1] != "session-b" {
			t.Fatalf("ranking merged or reordered two tenant principals: %+v", ranking)
		}
		personnel.Owner, personnel.Section = "alice", "detail"
		if result, _ := readPeople(personnel); result.MatchingTenants != 2 {
			t.Fatalf("unqualified same-name principal must be ambiguous: %+v", result)
		}
		personnel.Tenant, personnel.PreviousStart = "session-a", start.AddDate(0, 0, -2)
		_, detail := readPeople(personnel)
		summary := detail["summary"].(map[string]any)
		if summary["request_count"] != 2.0 || summary["session_count"] != 1.0 || summary["total_cost"] != 0.002 ||
			summary["credits_charged"] != 5.0 || detail["user"].(map[string]any)["email"] != "a@example.test" ||
			len(detail["trends"].(map[string]any)["sessions"].([]any)) != 2 {
			t.Fatalf("personnel detail disagrees with request and session grains: %+v", detail)
		}
		detailCosts := detail["trends"].(map[string]any)["cost"].([]any)
		if len(detailCosts) != 2 || detailCosts[0].(map[string]any)["total_cost_usd"] != 0.002 ||
			detailCosts[1].(map[string]any)["total_cost_usd"] != nil || detailCosts[1].(map[string]any)["credits_charged"] != 3.0 {
			t.Fatalf("personnel trend lost the amount units or missing USD evidence: %+v", detailCosts)
		}
		personnel.Section, personnel.PreviousStart = "projects", start
		_, projects := readPeople(personnel)
		if rows := projects["projects"].([]any); len(rows) != 1 || rows[0].(map[string]any)["project"] != "project/a" {
			t.Fatalf("explicit project lost to application fallback: %+v", projects)
		}
		personnel.Section, personnel.Project = "tasks", "project/a"
		_, tasks := readPeople(personnel)
		if rows := tasks["tasks"].([]any); len(rows) != 1 || rows[0].(map[string]any)["request_count"] != 2.0 {
			t.Fatalf("project drill did not retain scope and count: %+v", tasks)
		}
		personnel.Project = ""
		personnel.Section = "models"
		_, models := readPeople(personnel)
		if rows := models["models"].([]any); len(rows) != 1 || rows[0].(map[string]any)["request_count"] != 2.0 ||
			rows[0].(map[string]any)["model"] != "canonical-test" {
			t.Fatalf("personnel model drill disagrees with request facts: %+v", models)
		}
		personnel.Section = "api_keys"
		_, keys := readPeople(personnel)
		if rows := keys["api_keys"].([]any); len(rows) != 1 || rows[0].(map[string]any)["request_count"] != 2.0 ||
			rows[0].(map[string]any)["top_project"] != "project/a" || rows[0].(map[string]any)["key_alias"] != "work-key" {
			t.Fatalf("personnel key drill disagrees with request scope: %+v", keys)
		}
		dimensions := metrics.SessionDimensionQuery{Start: start, End: end, ActiveSince: end.Add(-24 * time.Hour),
			Tenant: "session-a", Timezone: "Asia/Shanghai", Limit: 10, Dimension: "clients"}
		readDimension := func(query metrics.SessionDimensionQuery) map[string]any {
			t.Helper()
			result, err := metrics.ReadSessionDimensions(ctx, pool, query)
			if err != nil || !result.Ready {
				t.Fatalf("session dimension %s/%s unavailable: %v", query.Dimension, query.ID, err)
			}
			var data map[string]any
			if len(result.Payload) != 0 {
				if err := json.Unmarshal(result.Payload, &data); err != nil {
					t.Fatal(err)
				}
			}
			return data
		}
		list := readDimension(dimensions)
		if rows := list["clients"].([]any); list["total"] != 1.0 || len(rows) != 1 ||
			rows[0].(map[string]any)["total_requests"] != 2.0 {
			t.Fatalf("client list mixed scoped requests: %+v", list)
		}
		dimensions.ID = "c1"
		client := readDimension(dimensions)
		if client["session_count"] != 1.0 || client["total_requests"] != 2.0 || client["total_cost_usd"] != 0.002 ||
			client["usd_unmeasured_requests"] != 1.0 || client["active_sessions_24h"] != 1.0 {
			t.Fatalf("client detail disagrees with the scoped list: %+v", client)
		}
		clientDays := client["daily_cost_trend"].([]any)
		if len(clientDays) != 2 || clientDays[1].(map[string]any)["cost"] != nil {
			t.Fatalf("client trend lost the selected day or converted missing USD to zero: %+v", clientDays)
		}
		dimensions.ID, dimensions.Dimension = "t1", "tasks"
		task := readDimension(dimensions)
		if task["total_requests"] != 2.0 || task["credits_charged"] != 5.0 ||
			len(task["related_clients"].([]any)) != 1 || len(task["recent_sessions"].([]any)) != 1 {
			t.Fatalf("task panels did not retain the same request scope: %+v", task)
		}
		dimensions.Tenant = "session-b"
		if data := readDimension(dimensions); data != nil {
			t.Fatalf("same session ID in another tenant leaked task data: %+v", data)
		}
		dimensions.Dimension, dimensions.ID, dimensions.Tenant = "owners", "alice", "session-a"
		profile := readDimension(dimensions)
		if profile["total_requests"] != 2.0 || profile["session_count"] != 1.0 ||
			profile["end_user_count"] != 1.0 || profile["total_cost_usd"] != 0.002 ||
			len(profile["top_tasks"].([]any)) != 1 || len(profile["top_end_users"].([]any)) != 1 {
			t.Fatalf("user profile panels split across request scopes: %+v", profile)
		}
		if item := profile["top_end_users"].([]any)[0].(map[string]any); item["end_user_id"] != "fixture-person" ||
			item["session_count"] != 1.0 {
			t.Fatalf("profile did not deduplicate end-user session activity: %+v", item)
		}
		scope := metrics.SessionPeopleQuery{Start: start, End: end, PreviousStart: start,
			Owner: "alice", Timezone: "Asia/Shanghai", Section: "identity"}
		if result, _ := readPeople(scope); result.MatchingTenants != 2 {
			t.Fatalf("unqualified profile selected one of two tenants: %+v", result)
		}
		options, err := metrics.ReadSessionFilterOptions(ctx, pool, start, end, "session-a", "alice")
		if err != nil || !options.Ready || !strings.Contains(string(options.Payload), `"supplier"`) ||
			!strings.Contains(string(options.Payload), `"canonical-test"`) || strings.Contains(string(options.Payload), "bob-model") {
			t.Fatalf("filter options escaped the viewer's request scope: %+v %v", options, err)
		}
		overviewResult, err := metrics.ReadSessionAnalyticsOverview(ctx, pool, metrics.SessionAnalyticsOverviewQuery{
			Start: start, End: end, PreviousStart: start.AddDate(0, 0, -2), Tenant: "session-a", Timezone: "Asia/Shanghai",
		})
		if err != nil || !overviewResult.Ready {
			t.Fatalf("analytics overview unavailable: %v", err)
		}
		var aggregateOverview map[string]any
		if err := json.Unmarshal(overviewResult.Payload, &aggregateOverview); err != nil {
			t.Fatal(err)
		}
		aggregate := aggregateOverview["summary"].(map[string]any)
		if aggregate["total_sessions"] != 3.0 || aggregate["total_sessions_growth"] != 200.0 ||
			aggregate["request_count"] != 4.0 || aggregate["total_cost_usd"] != 0.005 ||
			aggregate["credits_charged"] != 6.0 || aggregate["total_cost_usd_growth"] != nil ||
			aggregate["avg_context_size"] != nil {
			t.Fatalf("analytics overview mixed session and request measures: %+v", aggregate)
		}
		dailyCosts := aggregateOverview["trends"].(map[string]any)["cost"].([]any)
		var usd, credits float64
		for _, item := range dailyCosts {
			day := item.(map[string]any)
			if amount := day["total_cost_usd"]; amount != nil {
				usd += amount.(float64)
			}
			credits += day["credits_charged"].(float64)
		}
		if math.Abs(usd-0.005) > 1e-12 || credits != 6 {
			t.Fatalf("analytics overview trend disagrees with the summary: %+v", dailyCosts)
		}
		for _, dimension := range []string{"agents", "tasks"} {
			ranks, err := metrics.ReadSessionRequestRanking(ctx, pool, metrics.SessionRequestRankingQuery{
				Start: start, End: end, Tenant: "session-a", Dimension: dimension, SortBy: "sessions",
			})
			if err != nil || !ranks.Ready {
				t.Fatalf("%s ranking unavailable: %+v %v", dimension, ranks, err)
			}
			var rows []map[string]any
			if err := json.Unmarshal(ranks.Payload, &rows); err != nil || len(rows) != 1 {
				t.Fatalf("unexpected %s ranking: %s %v", dimension, ranks.Payload, err)
			}
			row := rows[0]
			if row["session_count"] != 1.0 || row["total_tokens"] != 50.0 || row["total_cost"] != 0.002 {
				t.Fatalf("%s ranking mixed session and request grains: %+v", dimension, row)
			}
			if dimension == "agents" && (row["agent_name"] != "fixture-agent" || row["avg_duration_seconds"] != 86400.0) {
				t.Fatalf("agent duration did not use the native session record: %+v", row)
			}
			if dimension == "tasks" && (row["task"] != "t1" || row["turn_count"] != 2.0) {
				t.Fatalf("task ranking disagrees with projected requests: %+v", row)
			}
		}
		query := metrics.SessionDashboardQuery{
			Start: start, End: end, PrevStart: start.AddDate(0, 0, -2),
			RecentStart: end.Add(-24 * time.Hour), AsOf: end, Timezone: "Asia/Shanghai",
			Tenant: "session-a", Owner: "alice",
		}
		read := func(query metrics.SessionDashboardQuery, section metrics.SessionDashboardSection) map[string]any {
			t.Helper()
			result, err := metrics.ReadSessionDashboard(ctx, pool, query, section)
			if err != nil || !result.Ready {
				t.Fatalf("session projection unavailable: %+v %v", result, err)
			}
			var data map[string]any
			if err := json.Unmarshal(result.Payload, &data); err != nil {
				t.Fatal(err)
			}
			return data
		}
		overview := read(query, metrics.SessionDashboardOverview)
		cost := overview["cost_stats"].(map[string]any)
		health := overview["health_distribution"].(map[string]any)
		compliance := overview["compliance_stats"].(map[string]any)
		if overview["total_sessions"] != 2.0 || overview["active_sessions"] != 1.0 ||
			overview["new_sessions"] != 1.0 || overview["closed_sessions"] != 1.0 || overview["closed_sessions_24h"] != 1.0 ||
			cost["total_requests"] != 2.0 || cost["total_tokens"] != 50.0 || cost["total_credits"] != 5.0 ||
			cost["total_cost_usd"] != 0.002 || cost["usd_requests"] != 1.0 || cost["cost_growth_pct"] != nil ||
			health["total"] != 1.0 || compliance["not_assessed"] != 2.0 || compliance["compliance_rate"] != nil {
			t.Fatalf("owner overview uses the wrong cohort or currency: %+v", overview)
		}
		trend := read(query, metrics.SessionDashboardTrend)
		points := trend["trend"].([]any)
		if len(points) != 2 {
			t.Fatalf("expected exactly 2 daily buckets: %+v", trend)
		}
		first, second := points[0].(map[string]any), points[1].(map[string]any)
		if summary := trend["summary"].(map[string]any); summary["total_active"] != 1.0 ||
			summary["growth_rate_pct"] != 0.0 || first["date"] != "2026-11-27" || first["active_count"] != 1.0 ||
			second["date"] != "2026-11-28" || second["active_count"] != 1.0 || second["closed_count"] != 1.0 ||
			second["total_cost"] != nil {
			t.Fatalf("daily trend or previous-period comparison is incorrect: %+v", trend)
		}
		query.Page, query.Size = 1, 1
		active := read(query, metrics.SessionDashboardActive)
		rows := active["sessions"].([]any)
		if len(rows) != 1 {
			t.Fatalf("expected one active owned session: %+v", active)
		}
		row := rows[0].(map[string]any)
		if active["total_active"] != 1.0 || row["request_count"] != 2.0 || row["total_cost"] != 0.002 ||
			row["usd_requests"] != 1.0 || row["session_id"] != "same-id" || row["model"] != "canonical-test" {
			t.Fatalf("active list disagrees with the overview: %+v", active)
		}
		healthData := read(query, metrics.SessionDashboardHealth)
		observations := healthData["trend"].([]any)
		if healthData["distribution"].(map[string]any)["not_assessed"] != 1.0 || len(observations) != 1 ||
			observations[0].(map[string]any)["date"] != "2026-11-28" {
			t.Fatalf("health distribution or assessment day is inconsistent: %+v", healthData)
		}
		query.Owner, query.Page = "", 2
		page2 := read(query, metrics.SessionDashboardActive)
		if rows := page2["sessions"].([]any); page2["total_active"] != 2.0 || len(rows) != 1 ||
			rows[0].(map[string]any)["session_id"] != "same-id" {
			t.Fatalf("active pagination reordered or changed the total: %+v", page2)
		}
		tenant := read(query, metrics.SessionDashboardOverview)
		if tenant["active_sessions"] != 2.0 || tenant["requests_without_session"] != 1.0 {
			t.Fatalf("tenant session coverage is incorrect: %+v", tenant)
		}
		attribution := tenant["session_attribution"].(map[string]any)
		var attributedCount, missingSessionCount, missingSessionUSD float64
		for _, item := range attribution["totals"].([]any) {
			row := item.(map[string]any)
			attributedCount += row["requests"].(float64)
			if row["reason"] == "no_session" {
				missingSessionCount, missingSessionUSD = row["requests"].(float64), row["cost_usd"].(float64)
			}
		}
		if attributedCount != 4 || missingSessionCount != 1 || missingSessionUSD != 0.003 ||
			len(attribution["daily"].([]any)) != 3 {
			t.Fatalf("request totals did not close with the session-ownership split: %+v", attribution)
		}
		query.IncludeProbes = true
		if all := read(query, metrics.SessionDashboardOverview); all["active_sessions"] != 3.0 || all["new_sessions"] != 3.0 {
			t.Fatalf("probe session is missing from the explicit all-traffic scope: %+v", all)
		}
		query.Tenant, query.IncludeProbes = "", false
		platform := read(query, metrics.SessionDashboardOverview)
		if platform["active_sessions"] != 3.0 ||
			platform["health_distribution"].(map[string]any)["total"] != 1.0 {
			t.Fatalf("platform scope merged same-ID sessions across tenants: %+v", platform)
		}

		api := func(method string, section metrics.SessionDashboardSection, suffix string, wantStatus int, fragments ...string) {
			t.Helper()
			request := httptest.NewRequest(method, "/api/admin/dashboard/v2/session-"+string(section)+"?start_date=2026-11-27&end_date=2026-11-28"+suffix, nil)
			request = request.WithContext(dashboardapi.SetAuthInfoToContext(request.Context(), dashboardapi.AuthInfo{
				TenantID: "session-a", UserRole: "user", IsJWT: true, Username: "alice",
			}))
			recorder := httptest.NewRecorder()
			dashboardapi.HandleProjectedSessionDashboard(recorder, request, pool, section)
			if recorder.Code != wantStatus {
				t.Fatalf("session dashboard HTTP %d, want %d: %s", recorder.Code, wantStatus, recorder.Body)
			}
			for _, fragment := range fragments {
				if !strings.Contains(recorder.Body.String(), fragment) {
					t.Fatalf("session dashboard response lacks %q: %s", fragment, recorder.Body)
				}
			}
		}
		api(http.MethodGet, metrics.SessionDashboardOverview, "", http.StatusOK, `"source":"request_metrics"`, `"lifecycle_source":"session_dim"`,
			`"period_start":"2026-11-26T16:00:00Z"`, `"active_sessions":1`, `"pending_since":null`)
		api(http.MethodGet, metrics.SessionDashboardActive, "&page=1&size=1", http.StatusOK,
			`"total_active":1`, `"pages":1`, `"session_id":"same-id"`)
		api(http.MethodGet, metrics.SessionDashboardOverview, "&tenant_id=session-b", http.StatusForbidden, `"code":"FORBIDDEN"`)
		api(http.MethodPost, metrics.SessionDashboardOverview, "", http.StatusMethodNotAllowed)
		exec(`UPDATE request_metrics_repair_state SET backfill_complete=false WHERE singleton`)
		t.Cleanup(func() { exec(`UPDATE request_metrics_repair_state SET backfill_complete=true WHERE singleton`) })
		api(http.MethodGet, metrics.SessionDashboardOverview, "", http.StatusServiceUnavailable, `"code":"DATA_NOT_READY"`)
	})
	t.Run("daily_summary_rebuild_keeps_exact_membership_and_measures", func(t *testing.T) {
		exec(`
INSERT INTO public.request_metrics_projection
 (request_id, tenant_id, occurred_at, occurred_date, billing_date, session_id, outbound_model,
  status, traffic_class, prompt_tokens, total_tokens, cached_tokens, cache_input_tokens,
  cost_amount, cost_currency, latency_ms, ttft_ms, usage_source, source, source_fact_id)
SELECT req, tenant, ts, (ts AT TIME ZONE 'UTC')::date, (ts AT TIME ZONE 'UTC')::date, session_id, model,
       status, traffic, tokens, tokens, cached, cache_input, amount, currency, latency, ttft, measured, 'async_processor', n
FROM (VALUES
 (10001,'summary1','summary-a',timestamptz '2026-11-18 00:00+08','shared','model-a','success','business',11,3,11,0.2,'USD',100,10,'llm'),
 (10002,'summary2','summary-a',timestamptz '2026-11-18 08:00+08','shared','model-a','timeout','business',17,0,NULL,0.2,'CNY',250,NULL,'estimated'),
 (10003,'summary3','summary-b',timestamptz '2026-11-18 08:00+08','shared','model-b','success','business',5,2,5,0.2,'USD',NULL,3,'corrected'),
 (10004,'summary4','summary-a',timestamptz '2026-11-19 12:00+08','shared','model-a','success','business',3,2,3,0.5,'USD',101,NULL,'llm'),
 (10005,'summary5','summary-a',timestamptz '2026-11-19 14:00+08','probe-only','model-b','success','probe',7,0,7,0.4,'EUR',999,200,'llm'),
 (10006,'summary6','summary-a',timestamptz '2026-11-20 12:00+08',NULL,NULL,'success','business',0,0,0,NULL,NULL,77,11,'llm'),
 (10007,'summary7','summary-b',timestamptz '2026-11-21 00:00+08','at-end','model-c','success','business',99,0,99,9,'USD',123,12,'llm')
) AS r(n,req,tenant,ts,session_id,model,status,traffic,tokens,cached,cache_input,amount,currency,latency,ttft,measured);`)
		zone := time.FixedZone("UTC+8", 8*3600)
		from := time.Date(2026, 11, 18, 0, 0, 0, 0, zone)
		through := from.AddDate(0, 0, 3)
		f := metrics.MetricsFilter{StartTime: from, EndTime: through}
		stats := func(t *testing.T, filter metrics.MetricsFilter, detail bool) string {
			t.Helper()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if detail {
				if _, err := tx.Exec(ctx, "UPDATE public.request_metrics_daily_state SET dirty=true WHERE NOT dirty"); err != nil {
					t.Fatal(err)
				}
			}
			var jitBefore, jitAfter string
			if err := tx.QueryRow(ctx, "SHOW jit").Scan(&jitBefore); err != nil {
				t.Fatal(err)
			}
			result, err := metrics.ReadRequestStats(ctx, tx, metrics.RequestStatsQuery{MetricsFilter: filter, Group: "summary", Timezone: "Asia/Shanghai"})
			if err != nil || !result.Ready || len(result.Items) != 1 {
				t.Fatalf("summary result %+v, error %v", result, err)
			}
			if err := tx.QueryRow(ctx, "SHOW jit").Scan(&jitAfter); err != nil || jitAfter != jitBefore {
				t.Fatalf("summary changed the caller's JIT setting from %q to %q: %v", jitBefore, jitAfter, err)
			}
			return string(result.Items[0])
		}
		assertParity := func(t *testing.T, filters ...metrics.MetricsFilter) {
			t.Helper()
			for _, filter := range filters {
				got, want := stats(t, filter, false), stats(t, filter, true)
				if got != want {
					t.Fatalf("summary differs from detail for %+v:\ngot  %s\nwant %s", filter, got, want)
				}
			}
		}
		refresh := func() {
			t.Helper()
			for range 30 {
				built, err := processor.RefreshDailySummary(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !built {
					return
				}
			}
			t.Fatal("daily refresh did not drain")
		}

		// Fail after writing the measures and membership but before publication.
		exec(`
CREATE FUNCTION public.reject_daily_publish() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected daily publish failure'; END $$;
CREATE TRIGGER reject_daily_publish BEFORE UPDATE ON public.request_metrics_daily_state
FOR EACH ROW WHEN (OLD.dirty AND NOT NEW.dirty) EXECUTE FUNCTION public.reject_daily_publish();`)
		if _, err := processor.RefreshDailySummary(ctx); err == nil || !strings.Contains(err.Error(), "injected daily publish failure") {
			t.Fatalf("expected injected publication error, got %v", err)
		}
		var rows int
		if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM public.request_metrics_daily_summary) + (SELECT count(*) FROM public.request_metrics_daily_session_codes) + (SELECT count(*) FROM public.request_metrics_daily_session_masks)").Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("failed publication left %d derived rows: %v", rows, err)
		}
		exec("DROP TRIGGER reject_daily_publish ON public.request_metrics_daily_state; DROP FUNCTION public.reject_daily_publish()")
		refresh()
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.request_metrics_daily_state
		    WHERE day >= '2026-11-18' AND day < '2026-11-21' AND NOT dirty`).Scan(&rows); err != nil || rows != 3 {
			t.Fatalf("expected three clean days, got %d: %v", rows, err)
		}
		tenant := f
		tenant.TenantID = "summary-a"
		probe := f
		probe.IncludeProbes = true
		explicitProbe := "probe"
		probeOnly := probe
		probeOnly.TrafficClass = &explicitProbe
		split := f
		split.StartTime = from.Add(time.Hour)
		split.EndTime = through.Add(-time.Hour)
		if got := stats(t, f, false); !strings.Contains(got, `"active_sessions": 2`) || !strings.Contains(got, `"total_requests": 5`) {
			t.Fatalf("unexpected cross-day and tenant union: %s", got)
		}
		assertParity(t, f, tenant, probe, probeOnly, split)

		// Move a request between two closed days. Replace a shared session
		// association, delete the only copy for one tenant, and add a new session.
		exec(`
UPDATE public.request_metrics_projection
SET occurred_at='2026-11-19 16:00+08', occurred_date='2026-11-19', session_id='replacement', total_tokens=42
WHERE request_id='summary1';
DELETE FROM public.request_metrics_projection WHERE request_id='summary3';
UPDATE public.request_metrics_projection SET session_id='new-after-build', cost_currency='CNY'
WHERE request_id='summary2';`)
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.request_metrics_daily_state
		    WHERE day IN ('2026-11-18','2026-11-19') AND dirty`).Scan(&rows); err != nil || rows != 2 {
			t.Fatalf("move left stale published days: dirty=%d, error=%v", rows, err)
		}
		assertParity(t, f, tenant, probe, probeOnly, split)

		// Two slots can rebuild different days while preserving shared codes.
		results := make(chan error, 2)
		for range 2 {
			go func() {
				built, err := processor.RefreshDailySummary(ctx)
				if err == nil && !built {
					err = errors.New("daily rebuild did not claim work")
				}
				results <- err
			}()
		}
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.request_metrics_daily_state
		    WHERE day IN ('2026-11-18','2026-11-19') AND dirty`).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("two-slot rebuild left %d dirty days: %v", rows, err)
		}
		assertParity(t, f, tenant, probe, probeOnly, split)
		// Hold the worker just before publication, then have the real projector
		// deliver a late fact for its locked day.
		exec(`
UPDATE public.request_metrics_daily_state SET dirty=true WHERE day='2026-11-20';
CREATE FUNCTION public.wait_for_daily_publish_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(983220);
    RETURN NEW;
END $$;
CREATE TRIGGER wait_for_daily_publish_fixture BEFORE UPDATE ON public.request_metrics_daily_state
FOR EACH ROW WHEN (OLD.day='2026-11-20' AND OLD.dirty AND NOT NEW.dirty)
EXECUTE FUNCTION public.wait_for_daily_publish_fixture();`)
		barrier, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer barrier.Rollback(ctx)
		if _, err := barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(983220)"); err != nil {
			t.Fatal(err)
		}
		waitFor := func(sql string) {
			t.Helper()
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			for {
				var waiting bool
				if err := pool.QueryRow(waitCtx, sql).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		go func() {
			_, err := processor.RefreshDailySummary(ctx)
			results <- err
		}()
		waitFor("SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=983220 AND NOT granted)")
		exec(`
INSERT INTO public.usage_facts (event_id,request_id,occurred_at,tenant_id,status,total_tokens)
VALUES ('summary-late','summary-late','2026-11-20 18:00+08','summary-a','success',69);`)
		go func() {
			_, err := processor.ProcessBatch(ctx, 50)
			results <- err
		}()
		waitFor(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
		    WHERE datname=current_database() AND wait_event_type='Lock' AND pid<>pg_backend_pid()
		      AND query LIKE '%WITH state AS (%')`)
		if err := barrier.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		var dirty bool
		if err := pool.QueryRow(ctx, "SELECT dirty FROM public.request_metrics_daily_state WHERE day='2026-11-20'").Scan(&dirty); err != nil || !dirty {
			t.Fatalf("concurrent projection failed to invalidate the rebuilt day: dirty=%v error=%v", dirty, err)
		}
		assertParity(t, f, tenant, split)
		exec("DROP TRIGGER wait_for_daily_publish_fixture ON public.request_metrics_daily_state; DROP FUNCTION public.wait_for_daily_publish_fixture()")
		refresh()
		assertParity(t, f, tenant, split)
	})

	t.Run("board_days_match_detail_during_and_after_invalidation", func(t *testing.T) {
		exec(`
INSERT INTO public.request_metrics_projection (
  request_id, tenant_id, occurred_at, occurred_date, billing_date, status, traffic_class, outbound_model,
  provider_id, provider_code, provider_name, api_key_id, owner_user, agent_name, client_ip, identity_hash,
  session_id, total_tokens, cost_amount, cost_currency, credits_charged, latency_ms, ttft_ms, error_kind, source, source_fact_id)
SELECT 'board-day-'||n,CASE WHEN n<15 THEN 'board-days' ELSE 'board-days-other' END,ts,ts::date,ts::date,
  CASE WHEN n=7 OR n=10 THEN 'timeout' ELSE 'success' END,
  CASE WHEN n=8 THEN 'probe' ELSE 'business' END,
  CASE WHEN n IN (9,12) THEN '__others__' ELSE 'board-model-'||(n%8) END,
  9+n%2, CASE WHEN n%2=0 THEN 'p' ELSE NULL END, CASE WHEN n%3=0 THEN 'actual' ELSE NULL END,
  CASE WHEN n%4=0 THEN -129 WHEN n%5=0 THEN 65 ELSE 7+n%3 END, 'board-reader', CASE WHEN n%3=0 THEN 'client' ELSE NULL END,
  CASE WHEN n%2=0 THEN '192.0.2.15'::inet ELSE NULL END, CASE WHEN n%2=0 THEN 'named' ELSE '' END,
  'session-'||(n%4),n,n*0.001,CASE WHEN n%2=0 THEN 'USD' ELSE 'CNY' END,n,n*17,n*3,
  CASE WHEN n=7 THEN 'upstream_timeout' ELSE NULL END,'async_processor',23000+n
FROM (SELECT n, timestamptz '2026-11-16 00:00+08'+(n%2)*interval '1 day'+
             CASE WHEN n=2 THEN interval '23 hours 58 minutes' ELSE (n%3)*interval '1 minute' END AS ts
	  FROM generate_series(1,16) n) rows;`)
		zone := time.FixedZone("UTC+8", 8*3600)
		start := time.Date(2026, 11, 16, 0, 0, 0, 0, zone)
		f := metrics.MetricsFilter{TenantID: "board-days", StartTime: start, EndTime: start.AddDate(0, 0, 2)}
		assertParity := func(t *testing.T, filter metrics.MetricsFilter) {
			t.Helper()
			for _, bucket := range []int{5, 15, 60, 360} {
				q := metrics.BoardQuery{MetricsFilter: filter, BucketMinutes: bucket}
				actual, err := metrics.ReadRequestBoard(ctx, pool, q)
				if err != nil || !actual.Ready {
					t.Fatalf("board merged read %+v: %v", actual, err)
				}
				q.Owner = "board-reader"
				detail, err := metrics.ReadRequestBoard(ctx, pool, q)
				if err != nil || !detail.Ready {
					t.Fatalf("board detail read %+v: %v", detail, err)
				}
				got, err := json.Marshal(actual)
				if err != nil {
					t.Fatal(err)
				}
				want, err := json.Marshal(detail)
				if err != nil || string(got) != string(want) {
					t.Fatalf("board bucket %d differs from detail, err %v\ngot %s\nwant %s", bucket, err, got, want)
				}
			}
		}
		partial := f
		partial.StartTime = start.Add(time.Minute)
		partial.EndTime = f.EndTime.Add(time.Minute)
		withProbe := f
		withProbe.IncludeProbes = true
		probeOnly := withProbe
		probe := "probe"
		probeOnly.TrafficClass = &probe
		refresh := func(fn func(context.Context) (bool, error)) {
			t.Helper()
			for range 30 {
				built, err := fn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !built {
					return
				}
			}
			t.Fatal("daily rebuild did not drain")
		}
		refresh(processor.RefreshDailySummary)
		refresh(processor.RefreshDailyBoard)
		countPublished := func(want int) {
			t.Helper()
			var n int
			err := pool.QueryRow(ctx, `SELECT count(*) FROM public.request_metrics_board_days
				WHERE day IN ('2026-11-16','2026-11-17')`).Scan(&n)
			if err != nil || n != want {
				t.Fatalf("published board days=%d want %d: %v", n, want, err)
			}
		}
		countPublished(2)
		assertParity(t, f)
		assertParity(t, partial)
		assertParity(t, withProbe)
		assertParity(t, probeOnly)
		allTenants := withProbe
		allTenants.TenantID = ""
		assertParity(t, allTenants)

		exec(`
UPDATE public.request_metrics_projection SET occurred_at='2026-11-17 05:00+08', total_tokens=42
WHERE request_id='board-day-2';
DELETE FROM public.request_metrics_projection WHERE request_id='board-day-12';`)
		countPublished(0)
		assertParity(t, f)
		refresh(processor.RefreshDailySummary)
		countPublished(0)
		exec(`
CREATE FUNCTION public.reject_board_publish() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected board publication failure'; END $$;
CREATE TRIGGER reject_board_publish BEFORE INSERT ON public.request_metrics_board_days
FOR EACH STATEMENT EXECUTE FUNCTION public.reject_board_publish();`)
		if _, err := processor.RefreshDailyBoard(ctx); err == nil || !strings.Contains(err.Error(), "injected board publication failure") {
			t.Fatalf("expected failed board publication: %v", err)
		}
		countPublished(0)
		assertParity(t, withProbe)
		exec("DROP TRIGGER reject_board_publish ON public.request_metrics_board_days; DROP FUNCTION public.reject_board_publish()")
		if built, err := processor.RefreshDailyBoard(ctx); !built || err != nil {
			t.Fatalf("expected one board day to republish: built=%v error=%v", built, err)
		}
		countPublished(1)
		assertParity(t, f)
		assertParity(t, withProbe)
		refresh(processor.RefreshDailyBoard)
		countPublished(2)
		assertParity(t, f)
		assertParity(t, withProbe)
		assertParity(t, allTenants)
	})

	t.Run("published_history_merges_exact_ranks_and_uncached_days", func(t *testing.T) {
		exec(`
INSERT INTO public.request_metrics_projection (
 request_id, tenant_id, occurred_at, occurred_date, billing_date, status, traffic_class, provider_id,
 outbound_model, prompt_tokens, cached_tokens, cache_input_tokens, total_tokens, latency_ms,
 cost_amount, cost_currency, error_kind, source, source_fact_id)
SELECT 'cached-history-'||id, CASE WHEN id=2 THEN 'history-b' ELSE 'history-a' END,
 stamp::timestamptz,(stamp::timestamptz AT TIME ZONE 'Asia/Shanghai')::date,
 (stamp::timestamptz AT TIME ZONE 'Asia/Shanghai')::date,status,traffic,provider,
 'history-model-'||(id%2),id*20,id*2,CASE WHEN id%2=0 THEN id*25 END, id*21,latency,
 CASE WHEN id=6 THEN -id*0.0000123 ELSE id*0.0000123 END,
 CASE WHEN id=4 THEN 'CNY' ELSE 'USD' END,kind,'async_processor',25000+id
FROM (VALUES
 (1,'2026-11-12 00:00+08','success','business',9,80,NULL),
 (2,'2026-11-12 23:59+08','success','business',10,190,NULL),
 (3,'2026-11-13 05:00+08','success','business',9,8589935572,NULL),
 (4,'2026-11-13 05:10+08','success','probe',9,3500,NULL),
 (5,'2026-11-13 05:20+08','timeout','business',NULL,999,'upstream_timeout'),
 (6,'2026-11-14 00:00+08','success','business',9,500,NULL),
 (7,'2026-11-14 05:20+08','failed','business',NULL,0,NULL)
) AS fixture(id,stamp,status,traffic,provider,latency,kind);
INSERT INTO public.request_metrics_daily_state (day) VALUES ('2026-11-11') ON CONFLICT DO NOTHING;`)
		loc, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, 11, 11, 0, 0, 0, 0, loc)
		end := start.AddDate(0, 0, 3)
		assertParity := func(wantMode, wantTenants string, filter reportrollup.GrainFilter) {
			t.Helper()
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			coverage, err := reportrollup.ReadProjectionCoverage(ctx, tx, start, end, end.AddDate(0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				view  reportrollup.View
				scope reportrollup.ProjectedSummaryScope
			}{
				{reportrollup.ViewProvider, reportrollup.ProjectedSummaryDistribution},
				{reportrollup.ViewProvider, reportrollup.ProjectedSummaryTotals},
				{reportrollup.ViewInternal, reportrollup.ProjectedSummaryDistribution},
				{reportrollup.ViewInternal, reportrollup.ProjectedSummaryTotals},
			} {
				want, err := reportrollup.BuildProjectedReport(ctx, tx, start, end, tc.view, filter, reportrollup.Names{}, false, coverage)
				if err != nil {
					t.Fatal(err)
				}
				want.Models = []reportrollup.ModelRow{}
				want.DailyModels, want.Credentials, want.APIKeys = nil, nil, nil
				if tc.view == reportrollup.ViewInternal {
					want.Providers = nil
				}
				if tc.view != reportrollup.ViewInternal || tc.scope == reportrollup.ProjectedSummaryTotals {
					want.Tenants, want.Persons = nil, nil
				}
				if tc.scope == reportrollup.ProjectedSummaryTotals {
					want.Providers, want.ModelTotals = nil, nil
				}
				probe := &historyQueryProbe{Querier: tx}
				got, err := reportrollup.BuildProjectedSummary(ctx, probe, start, end, tc.view, filter, reportrollup.Names{}, coverage, tc.scope)
				wantTenantMode := ""
				if tc.view == reportrollup.ViewInternal && (tc.scope == reportrollup.ProjectedSummaryDistribution || filter.TenantID != "") {
					wantTenantMode = wantTenants
				}
				if err != nil || probe.mode != wantMode || probe.tenantMode != wantTenantMode || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s/%s used history modes %q/%q want %q/%q: %v\ngot %+v\nwant %+v", tc.view, tc.scope,
						probe.mode, probe.tenantMode, wantMode, wantTenantMode, err, got, want)
				}
			}
		}
		drain := func(refresh func(context.Context) (bool, error)) {
			t.Helper()
			for range 75 {
				more, err := refresh(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !more {
					return
				}
			}
			t.Fatal("history worker did not drain")
		}
		assertParity("", "", reportrollup.GrainFilter{})
		drain(processor.RefreshDailySummary)
		drain(processor.RefreshDailyHistory)
		assertParity("published", "", reportrollup.GrainFilter{})
		assertParity("", "", reportrollup.GrainFilter{TenantID: "history-a"})
		apply("983n_request_metrics_daily_tenant_history.sql")
		apply("983n_request_metrics_daily_tenant_history.sql")
		drain(processor.RefreshDailyHistory)
		assertParity("published", "published", reportrollup.GrainFilter{})
		assertParity("published", "published", reportrollup.GrainFilter{BusinessOnly: true})
		assertParity("", "published", reportrollup.GrainFilter{TenantID: "history-a"})
		assertParity("", "published", reportrollup.GrainFilter{TenantID: "unknown-tenant"})
		assertParity("", "", reportrollup.GrainFilter{TenantID: "history-a", Model: "history-model-1"})
		exec(`UPDATE public.request_metrics_projection SET occurred_at='2026-11-13 10:00+08',
			occurred_date='2026-11-13', billing_date='2026-11-13', latency_ms=491, cost_amount=0.12345678
			WHERE request_id='cached-history-2';
			DELETE FROM public.request_metrics_projection WHERE request_id='cached-history-1';`)
		var published int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.request_metrics_history_days
			WHERE day BETWEEN '2026-11-12' AND '2026-11-14'`).Scan(&published); err != nil || published != 1 {
			t.Fatalf("both revised dates must become uncovered, published=%d: %v", published, err)
		}
		assertParity("contiguous", "contiguous", reportrollup.GrainFilter{})
		assertParity("", "contiguous", reportrollup.GrainFilter{TenantID: "history-b"})
		drain(processor.RefreshDailySummary)
		exec(`CREATE FUNCTION public.reject_history_publish() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'injected history publication failure'; END $$;
			CREATE TRIGGER reject_history_publish BEFORE INSERT ON public.request_metrics_history_days
			FOR EACH STATEMENT EXECUTE FUNCTION public.reject_history_publish();`)
		if _, err := processor.RefreshDailyHistory(ctx); err == nil || !strings.Contains(err.Error(), "injected history publication failure") {
			t.Fatalf("expected history publication to roll back: %v", err)
		}
		assertParity("contiguous", "contiguous", reportrollup.GrainFilter{})
		exec("DROP TRIGGER reject_history_publish ON public.request_metrics_history_days; DROP FUNCTION public.reject_history_publish()")
		exec(`CREATE FUNCTION public.reject_tenant_history_publish() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'injected tenant history publication failure'; END $$;
			CREATE TRIGGER reject_tenant_history_publish BEFORE INSERT ON public.request_metrics_tenant_history_days
			FOR EACH STATEMENT EXECUTE FUNCTION public.reject_tenant_history_publish();`)
		if _, err := processor.RefreshDailyHistory(ctx); err == nil || !strings.Contains(err.Error(), "injected tenant history publication failure") {
			t.Fatalf("expected tenant history publication to roll back: %v", err)
		}
		assertParity("contiguous", "contiguous", reportrollup.GrainFilter{})
		exec("DROP TRIGGER reject_tenant_history_publish ON public.request_metrics_tenant_history_days; DROP FUNCTION public.reject_tenant_history_publish()")
		results := make(chan error, 2)
		for _, worker := range []*ProjectionProcessor{processor, NewProjectionProcessor(db)} {
			go func() {
				_, err := worker.RefreshDailyHistory(ctx)
				results <- err
			}()
		}
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		drain(processor.RefreshDailyHistory)
		assertParity("published", "published", reportrollup.GrainFilter{})
		exec(`UPDATE public.request_metrics_daily_state SET dirty=true WHERE day IN ('2026-11-11','2026-11-14');`)
		assertParity("fragmented", "fragmented", reportrollup.GrainFilter{})
		assertParity("", "fragmented", reportrollup.GrainFilter{TenantID: "history-a"})
		drain(processor.RefreshDailySummary)
		drain(processor.RefreshDailyHistory)
		assertParity("published", "published", reportrollup.GrainFilter{})
	})

	t.Run("person_pages_match_full_internal_distributions", func(t *testing.T) {
		exec(`
INSERT INTO public.request_metrics_projection (
 request_id,tenant_id,person_key,occurred_at,occurred_date,billing_date,raw_model_name,
 status,traffic_class,total_tokens,credits_charged,cost_amount,cost_currency,latency_ms,source,source_fact_id)
SELECT 'person-page-'||n,
 CASE WHEN n=62 THEN 'page-c' WHEN n IN (59,60,61) OR n%2=0 THEN 'page-a' ELSE 'page-b' END,
 CASE n WHEN 59 THEN NULL WHEN 60 THEN '' WHEN 61 THEN 'probe-only' WHEN 101 THEN 'person-01'
      ELSE 'person-'||lpad((n%29)::text,2,'0') END,
 '2026-11-05 01:00+08','2026-11-05','2026-11-05','page-model',
 CASE WHEN n=62 OR n%7=0 THEN 'timeout' ELSE 'success' END,
 CASE WHEN n=61 THEN 'probe' ELSE 'business' END,
 n*3,n,n*0.0003123,'USD',n*40,'async_processor',27000+n
FROM (SELECT n FROM generate_series(1,62) n UNION ALL SELECT 101) fixture;

INSERT INTO public.request_metrics_projection (
 request_id,tenant_id,person_key,occurred_at,occurred_date,billing_date,raw_model_name,
 status,traffic_class,total_tokens,credits_charged,cost_amount,cost_currency,latency_ms,source,source_fact_id)
VALUES ('person-page-earlier','page-a','person-10','2026-11-04 01:00+08','2026-11-04','2026-11-04',
 'page-model','success','business',11,7,0.000173,'USD',505,'async_processor',28001);`)
		drain := func(refresh func(context.Context) (bool, error)) {
			t.Helper()
			for {
				built, err := refresh(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !built {
					return
				}
			}
		}
		drain(processor.RefreshDailySummary)
		drain(processor.RefreshDailyHistory)
		loc, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, 11, 4, 0, 0, 0, 0, loc)
		end := start.AddDate(0, 0, 1)
		assertPages := func(wantPacked bool, tenantModelMode string) []*reportrollup.GrainReport {
			t.Helper()
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			coverage, err := reportrollup.ReadProjectionCoverage(ctx, tx, start, end, end.AddDate(0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			var reports []*reportrollup.GrainReport
			for _, tc := range []struct {
				filter    reportrollup.GrainFilter
				requested int32
				page      int32
				people    int64
			}{
				{requested: 1, page: 1, people: 60},
				{requested: 2, page: 2, people: 60},
				{requested: 99, page: 2, people: 60},
				{filter: reportrollup.GrainFilter{TenantID: "page-a"}, requested: 1, page: 1, people: 30},
				{filter: reportrollup.GrainFilter{Person: "unknown"}, requested: 1, page: 1, people: 1},
				{filter: reportrollup.GrainFilter{Person: "probe-only"}, requested: 2, page: 1, people: 0},
			} {
				want, err := reportrollup.BuildProjectedSummary(ctx, tx, start, end, reportrollup.ViewInternal, tc.filter,
					reportrollup.Names{}, coverage, reportrollup.ProjectedSummaryDistribution)
				if err != nil || int64(len(want.Persons)) != tc.people {
					t.Fatalf("full internal distribution for %+v: %v", tc, err)
				}
				sort.Slice(want.Persons, func(i, j int) bool {
					a, b := want.Persons[i], want.Persons[j]
					if a.Totals.RequestCount != b.Totals.RequestCount {
						return a.Totals.RequestCount > b.Totals.RequestCount
					}
					if a.TenantID != b.TenantID {
						return a.TenantID < b.TenantID
					}
					return a.Person < b.Person
				})
				want.PersonPage = &reportrollup.PersonPage{Number: tc.page, Size: 50, Total: tc.people}
				offset := int(tc.page-1) * want.PersonPage.Size
				want.Persons = want.Persons[offset:min(offset+want.PersonPage.Size, len(want.Persons))]
				probe := &historyQueryProbe{Querier: tx}
				got, err := reportrollup.BuildProjectedPersonPage(ctx, probe, start, end, tc.filter, coverage, tc.requested)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("person page for %+v does not equal exact full slice: %v", tc, err)
				}
				wantPackedForScope := wantPacked && tc.filter.TenantID == "" && tc.filter.Person == ""
				if probe.packedRank != wantPackedForScope {
					t.Fatalf("packed rank for %+v = %t, want %t", tc, probe.packedRank, wantPackedForScope)
				}
				if tc.filter.TenantID == "" && tc.filter.Person == "" && (probe.mode != "published" || probe.tenantMode != "published") {
					t.Fatalf("unfiltered person page should use published days, got %q/%q", probe.mode, probe.tenantMode)
				}
				if tc.filter.TenantID != "" && (probe.mode != "" || probe.tenantMode != "published") {
					t.Fatalf("one tenant page should use only the published tenant days, got %q/%q", probe.mode, probe.tenantMode)
				}
				wantTenantModel := ""
				if tc.filter.TenantID != "" {
					wantTenantModel = tenantModelMode
				}
				if probe.tenantModelMode != wantTenantModel {
					t.Fatalf("tenant model history for %+v = %q, want %q", tc, probe.tenantModelMode, wantTenantModel)
				}
				reports = append(reports, got)
			}
			return reports
		}
		before := assertPages(false, "")
		apply("983o_request_metrics_daily_history_details.sql")
		apply("983o_request_metrics_daily_history_details.sql")
		drain(processor.RefreshDailyHistory)
		if got := assertPages(true, "published"); !reflect.DeepEqual(got, before) {
			t.Fatal("publishing details changed the report")
		}

		exec("UPDATE public.request_metrics_projection SET person_key='new-late-person', raw_model_name='page-model-late' WHERE request_id='person-page-58'")
		var published bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM public.request_metrics_history_detail_days WHERE day='2026-11-05')").Scan(&published); err != nil || published {
			t.Fatalf("revision must revoke the person rank publication marker: %t %v", published, err)
		}
		drain(processor.RefreshDailySummary)
		exec(`CREATE FUNCTION public.reject_person_rank_publish() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'injected person rank publication failure'; END $$;
			CREATE TRIGGER reject_person_rank_publish BEFORE INSERT ON public.request_metrics_history_detail_days
			FOR EACH STATEMENT EXECUTE FUNCTION public.reject_person_rank_publish();`)
		if _, err := processor.RefreshDailyHistory(ctx); err == nil || !strings.Contains(err.Error(), "injected person rank publication failure") {
			t.Fatalf("expected rank publication to roll back: %v", err)
		}
		if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM public.request_metrics_history_days WHERE day='2026-11-05')").Scan(&published); err != nil || published {
			t.Fatalf("rank publish failure must also roll back general history: %t %v", published, err)
		}
		exec("DROP TRIGGER reject_person_rank_publish ON public.request_metrics_history_detail_days; DROP FUNCTION public.reject_person_rank_publish()")
		drain(processor.RefreshDailyHistory)
		revised := assertPages(true, "published")

		// A closed day without a ranking publication uses the live indexed
		// projection even when both other historical publications are current.
		exec("DELETE FROM public.request_metrics_history_detail_days WHERE day='2026-11-05'")
		if got := assertPages(false, "contiguous"); !reflect.DeepEqual(got, revised) {
			t.Fatal("mixing published and rebuilt details changed the revised report")
		}
		drain(processor.RefreshDailyHistory)
		if got := assertPages(true, "published"); !reflect.DeepEqual(got, revised) {
			t.Fatal("republishing details changed the revised report")
		}
	})

	t.Run("history_percentiles_follow_the_displayed_group", func(t *testing.T) {
		exec(`
INSERT INTO public.request_metrics_projection (
	request_id, tenant_id, occurred_at, occurred_date, billing_date,
	status, traffic_class, raw_model_name, provider_id, credential_id, api_key_id,
	total_tokens, latency_ms, error_kind, source, source_fact_id)
SELECT 'history-rank-'||n, tenant, timestamptz '2026-11-15 10:00+08',
	'2026-11-15', '2026-11-15', status, 'business', 'history-rank', provider, credential, key_id,
	1, latency, CASE WHEN status='timeout' THEN 'upstream_timeout' END, 'async_processor', 24000+n
FROM (VALUES
	(1,'rank-a',9,NULL,NULL,100,'success'),
	(2,'rank-a',9,NULL,NULL,200,'success'),
	(3,'rank-a',9,NULL,NULL,300,'success'),
	(4,'rank-b',10,NULL,NULL,900,'success'),
	(5,'rank-b',10,NULL,NULL,800,'timeout'),
	(6,'rank-a',9,77,88,80,'success'),
	(7,'rank-b',10,77,88,9800,'success')
) AS inputs(n,tenant,provider,credential,key_id,latency,status);`)
		loc, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			t.Fatal(err)
		}
		day := time.Date(2026, 11, 15, 0, 0, 0, 0, loc)
		coverage, err := reportrollup.ReadProjectionCoverage(ctx, pool, day, day, day.AddDate(0, 0, 1))
		if err != nil {
			t.Fatal(err)
		}
		read := func(filter reportrollup.GrainFilter) *reportrollup.GrainReport {
			t.Helper()
			report, err := reportrollup.BuildProjectedReport(ctx, pool, day, day, reportrollup.ViewInternal, filter, reportrollup.Names{}, true, coverage)
			if err != nil {
				t.Fatal(err)
			}
			return report
		}
		check := func(label string, totals reportrollup.Totals, requests, measured int64, p50, p95 float64) {
			t.Helper()
			if totals.RequestCount != requests || totals.LatencyMeasuredRequests == nil || *totals.LatencyMeasuredRequests != measured ||
				math.Abs(totals.LatencyP50Ms-p50) > 1e-9 || math.Abs(totals.LatencyP95Ms-p95) > 1e-9 {
				t.Fatalf("%s has incorrect requests or ranks: %+v", label, totals)
			}
		}
		filter := reportrollup.GrainFilter{Model: "history-rank"}
		report := read(filter)
		if len(report.Credentials) != 2 || len(report.APIKeys) != 2 || len(report.DailyCredentials) != 2 || len(report.DailyAPIKeys) != 2 {
			t.Fatalf("expected two rows per dimension: %+v", report)
		}
		for _, row := range report.Credentials {
			if row.ProviderID != nil {
				t.Fatalf("ambiguous provider attribution on credential %d: %v", row.CredentialID, row.ProviderID)
			}
			if row.CredentialID == reportrollup.UnassignedID {
				check("unassigned credential", row.Totals, 5, 4, 250, 810)
				if row.ErrorBreakdown["upstream_timeout"] != 1 {
					t.Fatalf("incorrect errors: %v", row.ErrorBreakdown)
				}
			} else {
				check("shared credential", row.Totals, 2, 2, 4940, 9314)
				if row.QualityScore != 53.7 {
					t.Fatalf("incorrect credential quality score: %v", row.QualityScore)
				}
			}
		}
		for _, row := range report.APIKeys {
			if row.TenantID != "" {
				t.Fatalf("ambiguous tenant attribution on key %d: %s", row.APIKeyID, row.TenantID)
			}
			if row.APIKeyID == reportrollup.UnassignedID {
				check("unassigned key", row.Totals, 5, 4, 250, 810)
			} else {
				check("shared key", row.Totals, 2, 2, 4940, 9314)
			}
		}
		for _, rows := range [][]reportrollup.DailyGroupRow{report.DailyAPIKeys, report.DailyCredentials} {
			for _, row := range rows {
				if row.Key == "-1" {
					check("unassigned day", row.Totals, 5, 4, 250, 810)
				} else {
					check("shared day", row.Totals, 2, 2, 4940, 9314)
				}
			}
		}
		workbook, err := reportrollup.BuildGrainWorkbookBytes(report, reportrollup.GrainGroupCredential, true)
		if err != nil {
			t.Fatal(err)
		}
		archive, err := zip.NewReader(bytes.NewReader(workbook), int64(len(workbook)))
		if err != nil {
			t.Fatal(err)
		}
		sheet, err := archive.Open("xl/worksheets/sheet1.xml")
		if err != nil {
			t.Fatal(err)
		}
		defer sheet.Close()
		xml, err := io.ReadAll(sheet)
		if err != nil || bytes.Count(xml, []byte("<v>53.7</v>")) < 2 {
			t.Fatalf("export should contain both corrected quality scores: %v", err)
		}
		provider := int64(9)
		filter.ProviderID = &provider
		filtered := read(filter)
		if len(filtered.Credentials) != 2 || filtered.Credentials[1].CredentialID != 77 ||
			filtered.Credentials[1].ProviderID == nil || *filtered.Credentials[1].ProviderID != provider {
			t.Fatalf("filtered provider missing: %+v", filtered.Credentials)
		}
		if len(filtered.APIKeys) != 2 || filtered.APIKeys[1].APIKeyID != 88 || filtered.APIKeys[1].TenantID != "rank-a" {
			t.Fatalf("filtered tenant missing: %+v", filtered.APIKeys)
		}
	})

	t.Run("coverage_and_schema_rollback_keep_legacy_watermark", func(t *testing.T) {
		start := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
		coverage, err := readProjectionCoverage(ctx, db, start, start.AddDate(0, 0, 1))
		if err != nil || coverage.Expected < 3 || coverage.Expected != coverage.Current ||
			coverage.Fraction == nil || *coverage.Fraction != 1 {
			t.Fatalf("coverage should match the latest facts in the window: %+v %v", coverage, err)
		}
		exec(`UPDATE public.request_metrics_repair_state SET backfill_complete=true, updated_at=now();
			INSERT INTO public.request_metrics_refresh_pending (kind,tenant_id,request_id,occurred_at,enqueued_at)
			VALUES ('fact','tenant-recovery','readiness-sample',now(),now()-interval '6 minutes');`)
		if state := readProjectionWatermark(http.StatusServiceUnavailable); state.Ready ||
			state.PendingCount == 0 || state.OldestWaitSeconds < 300 {
			t.Fatalf("stale backlog should block readiness: %+v", state)
		}
		apply("983o_request_metrics_daily_history_details.down.sql")
		apply("983o_request_metrics_daily_history_details.down.sql")
		apply("983n_request_metrics_daily_tenant_history.down.sql")
		apply("983n_request_metrics_daily_tenant_history.down.sql")
		if _, err := processor.RefreshDailyHistory(ctx); err != nil {
			t.Fatalf("old history worker path should remain available after 983n rollback: %v", err)
		}
		loc, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			t.Fatal(err)
		}
		day := time.Date(2026, 11, 13, 0, 0, 0, 0, loc)
		historyCoverage, err := reportrollup.ReadProjectionCoverage(ctx, pool, day, day, day.AddDate(0, 0, 1))
		if err != nil {
			t.Fatal(err)
		}
		tenantProbe := &historyQueryProbe{Querier: pool}
		internal, err := reportrollup.BuildProjectedSummary(ctx, tenantProbe, day, day, reportrollup.ViewInternal,
			reportrollup.GrainFilter{}, reportrollup.Names{}, historyCoverage, reportrollup.ProjectedSummaryDistribution)
		if err != nil || tenantProbe.mode != "published" || tenantProbe.tenantMode != "" || len(internal.Tenants) == 0 {
			t.Fatalf("internal history should fall back only for tenants after 983n rollback: %+v %v", internal, err)
		}
		apply("983m_request_metrics_daily_history.down.sql")
		apply("983m_request_metrics_daily_history.down.sql")
		probe := &historyQueryProbe{Querier: pool}
		history, err := reportrollup.BuildProjectedSummary(ctx, probe, day, day, reportrollup.ViewInternal,
			reportrollup.GrainFilter{}, reportrollup.Names{}, historyCoverage, reportrollup.ProjectedSummaryTotals)
		if err != nil || probe.mode != "" || history.Totals.RequestCount != 3 {
			t.Fatalf("history should continue from the projection after schema rollback: %+v %v", history, err)
		}
		apply("983l_request_metrics_daily_board.down.sql")
		apply("983l_request_metrics_daily_board.down.sql")
		apply("983k_request_metrics_daily_summary.down.sql")
		apply("983k_request_metrics_daily_summary.down.sql")
		apply("983j_request_metrics_973_repair.down.sql")
		apply("983j_request_metrics_973_repair.down.sql")
		var releasedProjectionIntact bool
		if err := pool.QueryRow(ctx, `SELECT
			to_regclass('public.request_metrics_pending') IS NOT NULL
			AND EXISTS (SELECT FROM public.schema_migrations WHERE version='973')
			AND EXISTS (SELECT FROM public.request_metrics WHERE fact_request_id='must-retry'
				AND total_tokens=13 AND pg_typeof(total_tokens)='bigint'::regtype AND source_fact_id IS NOT NULL)`).Scan(&releasedProjectionIntact); err != nil || !releasedProjectionIntact {
			t.Fatalf("repair rollback must preserve the released projection and widened measurements: %t %v", releasedProjectionIntact, err)
		}
		var requestID string
		var head time.Time
		if err := db.QueryRowContext(ctx, watermarkSQL).Scan(&head, &requestID); err != nil ||
			requestID != "r1" || !head.Equal(occurredAt) {
			t.Fatalf("new-slot publish or schema rollback changed the old-slot watermark: %q %v %v", requestID, head, err)
		}
		var retained int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.usage_facts`).Scan(&retained); err != nil || retained < 3 {
			t.Fatalf("schema rollback removed durable facts: %d %v", retained, err)
		}
	})
}
