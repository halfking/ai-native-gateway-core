package v2

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// ApplyProbe preserves the legacy non-tenant probe entry point.
func (m *Manager) ApplyProbe(ctx context.Context, p api.ProbeOutcome) error {
	return m.ApplyProbeForTenant(ctx, "", p)
}

func (m *Manager) ApplyProbeForTenant(ctx context.Context, tenant string, p api.ProbeOutcome) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("ursm.v2: nil manager")
	}
	key := store.NodeKeyForTenant(m.cfg.RedisKeyPrefix, tenant, p.CredentialID, p.RawModel)
	// M3 (2026-07-28): apply_probe.lua now reads manual_hold directly inside
	// the script. Removes a hot-path HGet (one fewer Redis round-trip per
	// probe) and closes the prior TOCTOU window between Go-side HGet and
	// Eval. Admin priority still dominates via the lua-internal manual_hold
	// read. ARGV[4]=0 / ARGV[5]="0" keep ABI parity for older callers;
	// the lua's live-read short-circuit runs unconditionally.
	nodeTTLSeconds := int(m.cfg.NodeTTL / time.Second)
	if nodeTTLSeconds <= 0 {
		nodeTTLSeconds = int(DefaultConfig().NodeTTL / time.Second)
	}
	_, err := store.ApplyProbeScript.Run(ctx, m.store.RawClient(),
		[]string{key},
		store.BoolFlag(p.Success),
		fmt.Sprintf("%d", p.LatencyMs),
		fmt.Sprintf("%d", time.Now().UnixMilli()),
		"0", // deprecated: caller-supplied admin_hold (always 0 here)
		"0", // deprecated: pre-read current_admin_hold (lua reads it instead)
		fmt.Sprintf("%d", nodeTTLSeconds),
	).Slice()
	if err != nil {
		return fmt.Errorf("ursm.v2: apply_probe: %w", err)
	}
	m.invalidateNode(tenant, p.CredentialID, p.RawModel)
	return nil
}
