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
			name:           "KindQuotaPeriodic → upstream_quota_periodic",
			kind:           errorsx.KindQuotaPeriodic,
			upstreamStatus: http.StatusTooManyRequests,
			wantCode:       "upstream_quota_periodic",
			wantI18nKey:    i18n.MsgUpstreamQuotaPeriodic,
			wantHTTPStatus: http.StatusBadGateway,
			wantErrType:    "insufficient_quota",
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
		// 2026-08-09: KindQuota and KindQuotaBalance were listed here as
		// expected fall-throughs, which encoded the defect as a contract.
		// Both are IsCredentialFatal, so falling through sent a real
		// balance-exhaustion event to the client as 503 model_not_found
		// "No available provider". They are now mapped and asserted in
		// TestClassifyUpstreamCredentialFailure_CoversEveryCredentialFatalKind.
		errorsx.KindModelNotFound,
		errorsx.KindModelDeprecated,
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

// TestClassifyUpstreamCredentialFailure_CoversEveryCredentialFatalKind pins the
// invariant that broke in production on 2026-08-09: any kind that
// errorsx.IsCredentialFatal accepts MUST get a dedicated client-facing code
// here.
//
// When a credential-fatal kind is missing, classifyUpstreamCredentialFailure
// returns an empty tuple, the caller falls through to the generic
// all-candidates-failed branch, and the client is told 503 model_not_found
// "No available provider" for what was actually a spent account or a rejected
// key. KindQuota (bare HTTP 402) and KindQuotaBalance were both unmapped for
// this reason.
//
// Deriving the kind list from IsCredentialFatal rather than hardcoding it means
// adding a new credential-fatal kind fails this test until it is also given a
// user-facing message, instead of silently regressing to "No available
// provider".
func TestClassifyUpstreamCredentialFailure_CoversEveryCredentialFatalKind(t *testing.T) {
	// Every kind declared in errorsx. The test asserts the credential-fatal
	// subset is fully mapped; the rest are covered by the negative test above.
	allKinds := []errorsx.ErrorKind{
		errorsx.KindTransient, errorsx.KindTimeout, errorsx.KindNetwork,
		errorsx.KindRateLimit, errorsx.KindAuth, errorsx.KindAuthRevoked,
		errorsx.KindQuota, errorsx.KindQuotaPeriodic, errorsx.KindQuotaBalance,
		errorsx.KindQuotaPermanent, errorsx.KindUpstreamDown,
		errorsx.KindUpstreamOverloaded, errorsx.KindStreamTimeout,
		errorsx.KindConcurrent, errorsx.KindModelNotFound,
		errorsx.KindModelDeprecated, errorsx.KindContentFilter,
		errorsx.KindContextLength, errorsx.KindUnsupportedFeature,
		errorsx.KindToolCallIdMismatch, errorsx.KindCanceled,
		errorsx.KindEmptyResponse,
	}

	fatalSeen := 0
	for _, kind := range allKinds {
		if !errorsx.IsCredentialFatal(kind) {
			continue
		}
		fatalSeen++
		t.Run(string(kind), func(t *testing.T) {
			code, i18nKey, httpStatus, errType := classifyUpstreamCredentialFailure(kind, 402)
			if code == "" {
				t.Fatalf("kind=%q is IsCredentialFatal but has no client-facing "+
					"code; it will surface as 503 model_not_found "+
					"\"No available provider\" instead of a quota/auth message", kind)
			}
			if i18nKey == "" {
				t.Errorf("kind=%q: empty i18n key (client gets no localized reason)", kind)
			}
			if httpStatus != http.StatusBadGateway {
				t.Errorf("kind=%q: status=%d, want 502 — the caller's key is fine, "+
					"ours is not", kind, httpStatus)
			}
			if errType == "" {
				t.Errorf("kind=%q: empty OpenAI error type", kind)
			}
		})
	}

	if fatalSeen == 0 {
		t.Fatal("no credential-fatal kinds found — the kind list is stale, " +
			"not the mapping")
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
		"upstream_quota_periodic",
		"upstream_quota_permanent",
		// 2026-08-09: added with the KindQuota / KindQuotaBalance mapping.
		"upstream_quota_balance",
		"upstream_quota_generic",
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
		{"upstream_quota_periodic", "upstream_quota_periodic"},
		{"upstream_quota_permanent", "upstream_quota_permanent"},
		{"upstream_quota_balance", "upstream_quota_balance"},
		{"upstream_quota_generic", "upstream_quota_generic"},
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
		"upstream_quota_periodic":     "upstream",
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
