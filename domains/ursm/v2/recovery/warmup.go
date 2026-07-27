package recovery

import (
	"context"
	"fmt"
	"time"
)

type Seed struct {
	ProviderID   int
	CredentialID int
	RawModel     string
	TenantID     string
}

func (m *Manager) WarmupFromSeed(ctx context.Context, seeds []Seed) error {
	if len(seeds) == 0 {
		return fmt.Errorf("ursm.v2: empty warmup refused")
	}
	if err := m.SetReady(ctx, false); err != nil {
		return err
	}
	pipe := m.rdb.Pipeline()
	for _, s := range seeds {
		key := fmt.Sprintf("%snode:%s:%d:%s", m.prefix, s.TenantID, s.CredentialID, s.RawModel)
		if s.TenantID == "" {
			key = fmt.Sprintf("%snode:%d:%s", m.prefix, s.CredentialID, s.RawModel)
		}
		pipe.HSet(ctx, key,
			"available", "1",
			"source_priority", "30", // recover priority
			"generation", "1",
			"seed_source", "warmup",
			"updated_at_ms", fmt.Sprintf("%d", time.Now().UnixMilli()),
		)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return m.SetReady(ctx, true)
}
