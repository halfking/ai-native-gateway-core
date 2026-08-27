package v2

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
)

// ApplyAdmin writes a manual hold (or release) decision to the v2 store.
// It does not touch the readiness gate — admin operations must remain
// available even when the recovery gate is closed so operators can
// drain or re-enable nodes during incidents.
func (m *Manager) ApplyAdmin(ctx context.Context, a api.AdminAction) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("ursm.v2: nil manager")
	}
	if !m.scope.Allows(a.TenantID, a.CredentialID, a.RawModel) {
		return fmt.Errorf("%w: tenant=%q credential_id=%d model=%q", ErrOutOfScope, a.TenantID, a.CredentialID, a.RawModel)
	}
	legacyKey := store.NodeKeyForTenant(m.cfg.RedisKeyPrefix, a.TenantID, a.CredentialID, a.RawModel)
	keys := []string{legacyKey}
	schemaMode := m.store.KeySchemaMode()
	if schemaMode != store.KeySchemaModeLegacy {
		k2Key, err := store.K2NodeKeyForTenant(m.cfg.RedisKeyPrefix, a.TenantID, a.CredentialID, a.RawModel)
		if err != nil {
			return fmt.Errorf("ursm.v2: derive k2 admin key: %w", err)
		}
		if schemaMode == store.KeySchemaModeCanonical {
			keys[0] = k2Key
		} else {
			keys = append(keys, k2Key)
		}
	}
	disabled := "0"
	if a.ManualDisabled != nil && *a.ManualDisabled {
		disabled = "1"
	}
	var err error
	if schemaMode == store.KeySchemaModeDual {
		_, err = redissafe.RunScript(ctx, m.store.RawClient(), store.ApplyAdminDualScript, "apply_admin_dual.lua", keys, disabled, a.Actor, a.Reason,
			fmt.Sprintf("%d", a.IssuedAtMs)).Slice()
	} else {
		_, err = redissafe.RunScript(ctx, m.store.RawClient(), store.ApplyAdminScript, "apply_admin.lua", keys, disabled, a.Actor, a.Reason,
			fmt.Sprintf("%d", a.IssuedAtMs)).Slice()
	}
	if err != nil {
		return fmt.Errorf("ursm.v2: apply_admin: %w", err)
	}
	m.invalidateNode(a.TenantID, a.CredentialID, a.RawModel)
	return nil
}

// ClearState clears cooling state (cool_until_ms, cool_start_ms) and error
// counters (fail_streak, fail_count) in Redis for emergency repair operations
// "clear_circuit" and "reset_errors". It does NOT modify the PostgreSQL
// credentials table — that is caller's responsibility.
// ClearState clears the legacy non-tenant node state. New callers should use
// ClearStateForTenant so the target matches tenant-qualified request routing.
func (m *Manager) ClearState(ctx context.Context, credentialID int, rawModel string) error {
	return m.ClearStateForTenant(ctx, "", credentialID, rawModel)
}

// ClearStateForTenant clears cooling state and error counters for one
// tenant-qualified node after an emergency repair.
//
// A missing Redis node key is treated as success (not an error): an idle or
// freshly-added node may never have had cooling/fail state recorded, so there
// is nothing to clear. Returning a hard error here caused the routing-v2
// emergency-repair UI to warn "URSM 状态未清理" even though PostgreSQL (the
// source of truth for resolve) was already updated correctly (2026-08-13).
func (m *Manager) ClearStateForTenant(ctx context.Context, tenant string, credentialID int, rawModel string) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("ursm.v2: nil manager")
	}
	if !m.scope.Allows(tenant, credentialID, rawModel) {
		return fmt.Errorf("%w: tenant=%q credential_id=%d model=%q", ErrOutOfScope, tenant, credentialID, rawModel)
	}
	legacyKey := store.NodeKeyForTenant(m.cfg.RedisKeyPrefix, tenant, credentialID, rawModel)
	keys := []string{legacyKey}
	schemaMode := m.store.KeySchemaMode()
	if schemaMode != store.KeySchemaModeLegacy {
		k2Key, err := store.K2NodeKeyForTenant(m.cfg.RedisKeyPrefix, tenant, credentialID, rawModel)
		if err != nil {
			return fmt.Errorf("ursm.v2: derive k2 clear-state key: %w", err)
		}
		if schemaMode == store.KeySchemaModeCanonical {
			keys[0] = k2Key
		} else {
			keys = append(keys, k2Key)
		}
	}
	var err error
	if schemaMode == store.KeySchemaModeDual {
		_, err = redissafe.RunScript(ctx, m.store.RawClient(), store.ClearStateDualScript, "clear_state_dual.lua", keys,
			fmt.Sprintf("%d", time.Now().UnixMilli())).Slice()
	} else {
		_, err = redissafe.RunScript(ctx, m.store.RawClient(), store.ClearStateScript, "clear_state.lua", keys,
			fmt.Sprintf("%d", time.Now().UnixMilli())).Slice()
	}
	if err != nil {
		return fmt.Errorf("ursm.v2: clear_state: %w", err)
	}
	// key_not_found is benign — nothing to clear. Still invalidate the in-memory
	// cache so the next read reflects the (now clean) authoritative state.
	m.invalidateNode(tenant, credentialID, rawModel)
	return nil
}
