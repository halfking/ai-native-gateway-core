package ursm

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/runctx"
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

	if errorClass == ErrorClassTransient && !req.Success {
		nodeState, err := m.getNodeState(ctx, req.CredentialID, req.RawModel)
		if err == nil && nodeState != nil && nodeState.ConsecutiveFailures >= 2 {
			// 触发探测。探测提交是后台补偿动作，不应绑在请求 ctx 上。
			if m.probeSubmitter != nil {
				go func() {
					probeCtx, cancel := runctx.DetachedTimeout(ctx, 10*time.Second)
					defer cancel()
					m.probeSubmitter.SubmitModelProbe(probeCtx, req.CredentialID, req.RawModel)
				}()
			}
		}
	}

	// 6. 调用BatchWriter原子写入
	if m.batchWriter == nil {
		return fmt.Errorf("batch writer not initialized")
	}
	return m.batchWriter.ApplyUpdates(ctx, updates)
}

// getNodeState 获取节点状态
func (m *Manager) getNodeState(ctx context.Context, credentialID int, model string) (*NodeState, error) {
	if m.nodeCache == nil {
		return &NodeState{
			CredentialID:        credentialID,
			RawModel:            model,
			ConsecutiveFailures: 0,
			UpdatedAt:           time.Now(),
		}, nil
	}

	key := fmt.Sprintf("%d:%s", credentialID, model)
	state, err := m.nodeCache.Get(ctx, key)
	if err != nil {
		// 未找到或查询失败时返回默认状态
		return &NodeState{
			CredentialID:        credentialID,
			RawModel:            model,
			ConsecutiveFailures: 0,
			UpdatedAt:           time.Now(),
		}, nil
	}
	return state, nil
}

// stringPtr 返回字符串指针
func stringPtr(s string) *string {
	return &s
}

// intPtr 返回整数指针
func intPtr(i int) *int {
	return &i
}
