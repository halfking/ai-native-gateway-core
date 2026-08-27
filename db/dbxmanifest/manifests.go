// Package dbxmanifest is the host-side wiring point for the internal/dbx
// framework. It carries the project-specific conventions that the framework
// deliberately does not hard-code, and hosts the table manifests starting
// with the Phase 3 pilot (tenant_model_policies).
//
// No production data path routes through the framework yet: the registry is
// consumed by shadow reads and the metadata gate, while the legacy
// repositories (e.g. admin/model_policies.go) stay authoritative.
package dbxmanifest

import (
	"github.com/kaixuan/llm-gateway-go/internal/dbx"
)

// ScopeConfig returns this project's RLS GUC conventions for injection into
// dbx.ScopeRunner. Values mirror public.get_current_tenant() and the
// tenant_isolation_* / super_admin_bypass policies installed by the startup
// schema (db/db.go, sql/migrations/075-omnifree-schema.sql).
func ScopeConfig() dbx.ScopeConfig {
	return dbx.ScopeConfig{
		TenantGUC:       "app.current_tenant",
		RoleGUC:         "app.current_role",
		BypassGUC:       "app.bypass_rls",
		SuperAdminValue: "super_admin",
		BypassValue:     "true",
	}
}

// DefaultRegistry builds the registry snapshot. The Phase 3 pilot table
// (tenant_model_policies) is activated here: its PK drift against production
// was repaired by migrations 608/609 (2026-08-27), so the metadata gate has a
// migration-shaped, production-matching contract to enforce. Further tables
// graduate here only after their own reviewed-migration drift closure.
func DefaultRegistry() (*dbx.Registry, error) {
	return dbx.NewRegistry(TenantModelPolicies())
}
