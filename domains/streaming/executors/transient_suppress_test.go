package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 2026-09-14 audit O2: only transient upstream failures may arm the 5-minute
// (credential, model) routing suppression. Hard-signal kinds keep their own
// paths (model_not_found / model_deprecated → recordModelNotFound, auth /
// quota → credential-level policies) and request-shaped failures (client bug,
// context length, unsupported feature) must never suppress a healthy pair.
func TestTransientSuppressErrorCode(t *testing.T) {
	armed := map[errorsx.ErrorKind]string{
		errorsx.KindRateLimit:          "http_429",
		errorsx.KindTransient:          "http_5xx",
		errorsx.KindTimeout:            "timeout",
		errorsx.KindStreamTimeout:      "timeout",
		errorsx.KindNetwork:            "network",
		errorsx.KindUpstreamDown:       "upstream_down",
		errorsx.KindUpstreamOverloaded: "upstream_down",
		errorsx.KindConcurrent:         "concurrent",
	}
	for kind, wantCode := range armed {
		code, ok := transientSuppressErrorCode(kind)
		if !ok || code != wantCode {
			t.Fatalf("kind %s: got (%q, %v), want (%q, true)", kind, code, ok, wantCode)
		}
	}

	notArmed := []errorsx.ErrorKind{
		errorsx.KindModelNotFound,
		errorsx.KindModelDeprecated,
		errorsx.KindClientBug,
		errorsx.KindContextLength,
		errorsx.KindCanceled,
		errorsx.KindAuth,
		errorsx.KindAuthRevoked,
		errorsx.KindQuota,
		errorsx.KindQuotaPermanent,
		errorsx.KindUnsupportedFeature,
		errorsx.KindToolCallIdMismatch,
	}
	for _, kind := range notArmed {
		if _, ok := transientSuppressErrorCode(kind); ok {
			t.Fatalf("kind %s must not arm transient suppression", kind)
		}
	}
}
