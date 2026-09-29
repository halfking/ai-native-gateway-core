package streaming

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	outputhook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// R25-A regression pins: the deferred capture writer must hold executor
// bytes off the wire until output policy approves them, preserve safe
// upstream headers on commit, and recompute Content-Length for a rewritten
// body. Before R25-A the chat non-stream path ran InterceptNonStream only
// after the executor had already written the response, which made
// ShouldBlock a no-op on the wire.
func TestDeferredNonStreamWriterHoldsThenCommitsApprovedBody(t *testing.T) {
	dw := newDeferredNonStreamWriter()
	dw.Header().Set("X-Upstream-Keep", "yes")
	dw.Header().Set("Content-Length", "999")
	dw.WriteHeader(201)
	if _, err := dw.Write([]byte(`{"choices":[{"message":{"content":"raw"}}]}`)); err != nil {
		t.Fatal(err)
	}

	approved := []byte(`{"choices":[{"message":{"content":"clean"}}]}`)
	rec := httptest.NewRecorder()
	dw.commit(rec, approved)
	if rec.Code != 201 {
		t.Fatalf("status = %d, want captured 201", rec.Code)
	}
	if got := rec.Body.String(); got != string(approved) {
		t.Fatalf("client body = %q, want approved rewrite", got)
	}
	if rec.Header().Get("X-Upstream-Keep") != "yes" {
		t.Fatal("safe upstream header lost on commit")
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(approved)) {
		t.Fatalf("Content-Length = %q, want recomputed length %d", got, len(approved))
	}

	// Passthrough commit: no rewrite → the captured bytes reach the client
	// untouched (fail-open path when the interceptor errors).
	dw2 := newDeferredNonStreamWriter()
	dw2.WriteHeader(200)
	if _, err := dw2.Write([]byte(`ok`)); err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	dw2.commit(rec2, nil)
	if rec2.Code != 200 || rec2.Body.String() != "ok" {
		t.Fatalf("passthrough commit = %d %q", rec2.Code, rec2.Body.String())
	}

	// Default status when the executor never called WriteHeader.
	dw3 := newDeferredNonStreamWriter()
	if _, err := dw3.Write([]byte(`x`)); err != nil {
		t.Fatal(err)
	}
	rec3 := httptest.NewRecorder()
	dw3.commit(rec3, nil)
	if rec3.Code != 200 {
		t.Fatalf("implicit status = %d, want 200", rec3.Code)
	}
}

// A blocking decision must be observable before anything is committed: the
// capture buffer holds the body, the interceptor reports blocked, and the
// wire stays empty until the handler writes its explicit policy rejection.
func TestChatNonStreamBlockDecisionHoldsBodyOffWire(t *testing.T) {
	chain := response.NewInterceptorChain(&nativeNonStreamInterceptor{blocked: true})
	dw := newDeferredNonStreamWriter()
	dw.WriteHeader(200)
	secret := []byte(`{"choices":[{"message":{"content":"secret"}}]}`)
	if _, err := dw.Write(secret); err != nil {
		t.Fatal(err)
	}

	opt := nativeResponseInterception{
		ctx: context.Background(),
		request: response.InterceptRequest{
			SessionID:      "gw-r25",
			RequestID:      "req-r25",
			TenantID:       "tenant-1",
			CallerOwner:    "alice",
			ClientProtocol: "openai-chat",
			ClientModel:    "m",
		},
	}
	modified, blocked, err := interceptNativeResponseBody(chain, &opt, dw.body)
	if err != nil || !blocked || modified != nil {
		t.Fatalf("intercept = modified=%v blocked=%v err=%v", modified, blocked, err)
	}
	// The buffer was never committed, so the client received nothing yet;
	// the handler answers with a 403 policy block instead (writeErrorJSON).
	rec := httptest.NewRecorder()
	if rec.Body.Len() != 0 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("client must not receive the body on a block decision")
	}
}

// R25-C semantic pin: an authenticated CallerOwner equal to the session's
// data owner suppresses redaction; the pre-fix production state (empty
// CallerOwner) must keep the conservative redaction path.
func TestNonStreamOwnerMatchSkipsRedactionOwnerUnknownRedacts(t *testing.T) {
	chatBody := func(content string) []byte {
		return []byte(`{"id":"1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `"},"finish_reason":"stop"}],"usage":{"total_tokens":3}}`)
	}
	ownerFn := func(context.Context, string, string) string { return "alice" }
	run := func(callerOwner string) *response.InterceptResult {
		icpt := outputhook.NewOutputComplianceInterceptor(nativePhoneChecker{}, ownerFn)
		res, err := icpt.InterceptNonStream(context.Background(), &response.InterceptRequest{
			TenantID:       "tenant-1",
			SessionID:      "gw-r25",
			CallerOwner:    callerOwner,
			ClientProtocol: "openai-chat",
			ResponseBody:   chatBody("call 13800138000"),
		})
		if err != nil {
			t.Fatalf("InterceptNonStream: %v", err)
		}
		return res
	}
	if res := run("alice"); res != nil && len(res.ModifiedBody) > 0 {
		t.Fatalf("owner match must skip redaction, got rewrite: %s", res.ModifiedBody)
	}
	res := run("")
	if res == nil || len(res.ModifiedBody) == 0 || !strings.Contains(string(res.ModifiedBody), "[PHONE]") {
		t.Fatal("owner unknown (pre-R25-C wire state) must keep conservative redaction")
	}
}

// R25-C/R25-D at stream end: InterceptStreamEnd must propagate
// CallerOwner/ClientProtocol from StreamMeta so the owner compare and the
// protocol-aware rewrite keep working on the reassembled body.
func TestInterceptStreamEndCarriesOwnerAndProtocolFromMeta(t *testing.T) {
	anthropicBody := []byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"call 13800138000"}]}`)
	ownerFn := func(context.Context, string, string) string { return "alice" }
	run := func(callerOwner string) *response.EndResult {
		icpt := outputhook.NewOutputComplianceInterceptor(nativePhoneChecker{}, ownerFn)
		end, err := icpt.InterceptStreamEnd(context.Background(), &response.StreamMeta{
			TenantID:       "tenant-1",
			SessionID:      "gw-r25",
			CallerOwner:    callerOwner,
			ClientProtocol: "anthropic-messages",
			ResponseBody:   anthropicBody,
		})
		if err != nil {
			t.Fatalf("InterceptStreamEnd: %v", err)
		}
		return end
	}
	if end := run("alice"); end != nil && len(end.Metadata) > 0 {
		t.Fatalf("owner match must skip redaction at stream end, got metadata: %v", end.Metadata)
	}
	if end := run(""); end == nil || end.Metadata == nil || end.Metadata["pii_stripped"] != true {
		t.Fatalf("owner unknown must redact at stream end, got: %+v", end)
	}
}
