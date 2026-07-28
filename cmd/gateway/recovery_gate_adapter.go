// Package main (cmd/gateway) — recovery_gate_adapter.go
//
// 适配器：把 *ursmv2.Manager 包成 systemmonitor.RecoveryGate 接口实例。
//
// 2026-07-29 (audit follow-up #4): production wiring uses this adapter
// instead of importing domains/ursm/v2/recovery into bg/systemmonitor
// (which would create a cycle through the SystemMonitor wiring chain).
// Adapter does the typed field-by-field conversion of recovery.Stats
// → systemmonitor.RecoveryStats.
package main

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg/systemmonitor"
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
)

// v2RecoveryGateAdapter bridges domains/ursm/v2.Manager to
// systemmonitor.RecoveryGate. All methods delegate to the wrapped
// v2.Manager; Stats() converts recovery.Stats to systemmonitor.RecoveryStats.
type v2RecoveryGateAdapter struct {
	mgr *ursmv2.Manager
}

func newV2RecoveryGateAdapter(mgr *ursmv2.Manager) *v2RecoveryGateAdapter {
	return &v2RecoveryGateAdapter{mgr: mgr}
}

// MarkClosedDebounced delegates to v2.Manager (which in turn delegates
// to recovery.Manager). See recovery.Manager.MarkClosedDebounced for
// the cluster-coordination contract.
func (a *v2RecoveryGateAdapter) MarkClosedDebounced(ctx context.Context, reason string, debounceTTL time.Duration) (bool, error) {
	if a == nil || a.mgr == nil {
		return false, nil
	}
	return a.mgr.MarkClosedDebounced(ctx, reason, debounceTTL)
}

// RestoreIfClosed delegates to v2.Manager.
func (a *v2RecoveryGateAdapter) RestoreIfClosed(ctx context.Context) (int, error) {
	if a == nil || a.mgr == nil {
		return 0, nil
	}
	return a.mgr.RestoreIfClosed(ctx)
}

// Stats converts the recovery.Stats snapshot (with its named fields)
// to the systemmonitor.RecoveryStats struct (matching fields, same
// time.Time types). Safe on a nil adapter / nil manager.
func (a *v2RecoveryGateAdapter) Stats() systemmonitor.RecoveryStats {
	if a == nil || a.mgr == nil {
		return systemmonitor.RecoveryStats{}
	}
	s := a.mgr.RecoveryStats()
	keyCount := 0
	if !s.LastRecoveryAt.IsZero() {
		keyCount = a.mgr.LastRecoveryKeyCount()
	}
	return systemmonitor.RecoveryStats{
		LastError:            s.LastError,
		LastErrorAt:          s.LastErrorAt,
		LastRecoveryAt:       s.LastRecoveryAt,
		LastRecoveryKeyCount: keyCount,
	}
}

// Ensure unused import linter doesn't drop the time package.
// (Stats() touches time.Time but the type itself isn't referenced.)
var _ time.Time
