package executors

import (
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// TestUpstreamRequestSideKind_QuotaPermanent verifies that KindQuotaPermanent
// (and other credential-fatal kinds) are passed through by upstreamRequestSideKind
// so ExecuteError.LastKind preserves the precise classification for handler's
// credential branch.
//
// Context (2026-11-13 r1051 audit): Before this fix, quota_permanent errors
// were collapsed to KindTransient in dispatchErrToExecuteError (line 637),
// causing DB to record error_kind='transient' instead of 'quota_permanent'.
// The handler's classifyUpstreamCredentialFailure branch (handler.go:5781)
// requires exact LastKind to emit upstream_quota_permanent error code.
func TestUpstreamRequestSideKind_QuotaPermanent(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantKind errorsx.ErrorKind
	}{
		{
			name: "quota_permanent passes through",
			err: &upstreampkg.Error{
				Kind:       errorsx.KindQuotaPermanent,
				StatusCode: 400,
				Body:       []byte(`{"error":{"message":"no active limited quota"}}`),
			},
			wantKind: errorsx.KindQuotaPermanent,
		},
		{
			name: "quota_periodic passes through",
			err: &upstreampkg.Error{
				Kind:       errorsx.KindQuotaPeriodic,
				StatusCode: 429,
			},
			wantKind: errorsx.KindQuotaPeriodic,
		},
		{
			name: "quota_balance passes through",
			err: &upstreampkg.Error{
				Kind:       errorsx.KindQuotaBalance,
				StatusCode: 403,
			},
			wantKind: errorsx.KindQuotaBalance,
		},
		{
			name: "auth passes through",
			err: &upstreampkg.Error{
				Kind:       errorsx.KindAuth,
				StatusCode: 401,
			},
			wantKind: errorsx.KindAuth,
		},
		{
			name: "auth_revoked passes through",
			err: &upstreampkg.Error{
				Kind:       errorsx.KindAuthRevoked,
				StatusCode: 403,
			},
			wantKind: errorsx.KindAuthRevoked,
		},
		{
			name: "request_rejected (IsRequestSideError) passes through",
			err: &upstreampkg.Error{
				Kind:       errorsx.KindRequestRejected,
				StatusCode: 400,
			},
			wantKind: errorsx.KindRequestRejected,
		},
		{
			name: "transient does NOT pass through",
			err: &upstreampkg.Error{
				Kind:       errorsx.KindTransient,
				StatusCode: 503,
			},
			wantKind: "",
		},
		{
			name: "rate_limit does NOT pass through",
			err: &upstreampkg.Error{
				Kind:       errorsx.KindRateLimit,
				StatusCode: 429,
			},
			wantKind: "",
		},
		{
			name:     "non-upstream error returns empty",
			err:      errors.New("generic error"),
			wantKind: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upstreamRequestSideKind(tt.err)
			if got != tt.wantKind {
				t.Errorf("upstreamRequestSideKind() = %v, want %v", got, tt.wantKind)
			}
		})
	}
}
