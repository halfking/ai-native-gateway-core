package dbx

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// ScopeConfig carries host project conventions into the framework. The
// framework hard-codes none of them, so it can be extracted as an
// independent module.
type ScopeConfig struct {
	// TenantGUC is the transaction-local GUC holding the tenant id
	// (host value: "app.current_tenant").
	TenantGUC string
	// RoleGUC is the privilege role GUC (host value: "app.current_role").
	RoleGUC string
	// BypassGUC is the RLS bypass GUC (host value: "app.bypass_rls").
	BypassGUC string
	// SuperAdminValue is the privileged role value (host: "super_admin").
	SuperAdminValue string
	// BypassValue is the bypass-enabled value (host: "true").
	BypassValue string
}

// gucRe whitelists custom GUC names: dotted lowercase identifiers.
var gucRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*){0,3}$`)

// Validate ensures all GUC conventions are present and injectable as
// parameters (no SQL-text embedding of untrusted shapes).
func (c ScopeConfig) Validate() error {
	for name, v := range map[string]string{
		"TenantGUC":       c.TenantGUC,
		"RoleGUC":         c.RoleGUC,
		"BypassGUC":       c.BypassGUC,
		"SuperAdminValue": c.SuperAdminValue,
		"BypassValue":     c.BypassValue,
	} {
		if v == "" {
			return fmt.Errorf("dbx: ScopeConfig.%s is empty: %w", name, ErrInvalidInput)
		}
		if (name == "TenantGUC" || name == "RoleGUC" || name == "BypassGUC") && !gucRe.MatchString(v) {
			return fmt.Errorf("dbx: ScopeConfig.%s %q is not a valid GUC name: %w", name, v, ErrInvalidInput)
		}
	}
	return nil
}

// ScopeRunner opens transactions with the tenant (or privileged) GUC set
// transaction-locally. GUCs are bound as parameters via
// SELECT set_config($1, $2, true); they expire with the transaction and can
// never leak into a pooled connection.
type ScopeRunner struct {
	cfg      ScopeConfig
	beginner TxBeginner
}

// NewScopeRunner validates the config and binds a transaction beginner
// (usually the shared *pgxpool.Pool).
func NewScopeRunner(cfg ScopeConfig, beginner TxBeginner) (*ScopeRunner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if beginner == nil {
		return nil, fmt.Errorf("dbx: ScopeRunner: nil TxBeginner: %w", ErrInvalidInput)
	}
	return &ScopeRunner{cfg: cfg, beginner: beginner}, nil
}

// Config returns the injected configuration (for host-side wiring/tests).
func (r *ScopeRunner) Config() ScopeConfig { return r.cfg }

// WithTenantTx runs fn inside a read-write transaction with the tenant GUC
// set. tenantID must come from the trusted authenticated context, never
// from a request payload. Empty tenantID fails closed with ErrMissingScope;
// there is no default-tenant fallback in the framework.
func (r *ScopeRunner) WithTenantTx(ctx context.Context, tenantID string, fn func(context.Context, pgx.Tx) error) error {
	if tenantID == "" {
		return fmt.Errorf("dbx: WithTenantTx: %w", ErrMissingScope)
	}
	return WithTx(ctx, r.beginner, func(ctx context.Context, tx pgx.Tx) error {
		if err := setGUC(ctx, tx, r.cfg.TenantGUC, tenantID); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// WithTenantReadOnlyTx is WithTenantTx with a read-only transaction.
func (r *ScopeRunner) WithTenantReadOnlyTx(ctx context.Context, tenantID string, fn func(context.Context, pgx.Tx) error) error {
	if tenantID == "" {
		return fmt.Errorf("dbx: WithTenantReadOnlyTx: %w", ErrMissingScope)
	}
	return WithReadOnlyTx(ctx, r.beginner, func(ctx context.Context, tx pgx.Tx) error {
		if err := setGUC(ctx, tx, r.cfg.TenantGUC, tenantID); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// WithSuperAdminTx runs fn with both the super-admin role GUC and the RLS
// bypass GUC set. This is the explicit, auditable cross-tenant path: the
// host MUST have authenticated and authorized the caller before invoking
// it. It never sets a tenant GUC.
func (r *ScopeRunner) WithSuperAdminTx(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	return WithTx(ctx, r.beginner, func(ctx context.Context, tx pgx.Tx) error {
		if err := setGUC(ctx, tx, r.cfg.RoleGUC, r.cfg.SuperAdminValue); err != nil {
			return err
		}
		if err := setGUC(ctx, tx, r.cfg.BypassGUC, r.cfg.BypassValue); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// setGUC binds both the GUC name and value as parameters.
func setGUC(ctx context.Context, tx pgx.Tx, guc, value string) error {
	if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", guc, value); err != nil {
		return fmt.Errorf("dbx: set_config(%s): %w", guc, err)
	}
	return nil
}
