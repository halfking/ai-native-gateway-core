package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/storage/sqlite"
)

// The fixture DDL intentionally targets public, the same schema as the report.
// Run only against an owned, empty reconciliation_fixture_* database.
func TestRequestReconciliationPostgres(t *testing.T) {
	dsn := os.Getenv("RECONCILIATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set RECONCILIATION_TEST_DSN to an isolated empty fixture database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("invalid fixture connection configuration")
	}
	defer pool.Close()
	var db string
	var publicTables int
	if err := pool.QueryRow(ctx, `SELECT current_database(), count(*) FROM information_schema.tables WHERE table_schema = 'public'`).Scan(&db, &publicTables); err != nil {
		t.Fatal("could not verify fixture isolation")
	}
	if !strings.HasPrefix(db, "reconciliation_fixture_") || publicTables != 0 {
		t.Fatalf("fixture must run in its own empty reconciliation_fixture_* database")
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(reportFixtureDDL)
	exec(legacyUsageFixtureRows)
	migration, err := os.ReadFile("../../sql/migrations/startup/982_provider_call_attempts.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.ReplaceAll(string(migration), `\set ON_ERROR_STOP on`, "")
	exec(up)
	exec(reportFixtureRows)
	mediaMigration, err := os.ReadFile("../../sql/migrations/startup/986_provider_attempt_media_usage.sql")
	if err != nil {
		t.Fatal(err)
	}
	mediaUp := strings.ReplaceAll(string(mediaMigration), `\set ON_ERROR_STOP on`, "")
	exec(mediaUp)
	exec(mediaUp)
	rateMigration, err := os.ReadFile("../../sql/migrations/startup/925_supplier_contract_rates.sql")
	if err != nil {
		t.Fatal(err)
	}
	ratesUp := strings.ReplaceAll(string(rateMigration), `\set ON_ERROR_STOP on`, "")
	exec(ratesUp)
	exec(ratesUp)
	// Applying 982 against an existing schema preserves existing data and the
	// column order of unrelated historical tables on both runs.
	exec(up)
	var rows, registrations int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM legacy_drift),
		(SELECT count(*) FROM schema_migrations WHERE version='982')`).Scan(&rows, &registrations); err != nil || rows != 1 || registrations != 1 {
		t.Fatalf("migration replay rows=%d registrations=%d err=%v", rows, registrations, err)
	}
	verifyAttemptRecorder(t, ctx, pool)
	linkMigration, err := os.ReadFile("../../sql/migrations/startup/985_request_reconciliation_links.sql")
	if err != nil {
		t.Fatal(err)
	}
	linksUp := strings.ReplaceAll(string(linkMigration), `\set ON_ERROR_STOP on`, "")
	exec(linksUp)
	exec(linksUp)

	platform := ReportScope{Platform: true}
	tenant := ReportScope{TenantID: "a"}
	query := ReportQuery{
		Start: time.Date(2026, 11, 22, 16, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 23, 16, 0, 0, 0, time.UTC),
		Day: "2026-11-23", Timezone: "Asia/Shanghai", Limit: 20,
	}
	read := func(scope ReportScope, section string) Report {
		t.Helper()
		q := query
		q.Section = section
		report, err := ReadReport(ctx, pool, scope, q)
		if err != nil {
			t.Fatalf("read %s: %v", section, err)
		}
		return report
	}
	requests := read(tenant, "requests")
	assertReportFields(t, requests.Summary, map[string]string{"requests": "2", "terminal": "2", "success": "1", "rate_limited": "1", "unassigned_session": "1", "total_tokens": "120", "business_requests": "2", "platform_requests": "0", "complete_measured_requests": "1", "requests_without_measured_tokens": "1"})
	if len(requests.Rows) != 2 {
		t.Fatalf("request details count = %d", len(requests.Rows))
	}
	assertReportFields(t, requests.Rows[0], map[string]string{"tenant_id": `"a"`, "request_id": `"r"`, "local_day": `"2026-11-23"`, "status": `"success"`})
	assertReportFields(t, requests.Rows[1], map[string]string{"request_id": `"limited"`, "input_tokens": "null", "output_tokens": "null"})
	assertReportFields(t, requests.Rows[0], map[string]string{"recorded_supplier_sends": "null"})
	platformRequests := read(ReportScope{TenantID: "a", Platform: true}, "requests")
	assertReportFields(t, platformRequests.Summary, map[string]string{"requests_with_no_recorded_sends": "1", "requests_with_multiple_recorded_sends": "1"})
	assertReportFields(t, platformRequests.Rows[0], map[string]string{"recorded_supplier_sends": "2"})
	allRequests := read(platform, "requests")
	assertReportFields(t, allRequests.Summary, map[string]string{"requests": "4", "platform_requests": "1", "business_requests": "3"})
	assertReportFields(t, allRequests.Rows[3], map[string]string{"traffic_class": `"platform"`, "client_id": `"platform-automation"`})
	attempts := read(platform, "attempts")
	assertReportFields(t, attempts.Summary, map[string]string{
		"recorded_attempts": "4", "sent": "3", "business_sent": "2", "platform_sent": "1",
		"unknown_delivery": "1", "input_tokens": "110", "comparable_cache_read_tokens": "40", "comparable_cache_input_tokens": "100",
		"attempts_with_estimate": "1", "attempts_with_reported_amount": "1",
	})
	assertReportFields(t, attempts.Rows[0], map[string]string{"estimated_amount": "null", "reported_amount": "null"})
	assertReportFields(t, attempts.Rows[1], map[string]string{"estimated_amount": `"0.012"`, "reported_amount": `"0.015"`})
	postings := read(tenant, "postings")
	assertReportFields(t, postings.Summary, map[string]string{"postings": "3", "linked_requests": "1", "net_credits": "12", "internal_amount": "null"})
	sessions := read(tenant, "sessions")
	assertReportFields(t, sessions.Summary, map[string]string{"active": "1", "new": "0", "closed": "0"})
	assertReportFields(t, sessions.Rows[0], map[string]string{"session_id": `"yesterday"`, "window_requests": "1", "active": "true", "new": "false"})
	query.Limit, query.Offset = 1, 1
	page := read(tenant, "requests")
	assertReportFields(t, page.Summary, map[string]string{"requests": "2"})
	if len(page.Rows) != 1 {
		t.Fatalf("unexpected page length %d", len(page.Rows))
	}
	assertReportFields(t, page.Rows[0], map[string]string{"request_id": `"limited"`})
	exec(`INSERT INTO request_wal_with_current_month (tenant_id, request_id, status, stage, created_at) VALUES
	       ('a','modality-limit','rate_limited',13,'2026-11-23 03:00Z'),
	       ('a','modality-stream','unknown',13,'2026-11-23 04:00Z'),
	       ('a','modality-disconnect','cancelled',13,'2026-11-23 05:00Z'),
	       ('ops','modality-platform','success',4,'2026-11-23 06:00Z');
	     INSERT INTO request_context_attrs (tenant_id, request_id, api_key_id, source_channel, origin_stage) VALUES
	       ('a','modality-limit',1,'reconciliation_modality',''),
	       ('a','modality-stream',1,'reconciliation_modality','self_check'),
	       ('a','modality-disconnect',1,'reconciliation_modality',''),
	       ('ops','modality-platform',3,'reconciliation_modality','self_check');`)
	query.Limit, query.Offset = 20, 0
	assertReportFields(t, read(tenant, "requests").Summary, map[string]string{
		"requests": "5", "terminal": "4", "rate_limited": "2", "cancelled": "1", "unknown_status": "1", "business_requests": "5",
	})
	assertReportFields(t, read(platform, "requests").Summary, map[string]string{"requests": "8", "platform_requests": "2"})
	assertReportFields(t, read(ReportScope{TenantID: "ops"}, "requests").Summary, map[string]string{"requests": "0"})
	exec(`INSERT INTO request_wal_with_current_month (tenant_id, request_id, status, stage, created_at, error) VALUES
	       ('default','unauthenticated','failure',13,'2026-11-23 07:00Z','gateway_admission:unassigned:missing_key'),
	       ('a','admission-limit','rate_limited',13,'2026-11-23 08:00Z','gateway_admission:tenant:key_concurrent_limit'),
	       ('ops','admission-platform','failure',13,'2026-11-23 09:00Z','gateway_admission:platform:prompt_too_large');`)
	assertReportFields(t, read(ReportScope{TenantID: "default"}, "requests").Summary, map[string]string{"requests": "0", "admission_rejected_requests": "0"})
	assertReportFields(t, read(ReportScope{TenantID: "default", Platform: true}, "requests").Summary, map[string]string{"requests": "0", "admission_rejected_requests": "0"})
	assertReportFields(t, read(ReportScope{TenantID: "ops"}, "requests").Summary, map[string]string{"requests": "0", "admission_rejected_requests": "0"})
	tenantRequests := read(tenant, "requests")
	assertReportFields(t, tenantRequests.Summary, map[string]string{"requests": "6", "rate_limited": "3", "business_requests": "6", "admission_rejected_requests": "1"})
	assertReportFields(t, tenantRequests.Rows[5], map[string]string{"admission_rejection": `"key_concurrent_limit"`, "session_id": "null", "input_tokens": "null"})
	allAdmissions := read(platform, "requests")
	assertReportFields(t, allAdmissions.Summary, map[string]string{"requests": "11", "platform_requests": "3", "unassigned_requests": "1", "admission_rejected_requests": "3"})
	assertReportFields(t, allAdmissions.Rows[8], map[string]string{"request_id": `"unauthenticated"`, "tenant_id": "null", "traffic_class": `"unknown"`, "recorded_supplier_sends": "0"})
	assertReportFields(t, allAdmissions.Rows[10], map[string]string{"client_id": `"platform-automation"`, "recorded_supplier_sends": "0"})
	assertReportFields(t, read(platform, "coverage").Summary, map[string]string{"received_requests": "11"})
	assertReportFields(t, read(ReportScope{TenantID: "default", Platform: true}, "coverage").Summary, map[string]string{"received_requests": "0"})
	t.Run("wal_receipt_and_replay", func(t *testing.T) { testWALReceiptAndReplay(t, ctx, pool) })
	t.Run("attempt_outbox_replay", func(t *testing.T) { testAttemptOutboxReplay(t, ctx, pool) })
	t.Run("invoice_evidence", func(t *testing.T) { testInvoiceEvidence(t, ctx, pool) })
	t.Run("supplier_contract_rates", func(t *testing.T) { testSupplierContractRates(t, ctx, pool) })
	t.Run("supplier_media_rates", func(t *testing.T) { testSupplierMediaRates(t, ctx, pool) })
	t.Run("monthly_coverage", func(t *testing.T) { testMonthlyCoverage(t, ctx, pool) })
	t.Run("internal_monthly", func(t *testing.T) { testInternalMonthly(t, ctx, pool) })
	t.Run("orchestration", func(t *testing.T) { testOrchestrationReport(t, ctx, pool) })
	t.Run("daily_shadow", func(t *testing.T) { testDailyShadow(t, ctx, pool) })
	t.Run("daily_statement_across_local_months", func(t *testing.T) { testDailyInvoiceAcrossLocalMonths(t, ctx, pool) })
	t.Run("internal_hourly_evidence", func(t *testing.T) { testInternalHourlyEvidence(t, ctx, pool) })
	t.Run("installed_schema", func(t *testing.T) { testReconciliationSchemaCheck(t, ctx, pool) })
	t.Run("projected_closed_day", func(t *testing.T) { testProjectedClosedDay(t, ctx, pool) })
}

func testProjectedClosedDay(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`
INSERT INTO request_wal_with_current_month (tenant_id, request_id, gw_session_id, status, stage, created_at) VALUES
 ('projected-a','same-request','projection-session','pending',0,'2026-11-22 16:00Z'),
 ('projected-a','moved','projection-session','success',17,'2026-11-22 15:59Z'),
 ('projected-a','missing',NULL,'failure',17,'2026-11-23 01:00Z'),
 ('projected-a','at-end',NULL,'success',17,'2026-11-23 16:00Z'),
 ('projected-b','same-request','projection-session','success',17,'2026-11-22 16:00Z');
INSERT INTO request_context_attrs (tenant_id,request_id,api_key_id) VALUES ('projected-a','missing',12);
INSERT INTO api_keys VALUES (12,'projected-a',false),(12,'projected-b',false);
INSERT INTO session_dim VALUES ('projected-a','projection-session','2026-11-22 16:00Z',NULL,'projected-client');
INSERT INTO usage_facts (tenant_id,request_id,status,prompt_tokens,completion_tokens,usage_source,revision,finalized_at,fact_id,api_key_id,traffic_class,occurred_at) VALUES
 ('projected-a','same-request','success',900,100,'llm',99,'2026-11-23 02:00Z',9001,12,'business','2026-11-22 16:00Z');
INSERT INTO credit_ledger_with_current_month VALUES ('projected-a',9002,'request','same-request','consume','paid','fixture-user',-2,'2026-11-23 03:00Z');`)
	exec(`CREATE TABLE public.session_turns_hot (request_id text, tenant_id text, ts timestamptz)`)
	for _, filename := range []string{"983_request_metrics.sql", "983b_add_raw_model_name.sql", "973_request_metrics_projection.sql", "983j_request_metrics_973_repair.sql"} {
		ddl, err := os.ReadFile("../../sql/migrations/startup/" + filename)
		if err != nil {
			t.Fatal(err)
		}
		exec(strings.ReplaceAll(string(ddl), `\set ON_ERROR_STOP on`, ""))
	}
	const tenantID = "projected-a"
	day := "2026-11-23"
	zone := "Asia/Shanghai"
	start, end, err := NaturalDay(day, zone)
	if err != nil {
		t.Fatal(err)
	}
	q := ReportQuery{Start: start, End: end, Day: day, Timezone: zone, Section: "requests", Limit: 20}
	if _, err := ReadProjectedReport(ctx, pool, ReportScope{TenantID: tenantID}, q); !errors.Is(err, ErrTerminalProjectionBackfill) {
		t.Fatalf("backfill must not report missing evidence: %v", err)
	}
	if _, err := ReadProjectedDailyShadow(ctx, pool, day, zone); !errors.Is(err, ErrTerminalProjectionBackfill) {
		t.Fatalf("shadow must wait for the same backfill: %v", err)
	}
	read := func(scope ReportScope, section string) Report {
		t.Helper()
		query := q
		query.Section = section
		report, err := ReadProjectedReport(ctx, pool, scope, query)
		if err != nil || report.Version != ProjectedReportVersion {
			t.Fatalf("read projected %s: %+v %v", section, report, err)
		}
		return report
	}
	postings := read(ReportScope{TenantID: tenantID}, "postings")
	if postings.Projection != nil {
		t.Fatalf("ledger postings should be independent of projection: %+v", postings)
	}
	assertReportFields(t, postings.Summary, map[string]string{
		"postings": "1", "net_credits": "2", "internal_amount": "null",
		"recorded_debit_entries": "1", "recorded_gross_debit_credits": "2",
		"recorded_return_entries": "0", "recorded_returned_credits": "null",
	})
	exec(`
INSERT INTO request_metrics (tenant_id,request_id,fact_request_id,status,prompt_tokens,completion_tokens,total_tokens,usage_source,source_revision,
 source_finalized_at,source_fact_id,api_key_id,traffic_class,occurred_at,occurred_date,billing_date)
SELECT tenant, CASE WHEN fact_id IS NULL THEN id ELSE chr(1)||jsonb_build_array(tenant,id)::text END,
 CASE WHEN fact_id IS NOT NULL THEN id END, status,input_tokens,output_tokens,total_tokens,source,revision,
 finalized_at::timestamptz,fact_id,key_id,traffic_class,occurred_at::timestamptz,occurred_date::date,billing_date::date
FROM (VALUES
 ('projected-a','same-request','success',7,13,20,'llm',1,'2026-11-22 16:10Z',9010,12,'business','2026-11-22 16:00Z','2026-11-22','2026-11-22'),
 ('projected-a','same-request','success',100,30,149,'corrected',3,'2026-11-23 16:10Z',9011,12,'business','2026-11-23 16:01Z','2026-11-23','2026-11-23'),
 ('projected-a','same-request','success',777,20,797,'llm',3,'2026-11-23 16:09Z',9012,12,'business','2026-11-23 16:02Z','2026-11-23','2026-11-23'),
 ('projected-a','same-request','success',555,20,575,'llm',4,'2026-11-23 16:12Z',NULL,12,'business','2026-11-23 16:03Z','2026-11-23','2026-11-23'),
 ('projected-a','moved','timeout',5,0,5,'estimated',1,'2026-11-22 16:15Z',9015,12,'business','2026-11-22 16:15Z','2026-11-22','2026-11-22'),
 ('projected-a','orphan','success',50,30,80,'llm',1,'2026-11-22 16:00Z',9016,12,'business','2026-11-22 16:00Z','2026-11-22','2026-11-22'),
 ('projected-a','at-end','success',6,6,12,'llm',1,'2026-11-23 16:00Z',9017,12,'business','2026-11-23 16:00Z','2026-11-23','2026-11-23'),
 ('projected-b','same-request','success',1234,567,1801,'llm',7,'2026-11-22 16:01Z',9018,12,'business','2026-11-22 16:00Z','2026-11-22','2026-11-22')
) rows(tenant,id,status,input_tokens,output_tokens,total_tokens,source,revision,
 finalized_at,fact_id,key_id,traffic_class,occurred_at,occurred_date,billing_date);
UPDATE request_metrics_repair_state SET backfill_complete=true,updated_at='2026-11-24 00:01Z';
INSERT INTO usage_facts (tenant_id,request_id,status,occurred_at,fact_id) VALUES
 ('projected-b','not-synced','success','2026-11-23 17:00Z',9019);`)
	requests := read(ReportScope{TenantID: tenantID}, "requests")
	if requests.Projection == nil || requests.Projection.Source != "request_metrics" || requests.Projection.PendingSince != nil {
		t.Fatalf("scope includes another tenant's projection backlog: %+v", requests.Projection)
	}
	assertReportFields(t, requests.Summary, map[string]string{"requests": "2", "terminal": "2", "success": "1", "failure": "1", "total_tokens": "149", "measured_total_requests": "1", "requests_with_terminal_projection": "1"})
	assertReportFields(t, requests.Rows[0], map[string]string{"status": `"success"`, "projected_status": `"success"`, "input_tokens": "100", "output_tokens": "30", "total_tokens": "149", "usage_source": `"corrected"`})
	assertReportFields(t, requests.Rows[1], map[string]string{"status": `"failure"`, "projected_status": "null", "total_tokens": "null", "input_tokens": "null"})
	assertReportFields(t, read(ReportScope{TenantID: tenantID}, "sessions").Summary, map[string]string{"sessions": "1", "active": "1"})
	coverage := read(ReportScope{TenantID: tenantID, Platform: true}, "coverage")
	assertReportFields(t, coverage.Summary, map[string]string{"received_requests": "2", "receipts_without_retained_terminal_usage": "1", "terminal_usage_requests": "3", "terminal_usage_without_retained_receipt": "1", "terminal_usage_receipt_outside_window": "1", "posted_entries": "1"})
	peer := read(ReportScope{TenantID: "projected-b"}, "requests")
	if peer.Projection.PendingSince == nil {
		t.Fatal("a next-day revision in the caller's backlog must be visible")
	}
	v1, err := ReadReport(ctx, pool, ReportScope{TenantID: tenantID}, q)
	if err != nil || v1.Version != ReportVersion || v1.Projection != nil {
		t.Fatalf("legacy report must stay independent: %+v %v", v1, err)
	}
	assertReportFields(t, v1.Summary, map[string]string{"total_tokens": "1000"})
	shadow, err := ReadProjectedDailyShadow(ctx, pool, day, zone)
	if err != nil || shadow.Version != ProjectedDailyShadowVersion || shadow.Projection == nil ||
		shadow.Projection.Source != "request_metrics" || shadow.Projection.PendingSince == nil ||
		!shadow.Start.Equal(start) || !shadow.End.Equal(end) || len(shadow.Summaries) != 6 {
		t.Fatalf("invalid projected shadow: %+v %v", shadow, err)
	}
	if shadow.SourceEvidence == nil || shadow.SourceEvidence.ProjectionCoverage.Current >= shadow.SourceEvidence.ProjectionCoverage.Expected {
		t.Fatalf("the source check must flag the stale usage revision even when projected rows exist: %+v", shadow.SourceEvidence)
	}
	for _, section := range []string{"requests", "attempts", "postings", "coverage"} {
		summary := read(ReportScope{Platform: true}, section).Summary
		if string(shadow.Summaries[section]) != string(summary) {
			t.Fatalf("shadow %s differs from the V2 section: %s, want %s", section, shadow.Summaries[section], summary)
		}
	}
	billDay, err := ReadProjectedDailyShadow(ctx, pool, "2026-11-01", zone)
	if err != nil || len(billDay.SupplierBills) != 4 {
		t.Fatalf("projected shadow omitted exact-day supplier bills: %+v %v", billDay, err)
	}
	if billDay.SourceEvidence == nil || billDay.SourceEvidence.InvoicesOverlapping <= int64(len(billDay.SupplierBills)) ||
		billDay.SourceEvidence.InvoiceLinesOverlapping == 0 {
		t.Fatalf("retained evidence must include the longer-period statement and its lines: %+v", billDay.SourceEvidence)
	}
	for _, bill := range billDay.SupplierBills {
		var summary struct {
			ReadAt time.Time `json:"read_at"`
		}
		if err := json.Unmarshal(bill.Summary, &summary); err != nil || !summary.ReadAt.Equal(billDay.ReadAt) {
			t.Fatalf("invoice must report the enclosing snapshot time: %s (%v)", bill.Summary, err)
		}
	}
	var openDay string
	if err := pool.QueryRow(ctx, `SELECT to_char(timezone($1, now()), 'YYYY-MM-DD')`, zone).Scan(&openDay); err != nil {
		t.Fatal(err)
	}
	openShadow, err := ReadProjectedDailyShadow(ctx, pool, openDay, zone)
	if err != nil || openShadow.WindowState != "open" || openShadow.Projection == nil {
		t.Fatalf("open day must expose provisional evidence: %+v %v", openShadow, err)
	}
	exec(`UPDATE api_keys SET is_system=true WHERE tenant_id='projected-a' AND id=12;
		UPDATE request_metrics_projection SET traffic_class='probe' WHERE tenant_id='projected-b' AND request_id='same-request';`)
	business := read(ReportScope{TenantID: tenantID, Platform: true}, "requests")
	assertReportFields(t, business.Rows[0], map[string]string{"traffic_class": `"business"`})
	probe := read(ReportScope{TenantID: "projected-b", Platform: true}, "requests")
	assertReportFields(t, probe.Rows[0], map[string]string{"traffic_class": `"probe"`})
	assertReportFields(t, probe.Summary, map[string]string{"business_requests": "0", "other_traffic_requests": "1"})

	exec(`INSERT INTO credit_ledger_with_current_month VALUES
		('projected-a',9003,'request','same-request','refund','paid','fixture-user',0,'2026-11-23 03:01Z'),
		('projected-a',9004,'request','same-request','reversal','paid','fixture-user',1,'2026-11-23 03:02Z'),
		('projected-a',9005,'request','same-request','adjust','paid','fixture-user',-4,'2026-11-23 03:03Z'),
		('projected-a',9006,'request','same-request','refund','paid','fixture-user',8,'2026-11-23 16:00Z'),
		('projected-b',9007,'request','same-request','refund','paid','fixture-user',0,'2026-11-23 03:01Z');`)
	assertReportFields(t, read(ReportScope{TenantID: tenantID}, "postings").Summary, map[string]string{
		"postings": "4", "net_credits": "5", "recorded_debit_entries": "1", "recorded_gross_debit_credits": "2",
		"recorded_return_entries": "2", "recorded_returned_credits": "1",
	})
	assertReportFields(t, read(ReportScope{TenantID: "projected-b"}, "postings").Summary, map[string]string{
		"recorded_debit_entries": "0", "recorded_gross_debit_credits": "null",
		"recorded_return_entries": "1", "recorded_returned_credits": "0",
	})
	empty, err := ReadProjectedDailyShadow(ctx, pool, "2000-01-01", "UTC")
	if err != nil || empty.SourceEvidence == nil ||
		empty.SourceEvidence.ProjectionCoverage.Fraction != nil || empty.SourceEvidence.ContractRatesOverlapping != 0 {
		t.Fatalf("an empty day must have unknown coverage and no retained rate versions: %+v %v", empty.SourceEvidence, err)
	}
	exec(`INSERT INTO supplier_contract_rates
		(rate_id,provider_id,credential_id,model,reference,currency,effective_from,effective_to,terms,content_sha256,imported_by)
		VALUES
		('10000000-0000-0000-0000-000000000001',9000,9000,'fixture','before','USD','1999-12-01 00:00Z','2000-01-01 00:00Z','{}',repeat('a',64),'fixture'),
		('10000000-0000-0000-0000-000000000002',9000,9000,'fixture','same-day','USD','2000-01-01 00:00Z','2000-01-02 00:00Z','{}',repeat('b',64),'fixture'),
		('10000000-0000-0000-0000-000000000003',9000,9000,'fixture','after','USD','2000-01-02 00:00Z',NULL,'{}',repeat('c',64),'fixture');`)
	rates, err := ReadProjectedDailyShadow(ctx, pool, "2000-01-01", "UTC")
	if err != nil || rates.SourceEvidence == nil || rates.SourceEvidence.ContractRatesOverlapping != 1 {
		t.Fatalf("only the rate version overlapping the half-open window is retained: %+v %v", rates.SourceEvidence, err)
	}
}

func testReconciliationSchemaCheck(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	script, err := os.ReadFile("../../scripts/checks/request-reconciliation-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	check := func(want bool) {
		t.Helper()
		var got bool
		if err := tx.QueryRow(ctx, string(script)).Scan(&got); err != nil || got != want {
			t.Fatalf("installation schema check = %t, want %t, err=%v", got, want, err)
		}
	}
	check(true)
	if _, err := tx.Exec(ctx, `ALTER TABLE public.provider_invoice_lines DROP COLUMN bucket_video_output_duration_us`); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func testDailyShadow(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	snapshot, err := ReadDailyShadow(ctx, pool, "2026-11-01", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != DailyShadowVersion || snapshot.ReadAt.IsZero() || snapshot.SourceEvidence != nil || len(snapshot.Summaries) != 6 ||
		snapshot.Start.Format(time.RFC3339) != "2026-10-31T16:00:00Z" || snapshot.End.Format(time.RFC3339) != "2026-11-01T16:00:00Z" {
		t.Fatalf("invalid daily shadow metadata: %+v", snapshot)
	}
	if len(snapshot.SupplierBills) != 4 {
		t.Fatalf("expected exact-day invoices only, got %v", snapshot.SupplierBills)
	}
	var foundMedia bool
	for _, report := range snapshot.SupplierBills {
		var invoice struct {
			InvoiceRef string `json:"invoice_ref"`
		}
		if err := json.Unmarshal(report.Invoice, &invoice); err != nil {
			t.Fatal(err)
		}
		if invoice.InvoiceRef == "daily-media-buckets" {
			foundMedia = true
			assertReportFields(t, report.Summary, map[string]string{"bucket_matched_lines": "2", "review_lines": "2"})
		}
	}
	if !foundMedia {
		t.Fatal("day snapshot omitted the media invoice")
	}
	assertReportFields(t, snapshot.Summaries["coverage"], map[string]string{"legacy_usage_request_keys": "2", "legacy_usage_rows": "3"})
	for _, check := range snapshot.CountChecks {
		if check.Report != check.Source || check.State == "different" {
			t.Fatalf("fixture sources disagree: %+v", check)
		}
	}
	internal, err := ReadDailyShadow(ctx, pool, "2026-11-10", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	var foundTenant bool
	for _, raw := range internal.InternalTenants {
		var row struct {
			TenantID string `json:"tenant_id"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		if row.TenantID == "internal-a" {
			foundTenant = true
			assertReportFields(t, raw, map[string]string{
				"bill_comparison": `"not_applicable"`, "bucket_credits": "14",
				"recorded_gross_debit_credits": "13", "bucket_minus_recorded_debit_credits": "1",
			})
		}
	}
	if !foundTenant || len(internal.SupplierBills) != 0 {
		t.Fatalf("unexpected internal day result: %+v", internal)
	}
	historical, err := ReadDailyShadow(ctx, pool, "2025-06-05", "UTC")
	if err != nil || len(historical.SupplierBills) != 0 {
		t.Fatalf("historical day %+v err=%v", historical, err)
	}
	assertReportFields(t, historical.Summaries["coverage"], map[string]string{
		"legacy_usage_request_keys": "1", "legacy_usage_without_retained_receipt": "1",
		"legacy_usage_without_retained_terminal_usage": "1", "recorded_sends": "0", "first_observed_send_day": "null",
	})
	assertReportFields(t, historical.Summaries["attempts"], map[string]string{"sent": "0", "estimates_by_traffic_and_currency": "[]"})
	empty, err := ReadDailyShadow(ctx, pool, "2000-01-01", "UTC")
	if err != nil || empty.WindowState != "elapsed" || len(empty.SupplierBills) != 0 || len(empty.InternalTenants) != 0 {
		t.Fatalf("empty day %+v err=%v", empty, err)
	}
	for _, check := range empty.CountChecks {
		if check.State != "no_observations" {
			t.Fatalf("empty evidence must be labelled: %+v", check)
		}
	}
}

