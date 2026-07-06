package session

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

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
	if w == nil {
		return
	}
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
	_ = w.scanOnce(ctx)
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
	if w == nil || w.redis == nil {
		return nil
	}
	iter := w.redis.Scan(ctx, 0, "session:stopped:*", 100).Iterator()
	cutoff := time.Now().Add(-w.stoppedTTL)
	for iter.Next(ctx) {
		setKey := iter.Val()
		sessionIDs, err := w.redis.SMembers(ctx, setKey).Result()
		if err != nil {
			continue
		}
		for _, sessionID := range sessionIDs {
			_ = w.cleanExpired(ctx, sessionID, cutoff)
		}
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("scan iterator error: %w", err)
	}
	return nil
}

func (w *CleanupWorker) cleanExpired(ctx context.Context, sessionID string, cutoff time.Time) error {
	hashKey := "session:" + sessionID
	stoppedAtStr, err := w.redis.HGet(ctx, hashKey, FieldStoppedAt).Result()
	if err == redis.Nil {
		return w.removeFromStoppedSets(ctx, sessionID)
	}
	if err != nil {
		return err
	}
	stoppedAt, err := parseTime(stoppedAtStr)
	if err != nil {
		return err
	}
	if stoppedAt.After(cutoff) {
		return nil
	}
	pipe := w.redis.Pipeline()
	pipe.Del(ctx, hashKey)
	pipe.Del(ctx, credRotationsKey(sessionID))
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return w.removeFromStoppedSets(ctx, sessionID)
}

func (w *CleanupWorker) removeFromStoppedSets(ctx context.Context, sessionID string) error {
	iter := w.redis.Scan(ctx, 0, "session:stopped:*", 100).Iterator()
	for iter.Next(ctx) {
		w.redis.SRem(ctx, iter.Val(), sessionID)
	}
	return iter.Err()
}
