package reconciliation

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testInvoiceEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	migration, err := os.ReadFile("../../sql/migrations/startup/986_provider_invoice_evidence.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.ReplaceAll(string(migration), `\set ON_ERROR_STOP on`, "")
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO public.schema_migrations (version, description) VALUES ('983', 'request metrics fixture');`)
	exec(up)
	exec(up)
	exec(`INSERT INTO public.provider_invoices (invoice_id,provider_id,account_ref,currency,invoice_ref,month_start,timezone,
		window_start,window_end,credential_ids,stated_total,content_sha256,imported_by)
		VALUES ('00000000-0000-4000-8000-000000000985',999,'retained','USD','before-buckets','2026-10-01','UTC',
		'2026-10-01 00:00Z','2026-11-01 00:00Z',ARRAY[9999],0.01,repeat('0',64),'fixture');
		INSERT INTO public.provider_invoice_lines (invoice_id,line_ref,kind,amount)
		VALUES ('00000000-0000-4000-8000-000000000985','legacy','usage_bucket',0.01);`)
	buckets, err := os.ReadFile("../../sql/migrations/startup/987_provider_invoice_usage_buckets.sql")
	if err != nil {
		t.Fatal(err)
	}
	bucketsSQL := strings.ReplaceAll(string(buckets), `\set ON_ERROR_STOP on`, "")
	exec(bucketsSQL)
	exec(bucketsSQL)
	var preserved bool
	if err := pool.QueryRow(ctx, `SELECT amount=0.01 AND bucket_sent_attempts IS NULL AND bucket_generated_images IS NULL
		AND (SELECT description FROM public.schema_migrations WHERE version='983')='request metrics fixture'
		FROM public.provider_invoice_lines WHERE invoice_id='00000000-0000-4000-8000-000000000985' AND line_ref='legacy'`).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("legacy bill line preserved=%t, err=%v", preserved, err)
	}
	exec(`CREATE TABLE public.credentials (id bigint PRIMARY KEY, provider_id bigint NOT NULL);
		INSERT INTO public.credentials (id,provider_id) VALUES (118,17),(219,18);`)
	exec(`INSERT INTO public.provider_call_attempts (attempt_id, traffic_class, source, attempt_no,
		provider_id, credential_id, model, prepared_at, sent_at, delivery, provider_request_id,
		estimated_amount, estimated_currency, rate_snapshot, reported_amount, reported_currency, reported_semantics)
	VALUES
		('bill-r','unknown','fixture',1,17,117,'model','2026-10-31 16:00Z','2026-10-31 16:00Z','sent','r1',0.012,'USD','{"supplier_contract":{"rate_id":"fixture"}}',0.015,'USD','openrouter_usage_cost_usd'),
		('bill-cross-account','unknown','fixture',1,17,218,'model','2026-11-01 00:00Z','2026-11-01 00:00Z','sent','r1',NULL,NULL,NULL,NULL,NULL,NULL),
		('bill-cross-provider','unknown','fixture',1,18,117,'model','2026-11-01 00:00Z','2026-11-01 00:00Z','sent','r1',NULL,NULL,NULL,NULL,NULL,NULL),
		('bill-zero','unknown','fixture',1,17,117,'model','2026-11-01 01:00Z','2026-11-01 01:00Z','sent','r0',0,'CNY','{}',0,'USD','list_price'),
		('bill-ambiguous-1','unknown','fixture',1,17,117,'model','2026-11-01 02:00Z','2026-11-01 02:00Z','sent','r2',NULL,NULL,NULL,NULL,NULL,NULL),
		('bill-ambiguous-2','unknown','fixture',1,17,117,'model','2026-11-01 03:00Z','2026-11-01 03:00Z','sent','r2',NULL,NULL,NULL,NULL,NULL,NULL),
		('bill-no-line','unknown','fixture',1,17,117,'model','2026-11-02 00:00Z','2026-11-02 00:00Z','sent','r-unmatched',NULL,NULL,NULL,NULL,NULL,NULL),
		('bill-no-request-id','unknown','fixture',1,17,117,'model','2026-11-02 02:00Z','2026-11-02 02:00Z','sent',NULL,NULL,NULL,NULL,NULL,NULL,NULL),
		('bill-end','unknown','fixture',1,17,117,'model','2026-11-30 16:00Z','2026-11-30 16:00Z','sent','r-end',NULL,NULL,NULL,NULL,NULL,NULL),
		('bill-unknown','unknown','fixture',1,17,117,'model','2026-11-01 04:00Z',NULL,'unknown','r-maybe',NULL,NULL,NULL,NULL,NULL,NULL)`)
	input := InvoiceInput{
		ProviderID: 17, AccountRef: "sample-account", Currency: "usd", InvoiceRef: "sample-invoice",
		Month: "2026-11", CredentialIDs: []int64{118, 117}, StatedTotal: "0.1050",
		Lines: []InvoiceLine{
			{LineRef: "a", Kind: "request_total", ProviderRequestID: "r1", Amount: "0.015"},
			{LineRef: "b", Kind: "request_total", ProviderRequestID: "r0", Amount: "0.000"},
			{LineRef: "c", Kind: "request_total", ProviderRequestID: "r2", Amount: "0.020"},
			{LineRef: "d", Kind: "request_total", ProviderRequestID: "r-end", Amount: "0.030"},
			{LineRef: "e", Kind: "request_total", ProviderRequestID: "r-maybe", Amount: "0.010"},
			{LineRef: "f", Kind: "usage_bucket", Amount: "0.035"},
			{LineRef: "g", Kind: "tax", Amount: "0.002"},
			{LineRef: "h", Kind: "discount", Amount: "-0.007"},
		},
	}
	stored, err := ImportInvoice(ctx, pool, input, "fixture-user")
	if err != nil || !stored.Created {
		t.Fatalf("import = %+v, err = %v", stored, err)
	}
	slices.Reverse(input.Lines)
	slices.Reverse(input.CredentialIDs)
	input.StatedTotal = "+0.105"
	storedAgain, err := ImportInvoice(ctx, pool, input, "fixture-replay-user")
	if err != nil || storedAgain.Created || storedAgain.InvoiceID != stored.InvoiceID {
		t.Fatalf("replay = %+v, err = %v", storedAgain, err)
	}
	input.Lines[0].Description = "different evidence"
	if _, err := ImportInvoice(ctx, pool, input, "fixture-user"); !errors.Is(err, ErrInvoiceConflict) {
		t.Fatalf("expected conflicting import, got %v", err)
	}
	invalid := input
	invalid.InvoiceRef = "wrong-credential"
	invalid.CredentialIDs = []int64{219, 999}
	if _, err := ImportInvoice(ctx, pool, invalid, "fixture-user"); !errors.Is(err, ErrInvalidInvoice) {
		t.Fatalf("expected credentials without provider evidence to fail, got %v", err)
	}
	items, err := ListInvoices(ctx, pool, input.ProviderID, input.Month, 20, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %+v %v", items, err)
	}
	assertReportFields(t, items[0], map[string]string{"imported_by": `"fixture-user"`, "currency": `"USD"`, "month": `"2026-11"`})
	got, err := ReadInvoiceReport(ctx, pool, stored.InvoiceID, 20, 0)
	if err != nil || len(got.Lines) != 8 {
		t.Fatalf("report lines=%d, err=%v", len(got.Lines), err)
	}
	assertReportFields(t, got.Summary, map[string]string{
		"invoice_total": `"0.105"`, "invoice_currency": `"USD"`, "month": `"2026-11"`, "matched_lines": "2", "review_lines": "4", "estimate_comparable_lines": "1", "reported_comparable_lines": "1",
		"estimated_minus_invoiced_amount": `"-0.003"`, "reported_minus_invoiced_amount": `"0.000"`, "adjustment_total": `"-0.005"`,
		"recorded_sent_attempts": "6", "recorded_sends_without_provider_request_id": "1", "recorded_sends_without_invoice_request_total": "1", "recorded_unconfirmed_attempts": "1",
	})
	assertReportFields(t, got.Lines[0], map[string]string{"match_status": `"matched"`, "attempt_id": `"bill-r"`})
	assertReportFields(t, got.Lines[1], map[string]string{"invoice_amount": `"0"`, "captured_estimate": `"0"`, "estimated_currency": `"CNY"`, "reported_semantics": `"list_price"`, "comparable_estimate": "null", "comparable_reported": "null"})
	for i, want := range []string{`"attempt_ambiguous"`, `"attempt_missing"`, `"delivery_unconfirmed"`, `"bucket_review"`, `"bill_adjustment"`, `"bill_adjustment"`} {
		assertReportFields(t, got.Lines[i+2], map[string]string{"match_status": want})
	}
	other := InvoiceInput{ProviderID: 17, AccountRef: "other-alias", Currency: "USD", InvoiceRef: "other-bill", Month: "2026-11",
		CredentialIDs: []int64{117}, StatedTotal: "0.015", Lines: []InvoiceLine{{LineRef: "a", Kind: "request_total", ProviderRequestID: "r1", Amount: "0.015"}},
	}
	if _, err := ImportInvoice(ctx, pool, other, "fixture-user"); err != nil {
		t.Fatal(err)
	}
	got, err = ReadInvoiceReport(ctx, pool, stored.InvoiceID, 1, 0)
	if err != nil || len(got.Lines) != 1 {
		t.Fatalf("paged report lines=%d, err=%v", len(got.Lines), err)
	}
	assertReportFields(t, got.Lines[0], map[string]string{"match_status": `"invoice_reference_ambiguous"`, "attempt_id": "null"})
	assertReportFields(t, got.Summary, map[string]string{"matched_lines": "1", "estimate_comparable_lines": "0", "estimated_minus_invoiced_amount": "null"})
	t.Run("daily_statement", func(t *testing.T) { testDailyInvoiceEvidence(t, ctx, pool) })
	t.Run("daily_usage_buckets", func(t *testing.T) { testDailyUsageBuckets(t, ctx, pool) })
	t.Run("daily_media_buckets", func(t *testing.T) { testDailyMediaBuckets(t, ctx, pool) })
	t.Run("daily_invoice_ambiguity", func(t *testing.T) { testDailyInvoiceAmbiguity(t, ctx, pool) })
}

func testDailyMediaBuckets(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO public.credentials (id,provider_id) VALUES (4901,490),(4902,490);
		INSERT INTO public.provider_call_attempts (attempt_id,traffic_class,source,attempt_no,provider_id,credential_id,model,prepared_at,sent_at,
			delivery,audio_input_duration_us,audio_billed_duration_us,audio_output_duration_us,generated_images,video_output_duration_us,
			estimated_amount,estimated_currency,rate_snapshot)
		VALUES
		 ('media-invoice-images-a','unknown','fixture',1,490,4901,'images','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',NULL,NULL,NULL,0,NULL,0,'USD','{"supplier_contract":{"rate_id":"fixture"}}'),
		 ('media-invoice-images-b','unknown','fixture',1,490,4901,'images','2026-11-01 01:00Z','2026-11-01 01:00Z',
		   'sent',NULL,NULL,NULL,3,NULL,0.30,'USD','{"supplier_contract":{"rate_id":"fixture"}}'),
		 ('media-invoice-images-other','unknown','fixture',1,490,4902,'images','2026-11-01 01:00Z','2026-11-01 01:00Z',
		   'sent',NULL,NULL,NULL,99,NULL,NULL,NULL,NULL),
		 ('media-invoice-images-next','unknown','fixture',1,490,4901,'images','2026-11-01 15:59:59Z','2026-11-01 16:00Z',
		   'sent',NULL,NULL,NULL,99,NULL,NULL,NULL,NULL),
		 ('media-invoice-audio','unknown','fixture',1,490,4901,'audio','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',400000,1000000,0,NULL,NULL,NULL,NULL,NULL),
		 ('media-invoice-video','unknown','fixture',1,490,4901,'video','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',NULL,NULL,NULL,NULL,4125000,0.30,'USD','{"supplier_contract":{"rate_id":"fixture"}}'),
		 ('media-invoice-missing-a','unknown','fixture',1,490,4901,'missing','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',NULL,NULL,NULL,0,NULL,0,'USD','{"supplier_contract":{"rate_id":"fixture"}}'),
		 ('media-invoice-missing-b','unknown','fixture',1,490,4901,'missing','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',NULL,NULL,NULL,NULL,NULL,0,'USD','{"supplier_contract":{"rate_id":"fixture"}}');`); err != nil {
		t.Fatal(err)
	}
	line := func(model string, bucket InvoiceUsageBucket) InvoiceLine {
		bucket.Basis = "request_total"
		return InvoiceLine{LineRef: model, Kind: "usage_bucket", Model: model, Amount: "0.35", UsageBucket: &bucket}
	}
	var zero, three, inputUS, billedUS, differentVideoUS int64 = 0, 3, 400_000, 1_000_000, 4_125_001
	input := InvoiceInput{ProviderID: 490, AccountRef: "media-account", Currency: "USD", InvoiceRef: "daily-media-buckets",
		Day: "2026-11-01", CredentialIDs: []int64{4901}, StatedTotal: "1.40", Lines: []InvoiceLine{
			line("audio", InvoiceUsageBucket{SentAttempts: 1, AudioInputDurationUS: &inputUS, AudioBilledDurationUS: &billedUS, AudioOutputDurationUS: &zero}),
			line("images", InvoiceUsageBucket{SentAttempts: 2, GeneratedImages: &three}),
			line("missing", InvoiceUsageBucket{SentAttempts: 2, GeneratedImages: &zero}),
			line("video", InvoiceUsageBucket{SentAttempts: 1, VideoOutputDurationUS: &differentVideoUS}),
		}}
	stored, err := ImportInvoice(ctx, pool, input, "fixture")
	if err != nil || !stored.Created {
		t.Fatalf("media bucket import: %+v %v", stored, err)
	}
	replay, err := ImportInvoice(ctx, pool, input, "fixture")
	if err != nil || replay.Created || replay.InvoiceID != stored.InvoiceID {
		t.Fatalf("media bucket replay: %+v %v", replay, err)
	}
	input.Lines[2].UsageBucket.GeneratedImages = nil
	if _, err := ImportInvoice(ctx, pool, input, "fixture"); !errors.Is(err, ErrInvoiceConflict) {
		t.Fatalf("missing and explicitly zero declarations must differ: %v", err)
	}
	report, err := ReadInvoiceReport(ctx, pool, stored.InvoiceID, 20, 0)
	if err != nil || len(report.Lines) != 4 {
		t.Fatalf("media bucket report: %s %v", report.Lines, err)
	}
	assertReportFields(t, report.Summary, map[string]string{
		"bucket_matched_lines": "2", "review_lines": "2", "estimate_comparable_lines": "1", "estimated_minus_invoiced_amount": `"-0.05"`,
	})
	assertReportFields(t, report.Lines[0], map[string]string{
		"match_status": `"bucket_matched"`, "bucket_audio_input_duration_us": `"400000"`, "bucket_observed_audio_input_duration_us": `"400000"`,
		"bucket_audio_billed_duration_us": `"1000000"`, "bucket_observed_audio_billed_duration_us": `"1000000"`,
		"bucket_audio_output_duration_us": `"0"`, "bucket_observed_audio_output_duration_us": `"0"`,
	})
	assertReportFields(t, report.Lines[1], map[string]string{
		"match_status": `"bucket_matched"`, "bucket_generated_images": `"3"`, "bucket_observed_generated_images": `"3"`,
	})
	assertReportFields(t, report.Lines[2], map[string]string{
		"match_status": `"bucket_usage_missing"`, "bucket_generated_images": `"0"`, "bucket_observed_generated_images": "null", "comparable_estimate": "null",
	})
	assertReportFields(t, report.Lines[3], map[string]string{
		"match_status": `"bucket_usage_mismatch"`, "bucket_video_output_duration_us": `"4125001"`, "bucket_observed_video_output_duration_us": `"4125000"`,
		"comparable_estimate": "null",
	})
}

