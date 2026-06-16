// Package observability provides tenant-aware OpenTelemetry span
// attribute helpers for llm-gateway-go (Pattern A — direct tenant_id).
//
// Round 38 (2026-06-16) — third and final service to adopt the
// R34 OTel convention. llm-gateway-go is Pattern A (direct
// tenant_id) just like brandmind-go (R36). After R38, all 3
// multi-tenant services are lint-clean (blocking).
//
// Why this exists
// ───────────────
// Rounds 1-18 enforced isolation at the application layer; rounds
// 29-33 at the DB layer (PostgreSQL RLS). Round 34 codified the
// observability convention; R36 (brandmind) + R37 (crm-go) +
// R38 (llm-gateway-go) all adopt. Every authenticated request
// now carries tenant.id so production debugging of cross-tenant
// issues is trivial (filter Jaeger by tenant.id=X).
//
// Usage
// ─────
// In relay/handler.go ServeHTTP (line 174), AFTER keyInfo is
// available (line 294) but BEFORE returning to the client:
//
//	span := trace.SpanFromContext(r.Context())
//	observability.SetTenantAttrs(span, keyInfo.TenantID, "api_key", apiKeyID)
//
// Where:
//   - span: OTel span (from trace.SpanFromContext)
//   - keyInfo.TenantID: string from auth.KeyInfo (Pattern A)
//   - authMethod: "api_key" | "jwt" | "session" | "service_account"
//   - userID: API key owner or user ID
package observability

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Attribute keys (mirror scripts/_lib/otel-tenant-attrs.py; single
// source of truth across the stack)
const (
	AttrTenantID            = "tenant.id"
	AttrTenantShopIDs       = "tenant.shop_ids"
	AttrKaixuanTenantPattern = "kaixuan.tenant.pattern"
	AttrKaixuanAuthMethod   = "kaixuan.tenant.auth_method"
	AttrKaixuanUserID       = "kaixuan.user.id"
)

// SetTenantAttrs (Pattern A) sets the required + recommended
// multi-tenant OTel attributes on `span`.
//
// Required (L1 if missing):
//   - tenant.id
//   - kaixuan.tenant.pattern = "A"
//
// Recommended (L2 if missing):
//   - kaixuan.tenant.auth_method
//   - kaixuan.user.id
//
// Pattern A is for services that have a direct tenant_id column
// (brandmind-go, llm-gateway-go). For Pattern B (crm-go with
// shop_id / channel_id), use SetTenantAttrsPatternB instead.
//
// Empty values are skipped (L2 only — not L1). The caller is
// responsible for ensuring tenant_id is meaningful before calling
// this. In llm-gateway-go, keyInfo.TenantID is always set (defaults
// to "default" for legacy / system keys).
func SetTenantAttrs(span trace.Span, tenantID, authMethod, userID string) {
	attrs := []attribute.KeyValue{
		attribute.String(AttrKaixuanTenantPattern, "A"),
	}
	if tenantID != "" {
		attrs = append(attrs, attribute.String(AttrTenantID, tenantID))
	}
	if authMethod != "" {
		attrs = append(attrs, attribute.String(AttrKaixuanAuthMethod, authMethod))
	}
	if userID != "" {
		attrs = append(attrs, attribute.String(AttrKaixuanUserID, userID))
	}
	span.SetAttributes(attrs...)
}

// SetTenantAttrsPatternB (Pattern B) sets the attributes for
// services that use a shop_id list (crm-go). Not used in
// llm-gateway-go (Pattern A only); defined for cross-service
// spec consistency.
func SetTenantAttrsPatternB(span trace.Span, shopIDs []string, authMethod, userID string) {
	attrs := []attribute.KeyValue{
		attribute.String(AttrKaixuanTenantPattern, "B"),
	}
	if len(shopIDs) > 0 {
		joined := ""
		for i, s := range shopIDs {
			if i > 0 {
				joined += ","
			}
			joined += s
		}
		attrs = append(attrs, attribute.String(AttrTenantShopIDs, joined))
	}
	if authMethod != "" {
		attrs = append(attrs, attribute.String(AttrKaixuanAuthMethod, authMethod))
	}
	if userID != "" {
		attrs = append(attrs, attribute.String(AttrKaixuanUserID, userID))
	}
	span.SetAttributes(attrs...)
}
