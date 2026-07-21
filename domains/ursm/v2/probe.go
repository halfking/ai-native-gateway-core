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
	_, err := store.ApplyProbeScript.Run(ctx, m.store.RawClient(),
		[]string{key},
		store.BoolFlag(p.Success),
		fmt.Sprintf("%d", p.LatencyMs),
		fmt.Sprintf("%d", time.Now().UnixMilli()),
		// admin_hold and current_admin_hold: the in-Go Manager.ApplyProbe
		// does not yet take an admin-hold parameter (the manual hold is
		// applied via ApplyAdmin). We pass "0"/"0" so the Lua's manual-hold
		// short-circuit has real data to read — any actual hold check
		// against the hash field is a future-task enhancement.
		"0",
		"0",
	).Slice()
	if err != nil {
		return fmt.Errorf("ursm.v2: apply_probe: %w", err)
	}
	return nil
}
