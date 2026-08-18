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

// ApplyProbeForTenant keeps the legacy fixed-priority (Probe=20) probe write.
func (m *Manager) ApplyProbeForTenant(ctx context.Context, tenant string, p api.ProbeOutcome) error {
	return m.ApplyProbeForTenantWithSource(ctx, tenant, p, api.SourcePriorityProbe)
}

// probeWriteTTLFloor bounds the Redis TTL applied by probe-priority writes.
// The node probe worker re-verifies a healthy node at most every 1h and a
// failing one at most every 6h (NodeProbeBackoffChain cap), but node keys
// carry the traffic-oriented NodeTTL (minutes-to-hours). A probe confirming
// a node healthy with a TTL shorter than the next scheduled probe left the
// key expired in between; the T4 read contract then treats the missing key
// as Available=false and the model locked out until manual intervention
// (glm-5.2 outage on 154, 2026-08-18). 7h covers the 6h cap with slack.
const probeWriteTTLFloor = 7 * time.Hour

// ApplyProbeForTenantWithSource is the priority-parameterized variant added
// for 会话优化 v4 T5 / FR-4 R4.3 / UT-UR-08. apply_probe.lua previously
// hard-coded source priority=Probe(20); the 36h lookback recovery scan
// (bg/credential_recovery.go) needs to write at Recover(30)
// (api.SourcePriorityRecover). sourcePriority <= 0 keeps the legacy
// Probe=20 default so older callers are byte-for-byte compatible.
//
// Recovery semantics follow the existing lua state machine unchanged:
// success during a cooling window recovers the node immediately;
// manual_hold still short-circuits; record_request.lua's source-priority
// guard (priority > 10 → telemetry only) still prevents ordinary request
// traffic from overriding a Recover-priority write.
func (m *Manager) ApplyProbeForTenantWithSource(ctx context.Context, tenant string, p api.ProbeOutcome, sourcePriority int) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("ursm.v2: nil manager")
	}
	if !m.scope.Allows(tenant, p.CredentialID, p.RawModel) {
		return fmt.Errorf("%w: tenant=%q credential_id=%d model=%q", ErrOutOfScope, tenant, p.CredentialID, p.RawModel)
	}
	if sourcePriority <= 0 {
		sourcePriority = api.SourcePriorityProbe
	}
	key := store.NodeKeyForTenant(m.cfg.RedisKeyPrefix, tenant, p.CredentialID, p.RawModel)
	// M3 (2026-07-28): apply_probe.lua now reads manual_hold directly inside
	// the script. Removes a hot-path HGet (one fewer Redis round-trip per
	// probe) and closes the prior TOCTOU window between Go-side HGet and
	// Eval. Admin priority still dominates via the lua-internal manual_hold
	// read. ARGV[4]=0 / ARGV[5]="0" keep ABI parity for older callers;
	// the lua's live-read short-circuit runs unconditionally.
	nodeTTLSeconds := int(m.effectiveConfig().NodeTTL / time.Second)
	if nodeTTLSeconds <= 0 {
		nodeTTLSeconds = int(DefaultConfig().NodeTTL / time.Second)
	}
	// Probe evidence must outlive the worker's retry ladder — see
	// probeWriteTTLFloor. Traffic writes (record_request.lua) keep the
	// hot-configured NodeTTL because they refresh continuously while the
	// node serves requests.
	if floor := int(probeWriteTTLFloor / time.Second); nodeTTLSeconds < floor {
		nodeTTLSeconds = floor
	}
	_, err := store.ApplyProbeScript.Run(ctx, m.store.RawClient(),
		[]string{key},
		store.BoolFlag(p.Success),
		fmt.Sprintf("%d", p.LatencyMs),
		fmt.Sprintf("%d", time.Now().UnixMilli()),
		"0", // deprecated: caller-supplied admin_hold (always 0 here)
		"0", // deprecated: pre-read current_admin_hold (lua reads it instead)
		fmt.Sprintf("%d", nodeTTLSeconds),
		fmt.Sprintf("%d", sourcePriority), // 会话优化 v4 T5: Recover=30 for the 36h scan
	).Slice()
	if err != nil {
		return fmt.Errorf("ursm.v2: apply_probe: %w", err)
	}
	m.invalidateNode(tenant, p.CredentialID, p.RawModel)
	return nil
}
