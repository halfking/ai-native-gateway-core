package relay

// policy.go — tenant model policy enforcement for the relay hot path.
//
// Round 48 (2026-06-21): per-tenant model denylist.  Default
// behaviour (table empty) is "all models allowed for all tenants".
// super_admin can configure a denylist via
// /api/admin/tenants/{code}/model-policies.
//
// Decision audit (see docs/llm-gateway-go/2026-06-21-tenant-model-policy.md):
//   - Insertion point: BEFORE auto_route + GetCandidates, so a denied
//     request never reaches the upstream provider.
//   - model="auto" is exempt on the FIRST pass (user decision:
//     "auto 不纳入管理").  But because auto_route rewrites
//     reqBody.Model to the resolved model, we re-check AFTER auto_route
//     rewrites the model — this prevents using auto as a bypass
//     vector.  See enforceTenantModelPolicyAfterAuto().
//   - canonical_name comes from resolve.Resolver, which already
//     applies NormalizeRouteKeyAliases.  The Checker matches by
//     exact equality on the lowercased canonical.
//   - Fail-open: a governance DB outage must not become an
//     availability outage.  Checker.IsForbidden returns false on
//     any error.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/internal/modelpolicy"
	"github.com/kaixuan/llm-gateway-go/resolve"
)

// enforceTenantModelPolicy checks whether the requested model is
// forbidden for the caller's tenant.  Returns (denied, canonical,
// tenantID) where:
//
//   - denied: true → caller must reject with 403
//   - canonical: the resolved canonical model name (empty when
//     resolver is unavailable or model has no canonical entry)
//   - tenantID: the tenant whose policy was checked (useful for
//     request_log diagnostics; never echoed to the client)
//
// preAutoModel is the model as the client requested it.  When the
// caller is the auto-route path, this is "auto" and we skip the
// first check (the post-rewrite check is performed separately
// after auto_route resolves the model).
func enforceTenantModelPolicy(
	ctx context.Context,
	preAutoModel string,
	keyInfo *auth.KeyInfo,
	mp *modelpolicy.Checker,
	resolver *resolve.Resolver,
	profile string,
) (denied bool, canonical string, tenantID string) {
	tenantID = "default"
	if mp == nil || keyInfo == nil {
		return false, "", tenantID
	}
	if keyInfo.TenantID != "" {
		tenantID = keyInfo.TenantID
	}
	// Skip pre-auto check when client asked for "auto" — user
	// decision: model="auto" is exempt on the first pass.  The
	// post-rewrite check (enforceTenantModelPolicyAfterAuto)
	// re-evaluates with the rewritten canonical model.
	if preAutoModel == autoRequestMagic {
		return false, "", tenantID
	}

	canonical = resolveCanonical(ctx, preAutoModel, resolver, profile)
	if canonical == "" {
		return false, "", tenantID
	}

	if mp.IsForbidden(ctx, tenantID, canonical) {
		return true, canonical, tenantID
	}
	return false, canonical, tenantID
}

// enforceTenantModelPolicyAfterAuto re-checks the policy after
// auto_route has rewritten reqBody.Model from "auto" to the
// chosen canonical model.  Without this, a tenant could
// effectively bypass the denylist by always sending model="auto"
// because the first check sees "auto" and short-circuits.
//
// preAutoModel is the original request model ("auto" expected);
// postAutoModel is what reqBody.Model is now (may be the same
// as preAutoModel if auto_route failed to rewrite).
func enforceTenantModelPolicyAfterAuto(
	ctx context.Context,
	preAutoModel, postAutoModel string,
	keyInfo *auth.KeyInfo,
	mp *modelpolicy.Checker,
	resolver *resolve.Resolver,
	profile string,
) (denied bool, canonical string, tenantID string) {
	// Only run if the request was an auto request AND the model
	// was actually rewritten to something different.
	if preAutoModel != autoRequestMagic || postAutoModel == autoRequestMagic {
		return false, "", ""
	}
	if postAutoModel == "" || postAutoModel == preAutoModel {
		return false, "", ""
	}
	return enforceTenantModelPolicy(ctx, postAutoModel, keyInfo, mp, resolver, profile)
}

// resolveCanonical uses the resolve.Resolver to map a client model
// name to its canonical form.  Falls back to "" when the resolver
// is unavailable (e.g. no DB), which causes enforceTenantModelPolicy
// to return denied=false (no policy enforcement possible).
func resolveCanonical(
	ctx context.Context,
	clientModel string,
	resolver *resolve.Resolver,
	profile string,
) string {
	if clientModel == "" {
		return ""
	}
	if resolver == nil {
		return ""
	}
	res := resolver.Resolve(ctx, clientModel, profile)
	if res == nil || res.CanonicalName == nil {
		return ""
	}
	return *res.CanonicalName
}

// writeModelForbiddenError writes the standard 403 response when a
// request is rejected by the policy checker.  The message
// deliberately does NOT echo tenant_id to the client (privacy —
// see docs/.../tenant-model-policy.md §3.2).
func writeModelForbiddenError(w http.ResponseWriter, requestID, canonical string) {
	msg := fmt.Sprintf("Model '%s' is not available for your account", canonical)
	writeErrorJSON(w, http.StatusForbidden, requestID, msg, "permission_error", "model_forbidden")
}