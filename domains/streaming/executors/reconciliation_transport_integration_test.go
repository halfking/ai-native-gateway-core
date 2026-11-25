package executors_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/internal/httpwrite"
	"github.com/kaixuan/llm-gateway-go/maas"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/storage/sqlite"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// This test starts an HTTP server on loopback and exercises the executor,
// actual SQLite outbox, canonical migrations and normal report query.
func TestStreamingTransportRetryPreservesBothAttempts(t *testing.T) {
	dsn := os.Getenv("RECONCILIATION_TRANSPORT_TEST_DSN")
	if dsn == "" {
		t.Skip("set RECONCILIATION_TRANSPORT_TEST_DSN to an empty reconciliation_transport_fixture_* database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("invalid fixture connection")
	}
	defer db.Close()
	var database string
	var tables int
	err = db.QueryRow(ctx, `SELECT current_database(), count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&database, &tables)
	if err != nil || !strings.HasPrefix(database, "reconciliation_transport_fixture_") || tables != 0 {
		t.Fatal("dedicated empty transport fixture required")
	}
	apply := func(sql string) {
		t.Helper()
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	apply(`CREATE TABLE schema_migrations (version text PRIMARY KEY, description text);
		CREATE TABLE tenants (code varchar(64) PRIMARY KEY, name varchar(128) NOT NULL);
		CREATE SEQUENCE public.credit_ledger_partitioned_id_seq;
		CREATE TABLE credentials (id bigint PRIMARY KEY, provider_id bigint NOT NULL);
		INSERT INTO credentials VALUES (7001,701)`)
	for _, name := range []string{
		"objects/functions/get_current_tenant.sql",
		"objects/tables/tenant_credit_wallets.sql",
		"objects/constraints/tenant_credit_wallets_tenant_credit_wallets_pkey.sql",
		"objects/tables/tenant_subscriptions.sql",
		"objects/tables/maas_settings.sql",
		"objects/tables/credit_ledger.sql",
		"objects/tables/credit_ledger_hot.sql",
		"migrations/startup/389_maas_credit_consumption_buckets.sql",
		"migrations/startup/935_credit_ledger_owner_campaign_detail.sql",
		"migrations/startup/982_provider_call_attempts.sql",
		"migrations/startup/984_request_reconciliation_links.sql",
		"migrations/startup/985_provider_attempt_media_usage.sql",
		"migrations/startup/986_provider_invoice_evidence.sql",
		"migrations/startup/987_provider_invoice_usage_buckets.sql",
		"migrations/startup/988_supplier_contract_rates.sql",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "sql", name))
		if err != nil {
			t.Fatal(err)
		}
		apply(strings.ReplaceAll(string(data), `\set ON_ERROR_STOP on`, ""))
	}
	const tenant, requestID = "reconciliation-fixture", "gateway-business-request"
	apply(`INSERT INTO tenants VALUES ('reconciliation-fixture','Transport fixture');
		INSERT INTO tenant_credit_wallets (tenant_id, granted_balance, purchased_balance, balance_credits)
		VALUES ('reconciliation-fixture',100,0,100);
		INSERT INTO maas_settings (base_credits_per_1m, base_credits_per_1m_out, base_credits_per_1m_cache_in)
		VALUES (100000,80000,10000)`)
	const model = "retry-evidence-test"
	_, err = reconciliation.ImportSupplierRate(ctx, db, reconciliation.SupplierRateInput{
		ProviderID: 701, CredentialID: 7001, Model: model, Currency: "USD", Reference: "fixture-contract",
		EffectiveFrom: time.Now().Add(-time.Hour).Truncate(time.Second),
		Terms: reconciliation.SupplierTerms{Components: []reconciliation.SupplierComponent{
			{Meter: "input_tokens", Unit: 1_000_000, Tiers: []reconciliation.SupplierTier{{Price: "100"}}},
			{Meter: "output_tokens", Unit: 1_000_000, Tiers: []reconciliation.SupplierTier{{Price: "100"}}},
		}},
	}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := reconciliation.OpenAttemptOutbox(ctx, t.TempDir(), db, sqlite.OpenJournal)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()

	var calls atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"id":"provider-failed","error":{"message":"provider unavailable"},"usage":{"prompt_tokens":10,"completion_tokens":0}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "provider-success")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"provider-success\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture-success\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"provider-success\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"prompt_tokens_details\":{\"cached_tokens\":40}}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer vendor.Close()

	executor := newProtocolFailoverExecutor(t)
	executor.Upstream = upstream.NewWithRetries(1)
	defer executor.Upstream.Stop()
	executor.SetAttemptRecorder(outbox)
	executor.StreamChat = func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, _ string,
		_ executors.NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) executors.StreamOutcome {
		out := streaming.StreamChatWithPendingCapture(ctx, w, resp, clientModel, outboundModel, streaming.NewNormalizer(), capture, toolsRequested, nil, nil)
		return executors.StreamOutcome{Interrupted: out.Interrupted, Reason: out.Reason, ChunkCount: out.ChunkCount, Kind: out.Kind}
	}
	w := httptest.NewRecorder()
	start := time.Now()
	capture := audit.NewStreamCapture()
	result, err := executor.Execute(&executors.ExecParams{
		W: w, R: httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes: []byte(`{"model":"retry-evidence-test","messages":[{"role":"user","content":"hello"}],"stream":true}`),
		Model:     model, ClientModel: model, ClientProtocol: "openai-completions", IsStream: true, Capture: capture,
		ClientID: identity.ClientIdentity{IdentityHash: "retry-evidence"},
		TenantID: tenant, RequestID: requestID, AttemptBusinessKey: true,
		Candidates: []provider.Candidate{protocolCandidate(vendor.URL, "openai-completions", "openai", model, "fixture-only", 701, 7001)},
		Policy:     &provider.Policy{TierFallbackMax: 1, RetryPerCredential: 0},
	})
	if err != nil || result == nil || calls.Load() != 2 || !strings.Contains(w.Body.String(), "fixture-success") {
		t.Fatalf("execute failed: result=%v calls=%d response=%s error=%v", result != nil, calls.Load(), w.Body.String(), err)
	}
	wait := time.NewTicker(25 * time.Millisecond)
	defer wait.Stop()
	for {
		var terminal int
		err := db.QueryRow(ctx, `SELECT count(*) FROM public.provider_call_attempts
			WHERE request_id=$1 AND outcome IN ('success','failure')`, requestID).Scan(&terminal)
		if err != nil {
			t.Fatal(err)
		}
		if terminal == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("attempt outbox did not project both terminal outcomes")
		case <-wait.C:
		}
	}
	report, err := reconciliation.ReadReport(ctx, db, reconciliation.ReportScope{Platform: true, TenantID: tenant},
		reconciliation.ReportQuery{Start: start.Add(-time.Second), End: time.Now().Add(time.Second), Timezone: "UTC", Section: "attempts", Limit: 5})
	if err != nil || len(report.Rows) != 2 {
		t.Fatalf("attempt report rows=%d error=%v", len(report.Rows), err)
	}
	checkAttemptFields(t, report.Summary, map[string]string{"sent": "2", "business_sent": "2", "input_tokens": "110", "output_tokens": "20"})
	checkAttemptFields(t, report.Rows[0], map[string]string{
		"request_id": `"gateway-business-request"`, "provider_request_id": `"provider-failed"`,
		"outcome": `"failure"`, "input_tokens": "10", "output_tokens": "0", "estimated_amount": `"0.001"`, "reported_amount": "null",
	})
	checkAttemptFields(t, report.Rows[1], map[string]string{
		"request_id": `"gateway-business-request"`, "provider_request_id": `"provider-success"`,
		"outcome": `"success"`, "input_tokens": "100", "output_tokens": "20", "cache_read_tokens": "40", "estimated_amount": `"0.012"`,
	})
	var probeSends atomic.Int32
	probeVendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/warm" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read platform fixture request: %v", err)
		}
		if probeSends.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack platform fixture connection: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"platform-probe","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":6,"completion_tokens":1,"total_tokens":7}}`)
	}))
	defer probeVendor.Close()
	probeTransport := &http.Transport{DialContext: new(net.Dialer).DialContext}
	httpwrite.TrackTransport(probeTransport)
	defer probeTransport.CloseIdleConnections()
	probeClient := &http.Client{Transport: &fixtureIdempotentTransport{probeTransport}, Timeout: 3 * time.Second}
	warm, err := probeClient.Get(probeVendor.URL + "/warm")
	if err != nil {
		t.Fatal(err)
	}
	_ = warm.Body.Close()
	bg.SetAttemptRecorder(outbox)
	t.Cleanup(func() { bg.SetAttemptRecorder(nil) })
	probeExecutor := bg.NewActiveProbeExecutor(nil, nil, nil, 3000)
	probeExecutor.SetHTTPClient(probeClient)
	probe := probeExecutor.Run(ctx, &bg.ProbeTarget{
		BaseURL: probeVendor.URL, Protocol: "openai-completions", RawModel: model, APIKey: "fixture-only", ProviderID: 701, CredentialID: 7001,
	})
	if probe.Status != bg.ProbeStatusSuccess || probeSends.Load() != 2 {
		t.Fatalf("probe status=%s sends=%d error=%s", probe.Status, probeSends.Load(), probe.ErrMsg)
	}
	for {
		var measured bool
		err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public.provider_call_attempts
			WHERE provider_request_id='platform-probe' AND estimated_amount=0.0007)`).Scan(&measured)
		if err != nil {
			t.Fatal(err)
		}
		if measured {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("platform supplier estimate did not project")
		case <-wait.C:
		}
	}
	platformReport, err := reconciliation.ReadReport(ctx, db, reconciliation.ReportScope{Platform: true},
		reconciliation.ReportQuery{Start: start.Add(-time.Second), End: time.Now().Add(time.Second), Timezone: "UTC", Section: "attempts", Limit: 5})
	if err != nil || len(platformReport.Rows) != 4 {
		t.Fatalf("attempt report rows=%d error=%v", len(platformReport.Rows), err)
	}
	checkAttemptFields(t, platformReport.Summary, map[string]string{"sent": "4", "platform_sent": "2", "business_sent": "2", "measured_input_attempts": "3"})
	checkAttemptFields(t, platformReport.Rows[2], map[string]string{
		"provider_request_id": "null", "outcome": `"unknown"`, "delivery": `"sent"`, "input_tokens": "null", "estimated_amount": "null",
	})
	checkAttemptFields(t, platformReport.Rows[3], map[string]string{
		"request_id": "null", "tenant_id": "null", "client_id": `"platform-automation"`, "provider_request_id": `"platform-probe"`,
		"input_tokens": "6", "output_tokens": "1", "estimated_amount": `"0.0007"`, "estimate_basis": `"supplier_contract"`,
		"source": `"transport_replay:active_probe_ping"`, "attempt_no": "2",
	})
	// Choose a day containing the sends even when the test straddles UTC midnight.
	zone := time.UTC
	if start.UTC().Format(time.DateOnly) != time.Now().UTC().Format(time.DateOnly) {
		zone, err = time.LoadLocation("Asia/Shanghai")
		if err != nil {
			t.Fatal(err)
		}
	}
	invoice, err := reconciliation.ImportInvoice(ctx, db, reconciliation.InvoiceInput{
		ProviderID: 701, AccountRef: "fixture-account", InvoiceRef: "transport-retry",
		Day: start.In(zone).Format(time.DateOnly), Timezone: zone.String(), Currency: "USD",
		CredentialIDs: []int64{7001}, StatedTotal: "0.018",
		Lines: []reconciliation.InvoiceLine{
			{LineRef: "a", Kind: "request_total", ProviderRequestID: "provider-failed", Amount: "0.002"},
			{LineRef: "b", Kind: "request_total", ProviderRequestID: "provider-success", Amount: "0.015"},
			{LineRef: "c", Kind: "request_total", ProviderRequestID: "platform-probe", Amount: "0.001"},
		},
	}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	bill, err := reconciliation.ReadInvoiceReport(ctx, db, invoice.InvoiceID, 5, 0)
	if err != nil || len(bill.Lines) != 3 {
		t.Fatalf("invoice lines=%d error=%v", len(bill.Lines), err)
	}
	checkAttemptFields(t, bill.Summary, map[string]string{
		"matched_lines": "3", "estimate_comparable_lines": "3", "estimated_minus_invoiced_amount": `"-0.0043"`,
	})

	finalUsage := capture.CurrentAttemptUsage()
	if finalUsage.PromptTokens == nil || finalUsage.CompletionTokens == nil || finalUsage.CacheReadTokens == nil {
		t.Fatalf("successful stream usage missing: %+v", finalUsage)
	}
	service := maas.NewService(db)
	for range 2 {
		charged, err := service.ChargeRequest(ctx, tenant, requestID, "", *finalUsage.PromptTokens, *finalUsage.CompletionTokens, *finalUsage.CacheReadTokens, 0)
		if err != nil || charged != 12 {
			t.Fatalf("charge or replay=%d, error=%v", charged, err)
		}
	}
	var balance int64
	if err := db.QueryRow(ctx, `SELECT balance_credits FROM tenant_credit_wallets WHERE tenant_id=$1`, tenant).Scan(&balance); err != nil || balance != 88 {
		t.Fatalf("one request should debit the wallet once: balance=%d error=%v", balance, err)
	}
	postings, err := reconciliation.ReadReport(ctx, db, reconciliation.ReportScope{TenantID: tenant},
		reconciliation.ReportQuery{Start: start.Add(-time.Second), End: time.Now().Add(time.Second), Timezone: "UTC", Section: "postings", Limit: 5})
	if err != nil || len(postings.Rows) != 1 {
		t.Fatalf("posting report rows=%d error=%v", len(postings.Rows), err)
	}
	checkAttemptFields(t, postings.Summary, map[string]string{"postings": "1", "linked_client_requests": "1", "net_credits": "12"})
	checkAttemptFields(t, postings.Rows[0], map[string]string{"request_id": `"gateway-business-request"`, "net_credits": "12"})
}

type fixtureIdempotentTransport struct{ *http.Transport }

func (t *fixtureIdempotentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost {
		req = req.Clone(req.Context())
		req.Header.Set("Idempotency-Key", "transport-fixture")
	}
	return t.Transport.RoundTrip(req)
}

func checkAttemptFields(t *testing.T, row json.RawMessage, fields map[string]string) {
	t.Helper()
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(row, &parsed); err != nil {
		t.Fatal(err)
	}
	for key, want := range fields {
		if got := string(parsed[key]); got != want {
			t.Errorf("%s=%q want %q in %s", key, got, want, row)
		}
	}
}
