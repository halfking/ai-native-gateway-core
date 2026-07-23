package v2

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// ApplyAdmin writes a manual hold (or release) decision to the v2 store.
// It does not touch the readiness gate — admin operations must remain
// available even when the recovery gate is closed so operators can
// drain or re-enable nodes during incidents.
func (m *Manager) ApplyAdmin(ctx context.Context, a api.AdminAction) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("ursm.v2: nil manager")
	}
	key := store.NodeKey(m.cfg.RedisKeyPrefix, a.CredentialID, a.RawModel)
	disabled := "0"
	if a.ManualDisabled != nil && *a.ManualDisabled {
		disabled = "1"
	}
	_, err := store.ApplyAdminScript.Run(ctx, m.store.RawClient(),
		[]string{key}, disabled, a.Actor, a.Reason,
		fmt.Sprintf("%d", a.IssuedAtMs)).Slice()
	if err != nil {
		return fmt.Errorf("ursm.v2: apply_admin: %w", err)
	}
	return nil
}

// ClearState clears cooling state (cool_until_ms, cool_start_ms) and error
// counters (fail_streak, fail_count) in Redis for emergency repair operations
// "clear_circuit" and "reset_errors". It does NOT modify the PostgreSQL
// credentials table — that is caller's responsibility.
func (m *Manager) ClearState(ctx context.Context, credentialID int, rawModel string) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("ursm.v2: nil manager")
	}
	key := store.NodeKey(m.cfg.RedisKeyPrefix, credentialID, rawModel)
	result, err := store.ClearStateScript.Run(ctx, m.store.RawClient(),
		[]string{key}, fmt.Sprintf("%d", time.Now().UnixMilli())).Slice()
	if err != nil {
		return fmt.Errorf("ursm.v2: clear_state: %w", err)
	}
	if len(result) > 0 && result[0] == "key_not_found" {
		return fmt.Errorf("ursm.v2: node key not found in Redis")
	}
	return nil
}
