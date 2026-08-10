package errorsx

import (
	"testing"
	"time"
)

func TestDecideFailover(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		client     bool
		wantScope  FailoverScope
		wantFuse   bool
		wantProbe  bool
		wantFanout int
		wantWait   time.Duration
	}{
		{"upstream auth ejects credential", 401, `{"error":"invalid api key"}`, "", false, ScopeCredential, true, false, 0, DefaultFrontendWait},
		{"client auth does not eject credential", 401, `unauthorized`, "", true, ScopeNone, false, false, 0, DefaultFrontendWait},
		{"permanent quota ejects credential", 429, `{"error":{"message":"quota exceeded"}}`, "", false, ScopeCredential, true, false, 0, DefaultFrontendWait},
		{"periodic quota ejects only credential and skips node probe", 429, `{"error":{"message":"usage limit exceeded","window_type":"total"}}`, "", false, ScopeCredential, true, false, 0, DefaultFrontendWait},
		{"rate limit honors retry after", 429, `rate limit`, "7", false, ScopeModel, false, true, 1, DefaultFrontendWait},
		{"concurrent failure probes backups", 503, `service overloaded`, "", false, ScopeModel, false, true, DefaultProbeFanout, DefaultFrontendWait},
		{"upstream overload delays without probing", 502, `Our servers are currently overloaded. Please try again later.`, "", false, ScopeModel, false, false, 0, DefaultFrontendWait},
		{"model error does not probe", 404, `model foo is not found`, "", false, ScopeModel, false, false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideFailover(tt.status, []byte(tt.body), tt.retryAfter, tt.client)
			if got.Scope != tt.wantScope || got.Fuse != tt.wantFuse || got.EnqueueProbe != tt.wantProbe || got.ProbeFanout != tt.wantFanout || got.FrontendWait != tt.wantWait {
				t.Fatalf("decision=%+v, want scope=%q fuse=%v probe=%v fanout=%d wait=%s", got, tt.wantScope, tt.wantFuse, tt.wantProbe, tt.wantFanout, tt.wantWait)
			}
			if tt.retryAfter == "7" && got.RetryAfter != 7*time.Second {
				t.Fatalf("retry after=%s, want 7s", got.RetryAfter)
			}
			if tt.name == "upstream overload delays without probing" && got.RetryAfter != DefaultOverloadRetryDelay {
				t.Fatalf("overload retry delay=%s, want %s", got.RetryAfter, DefaultOverloadRetryDelay)
			}
		})
	}
}

func TestDecideFailoverPeriodicQuotaIsCredentialScoped(t *testing.T) {
	body := []byte(`{"error":{"message":"usage limit exceeded","window_type":"total"}}`)
	got := DecideFailover(429, body, "", false)
	if got.Kind != KindQuotaPeriodic {
		t.Fatalf("kind=%q, want %q", got.Kind, KindQuotaPeriodic)
	}
	if got.Scope != ScopeCredential || !got.Fuse || got.Permanent || got.EnqueueProbe || got.ProbeFanout != 0 {
		t.Fatalf("periodic quota decision=%+v, want credential-scoped fuse without permanent quarantine or node probe", got)
	}
}

func TestIsUpstreamAuthFailure(t *testing.T) {
	if !IsUpstreamAuthFailure(401, false) {
		t.Fatal("expected upstream 401 to be an auth failure")
	}
	if IsUpstreamAuthFailure(401, true) {
		t.Fatal("client 401 must not eject an upstream credential")
	}
}
