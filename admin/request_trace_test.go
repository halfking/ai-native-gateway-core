// admin/request_trace_test.go — unit tests for the probe-request trace
// synthesis added 2026-07-17. The DB-backed buildProbeTrace path is
// exercised via integration tests; here we cover the pure helpers
// (regex parsing of probe request_ids) that gate the synthesis.
package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestTraceRoutesFailClosedWithoutAuthorization(t *testing.T) {
	mux := http.NewServeMux()
	NewRequestTraceHandler(nil, nil).RegisterRoutes(mux, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/requests/req-1/trace", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestIsProbeRequestID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		// Real failure sample from the incident report.
		{"probe-direct-c16-mglm-5.2-a5-fail-1784225551431410131", true},
		// Real success sample.
		{"probe-direct-c13-mclaude-opus-4-8-a4-ok-1784225218026416456", true},
		// attempt 1, single-digit cred.
		{"probe-direct-c1-mgpt-4o-a1-ok-1000000000000000000", true},
		// Non-probe request ids must not match.
		{"req_abc123", false},
		{"chatcmpl-xxxx", false},
		{"", false},
		// Wrong prefix.
		{"probe-client_cancel-cred5-100", false},
		// Missing attempt suffix.
		{"probe-direct-c16-mglm-5.2-fail-1784225551431410131", false},
	}
	for _, c := range cases {
		if got := isProbeRequestID(c.id); got != c.want {
			t.Errorf("isProbeRequestID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

func TestProbeRequestIDRe_ExtractsCredAndTimestamp(t *testing.T) {
	id := "probe-direct-c16-mglm-5.2-a5-fail-1784225551431410131"
	m := probeRequestIDRe.FindStringSubmatch(id)
	if m == nil {
		t.Fatalf("regex did not match %q", id)
	}
	// m[1]=cred, m[2]=model(sanitised), m[3]=attempt, m[4]=ok|fail, m[5]=ns
	if m[1] != "16" {
		t.Errorf("cred = %q, want 16", m[1])
	}
	if m[2] != "glm-5.2" {
		t.Errorf("model = %q, want glm-5.2", m[2])
	}
	if m[3] != "5" {
		t.Errorf("attempt = %q, want 5", m[3])
	}
	if m[4] != "fail" {
		t.Errorf("suffix = %q, want fail", m[4])
	}
	if m[5] != "1784225551431410131" {
		t.Errorf("ns = %q, want 1784225551431410131", m[5])
	}
}
