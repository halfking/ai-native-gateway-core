package streaming

import (
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/i18n"
)

// TestClassifyUpstreamCredentialFailure_PositiveCases exercises the three
// upstream credential error kinds that the gateway must distinguish from
// the generic provider_error path. Operations staff rely on these codes
// to alert on "the gateway's stored credential is broken" without
// having to parse upstream error bodies.
func TestClassifyUpstreamCredentialFailure_PositiveCases(t *testing.T) {
	tests := []struct {
		name           string
		kind           errorsx.ErrorKind
		upstreamStatus int
		wantCode       string
		wantI18nKey    string
		wantHTTPStatus int
		wantErrType    string
	}{
		{
			name:           "KindAuth (HTTP 401) → upstream_credential_invalid",
			kind:           errorsx.KindAuth,
			upstreamStatus: http.StatusUnauthorized,
			wantCode:       "upstream_credential_invalid",
			wantI18nKey:    i18n.MsgUpstreamCredentialInvalid,
			wantHTTPStatus: http.StatusBadGateway,
			wantErrType:    "authentication_error",
		},
		{
			name:           "KindAuth (HTTP 403) → upstream_credential_invalid",
			kind:           errorsx.KindAuth,
			upstreamStatus: http.StatusForbidden,
			wantCode:       "upstream_credential_invalid",
			wantI18nKey:    i18n.MsgUpstreamCredentialInvalid,
			wantHTTPStatus: http.StatusBadGateway,
			wantErrType:    "authentication_error",
		},
		{
			name:           "KindAuthRevoked → upstream_credential_revoked",
			kind:           errorsx.KindAuthRevoked,
			upstreamStatus: http.StatusUnauthorized,
			wantCode:       "upstream_credential_revoked",
			wantI18nKey:    i18n.MsgUpstreamCredentialRevoked,
			wantHTTPStatus: http.StatusBadGateway,
			wantErrType:    "authentication_error",
		},
		{
			name:           "KindQuotaPermanent → upstream_quota_permanent",
			kind:           errorsx.KindQuotaPermanent,
			upstreamStatus: http.StatusPaymentRequired,
			wantCode:       "upstream_quota_permanent",
			wantI18nKey:    i18n.MsgUpstreamQuotaPermanent,
			wantHTTPStatus: http.StatusBadGateway,
			wantErrType:    "insufficient_quota",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, i18nKey, httpStatus, errType := classifyUpstreamCredentialFailure(tt.kind, tt.upstreamStatus)
			if code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if i18nKey != tt.wantI18nKey {
				t.Errorf("i18nKey = %q, want %q", i18nKey, tt.wantI18nKey)
			}
			if httpStatus != tt.wantHTTPStatus {
				t.Errorf("httpStatus = %d, want %d", httpStatus, tt.wantHTTPStatus)
			}
			if errType != tt.wantErrType {
				t.Errorf("errType = %q, want %q", errType, tt.wantErrType)
			}
		})
	}
}

// TestClassifyUpstreamCredentialFailure_NegativeCases confirms that
// non-credential upstream kinds fall through to the caller (which then
// keeps the legacy provider_error path). Without this guard we would
// silently re-categorise every upstream failure as a credential issue.
func TestClassifyUpstreamCredentialFailure_NegativeCases(t *testing.T) {
	nonCredentialKinds := []errorsx.ErrorKind{
		errorsx.KindTransient,
		errorsx.KindTimeout,
		errorsx.KindNetwork,
		errorsx.KindRateLimit,
		errorsx.KindUpstreamDown,
		errorsx.KindCanceled,
		errorsx.KindConcurrent,
		errorsx.KindQuota,
		errorsx.KindQuotaPeriodic,
		errorsx.KindQuotaBalance,
		errorsx.KindModelNotFound,
		errorsx.KindStreamTimeout,
		errorsx.KindToolCallIdMismatch,
		errorsx.KindContextLength,
		errorsx.KindUnsupportedFeature,
		errorsx.KindContentFilter,
		"",
	}
	for _, kind := range nonCredentialKinds {
		t.Run("fallthrough/"+string(kind), func(t *testing.T) {
			code, i18nKey, httpStatus, errType := classifyUpstreamCredentialFailure(kind, 500)
			if code != "" || i18nKey != "" || httpStatus != 0 || errType != "" {
				t.Errorf("kind=%q: expected empty tuple, got code=%q i18nKey=%q status=%d type=%q",
					kind, code, i18nKey, httpStatus, errType)
			}
		})
	}
}

