package ursm

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// ConcurrencySlotManager 管理并发槽资源
type ConcurrencySlotManager struct {
	redis *redis.Client
}

// NewConcurrencySlotManager 创建并发槽管理器
func NewConcurrencySlotManager(redis *redis.Client) *ConcurrencySlotManager {
	return &ConcurrencySlotManager{
		redis: redis,
	}
}

// CheckAndAcquire 检查并获取并发槽
// 返回: (acquired, reason)
func (m *ConcurrencySlotManager) CheckAndAcquire(
	ctx context.Context,
	credentialID int,
	sessionID string,
	concurrencyLimit int,
) (bool, string) {
	if m.redis == nil {
		return false, "redis_unavailable"
	}

	if concurrencyLimit <= 0 {
		// 无并发限制
		return true, ""
	}

	limKey := concSlotKey(credentialID)
	sessKey := concSessionKey(credentialID, sessionID)

	res, err := acquireConcurrencyScript.Run(ctx, m.redis,
		[]string{limKey, sessKey},
		concurrencyLimit,
	).Result()

	if err != nil {
		return false, fmt.Sprintf("redis_error: %v", err)
	}

	acquired, ok := res.(int64)
	if !ok {
		return false, "invalid_script_response"
	}

	if acquired != 1 {
		return false, "concurrency_limit_reached"
	}

	return true, ""
}

// Release 释放并发槽
func (m *ConcurrencySlotManager) Release(
	ctx context.Context,
	credentialID int,
	sessionID string,
) error {
	if m.redis == nil {
		return ErrInternalError
	}

	limKey := concSlotKey(credentialID)
	sessKey := concSessionKey(credentialID, sessionID)

	_, err := releaseConcurrencyScript.Run(ctx, m.redis,
		[]string{limKey, sessKey},
	).Result()

	return err
}
