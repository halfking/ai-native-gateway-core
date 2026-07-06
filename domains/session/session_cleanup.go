// Package session — session_cleanup.go
package session

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// CleanupWorker 定期清理过期 stopped session
type CleanupWorker struct {
	redis        *redis.Client
	stoppedTTL   time.Duration
	scanInterval time.Duration
	stopCh       chan struct{}
	doneCh       chan struct{}
}

func NewCleanupWorker(redisClient *redis.Client, stoppedTTL, scanInterval time.Duration) *CleanupWorker {
	if stoppedTTL <= 0 {
		stoppedTTL = 30 * time.Minute
	}
	if scanInterval <= 0 {
		scanInterval = 5 * time.Minute
	}
	return &CleanupWorker{
		redis:        redisClient,
		stoppedTTL:   stoppedTTL,
		scanInterval: scanInterval,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

func (w *CleanupWorker) Start(ctx context.Context) {
	go w.runLoop(ctx)
}

func (w *CleanupWorker) Stop() {
	if w == nil {
		return
	}
	close(w.stopCh)
	<-w.doneCh
}

func (w *CleanupWorker) runLoop(ctx context.Context) {
	defer close(w.doneCh)
	ticker := time.NewTicker(w.scanInterval)
	defer ticker.Stop()
	// 启动时立即扫描一次
	if err := w.scanOnce(ctx); err != nil {
		slog.Warn("session_cleanup: initial scan failed", "error", err)
	}
	for {
		select {
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.scanOnce(ctx); err != nil {
				slog.Warn("session_cleanup: scan failed", "error", err)
			}
		}
	}
}

func (w *CleanupWorker) scanOnce(ctx context.Context) error {
	if w.redis == nil {
		return nil
	}
	pattern := "session:stopped:*"
	iter := w.redis.Scan(ctx, 0, pattern, 100).Iterator()
	cutoff := time.Now().Add(-w.stoppedTTL)
	cleaned := 0
	for iter.Next(ctx) {
		stoppedKey := iter.Val()
		sessionIDs, err := w.redis.SMembers(ctx, stoppedKey).Result()
		if err != nil {
			continue
		}
		for _, sessionID := range sessionIDs {
			if w.cleanExpired(ctx, sessionID, cutoff) {
				cleaned++
			}
		}
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("scan iterator error: %w", err)
	}
	if cleaned > 0 {
		slog.Info("session_cleanup: completed", "cleaned", cleaned, "ttl", w.stoppedTTL)
	}
	return nil
}

func (w *CleanupWorker) cleanExpired(ctx context.Context, sessionID string, cutoff time.Time) bool {
	hashKey := "session:" + sessionID
	stoppedAtStr, err := w.redis.HGet(ctx, hashKey, FieldStoppedAt).Result()
	if err != nil {
		// hash 不存在，尝试从 set 移除
		w.removeFromSets(ctx, sessionID)
		return false
	}
	stoppedAt, err := parseTime(stoppedAtStr)
	if err != nil {
		return false
	}
	if stoppedAt.After(cutoff) {
		return false
	}
	pipe := w.redis.Pipeline()
	pipe.Del(ctx, hashKey)
	pipe.Del(ctx, credRotationsKey(sessionID))
	_, err = pipe.Exec(ctx)
	if err != nil {
		return false
	}
	w.removeFromSets(ctx, sessionID)
	return true
}

func (w *CleanupWorker) removeFromSets(ctx context.Context, sessionID string) {
	pattern := "session:stopped:*"
	iter := w.redis.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		w.redis.SRem(ctx, iter.Val(), sessionID)
	}
}
