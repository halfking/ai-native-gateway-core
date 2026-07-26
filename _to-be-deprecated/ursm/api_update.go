package ursm

import (
	"context"
	"fmt"
	"time"
)

// UpdateProvider 更新Provider状态
func (m *Manager) UpdateProvider(ctx context.Context, req UpdateProviderAPI) error {
	// 1. 验证参数
	if err := ValidateUpdateProvider(req); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	// 2. 构建更新
	updates := []StateUpdate{
		{
			Layer:          LayerProvider,
			ProviderID:     req.ProviderID,
			Enabled:        req.Enabled,
			ManualDisabled: req.ManualDisabled,
			Timestamp:      time.Now(),
			Actor:          req.Actor,
			Reason:         req.Reason,
			Source:         "manual",
		},
	}

	// 3. 审计日志（简化版）
	// TODO: 完善审计日志

	// 4. 原子写入（级联逻辑由BatchWriter处理）
	if m.batchWriter == nil {
		return fmt.Errorf("batch writer not initialized")
	}
	return m.batchWriter.ApplyUpdates(ctx, updates)
}

// UpdateCredential 更新Credential状态
func (m *Manager) UpdateCredential(ctx context.Context, req UpdateCredentialAPI) error {
	// 1. 验证参数
	if err := ValidateUpdateCredential(req); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	// 2. 验证Provider必须可用（简化版，后续完善）
	// TODO: 检查Provider状态

	// 3. 构建更新
	updates := []StateUpdate{
		{
			Layer:             LayerCredential,
			CredentialID:      req.CredentialID,
			AvailabilityState: req.AvailabilityState,
			QuotaState:        req.QuotaState,
			Timestamp:         time.Now(),
			Actor:             req.Actor,
			Reason:            req.Reason,
			Source:            "manual",
		},
	}

	// 如果设置了ManualDisabled，需要单独处理
	if req.ManualDisabled != nil {
		updates[0].ManualDisabled = req.ManualDisabled
	}

	// 4. 审计日志（简化版）
	// TODO: 完善审计日志

	// 5. 原子写入
	if m.batchWriter == nil {
		return fmt.Errorf("batch writer not initialized")
	}
	return m.batchWriter.ApplyUpdates(ctx, updates)
}
