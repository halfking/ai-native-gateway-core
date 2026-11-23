package reconciliation

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	migration, err := os.ReadFile("../../sql/migrations/startup/982_provider_call_attempts.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.ReplaceAll(string(migration), `\set ON_ERROR_STOP on`, "")
	exec(up)
	exec(reportFixtureRows)
	// Applying 982 against an existing schema preserves existing data and the
	// column order of unrelated historical tables on both runs.
	exec(up)
	var rows, registrations int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM legacy_drift),
		(SELECT count(*) FROM schema_migrations WHERE version='982')`).Scan(&rows, &registrations); err != nil || rows != 1 || registrations != 1 {
		t.Fatalf("migration replay rows=%d registrations=%d err=%v", rows, registrations, err)
	}

	platform := ReportScope{Platform: true}
	tenant := ReportScope{TenantID: "a"}
	query := ReportQuery{
		Start: time.Date(2026, 11, 22, 16, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 23, 16, 0, 0, 0, time.UTC),
		Timezone: "Asia/Shanghai", Limit: 20,
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
	assertReportFields(t, requests.Summary, map[string]string{"requests": "2", "terminal": "2", "success": "1", "rate_limited": "1", "unassigned_session": "1", "total_tokens": "120"})
	if len(requests.Rows) != 2 {
		t.Fatalf("request details count = %d", len(requests.Rows))
	}
	assertReportFields(t, requests.Rows[0], map[string]string{"tenant_id": `"a"`, "request_id": `"r"`, "local_day": `"2026-11-23"`, "status": `"success"`})
	assertReportFields(t, requests.Rows[1], map[string]string{"request_id": `"limited"`, "input_tokens": "null", "output_tokens": "null"})
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
CREATE TABLE legacy_drift (qty integer, note text);
INSERT INTO legacy_drift VALUES (1, 'retained');
CREATE TABLE request_wal_with_current_month (
    tenant_id text, request_id text, gw_session_id text, status text, stage int, completed_at timestamptz, created_at timestamptz);
CREATE TABLE request_context_attrs (
    tenant_id text, request_id text, gw_session_id text, virtual_client_id text, application_id bigint, ts timestamptz);
CREATE TABLE session_dim (
    tenant_id text, gw_session_id text, first_request_at timestamptz, closed_at timestamptz, client_id text);
CREATE TABLE usage_facts (
    tenant_id text, request_id text, status text, prompt_tokens bigint, completion_tokens bigint, usage_source text,
    revision int, finalized_at timestamptz, fact_id bigint);
CREATE TABLE credit_ledger_with_current_month (
    tenant_id text, id bigint, ref_type text, ref_id text, entry_type text, pool text, owner_user text, amount bigint, created_at timestamptz);`

const reportFixtureRows = `
INSERT INTO request_wal_with_current_month VALUES
 ('a','r','yesterday','pending',0,NULL,'2026-11-22 16:00Z'),
 ('a','r','yesterday','success',17,'2026-11-22 16:02Z','2026-11-22 16:00Z'),
 ('a','limited','early-provisional','failure',13,'2026-11-23 01:00Z','2026-11-23 01:00Z'),
 ('a','endpoint',NULL,'success',17,NULL,'2026-11-23 16:00Z'),
 ('b','r','other-session','success',17,NULL,'2026-11-22 16:00Z');
INSERT INTO request_context_attrs VALUES ('b','r','other-session','foreign',2,'2026-11-22 18:00Z');
INSERT INTO session_dim VALUES ('a','yesterday','2026-11-22 00:00Z',NULL,'cli-a'), ('b','other-session','2026-11-22 16:00Z',NULL,'cli-b');
INSERT INTO usage_facts VALUES
 ('a','r','success',99,19,'estimated',1,'2026-11-23 00:31Z',1),
 ('a','r','success',100,20,'llm',2,'2026-11-23 00:50Z',2),
 ('a','limited','rate_limited',0,0,NULL,1,'2026-11-23 01:00Z',3),
 ('b','r','success',999,999,'llm',3,'2026-11-23 01:00Z',4);
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
