package streaming

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestRequestLogContext_BuildFailureEntry_ClientRequestID asserts that
// the 2026-06-26 client-request-id propagation works end-to-end inside
// the streaming package: a failure entry produced by EmitFailure must
// carry the client-supplied id on telemetry.RequestLogEntry.ClientRequestID
// so request_logs.client_request_id is populated when the row is persisted.
//
// Regression context: the original bug let a client retry 5× with the
// same X-Request-Id and produce 5 rows in request_logs sharing one
// request_id. The fix introduces client_request_id as a separate
// column for the client value; this test guards that propagation.
func TestPreferCapturedBody_UsesContextSnapshotWhenPrimaryMissing(t *testing.T) {
	captured := []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hello"}]}`)
	if got := preferCapturedBody(nil, captured); string(got) != string(captured) {
		t.Fatalf("preferCapturedBody(nil, captured) = %q, want captured body", got)
	}
	primary := []byte(`{"model":"converted"}`)
	if got := preferCapturedBody(primary, captured); string(got) != string(primary) {
		t.Fatalf("preferCapturedBody(primary, captured) = %q, want primary body", got)
	}
}

func TestRequestLogContext_BuildFailureEntry_ClientRequestID(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.1"}`))
	r.Header.Set("X-Request-Id", "client-retry-XYZ")
	r.Header.Set("X-Gw-Client-Request-Id", "client-retry-XYZ")

	ctx := ch.NewRequestLogContext(r, "server-uuid-1", time.Now())
	ctx.ClientRequestID = "client-retry-XYZ" // what the middleware would set
	ctx.Body = []byte(`{"model":"glm-5.1"}`)
	ctx.SetClientModel("glm-5.1")
	ctx.SetKey(&authentication.KeyInfo{ID: 1, TenantID: "default"})

	entry := ctx.BuildFailureEntry("transient", "upstream transient", nil, nil)
	if entry == nil {
		t.Fatal("nil entry")
	}
	if entry.RequestID != "server-uuid-1" {
		t.Fatalf("RequestID=%q, want server-uuid-1", entry.RequestID)
	}
	if entry.ClientRequestID == nil || *entry.ClientRequestID != "client-retry-XYZ" {
		t.Fatalf("ClientRequestID=%v, want client-retry-XYZ", entry.ClientRequestID)
	}
}

// TestRequestLogContext_BuildFailureEntry_EmptyClientRequestID covers
// the no-client-header case: ClientRequestID must be a nil pointer (NOT
// &"") so the SQL COALESCE writes NULL rather than an empty string,
// keeping the partial index clean.
func TestRequestLogContext_BuildFailureEntry_EmptyClientRequestID(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.1"}`))
	// No X-Gw-Client-Request-Id set on the request.

	ctx := ch.NewRequestLogContext(r, "server-uuid-2", time.Now())
	ctx.Body = []byte(`{"model":"glm-5.1"}`)
	ctx.SetClientModel("glm-5.1")
	ctx.SetKey(&authentication.KeyInfo{ID: 1, TenantID: "default"})

	entry := ctx.BuildFailureEntry("transient", "upstream transient", nil, nil)
	if entry == nil {
		t.Fatal("nil entry")
	}
	if entry.ClientRequestID != nil {
		t.Fatalf("ClientRequestID must be nil when no client header was sent, got %v", *entry.ClientRequestID)
	}
}

func TestRequestLogContext_BuildFailureEntry_PersistsOutboundBody(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"minimax-m3"}`))
	ctx := ch.NewRequestLogContext(r, "server-uuid-outbound", time.Now())
	ctx.Body = []byte(`{"model":"minimax-m3"}`)
	ctx.OutboundBody = []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"hello"}]}`)

	entry := ctx.BuildFailureEntry("provider_error", "upstream failed", nil, nil)
	if entry == nil || entry.OutboundBody == nil {
		t.Fatal("failure entry must retain the body sent upstream")
	}
	if string(entry.OutboundBody) != string(ctx.OutboundBody) {
		t.Fatalf("OutboundBody=%s, want %s", entry.OutboundBody, ctx.OutboundBody)
	}
}