func testDailyUsageBuckets(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO public.credentials (id,provider_id) VALUES (4001,400),(4002,400);
		INSERT INTO public.provider_call_attempts (attempt_id,traffic_class,source,attempt_no,provider_id,credential_id,model,prepared_at,sent_at,
			delivery,provider_request_id,input_tokens,output_tokens,estimated_amount,estimated_currency,rate_snapshot,
			reported_amount,reported_currency,reported_semantics) VALUES
		 ('bucket-clean-a','unknown','fixture',1,400,4001,'clean','2026-10-31 15:59:59Z','2026-10-31 16:00Z',
		   'sent',NULL,10,0,0.040,'USD','{"supplier_contract":{"rate_id":"fixture"}}',0.05,'USD','openrouter_usage_cost_usd'),
		 ('bucket-clean-b','unknown','fixture',1,400,4001,'clean','2026-11-01 15:00Z','2026-11-01 15:00Z',
		   'sent',NULL,20,5,0.030,'USD','{"supplier_contract":{"rate_id":"fixture"}}',0.05,'USD','openrouter_usage_cost_usd'),
		 ('bucket-next-day','unknown','fixture',1,400,4001,'clean','2026-11-01 15:59:59Z','2026-11-01 16:00Z',
		   'sent',NULL,999,999,99,'USD','{}',99,'USD','openrouter_usage_cost_usd'),
		 ('bucket-other-account','unknown','fixture',1,400,4002,'clean','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',NULL,999,999,99,'USD','{}',99,'USD','openrouter_usage_cost_usd'),
		 ('bucket-missing-price','unknown','fixture',1,400,4001,'missing-price','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',NULL,2,0,0,'USD','{}',0,'USD','list_price'),
		 ('bucket-count','unknown','fixture',1,400,4001,'count','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',NULL,2,3,NULL,NULL,NULL,NULL,NULL,NULL),
		 ('bucket-no-usage','unknown','fixture',1,400,4001,'no-usage','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',NULL,2,NULL,NULL,NULL,NULL,NULL,NULL,NULL),
		 ('bucket-wrong-usage','unknown','fixture',1,400,4001,'wrong-usage','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',NULL,9,0,NULL,NULL,NULL,NULL,NULL,NULL),
		 ('bucket-uncertain-sent','unknown','fixture',1,400,4001,'uncertain','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent',NULL,2,3,NULL,NULL,NULL,NULL,NULL,NULL),
		 ('bucket-uncertain-maybe','unknown','fixture',1,400,4001,'uncertain','2026-11-01 01:00Z',NULL,
		   'unknown',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL),
		 ('bucket-overlap','unknown','fixture',1,400,4001,'overlap','2026-11-01 00:00Z','2026-11-01 00:00Z',
		   'sent','conflicting-total',2,3,0.10,'USD','{}',NULL,NULL,NULL);
		UPDATE public.provider_call_attempts SET traffic_class='business', tenant_id='bucket-tenant', request_id='bucket-request' WHERE attempt_id='bucket-clean-a';
		UPDATE public.provider_call_attempts SET traffic_class='platform', client_id='platform-automation' WHERE attempt_id='bucket-clean-b';`); err != nil {
		t.Fatal(err)
	}
	var zero, thirty, five, ten int64 = 0, 30, 5, 10
	line := func(ref, model string, sends int64, input, output *int64) InvoiceLine {
		return InvoiceLine{LineRef: ref, Kind: "usage_bucket", Model: model, Amount: "0.10",
			UsageBucket: &InvoiceUsageBucket{Basis: "request_total", SentAttempts: sends, InputTokens: input, OutputTokens: output}}
	}
	input := InvoiceInput{ProviderID: 400, AccountRef: "bucket-account", Currency: "USD", InvoiceRef: "daily-buckets",
		Day: "2026-11-01", CredentialIDs: []int64{4001}, StatedTotal: "0.70", Lines: []InvoiceLine{
			line("a", "clean", 2, &thirty, &five), line("b", "missing-price", 1, nil, &zero), line("c", "count", 2, nil, nil),
			line("d", "no-usage", 1, nil, &zero), line("e", "wrong-usage", 1, &ten, nil),
			line("f", "uncertain", 1, nil, nil), line("g", "overlap", 1, nil, nil),
		}}
	stored, err := ImportInvoice(ctx, pool, input, "fixture")
	if err != nil || !stored.Created {
		t.Fatalf("usage bucket import: %+v %v", stored, err)
	}
	replay, err := ImportInvoice(ctx, pool, input, "fixture")
	if err != nil || replay.Created || replay.InvoiceID != stored.InvoiceID {
		t.Fatalf("bucket replay: %+v %v", replay, err)
	}
	peer := InvoiceInput{ProviderID: 400, AccountRef: "separately-named-account", Currency: "USD", InvoiceRef: "request-and-next-day",
		Day: "2026-11-01", CredentialIDs: []int64{4001}, StatedTotal: "0.10", Lines: []InvoiceLine{
			{LineRef: "request", Kind: "request_total", ProviderRequestID: "conflicting-total", Model: "other-label", Amount: "0.10"},
		}}
	if _, err := ImportInvoice(ctx, pool, peer, "fixture"); err != nil {
		t.Fatal(err)
	}
	nextDay := peer
	nextDay.Day, nextDay.InvoiceRef = "2026-11-02", "next-day"
	nextDay.Lines = []InvoiceLine{line("next", "clean", 1, nil, nil)}
	if _, err := ImportInvoice(ctx, pool, nextDay, "fixture"); err != nil {
		t.Fatal(err)
	}
	invoices, err := ListDailyInvoices(ctx, pool, 0, "2026-11-01", DefaultMonthTimezone, 20, 0)
	if err != nil || len(invoices) != 3 {
		t.Fatalf("all-supplier day list selected %d invoices, err=%v", len(invoices), err)
	}
	report, err := ReadInvoiceReport(ctx, pool, stored.InvoiceID, 20, 0)
	if err != nil || len(report.Lines) != len(input.Lines) || report.ReadAt.IsZero() || report.Version != invoiceReportVersion {
		t.Fatalf("usage bucket report: %s %v", report.Lines, err)
	}
	assertReportFields(t, report.Summary, map[string]string{
		"version": `"supplier-invoice-v1"`, "matched_lines": "0", "bucket_matched_lines": "2", "bucket_matched_invoice_amount": `"0.2"`,
		"recorded_sends_in_matched_buckets": "3", "review_lines": "5", "estimate_comparable_lines": "1",
		"reported_comparable_lines": "1", "estimated_minus_invoiced_amount": `"-0.030"`, "reported_minus_invoiced_amount": `"0.00"`,
	})
	assertReportFields(t, report.Lines[0], map[string]string{
		"match_status": `"bucket_matched"`, "attempt_id": "null", "bucket_sent_attempts": "2", "bucket_observed_sends": "2",
		"bucket_observed_input": `"30"`, "bucket_observed_output": `"5"`, "comparable_estimate": `"0.070"`, "comparable_reported": `"0.10"`,
	})
	assertReportFields(t, report.Lines[1], map[string]string{
		"match_status": `"bucket_matched"`, "bucket_observed_output": `"0"`, "comparable_estimate": "null", "comparable_reported": "null",
		"bucket_sends_without_comparable_estimate": "1", "bucket_sends_without_comparable_report": "1",
	})
	for i, status := range []string{"bucket_count_mismatch", "bucket_usage_missing", "bucket_usage_mismatch", "bucket_delivery_unconfirmed", "bucket_scope_overlap"} {
		assertReportFields(t, report.Lines[i+2], map[string]string{"match_status": "\"" + status + "\"", "comparable_estimate": "null"})
	}
}

func testDailyInvoiceEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	if _, err := pool.Exec(ctx, `INSERT INTO public.credentials (id,provider_id) VALUES (3001,300);
		INSERT INTO public.provider_call_attempts (attempt_id, traffic_class, source, attempt_no, provider_id, credential_id,
			prepared_at, sent_at, delivery, provider_request_id) VALUES
		 ('daily-before','unknown','fixture',1,300,3001,'2026-10-31 15:59:59Z','2026-10-31 15:59:59Z','sent','reused'),
		 ('daily-open','unknown','fixture',1,300,3001,'2026-10-31 15:59:59Z','2026-10-31 16:00:00Z','sent','reused'),
		 ('daily-next','unknown','fixture',1,300,3001,'2026-11-01 15:59:59Z','2026-11-01 16:00:00Z','sent','reused');`); err != nil {
		t.Fatal(err)
	}
	input := InvoiceInput{ProviderID: 300, AccountRef: "day-account", Currency: "USD", InvoiceRef: "one-day",
		Day: "2026-11-01", CredentialIDs: []int64{3001}, StatedTotal: "0.15",
		Lines: []InvoiceLine{{LineRef: "1", Kind: "request_total", ProviderRequestID: "reused", Amount: "0.15"}},
	}
	stored, err := ImportInvoice(ctx, pool, input, "fixture")
	if err != nil || !stored.Created {
		t.Fatalf("daily import: %+v, %v", stored, err)
	}
	replay, err := ImportInvoice(ctx, pool, input, "fixture")
	if err != nil || replay.Created || replay.InvoiceID != stored.InvoiceID {
		t.Fatalf("daily replay: %+v, %v", replay, err)
	}
	period := input
	period.Day, period.Month, period.InvoiceRef = "", "2026-11", "one-month"
	period.Lines = []InvoiceLine{{LineRef: "1", Kind: "fixed_fee", Amount: "0.15"}}
	if _, err := ImportInvoice(ctx, pool, period, "fixture"); err != nil {
		t.Fatal(err)
	}
	items, err := ListDailyInvoices(ctx, pool, 300, "2026-11-01", "Asia/Shanghai", 20, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("daily list selected an overlapping statement: %s %v", items, err)
	}
	assertReportFields(t, items[0], map[string]string{"invoice_ref": `"one-day"`, "timezone": `"Asia/Shanghai"`})
	items, err = ListDailyInvoices(ctx, pool, 300, "2026-11-01", "UTC", 20, 0)
	if err != nil || len(items) != 0 {
		t.Fatalf("daily list selected a different window: %s %v", items, err)
	}
	report, err := ReadInvoiceReport(ctx, pool, stored.InvoiceID, 20, 0)
	if err != nil || len(report.Lines) != 1 {
		t.Fatalf("daily invoice match: %s %v", report.Lines, err)
	}
	assertReportFields(t, report.Lines[0], map[string]string{"match_status": `"matched"`, "attempt_id": `"daily-open"`, "invoice_amount": `"0.15"`})
	assertReportFields(t, report.Summary, map[string]string{
		"recorded_sent_attempts": "1", "invoice_total": `"0.15"`,
		"period_start_day": `"2026-11-01"`, "period_end_day": `"2026-11-02"`,
	})
}

func testDailyInvoiceAcrossLocalMonths(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO public.credentials (id,provider_id) VALUES (3011,301)`); err != nil {
		t.Fatal(err)
	}
	invoice := InvoiceInput{ProviderID: 301, AccountRef: "date-line-account", Currency: "USD", InvoiceRef: "date-line-day",
		Day: "2026-09-01", Timezone: "Pacific/Kiritimati", CredentialIDs: []int64{3011}, StatedTotal: "0.15",
		Lines: []InvoiceLine{{LineRef: "1", Kind: "fixed_fee", Amount: "0.15"}},
	}
	if _, err := ImportInvoice(ctx, pool, invoice, "fixture"); err != nil {
		t.Fatal(err)
	}
	month := invoice
	month.Day, month.Month, month.InvoiceRef = "", "2026-09", "date-line-month"
	if _, err := ImportInvoice(ctx, pool, month, "fixture"); err != nil {
		t.Fatal(err)
	}
	const day, zone = "2026-08-31", "Pacific/Honolulu"
	shadow, err := ReadDailyShadow(ctx, pool, day, zone)
	if err != nil || len(shadow.SupplierBills) != 1 {
		t.Fatalf("shadow should find the one exact-window invoice: %+v, %v", shadow.SupplierBills, err)
	}
	items, err := ListDailyInvoices(ctx, pool, 301, day, zone, 20, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("daily list should find the same exact-window invoice as shadow: %s, %v", items, err)
	}
	want := map[string]string{"invoice_ref": `"date-line-day"`, "month": `"2026-09"`, "period_start_day": `"2026-09-01"`}
	assertReportFields(t, items[0], want)
	assertReportFields(t, shadow.SupplierBills[0].Invoice, want)
	items, err = ListInvoices(ctx, pool, 301, "2026-09", 20, 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("monthly list should use each invoice's original month: %s, %v", items, err)
	}
}

