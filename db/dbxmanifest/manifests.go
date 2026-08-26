// Package dbxmanifest is the host-side wiring point for the internal/dbx
// framework. It carries the project-specific conventions that the framework
// deliberately does not hard-code, and will host real table manifests when
// the Phase 3 pilot migrates a low-risk repository.
//
// Nothing here is wired into any production data path yet.
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

// DefaultRegistry builds the registry snapshot. Phase 3 pilot tables are
// registered here (and only here) once a low-risk repository is selected;
// until then the registry is intentionally empty and no business code
// reaches the CRUD layer through it.
func DefaultRegistry() (*dbx.Registry, error) {
	return dbx.NewRegistry()
}
