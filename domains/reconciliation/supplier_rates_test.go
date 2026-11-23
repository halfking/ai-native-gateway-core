package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testSupplierContractRates(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(ctx, `INSERT INTO public.credentials (id,provider_id) VALUES (5011,501),(5012,502),(5013,501)`)
	check(err)
	boundary := time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC)
	end := boundary.AddDate(0, 0, 1)
	prior := SupplierRateInput{ProviderID: 501, CredentialID: 5011, Model: "m", Reference: "prior", Currency: "USD",
		EffectiveFrom: boundary.AddDate(0, 0, -1),
		Terms:         SupplierTerms{Components: []SupplierComponent{{Meter: "confirmed_send", Unit: 1, Tiers: []SupplierTier{{Price: "0.04"}}}}},
	}
	previous, err := ImportSupplierRate(ctx, pool, prior, "fixture-user")
	check(err)
	var fifty int64 = 50
	next := SupplierRateInput{ProviderID: 501, CredentialID: 5011, Model: "m", Reference: "next", Currency: "usd",
		EffectiveFrom: boundary, EffectiveTo: &end,
		Terms: SupplierTerms{
			SubtractFromInput: []string{"cache_read_tokens"},
			Components: []SupplierComponent{
				{Meter: "confirmed_send", Unit: 1, Tiers: []SupplierTier{{Price: "0.001"}}},
				{Meter: "input_tokens", Unit: 1000, Tiers: []SupplierTier{{Through: &fifty, Price: "2"}, {Price: "4"}}},
				{Meter: "output_tokens", Unit: 1000, Tiers: []SupplierTier{{Price: "3"}}},
				{Meter: "cache_read_tokens", Unit: 1000, Tiers: []SupplierTier{{Price: "0.5"}}},
			},
		}}
	version, err := ImportSupplierRate(ctx, pool, next, "fixture-user")
	check(err)
	slices.Reverse(next.Terms.Components)
	replay, err := ImportSupplierRate(ctx, pool, next, "fixture-replay")
	check(err)
	if !version.Created || !previous.Created || replay.Created || replay.RateID != version.RateID {
		t.Fatalf("contract imports: prior=%+v next=%+v replay=%+v", previous, version, replay)
	}
	changed := next
	changed.Reference = "different revision"
	if _, err := ImportSupplierRate(ctx, pool, changed, "fixture"); !errors.Is(err, ErrSupplierRateConflict) {
		t.Fatalf("changed rate at same effective time returned %v", err)
	}
	changed.CredentialID = 5012
	if _, err := ImportSupplierRate(ctx, pool, changed, "fixture"); !errors.Is(err, ErrInvalidSupplierRate) {
		t.Fatalf("other supplier credential returned %v", err)
	}
	items, err := ListSupplierRates(ctx, pool, 501, 5011, "m", 20, 0)
	check(err)
	if len(items) != 2 {
		t.Fatalf("unexpected contract history %s", items)
	}
	assertReportFields(t, items[0], map[string]string{"reference": `"next"`, "imported_by": `"fixture-user"`})

	// Simulate evidence imported before the historical fixture sends. A real
	// import always acquires its own database timestamp.
	_, err = pool.Exec(ctx, `UPDATE public.supplier_contract_rates SET imported_at=$1 WHERE provider_id=501`, boundary.AddDate(0, 0, -2))
	check(err)
	recorder := NewAttemptRecorder(pool)
	attempt := func(id string, credentialID int, model string, preparedAt time.Time) {
		t.Helper()
		check(recorder.Prepare(ctx, Attempt{ID: id, ProviderID: 501, CredentialID: credentialID, Model: model,
			AttemptNo: 1, TrafficClass: "business", TenantID: "supplier-contract-fixture", RequestID: id,
			Source: "supplier-contract-test", PreparedAt: preparedAt,
			RateSnapshot: map[string]any{"source": "routing_reference", "input": "9999"}}))
		check(recorder.MarkDispatched(ctx, id))
	}
	assertAmount := func(id, amount, currency, reference string) {
		t.Helper()
		var actualAmount, actualCurrency, actualRef string
		check(pool.QueryRow(ctx, `SELECT COALESCE(estimated_amount::text,'missing'), COALESCE(estimated_currency,'missing'),
			COALESCE(rate_snapshot#>>'{supplier_contract,reference}','missing')
			FROM public.provider_call_attempts WHERE attempt_id=$1`, id).Scan(&actualAmount, &actualCurrency, &actualRef))
		if actualAmount != amount || actualCurrency != currency || actualRef != reference {
			t.Fatalf("%s amount/currency/reference = %s/%s/%s, expected %s/%s/%s", id, actualAmount, actualCurrency, actualRef, amount, currency, reference)
		}
	}
	attempt("rate-before", 5011, "m", boundary.Add(-2*time.Second))
	check(recorder.MarkSent(ctx, "rate-before", boundary.Add(-time.Second)))
	assertAmount("rate-before", "0.04", "USD", "prior")
	attempt("rate-boundary", 5011, "m", boundary.Add(-time.Second))
	var input, output, cache int64 = 100, 20, 20
	check(recorder.RecordUsage(ctx, "rate-boundary", UsageEvidence{InputTokens: &input, OutputTokens: &output, Source: "fixture"}))
	assertAmount("rate-boundary", "missing", "missing", "missing")
	check(recorder.RecordHTTP(ctx, "rate-boundary", 200, "supplier-request", boundary))
	assertAmount("rate-boundary", "missing", "missing", "next")
	check(recorder.RecordUsage(ctx, "rate-boundary", UsageEvidence{CacheReadTokens: &cache, Source: "fixture"}))
	assertAmount("rate-boundary", "0.291", "USD", "next")
	legacy := "7"
	check(recorder.RecordUsage(ctx, "rate-boundary", UsageEvidence{EstimatedAmount: &legacy, EstimatedCurrency: "CNY",
		RateSnapshot: map[string]any{"source": "routing_reference"}}))
	assertAmount("rate-boundary", "0.291", "USD", "next")
	for _, tc := range []struct {
		id, model string
		cred      int
		at        time.Time
	}{
		{"rate-expired", "m", 5011, end},
		{"rate-wrong-model", "m2", 5011, boundary},
		{"rate-wrong-credential", "m", 5013, boundary},
	} {
		attempt(tc.id, tc.cred, tc.model, tc.at)
		check(recorder.MarkSent(ctx, tc.id, tc.at))
		assertAmount(tc.id, "missing", "missing", "missing")
	}
	late := prior
	late.EffectiveFrom, late.Model, late.Reference = boundary, "late", "late-import"
	lateRate, err := ImportSupplierRate(ctx, pool, late, "fixture")
	check(err)
	_, err = pool.Exec(ctx, `UPDATE public.supplier_contract_rates SET imported_at=$1 WHERE rate_id=$2`, boundary.Add(time.Minute), lateRate.RateID)
	check(err)
	attempt("rate-late-import", 5011, "late", boundary)
	check(recorder.MarkSent(ctx, "rate-late-import", boundary))
	assertAmount("rate-late-import", "missing", "missing", "missing")
	invoice, err := ImportInvoice(ctx, pool, InvoiceInput{ProviderID: 501, AccountRef: "contract-account", InvoiceRef: "contract-day",
		Currency: "USD", Day: "2026-11-25", CredentialIDs: []int64{5011}, StatedTotal: "0.281", Lines: []InvoiceLine{
			{LineRef: "1", Kind: "request_total", ProviderRequestID: "supplier-request", Model: "m", Amount: "0.281"},
		}}, "fixture")
	check(err)
	invoiceReport, err := ReadInvoiceReport(ctx, pool, invoice.InvoiceID, 10, 0)
	check(err)
	assertReportFields(t, invoiceReport.Summary, map[string]string{"estimate_comparable_lines": "1", "estimated_minus_invoiced_amount": `"0.010"`})
	assertReportFields(t, invoiceReport.Lines[0], map[string]string{"estimate_basis": `"supplier_contract"`, "comparable_estimate": `"0.291"`})

	coverage, err := ReadReport(ctx, pool, ReportScope{TenantID: "supplier-contract-fixture", Platform: true},
		ReportQuery{Section: "coverage", Day: "2026-11-25", Timezone: DefaultMonthTimezone, Start: boundary, End: end, Limit: 10})
	check(err)
	assertReportFields(t, coverage.Summary, map[string]string{"recorded_sends": "4", "recorded_sends_without_contract_estimate": "3"})

	// A deployment with the evidence ledger available still records transports
	// while the additive contract table is unavailable.
	_, err = pool.Exec(ctx, `ALTER TABLE public.supplier_contract_rates RENAME TO supplier_contract_rates_unavailable_fixture`)
	check(err)
	defer func() {
		_, err := pool.Exec(context.WithoutCancel(ctx), `ALTER TABLE public.supplier_contract_rates_unavailable_fixture RENAME TO supplier_contract_rates`)
		check(err)
	}()
	attempt("rate-table-unavailable", 5011, "m", boundary)
	check(recorder.MarkSent(ctx, "rate-table-unavailable", boundary))
	var delivery string
	check(pool.QueryRow(ctx, `SELECT delivery FROM public.provider_call_attempts WHERE attempt_id='rate-table-unavailable'`).Scan(&delivery))
	if delivery != "sent" {
		t.Fatalf("expected confirmed transport during rate lookup failure, got %s", delivery)
	}
}