func testDailyInvoiceAmbiguity(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO public.credentials (id,provider_id) VALUES (8101,810),(8102,810);
		INSERT INTO public.provider_call_attempts (attempt_id,traffic_class,source,attempt_no,provider_id,credential_id,model,
			prepared_at,sent_at,delivery,provider_request_id,estimated_amount,estimated_currency,rate_snapshot) VALUES
		 ('bill-confirmed','unknown','fixture',1,810,8101,'uncertain-model','2026-12-14 01:00Z','2026-12-14 01:00Z',
		   'sent','uncertain',0.008,'USD','{"supplier_contract":{"rate_id":"fixture"}}'),
		 ('bill-uncertain-peer','unknown','fixture',2,810,8101,'uncertain-model','2026-12-14 01:00Z',NULL,'unknown','uncertain',NULL,NULL,NULL),
		 ('bill-unique','unknown','fixture',1,810,8101,'unique-model','2026-12-14 02:00Z','2026-12-14 02:00Z',
		   'sent','unique',0.016,'USD','{"supplier_contract":{"rate_id":"fixture"}}'),
		 ('bill-unique-other-account','unknown','fixture',1,810,8102,'unique-model','2026-12-14 02:00Z','2026-12-14 02:00Z',
		   'sent','unique',0.008,'EUR','{"supplier_contract":{"rate_id":"fixture"}}'),
		 ('bill-unique-next-day','unknown','fixture',1,810,8101,'unique-model','2026-12-14 16:00Z',NULL,'unknown','unique',NULL,NULL,NULL),
		 ('bill-cross-currency','unknown','fixture',1,810,8101,'currency-model','2026-12-14 03:00Z','2026-12-14 03:00Z',
		   'sent','cross-currency',0.025,'USD','{"supplier_contract":{"rate_id":"fixture"}}'),
		 ('bill-currency-bucket','unknown','fixture',1,810,8101,'bucket-model','2026-12-14 04:00Z','2026-12-14 04:00Z',
		   'sent',NULL,0.035,'USD','{"supplier_contract":{"rate_id":"fixture"}}'),
		 ('bill-prepared-confirmed','unknown','fixture',1,810,8101,'prepared-model','2026-12-14 05:00Z','2026-12-14 05:00Z',
		   'sent','prepared',0.045,'USD','{"supplier_contract":{"rate_id":"fixture"}}'),
		 ('bill-prepared-peer','unknown','fixture',2,810,8101,'prepared-model','2026-12-14 05:00Z',NULL,'prepared','prepared',NULL,NULL,NULL);`); err != nil {
		t.Fatal(err)
	}
	request := func(ref, id, model, amount string) InvoiceLine {
		return InvoiceLine{LineRef: ref, Kind: "request_total", ProviderRequestID: id, Model: model, Amount: amount}
	}
	bucket := InvoiceLine{LineRef: "d", Kind: "usage_bucket", Model: "bucket-model", Amount: "0.04",
		UsageBucket: &InvoiceUsageBucket{Basis: "request_total", SentAttempts: 1}}
	duplicate := request("c", "cross-currency", "currency-model", "0.03")
	usd := InvoiceInput{ProviderID: 810, AccountRef: "account", Currency: "USD", InvoiceRef: "primary-daily",
		Day: "2026-12-14", CredentialIDs: []int64{8101}, StatedTotal: "0.15", Lines: []InvoiceLine{
			request("a", "uncertain", "uncertain-model", "0.01"), request("b", "unique", "unique-model", "0.02"), duplicate, bucket,
			request("e", "prepared", "prepared-model", "0.05"),
		}}
	eur := InvoiceInput{ProviderID: 810, AccountRef: "account-alias", Currency: "EUR", InvoiceRef: "secondary-daily",
		Day: usd.Day, CredentialIDs: usd.CredentialIDs, StatedTotal: "0.07", Lines: []InvoiceLine{duplicate, bucket}}
	separate := InvoiceInput{ProviderID: 810, AccountRef: "different-credential", Currency: "EUR", InvoiceRef: "disjoint-daily",
		Day: usd.Day, CredentialIDs: []int64{8102}, StatedTotal: "0.01", Lines: []InvoiceLine{request("b", "unique", "unique-model", "0.01")}}
	read := func(in InvoiceInput, wantStatuses ...string) InvoiceReport {
		t.Helper()
		stored, err := ImportInvoice(ctx, pool, in, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		report, err := ReadInvoiceReport(ctx, pool, stored.InvoiceID, 20, 0)
		if err != nil || len(report.Lines) != len(wantStatuses) {
			t.Fatalf("invoice %s report lines=%s err=%v", in.InvoiceRef, report.Lines, err)
		}
		for i, status := range wantStatuses {
			assertReportFields(t, report.Lines[i], map[string]string{"match_status": `"` + status + `"`})
			if status != "matched" && status != "bucket_matched" {
				assertReportFields(t, report.Lines[i], map[string]string{"attempt_id": "null", "comparable_estimate": "null", "comparable_reported": "null"})
			}
		}
		return report
	}
	if _, err := ImportInvoice(ctx, pool, usd, "fixture"); err != nil {
		t.Fatal(err)
	}
	reportEUR := read(eur, "invoice_reference_ambiguous", "bucket_scope_overlap")
	assertReportFields(t, reportEUR.Summary, map[string]string{
		"review_lines": "2", "estimate_comparable_lines": "0", "estimated_minus_invoiced_amount": "null",
		"recorded_sends_without_invoice_request_total": "0",
	})
	other := read(separate, "matched")
	assertReportFields(t, other.Lines[0], map[string]string{"attempt_id": `"bill-unique-other-account"`, "estimated_minus_invoiced_amount": `"-0.002"`})
	reportUSD := read(usd, "delivery_unconfirmed", "matched", "invoice_reference_ambiguous", "bucket_scope_overlap", "delivery_unconfirmed")
	assertReportFields(t, reportUSD.Lines[1], map[string]string{"attempt_id": `"bill-unique"`})
	assertReportFields(t, reportUSD.Summary, map[string]string{
		"matched_lines": "1", "review_lines": "4", "estimate_comparable_lines": "1", "estimated_minus_invoiced_amount": `"-0.004"`,
	})
}

func TestInvoiceRejectsInconsistentEvidence(t *testing.T) {
	for name, change := range map[string]func(*InvoiceInput){
		"total mismatch":        func(in *InvoiceInput) { in.StatedTotal = "0.03" },
		"exponent":              func(in *InvoiceInput) { in.Lines[0].Amount = "2e-2" },
		"duplicate line":        func(in *InvoiceInput) { in.Lines = append(in.Lines, in.Lines[0]) },
		"duplicate credentials": func(in *InvoiceInput) { in.CredentialIDs = []int64{1, 1} },
		"negative usage":        func(in *InvoiceInput) { in.Lines[0].Amount = "-0.02" },
		"positive refund":       func(in *InvoiceInput) { in.Lines[0].Kind, in.Lines[0].ProviderRequestID = "refund", "" },
		"two periods":           func(in *InvoiceInput) { in.Day = "2026-11-01" },
		"missing period":        func(in *InvoiceInput) { in.Month = "" },
		"unsupported bucket basis": func(in *InvoiceInput) {
			in.Lines[0] = InvoiceLine{LineRef: "1", Kind: "usage_bucket", Model: "model", Amount: "0.02",
				UsageBucket: &InvoiceUsageBucket{Basis: "input_tokens", SentAttempts: 1}}
		},
		"bucket without model": func(in *InvoiceInput) {
			in.Lines[0] = InvoiceLine{LineRef: "1", Kind: "usage_bucket", Amount: "0.02",
				UsageBucket: &InvoiceUsageBucket{Basis: "request_total", SentAttempts: 1}}
		},
		"negative media quantity": func(in *InvoiceInput) {
			duration := int64(-1)
			in.Lines[0] = InvoiceLine{LineRef: "1", Kind: "usage_bucket", Model: "speech", Amount: "0.02",
				UsageBucket: &InvoiceUsageBucket{Basis: "request_total", SentAttempts: 1, AudioBilledDurationUS: &duration}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := InvoiceInput{ProviderID: 3, AccountRef: "acct", InvoiceRef: "invoice", Currency: "USD", Month: "2026-11", CredentialIDs: []int64{1}, StatedTotal: "0.02",
				Lines: []InvoiceLine{{LineRef: "1", Kind: "request_total", ProviderRequestID: "r1", Amount: "0.02"}}}
			change(&in)
			if _, _, _, err := normalizeInvoice(in); !errors.Is(err, ErrInvalidInvoice) {
				t.Fatalf("got %v, want invalid invoice", err)
			}
		})
	}
}
