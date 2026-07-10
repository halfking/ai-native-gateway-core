package ursm

import (
	"context"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/runctx"
	"github.com/redis/go-redis/v9"
)

// FingerprintSlotManager 管理指纹槽资源
type FingerprintSlotManager struct {
	redis      *redis.Client
	activeGate time.Duration // 活跃阈值（默认5分钟）
	slotTTL    time.Duration // 槽位TTL（默认30分钟）
	pinTTL     time.Duration // Pin TTL（默认24小时）
}

// NewFingerprintSlotManager 创建指纹槽管理器
func NewFingerprintSlotManager(redis *redis.Client, cfg FpSlotConfig) *FingerprintSlotManager {
	return &FingerprintSlotManager{
		redis:      redis,
		activeGate: cfg.ActiveGate,
		slotTTL:    cfg.SlotTTL,
		pinTTL:     cfg.PinTTL,
	}
}

// CheckAndAcquire 检查并获取指纹槽
// 返回: (slotIndex, acquired, reason)
// 策略: Pin复用 → 空闲槽 → LRU抢占
func (m *FingerprintSlotManager) CheckAndAcquire(
	ctx context.Context,
	credentialID int,
	sessionID string,
	fpSlotLimit int,
) (int, bool, string) {
	if m.redis == nil {
		return -1, false, "redis_unavailable"
	}

	if fpSlotLimit <= 0 {
		// 无限制：返回槽位0
		return 0, true, ""
	}

	// 步骤1: 尝试Pin复用
	pinKey := fpPinKey(sessionID, credentialID)
	pinnedSlotStr, err := m.redis.Get(ctx, pinKey).Result()
	if err == nil && pinnedSlotStr != "" {
		// 尝试获取pin指向的槽位
		slotIndex := 0
		fmt.Sscanf(pinnedSlotStr, "%d", &slotIndex)

		slotKey := fpSlotKey(credentialID, slotIndex)

		res, err := tryPinReuseScript.Run(ctx, m.redis,
			[]string{pinKey, slotKey},
			sessionID, int(m.slotTTL.Seconds()), int(m.pinTTL.Seconds()),
		).Result()

		if err == nil {
			arr, ok := res.([]interface{})
			if ok && len(arr) >= 3 {
				acquired, _ := arr[0].(int64)
				if acquired == 1 {
					slot, _ := arr[1].(string)
					slotIdx := 0
					fmt.Sscanf(slot, "%d", &slotIdx)
					return slotIdx, true, "pin_reuse"
				}
			}
		}
	}

	// 步骤2: LRU抢占（包含空闲槽扫描）
	prefix := fmt.Sprintf("llmgw:cred_fp_slot:%d", credentialID)

	res, err := acquireLRUScript.Run(ctx, m.redis,
		[]string{prefix},
		fpSlotLimit,
		sessionID,
		int(m.slotTTL.Seconds()),
		int(m.pinTTL.Seconds()),
		int(m.activeGate.Seconds()),
		pinKey,
		credentialID,
	).Result()

	if err != nil {
		return -1, false, fmt.Sprintf("redis_error: %v", err)
	}

	arr, ok := res.([]interface{})
	if !ok || len(arr) < 3 {
		return -1, false, "invalid_script_response"
	}

	acquired, _ := arr[0].(int64)
	if acquired != 1 {
		// 全部槽位活跃，无法抢占
		return -1, false, "all_slots_active"
	}

	slotIndex, _ := arr[1].(int64)
	oldHolder, _ := arr[2].(string)

	reason := "free_slot_acquired"
	if oldHolder != "" {
		reason = "lru_preempt"
	}

	return int(slotIndex), true, reason
}

// Release 释放指纹槽（刷新TTL，保留pin）
func (m *FingerprintSlotManager) Release(
	ctx context.Context,
	credentialID int,
	slotIndex int,
	sessionID string,
) error {
	if m.redis == nil {
		return ErrInternalError
	}

	releaseCtx := ctx
	if releaseCtx == nil {
		releaseCtx, _ = runctx.BackgroundTimeout(3 * time.Second)
	}
	if releaseCtx.Err() != nil {
		var cancel context.CancelFunc
		releaseCtx, cancel = runctx.BackgroundTimeout(3 * time.Second)
		defer cancel()
	}

	slotKey := fpSlotKey(credentialID, slotIndex)
	pinKey := fpPinKey(sessionID, credentialID)

	_, err := releaseSlotScript.Run(releaseCtx, m.redis,
		[]string{slotKey, pinKey},
		sessionID,
		int(m.slotTTL.Seconds()),
		int(m.pinTTL.Seconds()),
	).Result()

	return err
}

// ForceUnpin 强制解绑pin（用于测试或管理操作）
func (m *FingerprintSlotManager) ForceUnpin(
	ctx context.Context,
	credentialID int,
	sessionID string,
) error {
	if m.redis == nil {
		return ErrInternalError
	}

	pinKey := fpPinKey(sessionID, credentialID)

	_, err := forceUnpinScript.Run(ctx, m.redis,
		[]string{pinKey},
	).Result()

	return err
}
