package v2

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

func (m *Manager) ApplyProbe(ctx context.Context, p api.ProbeOutcome) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("ursm.v2: nil manager")
	}
	key := store.NodeKey(m.cfg.RedisKeyPrefix, p.CredentialID, p.RawModel)
	// Read the existing manual_hold so the Lua script's manual-hold
	// short-circuit has real data. A missing key or transport error must
	// NOT block the probe: only an explicit "1" on manual_hold causes the
	// Lua to ignore the probe. Any other outcome (Nil, error, empty) is
	// treated as "0" and the probe proceeds normally.
	manualHold, _ := m.store.RawClient().HGet(ctx, key, "manual_hold").Result()
	currentAdminHold := "0"
	if manualHold == "1" {
		currentAdminHold = "1"
	}
	_, err := store.ApplyProbeScript.Run(ctx, m.store.RawClient(),
		[]string{key},
		store.BoolFlag(p.Success),
		fmt.Sprintf("%d", p.LatencyMs),
		fmt.Sprintf("%d", time.Now().UnixMilli()),
		// admin_hold: the in-Go Manager.ApplyProbe does not take an
		// admin-hold parameter; the manual hold is applied via ApplyAdmin.
		"0",
		// current_admin_hold: derived from the live manual_hold field above.
		currentAdminHold,
	).Slice()
	if err != nil {
		return fmt.Errorf("ursm.v2: apply_probe: %w", err)
	}
	return nil
}