func testOrchestrationReport(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	links := NewRequestLinks(pool)
	const tenant, parent = "orchestration-a", "orchestration-parent"
	const keyID = 17
	bodySHA := strings.Repeat("1", 64)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	register := func() string {
		t.Helper()
		token, err := links.Register(ctx, tenant, keyID, parent, bodySHA)
		check(err)
		return token
	}
	claim := func(token, claimTenant string, claimKey int, child, claimBodySHA string, want bool) {
		t.Helper()
		ok, err := NewRequestLinks(pool).Claim(ctx, token, claimTenant, claimKey, child, claimBodySHA)
		check(err)
		if ok != want {
			t.Fatalf("claim tenant=%q key=%d child=%q = %t, want %t", claimTenant, claimKey, child, ok, want)
		}
	}
	first, second, expired := register(), register(), register()
	claim(first, "other-tenant", keyID, "orchestration-child-1", bodySHA, false)
	claim(first, tenant, keyID+1, "orchestration-child-1", bodySHA, false)
	claim(first, tenant, keyID, "orchestration-child-1", strings.Repeat("2", 64), false)
	claim(first, tenant, keyID, parent, bodySHA, false)
	claim(first, tenant, keyID, "orchestration-child-1", bodySHA, true)
	claim(first, tenant, keyID, "replayed", bodySHA, false)
	claim(second, tenant, keyID, "orchestration-child-2", bodySHA, true)
	_, err := pool.Exec(ctx, `UPDATE public.request_reconciliation_links
		SET registered_at = clock_timestamp() - interval '1 day' WHERE tenant_id = $1 AND child_request_id IS NULL`, tenant)
	check(err)
	claim(expired, tenant, keyID, "expired", bodySHA, false)
	_, err = pool.Exec(ctx, `UPDATE public.request_reconciliation_links
		SET registered_at = '2026-11-24 15:59:59Z' WHERE tenant_id = $1`, tenant)
	check(err)

	_, err = pool.Exec(ctx, `INSERT INTO request_wal_with_current_month (tenant_id, request_id, gw_session_id, status, stage, created_at) VALUES
		('orchestration-a','orchestration-parent',NULL,'success',15,'2026-11-24 15:59:58Z'),
		('orchestration-a','orchestration-child-1','orchestration-only','success',15,'2026-11-25 00:00Z'),
		('orchestration-a','orchestration-child-2','orchestration-shared','success',15,'2026-11-25 00:01Z'),
		('orchestration-a','independent-client','orchestration-shared','success',15,'2026-11-25 00:02Z'),
		('orchestration-b','orchestration-child-1',NULL,'success',15,'2026-11-25 00:03Z');
		INSERT INTO session_dim (tenant_id, gw_session_id, first_request_at) VALUES
		('orchestration-a','orchestration-only','2026-11-25 00:00Z'),
		('orchestration-a','orchestration-shared','2026-11-25 00:01Z');
		INSERT INTO usage_facts (tenant_id, request_id, status, occurred_at) VALUES
		('orchestration-a','orchestration-child-1','success','2026-11-25 00:01Z'),
		('orchestration-a','orchestration-child-2','success','2026-11-25 00:02Z');`)
	check(err)
	_, err = pool.Exec(ctx, `INSERT INTO public.provider_call_attempts
		(attempt_id,tenant_id,request_id,traffic_class,source,attempt_no,prepared_at,sent_at,delivery,outcome) VALUES
		('orchestration-attempt-1','orchestration-a','orchestration-child-1','business','fixture',1,'2026-11-25 00:00Z','2026-11-25 00:00Z','sent','success'),
		('orchestration-attempt-2','orchestration-a','orchestration-child-2','business','fixture',1,'2026-11-25 00:01Z','2026-11-25 00:01Z','sent','success');`)
	check(err)
	_, err = pool.Exec(ctx, `INSERT INTO credit_ledger_with_current_month
		(id,tenant_id,ref_id,ref_type,entry_type,pool,amount,created_at) VALUES
		(701,'orchestration-a','orchestration-child-1','request','consume','test',-2,'2026-11-25 00:01Z'),
		(702,'orchestration-a','orchestration-child-2','request','consume','test',-3,'2026-11-25 00:02Z');`)
	check(err)
	read := func(scope ReportScope, section, day string) Report {
		t.Helper()
		start, end, err := NaturalDay(day, "Asia/Shanghai")
		check(err)
		res, err := ReadReport(ctx, pool, scope, ReportQuery{
			Section: section, Day: day, Timezone: "Asia/Shanghai", Start: start, End: end, Limit: 10,
		})
		check(err)
		return res
	}
	scope, platform := ReportScope{TenantID: tenant}, ReportScope{TenantID: tenant, Platform: true}
	firstDay, secondDay := "2026-11-24", "2026-11-25"
	clientDay := read(platform, "requests", firstDay)
	assertReportFields(t, clientDay.Summary, map[string]string{"requests": "1"})
	assertReportFields(t, clientDay.Rows[0], map[string]string{"request_id": `"orchestration-parent"`, "recorded_supplier_sends": "2"})
	assertReportFields(t, read(scope, "requests", secondDay).Summary, map[string]string{"requests": "1"})
	assertReportFields(t, read(ReportScope{TenantID: "orchestration-b"}, "requests", secondDay).Summary, map[string]string{"requests": "1"})
	assertReportFields(t, read(scope, "sessions", secondDay).Summary, map[string]string{"sessions": "1", "active": "1", "new": "1"})
	assertReportFields(t, read(platform, "attempts", firstDay).Summary, map[string]string{"sent": "0"})
	attempts := read(platform, "attempts", secondDay)
	assertReportFields(t, attempts.Summary, map[string]string{"sent": "2"})
	assertReportFields(t, attempts.Rows[0], map[string]string{"parent_request_id": `"orchestration-parent"`, "request_id": `"orchestration-child-1"`})
	postings := read(scope, "postings", secondDay)
	assertReportFields(t, postings.Summary, map[string]string{"postings": "2", "linked_requests": "2", "linked_client_requests": "1", "net_credits": "5"})
	assertReportFields(t, postings.Rows[0], map[string]string{"parent_request_id": `"orchestration-parent"`, "request_id": `"orchestration-child-1"`})
	assertReportFields(t, read(platform, "coverage", firstDay).Summary, map[string]string{
		"received_requests": "1", "orchestration_child_receipts": "0", "orchestration_dispatches_registered": "3", "orchestration_dispatches_unlinked": "1",
	})
	assertReportFields(t, read(platform, "coverage", secondDay).Summary, map[string]string{
		"received_requests": "1", "orchestration_child_receipts": "2", "orchestration_dispatches_registered": "0", "postings_without_retained_receipt": "0",
		"terminal_usage_requests": "2", "terminal_usage_without_retained_receipt": "0",
	})
}

