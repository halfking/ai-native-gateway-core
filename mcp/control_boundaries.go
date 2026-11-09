package mcp

import (
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/httpx"
)

// bindControlTenant checks the decoded body as well as the header/query tenant.
// Only the configured management token may write outside its own tenant.
func (c *ControlPlane) bindControlTenant(w http.ResponseWriter, r *http.Request, tenant *string) bool {
	if *tenant == "" {
		*tenant = c.tenantOf(r)
	}
	return c.controlOwnerOK(w, r, *tenant)
}

func (c *ControlPlane) controlOwnerOK(w http.ResponseWriter, r *http.Request, owner string) bool {
	if id := controlIdentity(r); id != nil && !id.Admin && owner != id.TenantID {
		c.writeTenantMismatch(w, id.TenantID)
		return false
	}
	return true
}

func (c *ControlPlane) auditTenant(r *http.Request) string {
	if id := controlIdentity(r); id != nil && !id.Admin {
		return id.TenantID
	}
	// The management token keeps its existing global audit view unless the
	// operator explicitly selects a tenant.
	return requestedTenant(r)
}

// POST registration retains administrator upsert compatibility, but replacing an
// existing object is a management operation, even when reached through POST.
func (c *ControlPlane) canReplaceRegistration(w http.ResponseWriter, r *http.Request, owner string) bool {
	if !c.controlOwnerOK(w, r, owner) {
		return false
	}
	if id := controlIdentity(r); id != nil && !id.HasScope(ScopeManage) {
		c.writeForbidden(w, ScopeManage, id.Scopes, "scope_denied")
		return false
	}
	return true
}

func writeControlStoreError(w http.ResponseWriter, err error) {
	_ = httpx.WriteJSON(w, http.StatusInternalServerError, "application/json", map[string]any{"error": err.Error()})
}

// delegationWithinIssuer checks the entire authority envelope, not only scopes.
// Principal changes are rejected rather than guessed: team/private visibility
// depends on those fields. Empty allow lists mean unrestricted access.
func delegationWithinIssuer(issuer *Identity, spec KeySpec) bool {
	if issuer == nil {
		return false
	}
	if issuer.Admin {
		return true
	}
	if issuer.Record == nil || spec.TenantID != issuer.TenantID ||
		spec.OrgID != issuer.OrgID || spec.UserID != issuer.UserID || spec.AgentID != issuer.AgentID {
		return false
	}
	parent := issuer.Record
	if !allowPatternsNarrower(parent.AllowedTools, spec.AllowedTools) ||
		!allowPatternsNarrower(parent.AllowedAdapters, spec.AllowedAdapters) {
		return false
	}
	for _, denied := range parent.DeniedTools {
		if !containsStr(spec.DeniedTools, denied) {
			return false
		}
	}
	for backend, enabled := range parent.Bindings {
		if childEnabled, exists := spec.Bindings[backend]; !enabled && (!exists || childEnabled) {
			return false
		}
	}
	if parent.Enforce && !spec.ToolAllowEnforce {
		return false
	}
	return parent.ExpiresAt == nil || spec.ExpiresAt != nil && !spec.ExpiresAt.After(*parent.ExpiresAt)
}

func allowPatternsNarrower(parent, child []string) bool {
	if len(parent) == 0 {
		return true
	}
	if len(child) == 0 {
		return false
	}
	for _, pattern := range child {
		if !patternCovered(pattern, parent) {
			return false
		}
	}
	return true
}

func patternCovered(pattern string, covering []string) bool {
	for _, candidate := range covering {
		// Compare wildcard languages conservatively: preserve a requested glob
		// verbatim; only concrete tool names can be covered by a parent glob.
		if candidate == pattern || !strings.Contains(pattern, "*") && matchPattern(candidate, pattern) {
			return true
		}
	}
	return false
}

func keySpecOf(k *Key) KeySpec {
	return KeySpec{
		TenantID: k.TenantID, OrgID: k.OrgID, UserID: k.UserID, AgentID: k.AgentID,
		Scopes: k.Scopes, AllowedTools: k.AllowedTools, DeniedTools: k.DeniedTools,
		AllowedAdapters: k.AllowedAdapters, Bindings: k.Bindings,
		ToolAllowEnforce: k.ToolAllowEnforce, ExpiresAt: k.ExpiresAt,
	}
}
