package ursm

import (
	"context"
	"time"
)

// ProbeResult 探测结果
type ProbeResult struct {
	CredentialID      int
	ProbeModel        string
	HealthStatus      string
	AvailabilityState string
	ProbeState        string
	LatencyMs         int
	ErrorMessage      string
	Timestamp         time.Time
	Source            string
}

// RecordProbeResult 记录探测结果（内部调用）
func (m *Manager) RecordProbeResult(ctx context.Context, result ProbeResult) error {
	updates := []StateUpdate{
		{
			Layer:             LayerCredential,
			CredentialID:      result.CredentialID,
			HealthStatus:      &result.HealthStatus,
			AvailabilityState: &result.AvailabilityState,
			Timestamp:         result.Timestamp,
			Source:            result.Source,
		},
		{
			Layer:        LayerModel,
			CredentialID: result.CredentialID,
			Model:        result.ProbeModel,
			ProbeState:   &result.ProbeState,
			Timestamp:    result.Timestamp,
			Source:       result.Source,
		},
		{
			Layer:               LayerNode,
			CredentialID:        result.CredentialID,
			Model:               result.ProbeModel,
			ConsecutiveFailures: intPtr(0), // 重置连续失败计数
			Timestamp:           result.Timestamp,
			Source:              result.Source,
		},
	}

	if m.batchWriter == nil {
		return ErrInternalError
	}
	return m.batchWriter.ApplyUpdates(ctx, updates)
}
