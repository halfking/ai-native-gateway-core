package bg

import (
	"strings"
	"testing"
	"time"
)

// TestClassifyProbeRootCause pins the 2026-09-25 "协议问题还是节点的问题"
// taxonomy. Each bucket drives a different policy: node → generic ladder,
// protocol → confirmed-protocol 6h park (re-probing cannot heal a contract
// mismatch), gateway → shared surfaces untouched + fixed 15m pace.
func TestClassifyProbeRootCause(t *testing.T) {
	cases := []struct {
		name    string
		errCode string
		status  int
		body    string
		want    ProbeRootCause
	}{
		// Gateway-side: this instance built/decrypted wrong — never the node.
		{"endpoint build", "endpoint_build", 0, "", ProbeRootCauseGateway},
		{"request build", "request_build", 0, "", ProbeRootCauseGateway},
		{"pin unsupported", "gateway_pin_unsupported", 0, "", ProbeRootCauseGateway},
		{"gateway not configured", "gateway_not_configured", 0, "", ProbeRootCauseGateway},

		// Node: transport + auth + rate + payment + 5xx.
		{"timeout", "timeout", 0, "", ProbeRootCauseNode},
		{"dns", "dns_error", 0, "", ProbeRootCauseNode},
		{"connection refused", "connection_error", 0, "", ProbeRootCauseNode},
		{"auth", "http_401", 401, `{"error":"invalid_api_key"}`, ProbeRootCauseNode},
		{"forbidden", "http_403", 403, "", ProbeRootCauseNode},
		{"payment required", "http_402", 402, "", ProbeRootCauseNode},
		{"rate limited", "http_429", 429, "", ProbeRootCauseNode},
		{"upstream 500", "http_500", 500, "", ProbeRootCauseNode},
		{"upstream 503", "http_503", 503, "", ProbeRootCauseNode},

		// Protocol: contract-shaped statuses.
		{"model not served 404", "http_404", 404, `{"error":{"message":"model not found"}}`, ProbeRootCauseProtocol},
		{"method not allowed", "http_405", 405, "", ProbeRootCauseProtocol},
		{"gone", "http_410", 410, "", ProbeRootCauseProtocol},
		{"unsupported media type", "http_415", 415, "", ProbeRootCauseProtocol},
		{"bad request contract", "http_400", 400, `{"error":{"message":"unknown parameter: max_tokens"}}`, ProbeRootCauseProtocol},
		{"unprocessable contract", "http_422", 422, `{"error":"invalid_request_error"}`, ProbeRootCauseProtocol},

		// 400/422 wearing a billing costume is a NODE problem: OpenAI-family
		// upstreams answer an exhausted quota with 400 insufficient_quota.
		// Misclassifying it as protocol would park a recharge-recoverable
		// credential at 6h.
		{"quota-shaped 400", "http_400", 400, `{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`, ProbeRootCauseNode},
		{"balance-shaped 422", "http_422", 422, `{"message":"账户余额不足"}`, ProbeRootCauseNode},
		{"arrears-shaped 400", "http_400", 400, `{"message":"该令牌已欠费"}`, ProbeRootCauseNode},

		// Conservative defaults.
		{"unknown 4xx", "http_409", 409, "", ProbeRootCauseNode},
		{"empty response no status", "empty_response", 0, "", ProbeRootCauseNode},
		{"ok round unclassified", "none", 0, "", ProbeRootCause("")}, // "none" is the ok sentinel; status 0 + none → empty cause.

	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyProbeRootCause(c.errCode, c.status, c.body); got != c.want {
				t.Fatalf("classifyProbeRootCause(%q, %d, body) = %q, want %q",
					c.errCode, c.status, got, c.want)
			}
		})
	}
}

