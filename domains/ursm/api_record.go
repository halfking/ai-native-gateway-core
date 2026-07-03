package ursm

import (
	"context"
	"fmt"
	"time"
)

// ProbeSubmitter 探测提交接口（避免循环依赖）
type ProbeSubmitter interface {
	SubmitModelProbe(ctx context.Context, credentialID int, model string)
}

// RecordRequest 记录请求结果
func (m *Manager) RecordRequest(ctx context.Context, req RecordRequestAPI) error {
	// 1. 验证参数
	if err := ValidateRecordRequest(req); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	// 2. 分类错误
	errorClass := ClassifyError(req.ErrorKind)

	// 3. 构建StateUpdate列表
	updates := []StateUpdate{
		{
			Layer:        LayerNode,
			CredentialID: req.CredentialID,
			Model:        req.RawModel,
			Success:      req.Success,
			ErrorKind:    req.ErrorKind,
			LatencyMs:    req.LatencyMs,
			Timestamp:    req.Timestamp,
			Source:       "request",
		},
	}

	// 4. 永久故障：连续失败≥2 → 标记Model层broken
	if errorClass == ErrorClassPermanent {
		nodeState, err := m.getNodeState(ctx, req.CredentialID, req.RawModel)
		if err == nil && nodeState != nil && nodeState.ConsecutiveFailures >= 1 {
			brokenState := "broken_confirmed"
			updates = append(updates, StateUpdate{
				Layer:        LayerModel,
				CredentialID: req.CredentialID,
				Model:        req.RawModel,
				ProbeState:   &brokenState,
				Timestamp:    req.Timestamp,
				Source:       "request",
			})
		}
	}

	// 5. 临时故障：连续失败≥3 → 触发探测
	if errorClass == ErrorClassTransient && !req.Success {
		nodeState, err := m.getNodeState(ctx, req.CredentialID, req.RawModel)
		if err == nil && nodeState != nil && nodeState.ConsecutiveFailures >= 2 {
			// 触发探测
			if m.probeSubmitter != nil {
				go m.probeSubmitter.SubmitModelProbe(ctx, req.CredentialID, req.RawModel)
			}
		}
	}

	// 6. 调用BatchWriter原子写入
	if m.batchWriter == nil {
		return fmt.Errorf("batch writer not initialized")
	}
	return m.batchWriter.ApplyUpdates(ctx, updates)
}

// getNodeState 获取节点状态（临时实现，后续从cache读取）
func (m *Manager) getNodeState(ctx context.Context, credentialID int, model string) (*NodeState, error) {
	// TODO: 从nodeCache读取
	// 暂时返回模拟数据
	return &NodeState{
		CredentialID:        credentialID,
		RawModel:            model,
		ConsecutiveFailures: 0,
		UpdatedAt:           time.Now(),
	}, nil
}

// stringPtr 返回字符串指针
func stringPtr(s string) *string {
	return &s
}

// intPtr 返回整数指针
func intPtr(i int) *int {
	return &i
}