func TestSupplierRateValidation(t *testing.T) {
	now := time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC)
	for _, invalid := range []SupplierComponent{
		{Meter: "image_count", Unit: 1, Tiers: []SupplierTier{{Price: "0.01"}}},
		{Meter: "confirmed_send", Unit: 1000, Tiers: []SupplierTier{{Price: "0.01"}}},
		{Meter: "output_tokens", Unit: 7, Tiers: []SupplierTier{{Price: "0.01"}}},
		{Meter: "input_tokens", Unit: 1, Tiers: []SupplierTier{{Price: "-0.01"}}},
	} {
		t.Run(fmt.Sprintf("%s/%d", invalid.Meter, invalid.Unit), func(t *testing.T) {
			_, err := normalizeSupplierRate(SupplierRateInput{ProviderID: 1, CredentialID: 2, Currency: "USD", Model: "m",
				Reference: "fixture", EffectiveFrom: now, Terms: SupplierTerms{Components: []SupplierComponent{invalid}}})
			if !errors.Is(err, ErrInvalidSupplierRate) {
				t.Fatalf("invalid supplier rate accepted: %+v, %v", invalid, err)
			}
		})
	}
}

func testSupplierMediaRates(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(ctx, `INSERT INTO public.credentials (id,provider_id) VALUES (5031,503)`)
	check(err)
	day := "2026-11-25"
	start, end, err := NaturalDay(day, DefaultMonthTimezone)
	check(err)
	var scale = 4
	for _, tt := range []struct {
		model, meter, price string
		unit                int64
		roundTo             *int
	}{
		{"image", "generated_images", "0.0125", 1, nil},
		{"video", "video_output_duration_us", "1.2", 1_000_000, nil},
		{"rounded-audio", "audio_billed_duration_us", "1", 60_000_000, &scale},
		{"exact-audio", "audio_billed_duration_us", "1", 60_000_000, nil},
	} {
		_, err := ImportSupplierRate(ctx, pool, SupplierRateInput{ProviderID: 503, CredentialID: 5031, Model: tt.model,
			Reference: "media-fixture", Currency: "USD", EffectiveFrom: start,
			Terms: SupplierTerms{AmountScale: tt.roundTo, Components: []SupplierComponent{
				{Meter: tt.meter, Unit: tt.unit, Tiers: []SupplierTier{{Price: tt.price}}},
			}}}, "fixture")
		check(err)
	}
	_, err = pool.Exec(ctx, `UPDATE public.supplier_contract_rates SET imported_at=$1 WHERE provider_id=503`, start.Add(-time.Hour))
	check(err)
	var three, zero, second, clip int64 = 3, 0, 1_000_000, 4_125_000
	recorder := NewAttemptRecorder(pool)
	for _, tt := range []struct {
		id, model, source, price string
		media                    MediaUsageEvidence
	}{
		{"measured-images", "image", "images_generations", "0.0375", MediaUsageEvidence{GeneratedImages: &three, Source: "provider_image_assets"}},
		{"zero-images", "image", "images_generations", "0", MediaUsageEvidence{GeneratedImages: &zero, Source: "provider_image_assets"}},
		{"missing-image-quantity", "image", "images_generations", "missing", MediaUsageEvidence{}},
		{"measured-video", "video", "videos_generations", "4.95", MediaUsageEvidence{VideoOutputDurationUS: &clip, Source: "provider_completed_video"}},
		{"rounded-minute", "rounded-audio", "audio_transcription_multipart", "0.0167", MediaUsageEvidence{AudioBilledDurationUS: &second, Source: "provider_transcription"}},
		{"no-rounding-policy", "exact-audio", "audio_transcription_multipart", "missing", MediaUsageEvidence{AudioBilledDurationUS: &second, Source: "provider_transcription"}},
		{"missing-billed-duration", "rounded-audio", "audio_transcription_multipart", "missing", MediaUsageEvidence{AudioInputDurationUS: &second, Source: "provider_transcription"}},
	} {
		check(recorder.Prepare(ctx, Attempt{ID: tt.id, ProviderID: 503, CredentialID: 5031, Model: tt.model,
			AttemptNo: 1, TrafficClass: "business", TenantID: "media-rate-fixture", RequestID: tt.id, Source: tt.source,
			PreparedAt: start.Add(-time.Second)}))
		check(recorder.MarkSent(ctx, tt.id, start))
		check(recorder.RecordUsage(ctx, tt.id, UsageEvidence{Media: tt.media}))
		// An older token-only observation cannot replace a measured quantity.
		check(recorder.RecordUsage(ctx, tt.id, UsageEvidence{Source: "fixture"}))
		var amount string
		check(pool.QueryRow(ctx, `SELECT COALESCE(estimated_amount::text, 'missing') FROM public.provider_call_attempts WHERE attempt_id=$1`,
			tt.id).Scan(&amount))
		if amount != tt.price {
			t.Fatalf("%s amount %s, expected %s", tt.id, amount, tt.price)
		}
	}
	scope := ReportScope{TenantID: "media-rate-fixture", Platform: true}
	query := ReportQuery{Section: "attempts", Day: day, Timezone: DefaultMonthTimezone, Start: start, End: end, Limit: 20}
	attempts, err := ReadReport(ctx, pool, scope, query)
	check(err)
	assertReportFields(t, attempts.Summary, map[string]string{"sent": "7", "measured_generated_images": "3",
		"measured_video_output_duration_us": "4125000", "measured_audio_billed_duration_us": "2000000", "attempts_with_contract_estimate": "4"})
	query.Section = "coverage"
	coverage, err := ReadReport(ctx, pool, scope, query)
	check(err)
	assertReportFields(t, coverage.Summary, map[string]string{"recorded_media_sends": "7", "recorded_media_sends_without_quantity": "1"})
}