// TestNodeShapedQuotaBody checks the bilingual billing-keyword matcher that
// splits quota-shaped 400/422 bodies from contract-shaped ones.
func TestNodeShapedQuotaBody(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"insufficient_quota"}}`,
		`{"message":"Insufficient Balance"}`,
		`{"message":"余额不足，请充值"}`,
		`upstream says: 已欠费`,
	} {
		if !nodeShapedQuotaBody(body) {
			t.Fatalf("nodeShapedQuotaBody(%q) = false, want true", body)
		}
	}
	for _, body := range []string{
		`{"error":{"message":"unknown parameter"}}`,
		``,
		`{"error":{"message":"model does not exist"}}`,
	} {
		if nodeShapedQuotaBody(body) {
			t.Fatalf("nodeShapedQuotaBody(%q) = true, want false", body)
		}
	}
}

// TestProbeBackoffForDirectOutcome locks the policy split: node failures keep
// the err-code ladder, confirmed protocol failures park at the model-not-served
// horizon (a contract mismatch never heals by re-probing), and the FIRST
// protocol attempt keeps the short ladder so a one-off blip re-verifies fast.
func TestProbeBackoffForDirectOutcome(t *testing.T) {
	if got := probeBackoffForDirectOutcome("http_400", ProbeRootCauseProtocol, 1); got != 5*time.Second {
		t.Fatalf("protocol attempt 1 = %v, want generic-ladder 5s", got)
	}
	if got := probeBackoffForDirectOutcome("http_400", ProbeRootCauseProtocol, 2); got != modelNotServedRecheckInterval {
		t.Fatalf("protocol attempt 2 = %v, want %v park", got, modelNotServedRecheckInterval)
	}
	if got := probeBackoffForDirectOutcome("http_415", ProbeRootCauseProtocol, 7); got != modelNotServedRecheckInterval {
		t.Fatalf("protocol attempt 7 = %v, want %v park", got, modelNotServedRecheckInterval)
	}
	// Node-shaped keeps the existing err-code policy (quota-shaped 400 walks
	// the generic chain, not the park).
	if got := probeBackoffForDirectOutcome("http_400", ProbeRootCauseNode, 5); got != ProbeBackoffForErrCode("http_400", 5) {
		t.Fatalf("node 400 attempt 5 = %v, want plain ProbeBackoffForErrCode", got)
	}
	// Empty cause (e.g. hand-built round in tests / unclassified path) must
	// behave exactly like the pre-root-cause policy.
	if got := probeBackoffForDirectOutcome("network_error", "", 1); got != ProbeBackoffForErrCode("network_error", 1) {
		t.Fatalf("empty cause attempt 1 = %v, want plain ProbeBackoffForErrCode", got)
	}
}

// TestAnnotateRootCause ensures the err_detail suffix is added once and never
// clobbers contains-based sentinels ("decrypt: ", "no rows in result set").
func TestAnnotateRootCause(t *testing.T) {
	// rootCause is set by the round producers' defer (probeDirect/probeGateway
	// choke point); the test mirrors that shape.
	r := nodeProbeRoundResult{ok: false, errCode: "http_400", errDetail: `bad request: {"a":1}`, rootCause: ProbeRootCauseProtocol}
	annotateRootCause(&r)
	if !strings.Contains(r.errDetail, "(root_cause=protocol)") {
		t.Fatalf("errDetail = %q, want root-cause suffix", r.errDetail)
	}
	once := r.errDetail
	annotateRootCause(&r)
	if r.errDetail != once {
		t.Fatalf("annotation not idempotent: %q → %q", once, r.errDetail)
	}

	dec := nodeProbeRoundResult{ok: false, errCode: "endpoint_build", errDetail: "build endpoint failed: decrypt: cannot decrypt: unknown format", rootCause: ProbeRootCauseGateway}
	annotateRootCause(&dec)
	if !strings.Contains(dec.errDetail, "decrypt: ") || !strings.Contains(dec.errDetail, "(root_cause=gateway)") {
		t.Fatalf("decrypt detail mangled: %q", dec.errDetail)
	}

	ok := nodeProbeRoundResult{ok: true}
	annotateRootCause(&ok)
	if ok.errDetail != "" {
		t.Fatalf("ok round annotated: %q", ok.errDetail)
	}
}