func TestRequestLogContext_BuildFailureEntry_EventAtUsesStartPlusLatency(t *testing.T) {
	before := time.Now().UTC()
	ctx := &RequestLogContext{
		RequestID: "req-1",
		StartTime: before.Add(-250 * time.Millisecond),
	}
	entry := ctx.BuildFailureEntry("transient", "upstream transient", nil, nil)
	if entry == nil || entry.EventAt == nil {
		t.Fatal("expected EventAt on failure entry")
	}
	after := time.Now().UTC()
	if entry.EventAt.Before(before) || entry.EventAt.After(after.Add(100*time.Millisecond)) {
		t.Fatalf("EventAt=%s want between %s and %s", entry.EventAt.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano), after.Format(time.RFC3339Nano))
	}
}

// TestRequestLogContext_RateLimitedStatus asserts the rate-limit vs failure
// status split: gateway RPM/throttle rejections must record
// request_status="rate_limited" (not "failure") so dashboards can exclude
// them from provider error counts while still keeping them in the success-rate
// denominator. Success stays false in both cases — the request did not
// complete — but the status category is what separates "client was rate
// limited" from "system error".
func TestRequestLogContext_RateLimitedStatus(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"minimax-m3"}`))

	ctx := ch.NewRequestLogContext(r, "server-uuid-rl", time.Now())
	ctx.Body = []byte(`{"model":"minimax-m3"}`)
	ctx.SetClientModel("minimax-m3")
	ctx.SetKey(&authentication.KeyInfo{ID: 1, TenantID: "default"})

	// A genuine failure entry stays "failure".
	failEntry := ctx.BuildFailureEntry("transient", "upstream transient", nil, nil)
	if failEntry == nil || failEntry.RequestStatus == nil {
		t.Fatal("nil failure entry / status")
	}
	if *failEntry.RequestStatus != "failure" {
		t.Fatalf("BuildFailureEntry status=%q, want failure", *failEntry.RequestStatus)
	}
	if failEntry.Success {
		t.Fatal("failure entry must have Success=false")
	}

	// A rate-limited entry uses the dedicated status.
	rlEntry := ctx.buildEntry("rate_limit_exceeded", "rate limit exceeded", nil, nil, "rate_limited")
	if rlEntry == nil || rlEntry.RequestStatus == nil {
		t.Fatal("nil rate-limited entry / status")
	}
	if *rlEntry.RequestStatus != "rate_limited" {
		t.Fatalf("rate-limited status=%q, want rate_limited", *rlEntry.RequestStatus)
	}
	if rlEntry.Success {
		t.Fatal("rate-limited entry must have Success=false (request did not complete)")
	}
	// ErrorKind is preserved so the specific cause (rpm vs throttle) is queryable.
	if rlEntry.ErrorKind == nil || *rlEntry.ErrorKind != "rate_limit_exceeded" {
		t.Fatalf("ErrorKind=%v, want rate_limit_exceeded", *rlEntry.ErrorKind)
	}
}

// TestApplySessionCompressorFields_OutboundBodyPersistedWithoutCompression
// asserts the 2026-07-27 fix: when no session-compression strategy fired
// (delta-only / fresh session), but the handler populated OutboundBody
// from executor's result.RequestBody, the entry must still carry the body
// so the admin UI's v3 转发体 tab is non-empty.
//
// Regression context: request d94fd76c5880ea12b229db681bc1b83b (minimax-m3,
// 23K prompt tokens, completion=9) had outbound_msg_count=10 and
// outbound_token_est=25701 set, but outbound_body was JSONB null —
// admin UI showed an empty v3 转发体 tab. Root cause: handler.go:2244
// guarded OutboundBody persistence on scResult.CompressionStrategy != "",
// so non-compression requests lost their upstream body.
func TestApplySessionCompressorFields_OutboundBodyPersistedWithoutCompression(t *testing.T) {
	entry := &telemetry.RequestLogEntry{}
	c := &RequestLogContext{}
	// No compression strategy fired (delta-only or fresh session).
	c.OutboundStrategy = ""
	c.OutboundBody = []byte(`{"model":"MiniMax-M3","messages":[{"role":"user","content":"hi"}]}`)
	msgCount := 10
	c.OutboundMsgCount = &msgCount
	c.OutboundTokenEst = outboundIntPtr(25701)

	applySessionCompressorFields(entry, c)

	if entry.OutboundBody == nil {
		t.Fatal("OutboundBody must be populated even when CompressionStrategy is empty " +
			"(regression: handler.go:3123 fallback now writes result.RequestBody)")
	}
	if string(entry.OutboundBody) != string(c.OutboundBody) {
		t.Fatalf("OutboundBody=%s, want %s", entry.OutboundBody, c.OutboundBody)
	}
	if entry.OutboundMsgCount == nil || *entry.OutboundMsgCount != 10 {
		t.Fatalf("OutboundMsgCount=%v, want 10", entry.OutboundMsgCount)
	}
	if entry.OutboundTokenEst == nil || *entry.OutboundTokenEst != 25701 {
		t.Fatalf("OutboundTokenEst=%v, want 25701", entry.OutboundTokenEst)
	}
	// Without compression strategy, no compression_meta merge should happen.
	if entry.CompressionStrategy != nil && *entry.CompressionStrategy != "" {
		t.Fatalf("CompressionStrategy=%v, want nil/empty when no compression fired", entry.CompressionStrategy)
	}
}

// TestApplySessionCompressorFields_CompressionKeepsHashes verifies that
// compression-fired requests still get MsgHashes + Strategy copied through.
func TestApplySessionCompressorFields_CompressionKeepsHashes(t *testing.T) {
	entry := &telemetry.RequestLogEntry{}
	c := &RequestLogContext{}
	c.OutboundStrategy = "mechanical_trim"
	c.OutboundBody = []byte(`{"model":"MiniMax-M3","messages":[]}`)
	c.OutboundMsgHashes = json.RawMessage(`["abc","def"]`)
	c.OutboundSummaryMarker = "smm_v1:abc"
	c.OutboundWindowTriggered = "sliding_window_overflow"
	msgCount := 5
	c.OutboundMsgCount = &msgCount
	c.OutboundTokenEst = outboundIntPtr(12000)

	applySessionCompressorFields(entry, c)

	if entry.OutboundBody == nil {
		t.Fatal("OutboundBody must be set on compression path too")
	}
	if entry.OutboundMsgHashes == nil {
		t.Fatal("OutboundMsgHashes must be set when compression fired")
	}
	if entry.CompressionStrategy == nil || *entry.CompressionStrategy != "mechanical_trim" {
		t.Fatalf("CompressionStrategy=%v, want mechanical_trim", entry.CompressionStrategy)
	}
}

func outboundIntPtr(v int) *int { return &v }

func TestRequestLogContextTerminalGateCompetingOutcomes(t *testing.T) {
	ctx := &RequestLogContext{}
	var won atomic.Int64
	var wg sync.WaitGroup
	for _, kind := range []string{"success", "failure", "disconnect"} {
		wg.Add(1)
		go func(kind string) {
			defer wg.Done()
			if ctx.SetTerminal(kind, nil) {
				won.Add(1)
			}
		}(kind)
	}
	wg.Wait()
	if won.Load() != 1 || !ctx.IsTerminal() {
		t.Fatalf("terminal gate winners=%d terminal=%v", won.Load(), ctx.IsTerminal())
	}
}

// TestRequestLogContext_KeyInfoParity (2026-08-06) — the audit of the
// dc767386f... end-user fix surfaced a parity risk between success-path
// emitTelemetry and failure-path buildEntry. They live in different
// files and populate the api-key display fields through different code
// paths (applyKeyInfoToRequestLog vs enrichRequestLogFromMeta). If one
// path stops calling its enrichment helper — e.g. someone refactors
// fillAttemptMeta — the two rows would silently diverge.
//
// This test pins the contract by building a synthetic keyInfo and
// asserting BOTH builders produce identical values for the api-key
// display fields. The investigation confirmed parity holds when keyInfo
// is fully populated; this test now locks it down for future refactors.
//
// The success-path emitTelemetry builder is in handler.go and not
// reachable from this package without wiring an executor + audit
// event — too heavy for a unit test. We approximate it with the same
// field list (the literal in emitTelemetry) so that we exercise the
// SUCCESS-LIKE construction inline. The failure path is exercised
// through BuildFailureEntry which calls the real buildEntry.
//
// Both builders must agree on these 7 fields:
//   - TenantID
//   - ApplicationID
//   - APIKeyID
//   - APIKeyPrefix
//   - APIKeyOwnerUser
//   - ApplicationCode
//   - EndUserID (covered by end-user fix Round-1+2; sanity-checked here)
func TestRequestLogContext_KeyInfoParity(t *testing.T) {
	var (
		tenantID       = "tenant-acme"
		apiKeyID       = 42
		applicationID  = 7
		keyPrefix      = "sk-acme-1a2b"
		ownerUser      = "ops@acme.com"
		appCode        = "PROD"
		endUser        = "alice@acme.com"
	)
	ki := &authentication.KeyInfo{
		ID:              apiKeyID,
		TenantID:        tenantID,
		ApplicationID:   applicationID,
		ApplicationCode: appCode,
		KeyPrefix:       keyPrefix,
		OwnerUser:       &ownerUser,
	}

	// ---- failure path: BuildFailureEntry ----
	// Use a body that contains "user":"alice@acme.com" so the failure-path
	// resolveEndUser() picks it up via extractEndUserFromBody. This
	// matches the realistic production case (a typed reqBody.User).
	bodyWithUser := `{"model":"glm-5.1","user":"alice@acme.com"}`
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(bodyWithUser))
	r.Header.Set("X-Request-Id", "client-req-XYZ")
	failCtx := ch.NewRequestLogContext(r, "server-uuid-fail", time.Now())
	failCtx.Body = []byte(bodyWithUser)
	failCtx.SetClientModel("glm-5.1")
	failCtx.SetKey(ki)

	failEntry := failCtx.BuildFailureEntry("transient", "upstream transient", nil, nil)
	if failEntry == nil {
		t.Fatal("nil failure entry")
	}

	// ---- success path: emulate the literal emitted by emitTelemetry
	// (handler.go:3929) so we exercise the SAME field set the success
	// builder uses, without the full executor wiring. The literal
	// intentionally leaves APIKeyPrefix / APIKeyOwnerUser / ApplicationCode
	// as nil pointers — applyKeyInfoToRequestLog below populates them
	// from keyInfo (and adds the "***" display suffix on the prefix).
	// Setting them here would be a no-op overwritten by the helper.
	now := time.Now()
	successEntry := &telemetry.RequestLogEntry{
		RequestID:     "server-uuid-success",
		TenantID:      ki.TenantID,
		ApplicationID: &applicationID,
		APIKeyID:      &apiKeyID,
		EndUserID:     strPtr(endUser),
		ClientModel:   strPtr("glm-5.1"),
		Success:       true,
		RequestStatus: strPtr(telemetry.RequestStatusSuccess),
		EventAt:       &now,
	}
	applyKeyInfoToRequestLog(successEntry, ki)
	enrichRequestLogFromMeta(successEntry, ki, &failCtx.meta)
	successEntry.EndUserID = strPtr(endUser)

	// ---- parity assertions: both rows must carry the same display fields.
	check := func(field string, failVal, succVal *string) {
		t.Helper()
		switch {
		case failVal == nil && succVal == nil:
			return
		case failVal == nil && succVal != nil:
			t.Errorf("%s: FAILURE row is nil but SUCCESS row is %q (parity lost)", field, *succVal)
		case failVal != nil && succVal == nil:
			t.Errorf("%s: FAILURE row is %q but SUCCESS row is nil (parity lost)", field, *failVal)
		case *failVal != *succVal:
			t.Errorf("%s: FAILURE row is %q but SUCCESS row is %q (parity lost)", field, *failVal, *succVal)
		}
	}
	check("ApplicationID", intStrPtr(failEntry.ApplicationID), intStrPtr(successEntry.ApplicationID))
	check("APIKeyID", intStrPtr(failEntry.APIKeyID), intStrPtr(successEntry.APIKeyID))
	check("APIKeyPrefix", failEntry.APIKeyPrefix, successEntry.APIKeyPrefix)
	check("APIKeyOwnerUser", failEntry.APIKeyOwnerUser, successEntry.APIKeyOwnerUser)
	check("ApplicationCode", failEntry.ApplicationCode, successEntry.ApplicationCode)
	check("EndUserID", failEntry.EndUserID, successEntry.EndUserID)
	check("TenantID", strPtr(failEntry.TenantID), strPtr(successEntry.TenantID))
}

// TestRequestLogContext_BuildFailureEntry_KeyMetaParity (2026-08-06) —
// granular check: even when keyInfo is populated, the failure-path
// buildEntry must produce the SAME api-key display fields as the
// success path. This is the regression that the audit agent flagged.
//
// Pre-fix observation: buildEntry never directly sets APIKeyPrefix /
// APIKeyOwnerUser / ApplicationCode — those flow in only via
// enrichRequestLogFromMeta + meta (populated by refreshMeta →
// fillAttemptMeta → resolveKeyMeta). If any of those helpers stops
// running, the failure-path row silently loses the fields while the
// success path keeps them. This test pins that contract.
//
// If this test ever fails after a refactor, the fix is to call
// applyKeyInfoToRequestLog directly inside buildEntry (or pass an
// already-populated meta).
func TestRequestLogContext_BuildFailureEntry_KeyMetaParity(t *testing.T) {
	var (
		keyPrefix = "sk-test-X1Y2Z3"
		ownerUser = "billing@example.com"
		appCode   = "staging"
	)
	ki := &authentication.KeyInfo{
		ID:              100,
		TenantID:        "default",
		ApplicationID:   5,
		ApplicationCode: appCode,
		KeyPrefix:       keyPrefix,
		OwnerUser:       &ownerUser,
	}

	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	r.Header.Set("X-Request-Id", "client-req-meta")

	ctx := ch.NewRequestLogContext(r, "server-uuid-meta", time.Now())
	ctx.Body = []byte(`{"model":"x"}`)
	ctx.SetClientModel("x")
	ctx.SetKey(ki)

	entry := ctx.BuildFailureEntry("auth_unavailable", "auth service down", nil, nil)
	if entry == nil {
		t.Fatal("nil entry")
	}

	if entry.APIKeyPrefix == nil || *entry.APIKeyPrefix != keyPrefix+"***" {
		t.Errorf("APIKeyPrefix = %v, want %q (failure-path parity lost; enrichRequestLogFromMeta didn't carry meta.APIKeyPrefix across)", entry.APIKeyPrefix, keyPrefix+"***")
	}
	if entry.APIKeyOwnerUser == nil || *entry.APIKeyOwnerUser != ownerUser {
		t.Errorf("APIKeyOwnerUser = %v, want %q (failure-path parity lost; enrichRequestLogFromMeta didn't carry meta.APIKeyOwnerUser across)", entry.APIKeyOwnerUser, ownerUser)
	}
	if entry.ApplicationCode == nil || *entry.ApplicationCode != appCode {
		t.Errorf("ApplicationCode = %v, want %q (failure-path parity lost; enrichRequestLogFromMeta didn't carry meta.ApplicationCode across)", entry.ApplicationCode, appCode)
	}
}

// intStrPtr returns a pointer to the decimal string of an *int pointer.
// Used by the parity check to compare ApplicationID / APIKeyID. Uses
// stdlib strconv.Itoa — the previous hand-rolled itoa was a premature
// micro-optimisation that hid intent.
func intStrPtr(v *int) *string {
	if v == nil {
		return nil
	}
	s := strconv.Itoa(*v)
	return &s
}