// TestClassifyFailureStage_UpstreamCredentialIsUpstream ensures the new
// codes are NOT classified as "gateway" failures. They originate from
// the upstream provider, so the request_log.failure_stage column must
// read "upstream" — otherwise ops dashboards that filter on
// stage='gateway' will wrongly attribute upstream credential breakage to
// the gateway itself.
func TestClassifyFailureStage_UpstreamCredentialIsUpstream(t *testing.T) {
	upstreamCredCodes := []string{
		"upstream_credential_invalid",
		"upstream_credential_revoked",
		"upstream_quota_permanent",
	}
	for _, code := range upstreamCredCodes {
		t.Run(code, func(t *testing.T) {
			if got := classifyFailureStage(code); got != "upstream" {
				t.Errorf("classifyFailureStage(%q) = %q, want %q", code, got, "upstream")
			}
		})
	}
}

// TestMapGatewayErrorToDetail_UpstreamCredentialPassthrough ensures the
// new codes pass through mapGatewayErrorToDetail WITHOUT being prefixed
// with "gw_". They are upstream-originated codes; the legacy "gw_*"
// prefix is reserved for failures that happen before the request
// reaches an upstream provider.
func TestMapGatewayErrorToDetail_UpstreamCredentialPassthrough(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"upstream_credential_invalid", "upstream_credential_invalid"},
		{"upstream_credential_revoked", "upstream_credential_revoked"},
		{"upstream_quota_permanent", "upstream_quota_permanent"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := mapGatewayErrorToDetail(tt.in); got != tt.want {
				t.Errorf("mapGatewayErrorToDetail(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestClientVsUpstreamKeyCodes_AreDistinct is the headline invariant of
// the 2026-07-12 fix: ops must be able to look at any error code and
// immediately know which side has the bad key.
//
//	Client-side key problems  → invalid_key, missing_key (HTTP 401, gateway stage)
//	Upstream-side key problems → upstream_credential_invalid,
//	                             upstream_credential_revoked,
//	                             upstream_quota_permanent (HTTP 502, upstream stage)
//
// These two sets must NEVER overlap, and the legacy "provider_error"
// catch-all must not be the only signal for upstream key issues.
func TestClientVsUpstreamKeyCodes_AreDistinct(t *testing.T) {
	clientSide := map[string]string{
		"invalid_key": "gateway",
		"missing_key": "gateway",
	}
	upstreamSide := map[string]string{
		"upstream_credential_invalid": "upstream",
		"upstream_credential_revoked": "upstream",
		"upstream_quota_permanent":    "upstream",
	}

	for code := range clientSide {
		if _, ok := upstreamSide[code]; ok {
			t.Errorf("code %q appears in both client-side and upstream-side sets", code)
		}
		if got := classifyFailureStage(code); got != "gateway" {
			t.Errorf("client-side code %q classified as %q, want gateway", code, got)
		}
	}
	for code, wantStage := range upstreamSide {
		if _, ok := clientSide[code]; ok {
			t.Errorf("code %q appears in both client-side and upstream-side sets", code)
		}
		if got := classifyFailureStage(code); got != wantStage {
			t.Errorf("upstream-side code %q classified as %q, want %q", code, got, wantStage)
		}
		if got := mapGatewayErrorToDetail(code); got != code {
			t.Errorf("upstream-side code %q got detail %q, want unchanged", code, got)
		}
	}
}