func testMonthlyCoverage(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	if _, err := pool.Exec(ctx, coverageFixtureRows); err != nil {
		t.Fatal(err)
	}
	read := func(month string, limit, offset int) Report {
		t.Helper()
		start, end, err := NaturalMonth(month, "Asia/Shanghai")
		if err != nil {
			t.Fatal(err)
		}
		query := ReportQuery{Start: start, End: end, Month: month, Timezone: "Asia/Shanghai", Section: "coverage", Limit: limit, Offset: offset}
		if _, err := ReadReport(ctx, pool, ReportScope{TenantID: "coverage-a"}, query); err == nil {
			t.Fatal("tenant caller can read coverage evidence")
		}
		report, err := ReadReport(ctx, pool, ReportScope{TenantID: "coverage-a", Platform: true}, query)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	got := read("2026-11", 20, 0)
	if len(got.Rows) != 2 || got.Coverage != "retained_evidence_only_historical_supplier_population_unknown" {
		t.Fatalf("unexpected day count or coverage descriptor: %+v", got)
	}
	assertReportFields(t, got.Summary, map[string]string{
		"received_requests": "2", "receipts_without_retained_terminal_usage": "1",
		"terminal_usage_requests": "3", "terminal_usage_without_retained_receipt": "1", "terminal_usage_receipt_outside_window": "1",
		"legacy_usage_request_keys": "6", "legacy_usage_rows": "7", "legacy_usage_without_retained_receipt": "2",
		"legacy_usage_receipt_other_day": "2", "legacy_usage_without_retained_terminal_usage": "2",
		"first_observed_legacy_usage_day": `"2026-11-01"`, "last_observed_legacy_usage_day": `"2026-11-02"`,
		"posted_entries": "3", "postings_without_retained_receipt": "1", "postings_receipt_outside_window": "1",
		"recorded_sends": "3", "recorded_sends_without_estimate": "2", "recorded_sends_without_reported_amount": "1",
		"recorded_sends_reported_amount_without_currency": "1", "recorded_sends_without_provider_request_id": "1",
		"terminal_usage_with_retained_receipt_fraction": "0.666667", "recorded_sends_with_estimate_fraction": "0.333333",
		"recorded_sends_with_reported_amount_and_currency_fraction": "0.333333",
		"first_observed_receipt_day":                                `"2026-11-01"`, "last_observed_send_day": `"2026-11-02"`,
	})
	assertReportFields(t, got.Rows[0], map[string]string{
		"local_day": `"2026-11-01"`, "received_requests": "1", "recorded_sends": "2",
		"legacy_usage_request_keys": "2", "legacy_usage_rows": "3", "legacy_usage_receipt_other_day": "1",
	})
	assertReportFields(t, got.Rows[1], map[string]string{
		"local_day": `"2026-11-02"`, "terminal_usage_without_retained_receipt": "1", "posted_entries": "2",
		"legacy_usage_request_keys": "4", "legacy_usage_rows": "4", "legacy_usage_without_retained_receipt": "2",
	})
	page := read("2026-11", 1, 1)
	if len(page.Rows) != 1 || string(page.Rows[0]) != string(got.Rows[1]) || string(page.Summary) != string(got.Summary) {
		t.Fatalf("coverage pagination changed the cohort or totals: %+v", page)
	}
	empty := read("2026-09", 20, 0)
	if len(empty.Rows) != 0 {
		t.Fatalf("unexpected evidence in an empty month: %s", empty.Rows)
	}
	assertReportFields(t, empty.Summary, map[string]string{
		"received_requests": "0", "terminal_usage_requests": "0", "recorded_sends": "0", "posted_entries": "0",
		"terminal_usage_with_retained_receipt_fraction": "null", "recorded_sends_with_estimate_fraction": "null",
		"recorded_sends_with_reported_amount_and_currency_fraction": "null",
		"first_observed_send_day":                                   "null", "last_observed_receipt_day": "null",
		"legacy_usage_request_keys": "0", "first_observed_legacy_usage_day": "null",
	})
	start, end, err := NaturalDay("2026-11-01", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	daily, err := ReadReport(ctx, pool, ReportScope{Platform: true, TenantID: "coverage-a"}, ReportQuery{
		Start: start, End: end, Day: "2026-11-01", Timezone: "Asia/Shanghai", Section: "coverage", Limit: 20,
	})
	if err != nil || daily.Day != "2026-11-01" || daily.Month != "" || len(daily.Rows) != 1 {
		t.Fatalf("invalid daily coverage: %+v, %v", daily, err)
	}
	assertReportFields(t, daily.Summary, map[string]string{
		"received_requests": "1", "terminal_usage_requests": "2", "terminal_usage_receipt_outside_window": "1",
		"legacy_usage_request_keys": "2", "legacy_usage_without_retained_receipt": "0",
		"legacy_usage_receipt_other_day": "1", "legacy_usage_without_retained_terminal_usage": "0",
		"terminal_usage_without_retained_receipt": "0", "recorded_sends": "2", "posted_entries": "1",
	})
}

func testInternalMonthly(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	for _, name := range []string{
		"objects/functions/get_current_tenant.sql",
		"migrations/startup/389_maas_credit_consumption_buckets.sql",
		"migrations/startup/913_maas_monthly_bills_coupons.sql",
	} {
		migration, err := os.ReadFile("../../sql/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, internalFixtureRows); err != nil {
		t.Fatal(err)
	}
	read := func(tenant, zone string) Report {
		t.Helper()
		month := "2026-11"
		start, end, err := NaturalMonth(month, zone)
		if err != nil {
			t.Fatal(err)
		}
		report, err := ReadReport(ctx, pool, ReportScope{TenantID: tenant}, ReportQuery{
			Start: start, End: end, Month: month, Timezone: zone, Section: "internal", Limit: 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Rows) != 1 {
			t.Fatalf("expected exactly one tenant row, got %s", report.Rows)
		}
		return report
	}
	utc := read("internal-a", "UTC")
	assertReportFields(t, utc.Rows[0], map[string]string{
		"tenant_id": `"internal-a"`, "request_postings": "4", "recorded_debited_requests": "2",
		"recorded_gross_debit_credits": "17", "recorded_net_credits": "16", "bucket_credits": "18",
		"bill_status": `"paid"`, "bill_comparison": `"comparable"`, "bill_amount_cents": `"9007199254740993"`, "bill_currency": `"CNY"`,
		"bucket_minus_recorded_debit_credits": "1", "bucket_minus_bill_credits": "1",
	})
	assertReportFields(t, utc.Summary, map[string]string{
		"tenants_with_evidence": "1", "tenants_with_bucket_debit_difference": "1", "tenants_with_bucket_bill_difference": "1",
	})
	shanghai := read("internal-a", "Asia/Shanghai")
	assertReportFields(t, shanghai.Rows[0], map[string]string{
		"recorded_gross_debit_credits": "13", "recorded_net_credits": "12", "bucket_credits": "14",
		"bill_status": `"paid"`, "bill_comparison": `"window_mismatch"`, "bucket_minus_bill_credits": "null",
	})
	assertReportFields(t, shanghai.Summary, map[string]string{"tenants_with_bill_window_mismatch": "1", "tenants_with_bucket_bill_comparison": "0"})
	open := read("internal-open", "UTC")
	assertReportFields(t, open.Rows[0], map[string]string{
		"bill_comparison": `"open"`, "bucket_minus_bill_credits": "null", "bucket_minus_recorded_debit_credits": "null",
	})
	boundary := read("internal-boundary", "Asia/Kolkata")
	assertReportFields(t, boundary.Rows[0], map[string]string{
		"consumption_buckets": "1", "boundary_buckets": "2", "bucket_credits": "3",
		"bill_comparison": `"missing"`, "bucket_minus_bill_credits": "null", "bucket_minus_recorded_debit_credits": "null",
	})
	start, end, err := NaturalDay("2026-11-10", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	daily, err := ReadReport(ctx, pool, ReportScope{TenantID: "internal-a"}, ReportQuery{
		Start: start, End: end, Day: "2026-11-10", Timezone: "Asia/Shanghai", Section: "internal", Limit: 20,
	})
	if err != nil || len(daily.Rows) != 1 {
		t.Fatalf("invalid internal daily report: %+v, %v", daily, err)
	}
	assertReportFields(t, daily.Rows[0], map[string]string{
		"recorded_gross_debit_credits": "13", "recorded_net_credits": "12", "bucket_credits": "14",
		"bucket_minus_recorded_debit_credits": "1", "bill_comparison": `"not_applicable"`, "bill_amount_cents": "null", "bucket_minus_bill_credits": "null",
	})
}

func testInternalHourlyEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `
INSERT INTO tenants (code) VALUES ('hour-offsets'), ('hour-gaps'), ('hour-unaligned');
INSERT INTO credit_ledger_with_current_month VALUES
 ('hour-offsets',1,'request','first','consume','granted','alice',-7,'2026-12-09 01:05Z'),
 ('hour-offsets',2,'request','second','consume','granted','alice',-13,'2026-12-09 02:05Z'),
 ('hour-offsets',3,'request','second','refund','granted','alice',3,'2026-12-09 03:05Z'),
 ('hour-gaps',1,'request','missing-bucket','consume','granted','alice',-6,'2026-12-09 01:05Z'),
 ('hour-unaligned',1,'request','first-overlap','consume','granted','alice',-5,'2026-12-09 01:45Z'),
 ('hour-unaligned',2,'request','second-overlap','consume','granted','alice',-4,'2026-12-09 02:15Z');
INSERT INTO maas_credit_consumption_buckets (tenant_id,bucket_start,credits,request_count) VALUES
 ('hour-offsets','2026-12-09 01:00Z',10,1), ('hour-offsets','2026-12-09 02:00Z',10,1),
 ('hour-gaps','2026-12-09 02:00Z',6,1),
 ('hour-unaligned','2026-12-09 01:00Z',7,1), ('hour-unaligned','2026-12-09 01:30Z',99,1),
 ('hour-unaligned','2026-12-09 02:00Z',8,1);`)
	if err != nil {
		t.Fatal(err)
	}
	const day = "2026-12-09"
	start, end, err := NaturalDay(day, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	assertTenant := func(tenant string, want map[string]string) {
		t.Helper()
		report, err := ReadReport(ctx, pool, ReportScope{TenantID: tenant}, ReportQuery{
			Start: start, End: end, Day: day, Timezone: "UTC", Section: "internal", Limit: 20,
		})
		if err != nil || len(report.Rows) != 1 {
			t.Fatalf("%s: invalid internal daily report: %+v, %v", tenant, report, err)
		}
		assertReportFields(t, report.Rows[0], want)
	}
	assertTenant("hour-offsets", map[string]string{
		"recorded_gross_debit_credits": "20", "recorded_net_credits": "17", "bucket_credits": "20",
		"bucket_minus_recorded_debit_credits": "0", "hours_with_bucket_debit_difference": "2",
		"ledger_hours_without_bucket": "0", "bucket_hours_without_debits": "0",
	})
	assertTenant("hour-gaps", map[string]string{
		"recorded_gross_debit_credits": "6", "bucket_credits": "6", "bucket_minus_recorded_debit_credits": "null",
		"ledger_hours_without_bucket": "1", "bucket_hours_without_debits": "1", "hours_with_bucket_debit_difference": "0",
	})
	assertTenant("hour-unaligned", map[string]string{
		"bucket_credits": "15", "consumption_buckets": "2", "unaligned_hour_buckets": "1",
		"bucket_minus_recorded_debit_credits": "null", "hours_with_bucket_debit_difference": "0",
	})
	shadow, err := ReadDailyShadow(ctx, pool, day, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	assertReportFields(t, shadow.Summaries["internal"], map[string]string{
		"tenants_with_bucket_debit_comparison": "1",
		"tenants_with_bucket_debit_difference": "1",
		"tenants_with_hourly_bucket_gaps":      "2",
	})
}

func testAttemptOutboxReplay(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	requireAttemptJournal(t)
	store := NewAttemptRecorder(pool)
	interrupted, interrupt := context.WithCancel(ctx)
	defer interrupt()
	path := filepath.Join(t.TempDir(), "attempts.db")
	outbox, err := openAttemptOutbox(ctx, path, sqlite.OpenJournal, func(_ context.Context, event attemptEvent) error {
		// Simulate a committed PostgreSQL write followed by process interruption
		// before the outbox has acknowledged the SQLite record.
		err := store.applyEvent(ctx, event)
		interrupt()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	a := NewAttempt(Attempt{AttemptNo: 1, TrafficClass: "platform", ClientID: "platform-automation", Source: "outbox-fixture", PreparedAt: when.Add(-time.Second)})
	for _, err := range []error{
		outbox.Prepare(ctx, a), outbox.MarkDispatched(ctx, a.ID), outbox.MarkSent(ctx, a.ID, when),
		outbox.RecordHTTP(ctx, a.ID, 200, "invoice-reference", when),
		outbox.append(ctx, attemptEvent{Kind: "finish", ID: a.ID, At: when.Add(time.Second), Outcome: "success"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := outbox.drain(interrupted); err == nil {
		t.Fatal("interrupted projection was acknowledged")
	}
	if err := outbox.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := openAttemptOutbox(ctx, path, sqlite.OpenJournal, store.applyEvent)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if count, err := recovered.drain(ctx); count != 5 || err != nil {
		t.Fatalf("outbox recovery count=%d err=%v", count, err)
	}
	var count int
	var sent, finished time.Time
	var outcome, providerRequestID string
	if err := pool.QueryRow(ctx, `SELECT count(*) OVER (), sent_at, finished_at, outcome, provider_request_id
		FROM provider_call_attempts WHERE attempt_id=$1`, a.ID).Scan(&count, &sent, &finished, &outcome, &providerRequestID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !sent.Equal(when) || !finished.Equal(when.Add(time.Second)) || outcome != "success" || providerRequestID != "invoice-reference" {
		t.Fatalf("replay changed attempt: %d %s %s %s %s", count, sent, finished, outcome, providerRequestID)
	}
	notSent := NewAttempt(Attempt{AttemptNo: 1, TrafficClass: "unknown", Source: "outbox-fixture", PreparedAt: when})
	if err := recovered.Prepare(ctx, notSent); err != nil {
		t.Fatal(err)
	}
	if err := recovered.MarkDispatched(ctx, notSent.ID); err != nil {
		t.Fatal(err)
	}
	if err := RecordTransportFailure(ctx, recovered, notSent.ID, &net.DNSError{Err: "fixture DNS failure"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if count, err := recovered.drain(ctx); count != 4 || err != nil {
		t.Fatalf("unsent evidence recovery count=%d err=%v", count, err)
	}
	var delivery string
	var hasSentTime bool
	if err := pool.QueryRow(ctx, `SELECT delivery, sent_at IS NOT NULL FROM provider_call_attempts WHERE attempt_id=$1`, notSent.ID).Scan(&delivery, &hasSentTime); err != nil {
		t.Fatal(err)
	}
	if delivery != "not_sent" || hasSentTime {
		t.Fatalf("DNS failure delivery=%q, has sent time=%t", delivery, hasSentTime)
	}
	for _, month := range []string{"2026-10", "2026-11"} {
		start, end, err := NaturalMonth(month, "Asia/Shanghai")
		if err != nil {
			t.Fatal(err)
		}
		report, err := ReadReport(ctx, pool, ReportScope{Platform: true}, ReportQuery{
			Start: start, End: end, Month: month, Timezone: "Asia/Shanghai", Section: "attempts", Limit: 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range report.Rows {
			var record struct {
				ID string `json:"attempt_id"`
			}
			if err := json.Unmarshal(row, &record); err != nil {
				t.Fatal(err)
			}
			if record.ID == a.ID {
				found = true
				assertReportFields(t, row, map[string]string{"local_day": `"2026-11-01"`})
			}
		}
		if found != (month == "2026-11") {
			t.Errorf("attempt presence in month %s = %t", month, found)
		}
	}
}

func testWALReceiptAndReplay(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	for _, file := range []string{"../../sql/objects/tables/request_wal_hot.sql", "../../sql/objects/indexes/udx_request_wal_hot_request_id.sql"} {
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	logger := telemetry.NewRequestLogger(pool, nil)
	defer logger.Stop()
	oldMonth := time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC)
	newMonth := oldMonth.Add(time.Second)
	update := telemetry.LogUpdate{
		RequestID: "modality-replay", Status: telemetry.StatusRateLimited, Stage: telemetry.StageResponseFail,
		Error: "key_throttled", CompletedAt: newMonth, RequireInitial: true,
	}
	if err := logger.UpdateSync(ctx, &update); err == nil {
		t.Fatal("terminal update before receipt must remain replayable")
	}
	if err := logger.CreateInitial(ctx, &telemetry.InitialRequest{RequestID: update.RequestID, TenantID: "a", ReceivedAt: newMonth}); err != nil {
		t.Fatal(err)
	}
	if err := logger.UpdateSync(ctx, &update); err != nil {
		t.Fatal(err)
	}
	// Replay an old-format receipt after the authenticated newer write and the
	// terminal update. The timestamp of the original receipt wins.
	record := dbdegradation.BackupRecord{
		Type: "request_wal", RecordKey: update.RequestID + ":initial", Timestamp: oldMonth,
		Payload: json.RawMessage(`{"RequestID":"modality-replay","TenantID":"default","Provisional":true}`),
	}
	if err := logger.ReplayFallback(ctx, record); err != nil {
		t.Fatal(err)
	}
	update.Status = telemetry.StatusSuccess
	if err := logger.UpdateSync(ctx, &update); err != nil {
		t.Fatal(err)
	}
	var tenant, status, month string
	var received time.Time
	if err := pool.QueryRow(ctx, `SELECT tenant_id, status, created_at, to_char(timezone('Asia/Shanghai', created_at), 'YYYY-MM')
		FROM request_wal_hot WHERE request_id=$1`, update.RequestID).Scan(&tenant, &status, &received, &month); err != nil {
		t.Fatal(err)
	}
	if tenant != "a" || status != telemetry.StatusRateLimited || !received.Equal(oldMonth) || month != "2026-10" {
		t.Fatalf("replay changed tenant/status/receipt/month: %s %s %s %s", tenant, status, received, month)
	}
	admission := telemetry.InitialRequest{
		RequestID: "admission-replay", TenantID: "default", ReceivedAt: oldMonth, Provisional: true,
		Rejection: &telemetry.AdmissionRejection{
			Status: telemetry.StatusFailure, Reason: "gateway_admission:unassigned:missing_key", CompletedAt: newMonth,
		},
	}
	encoded, err := json.Marshal(admission)
	if err != nil {
		t.Fatal(err)
	}
	record = dbdegradation.BackupRecord{
		Type: "request_wal", RecordKey: admission.RequestID + ":initial", Timestamp: newMonth, Payload: encoded,
	}
	for range 2 {
		if err := logger.ReplayFallback(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	var completed time.Time
	var reason string
	if err := pool.QueryRow(ctx, `SELECT status, error, created_at, completed_at FROM request_wal_hot WHERE request_id = $1`,
		admission.RequestID).Scan(&status, &reason, &received, &completed); err != nil {
		t.Fatal(err)
	}
	if status != telemetry.StatusFailure || reason != admission.Rejection.Reason || !received.Equal(oldMonth) || !completed.Equal(newMonth) {
		t.Fatalf("admission replay changed terminal evidence: %s %s %s %s", status, reason, received, completed)
	}
}

func TestRequestReconciliationInstallerSchema(t *testing.T) {
	dsn := os.Getenv("RECONCILIATION_INSTALLED_TEST_DSN")
	if dsn == "" {
		t.Skip("set RECONCILIATION_INSTALLED_TEST_DSN to an isolated installer fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("invalid fixture connection configuration")
	}
	defer pool.Close()
	var db string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&db); err != nil || !strings.HasPrefix(db, "reconciliation_") || !strings.HasSuffix(db, "_test") {
		t.Fatal("could not verify installed fixture isolation")
	}
	start := time.Date(2026, 11, 22, 16, 0, 0, 0, time.UTC)
	for _, section := range []string{"requests", "attempts", "postings", "sessions", "coverage", "internal"} {
		t.Run(section, func(t *testing.T) {
			scope := ReportScope{Platform: true}
			if section == "internal" {
				scope.TenantID = "reconciliation-install-schema-check"
			}
			query := ReportQuery{
				Start: start, End: start.Add(24 * time.Hour), Timezone: "Asia/Shanghai", Section: section, Limit: 20,
			}
			if ReportRequiresCalendarPeriod(section) {
				query.Day = "2026-11-23"
				query.Start, query.End, _ = NaturalDay(query.Day, query.Timezone)
			}
			got, err := ReadReport(ctx, pool, scope, query)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Rows) != 0 || got.Summary == nil || got.ReadAt.IsZero() {
				t.Fatalf("unexpected report in fresh fixture: %+v", got)
			}
		})
	}
}

func verifyAttemptRecorder(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	store := NewAttemptRecorder(pool)
	a := NewAttempt(Attempt{
		Source: "fixture", TrafficClass: "platform", ClientID: "platform-automation", AttemptNo: 1,
		PreparedAt: time.Date(2026, 11, 21, 1, 0, 0, 0, time.UTC),
	})
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(store.Prepare(ctx, a))
	check(store.MarkDispatched(ctx, a.ID))
	sentAt := a.PreparedAt.Add(time.Minute)
	check(store.RecordHTTP(ctx, a.ID, 200, "upstream-id", sentAt))
	check(store.Prepare(ctx, a))
	check(store.MarkDispatched(ctx, a.ID))
	zero, amount := int64(0), "0.015000000001"
	check(store.RecordUsage(ctx, a.ID, UsageEvidence{Source: "fixture", InputTokens: &zero,
		ReportedAmount: &amount, ReportedSemantics: "unknown"}))
	check(store.Finish(ctx, a.ID, "success"))
	var savedAt time.Time
	var input, output *int64
	var delivery, recordedAmount string
	check(pool.QueryRow(ctx, `SELECT delivery, sent_at, input_tokens, output_tokens, reported_amount::text
		FROM public.provider_call_attempts WHERE attempt_id=$1`, a.ID).Scan(&delivery, &savedAt, &input, &output, &recordedAmount))
	if delivery != "sent" || !savedAt.Equal(sentAt) || input == nil || *input != 0 || output != nil || recordedAmount != amount {
		t.Fatalf("recorded delivery=%s, sent_at=%s, input=%v, output=%v, reported=%s", delivery, savedAt, input, output, recordedAmount)
	}
}

func assertReportFields(t *testing.T, raw json.RawMessage, want map[string]string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range want {
		if got := string(fields[key]); got != value {
			t.Errorf("%s = %s, want %s in %s", key, got, value, raw)
		}
	}
}

const reportFixtureDDL = `
CREATE TABLE schema_migrations (version text PRIMARY KEY, description text);
CREATE TABLE tenants (code varchar(64) PRIMARY KEY);
CREATE TABLE legacy_drift (qty integer, note text);
INSERT INTO legacy_drift VALUES (1, 'retained');
CREATE TABLE candidate_failure_logs (id bigint, error_detail text, status_code integer);
CREATE TABLE candidate_failure_logs_hot (id bigint, status_code integer, error_detail text);
CREATE TABLE request_wal_with_current_month (
    tenant_id text, request_id text, gw_session_id text, status text, stage int, completed_at timestamptz, created_at timestamptz, error text);
CREATE TABLE request_context_attrs (
    tenant_id text, request_id text PRIMARY KEY, gw_session_id text, virtual_client_id text, application_id bigint,
    ts timestamptz, api_key_id bigint, source_channel text, origin_stage text);
CREATE TABLE api_keys (id bigint, tenant_id text, is_system boolean);
CREATE TABLE session_dim (
    tenant_id text, gw_session_id text, first_request_at timestamptz, closed_at timestamptz, client_id text);
CREATE TABLE usage_facts (
    tenant_id text, request_id text, status text, prompt_tokens bigint, completion_tokens bigint, usage_source text,
    revision int, finalized_at timestamptz, fact_id bigint, api_key_id bigint, traffic_class text, occurred_at timestamptz);
CREATE TABLE usage_ledger (request_id text NOT NULL, ts timestamptz NOT NULL, tenant_id text NOT NULL) PARTITION BY RANGE (ts);
CREATE TABLE usage_ledger_old_fixture PARTITION OF usage_ledger FOR VALUES FROM ('2025-06-01 00:00Z') TO ('2025-07-01 00:00Z');
CREATE TABLE usage_ledger_oct PARTITION OF usage_ledger FOR VALUES FROM ('2026-10-01 00:00Z') TO ('2026-11-01 00:00Z');
CREATE TABLE usage_ledger_nov PARTITION OF usage_ledger FOR VALUES FROM ('2026-11-01 00:00Z') TO ('2026-12-01 00:00Z');
CREATE TABLE usage_ledger_hot (LIKE usage_ledger);
CREATE VIEW usage_ledger_with_current_month AS SELECT * FROM usage_ledger_hot UNION ALL SELECT * FROM usage_ledger;
CREATE TABLE credit_ledger_with_current_month (
    tenant_id text, id bigint, ref_type text, ref_id text, entry_type text, pool text, owner_user text, amount bigint, created_at timestamptz);`

const legacyUsageFixtureRows = `
INSERT INTO usage_ledger_hot VALUES
 ('both','2026-10-31 16:04Z','coverage-a'), ('prior','2026-10-31 16:00Z','coverage-a'),
 ('legacy-only','2026-11-01 16:07Z','coverage-a'), ('missing','2026-11-01 16:04Z','coverage-a'),
 ('receipt-only','2026-11-01 16:04Z','coverage-a'), ('missing','2026-11-01 16:04Z','coverage-b'),
 ('','2026-11-01 16:00Z','coverage-a'), ('next-month','2026-11-30 16:00Z','coverage-a');
INSERT INTO usage_ledger VALUES
 ('historical-only','2025-06-05 00:00Z','coverage-a'),
 ('both','2026-10-31 16:04Z','coverage-a'), ('both','2026-11-01 16:05Z','coverage-a');`

const reportFixtureRows = `
INSERT INTO request_wal_with_current_month (tenant_id, request_id, gw_session_id, status, stage, completed_at, created_at) VALUES
 ('a','r','yesterday','pending',0,NULL,'2026-11-22 16:00Z'),
 ('a','r','yesterday','success',17,'2026-11-22 16:02Z','2026-11-22 16:00Z'),
 ('a','limited','early-provisional','failure',13,'2026-11-23 01:00Z','2026-11-23 01:00Z'),
 ('a','older','yesterday','pending',0,NULL,'2026-11-22 15:59Z'),
 ('a','older','yesterday','success',17,'2026-11-22 16:01Z','2026-11-22 16:01Z'),
 ('a','endpoint',NULL,'success',17,NULL,'2026-11-23 16:00Z'),
 ('b','r','other-session','success',17,NULL,'2026-11-22 16:00Z'),
 ('ops','background-job',NULL,'success',17,NULL,'2026-11-23 02:00Z');
INSERT INTO request_context_attrs (tenant_id, request_id, gw_session_id, virtual_client_id, application_id, ts, api_key_id)
 VALUES ('b','r','other-session','foreign',2,'2026-11-22 18:00Z',2);
INSERT INTO api_keys VALUES (1,'a',false),(2,'b',false),(3,'ops',true);
INSERT INTO session_dim VALUES ('a','yesterday','2026-11-22 00:00Z',NULL,'cli-a'), ('b','other-session','2026-11-22 16:00Z',NULL,'cli-b');
INSERT INTO usage_facts VALUES
 ('a','r','success',99,19,'estimated',1,'2026-11-23 00:31Z',1,1,'business'),
 ('a','r','success',100,20,'llm',2,'2026-11-23 00:50Z',2,1,'business'),
 ('a','limited','rate_limited',0,0,NULL,1,'2026-11-23 01:00Z',3,1,'probe'),
 ('b','r','success',999,999,'llm',3,'2026-11-23 01:00Z',4,2,'business'),
 ('ops','background-job','success',0,0,NULL,1,'2026-11-23 02:00Z',5,3,'self_check');
INSERT INTO provider_call_attempts (attempt_id, tenant_id, request_id, traffic_class, client_id, source,
 attempt_no, prepared_at, sent_at, delivery, outcome, input_tokens, output_tokens, cache_read_tokens,
 estimated_amount, estimated_currency, rate_snapshot, reported_amount, reported_currency, reported_semantics) VALUES
 ('at-1','a','r','business','cli-a','executor',1,'2026-11-22 15:59Z','2026-11-22 16:00Z','sent','failure',10,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL),
 ('at-2','a','r','business','cli-a','executor',2,'2026-11-22 16:01Z','2026-11-22 16:01Z','sent','success',100,20,40,0.012,'USD','{"version":"fixture"}',0.015,'USD','settlement'),
 ('at-3',NULL,NULL,'platform','platform-automation','node-probe',1,'2026-11-23 01:00Z','2026-11-23 01:00Z','sent','success',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL),
 ('at-4',NULL,NULL,'unknown',NULL,'executor',1,'2026-11-23 02:00Z',NULL,'unknown','unknown',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO credit_ledger_with_current_month VALUES
 ('a',1,'request','r','consume','grant','alice',-5,'2026-11-22 16:10Z'),
 ('a',1,'request','r','consume','grant','alice',-5,'2026-11-22 16:10Z'),
 ('a',2,'request','r','consume','wallet','alice',-8,'2026-11-22 16:10Z'),
 ('a',3,'request','r','refund','wallet','alice',1,'2026-11-22 17:00Z'),
 ('a',4,'coupon','c','grant','wallet','alice',10000,'2026-11-22 17:00Z'),
 ('b',5,'request','r','consume','wallet','bob',-100,'2026-11-22 17:00Z');`

const coverageFixtureRows = `
INSERT INTO request_wal_with_current_month (tenant_id, request_id, created_at) VALUES
 ('coverage-a','prior','2026-10-31 15:59Z'), ('coverage-a','prior','2026-10-31 16:10Z'),
 ('coverage-a','both','2026-10-31 16:00Z'), ('coverage-a','both','2026-10-31 18:00Z'),
 ('coverage-a','receipt-only','2026-11-01 16:00Z'), ('coverage-b','missing','2026-11-01 16:00Z'),
 ('coverage-a','next-month','2026-11-30 16:00Z');
INSERT INTO usage_facts (tenant_id, request_id, status, occurred_at, revision) VALUES
 ('coverage-a','both','success','2026-10-31 16:04Z',1), ('coverage-a','both','success','2026-10-31 16:04Z',2),
 ('coverage-a','prior','success','2026-10-31 16:00Z',1), ('coverage-a','missing','failure','2026-11-01 16:04Z',1),
 ('coverage-a','prior-usage','failure','2026-10-31 15:59Z',1), ('coverage-a','prior-usage','success','2026-10-31 16:00Z',2),
 ('coverage-a','next-month','success','2026-11-30 16:00Z',1);
INSERT INTO credit_ledger_with_current_month VALUES
 ('coverage-a',21,'request','both','consume','grant','alice',-5,'2026-10-31 16:05Z'),
 ('coverage-a',21,'request','both','consume','grant','alice',-5,'2026-10-31 16:05Z'),
 ('coverage-a',22,'request','prior','consume','wallet','alice',-8,'2026-11-01 16:10Z'),
 ('coverage-a',23,'request','missing','refund','wallet','alice',5,'2026-11-01 17:00Z'),
 ('coverage-a',24,'coupon','missing','grant','wallet','alice',100,'2026-11-01 17:00Z'),
 ('coverage-a',25,'request','next-month','consume','wallet','alice',-5,'2026-11-30 16:00Z');
INSERT INTO provider_call_attempts (attempt_id, tenant_id, request_id, traffic_class, source, attempt_no, prepared_at,
 sent_at, delivery, estimated_amount, estimated_currency, rate_snapshot, reported_amount, reported_currency, reported_semantics, provider_request_id) VALUES
 ('cov-send-zero','coverage-a','both','business','fixture',1,'2026-10-31 15:59Z','2026-10-31 16:00Z','sent',0,'USD','{}',0,'USD','settlement','zero'),
 ('cov-send-no-cost','coverage-a','prior','business','fixture',1,'2026-10-31 16:00Z','2026-10-31 16:05Z','sent',NULL,NULL,NULL,NULL,NULL,NULL,NULL),
 ('cov-send-no-currency','coverage-a','missing','unknown','fixture',1,'2026-11-01 16:00Z','2026-11-01 16:00Z','sent',NULL,NULL,NULL,0.05,NULL,'unknown','other'),
 ('cov-send-unknown','coverage-a','both','business','fixture',2,'2026-10-31 16:05Z',NULL,'unknown',NULL,NULL,NULL,NULL,NULL,NULL,NULL),
 ('cov-send-other-tenant','coverage-b','missing','business','fixture',1,'2026-11-01 16:00Z','2026-11-01 16:00Z','sent',NULL,NULL,NULL,NULL,NULL,NULL,NULL),
 ('cov-send-next-month','coverage-a','next-month','business','fixture',1,'2026-11-30 15:59Z','2026-11-30 16:00Z','sent',NULL,NULL,NULL,NULL,NULL,NULL,NULL);`

const internalFixtureRows = `
INSERT INTO tenants (code) VALUES ('internal-a'), ('internal-open'), ('internal-boundary');
INSERT INTO credit_ledger_with_current_month VALUES
 ('internal-a',1,'request','split','consume','granted','alice',-5,'2026-11-10 08:01Z'),
 ('internal-a',1,'request','split','consume','granted','alice',-5,'2026-11-10 08:01Z'),
 ('internal-a',2,'request','split','consume','purchased','alice',-8,'2026-11-10 08:01Z'),
 ('internal-a',3,'request','split','refund','purchased','alice',1,'2026-11-10 08:01Z'),
 ('internal-a',4,'request','month-end','consume','granted','alice',-4,'2026-11-30 19:05Z'),
 ('internal-a',5,'coupon','split','grant','purchased','alice',500,'2026-11-10 08:01Z'),
 ('internal-boundary',6,'request','partial','consume','granted','alice',-3,'2026-11-10 08:01Z');
INSERT INTO maas_credit_consumption_buckets (tenant_id,bucket_start,credits,request_count) VALUES
 ('internal-a','2026-11-10 08:00Z',14,1), ('internal-a','2026-11-30 19:00Z',4,1),
 ('internal-open','2026-11-10 08:00Z',0,0),
 ('internal-boundary','2026-10-31 18:00Z',9,2), ('internal-boundary','2026-11-10 08:00Z',3,1), ('internal-boundary','2026-11-30 18:00Z',8,2);
INSERT INTO maas_monthly_bills (tenant_id,period_ym,period_start,period_end,total_credits,total_requests,amount_cents,currency,status) VALUES
 ('internal-a','2026-11','2026-11-01 00:00Z','2026-12-01 00:00Z',17,2,9007199254740993,'CNY','paid'),
 ('internal-open','2026-11','2026-11-01 00:00Z','2026-12-01 00:00Z',0,0,0,'USD','open');`
