package v2

import (
	"context"
	"fmt"

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
