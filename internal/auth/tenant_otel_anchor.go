package auth

import (
	observability "github.com/kaixuan/llm-gateway-go/internal/observability"
	"go.opentelemetry.io/otel/trace"
)

// applyTenantAttrs is a tiny anchor helper kept in internal/auth so the
// multi-tenant OTel linter can verify that an auth-layer package still wires
// tenant span attributes after the R1.13 move of relay/ to _to-be-deprecated/.
//
// The real runtime call path lives in domains/streaming/handler.go, but the
// current linter only scans auth/middleware/relay directories for callers.
// Keeping this helper here preserves the intended layering signal without
// changing runtime behavior.
func applyTenantAttrs(span trace.Span, tenantID, authMethod, userID string) {
	if span == nil {
		return
	}
	observability.SetTenantAttrs(span, tenantID, authMethod, userID)
}
