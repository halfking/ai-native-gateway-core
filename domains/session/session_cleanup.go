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

const (
	stoppedSessionIndexKey      = "session:stopped:index"
	stoppedSessionIndexSentinel = "__stopped_session_index_initialized__"
)

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
	stoppedSets, indexed, err := w.stoppedSetKeys(ctx)
	if err != nil {
		return err
	}
	if !indexed {
		iter := w.redis.Scan(ctx, 0, "session:stopped:*", 100).Iterator()
		for iter.Next(ctx) {
			stoppedSets = append(stoppedSets, iter.Val())
		}
		if err := iter.Err(); err != nil {
			return fmt.Errorf("scan stopped session indexes failed: %w", err)
		}
		members := append(stringSliceToAny(stoppedSets), stoppedSessionIndexSentinel)
		if err := w.redis.SAdd(ctx, stoppedSessionIndexKey, members...).Err(); err != nil {
			return err
		}
	}
	cutoff := time.Now().Add(-w.stoppedTTL)
	for _, setKey := range stoppedSets {
		sessionIDs, err := w.redis.SMembers(ctx, setKey).Result()
		if err != nil {
			if err == redis.Nil {
				_ = w.redis.SRem(ctx, stoppedSessionIndexKey, setKey).Err()
			}
			continue
		}
		for _, sessionID := range sessionIDs {
			_ = w.cleanExpired(ctx, sessionID, cutoff)
		}
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
	pipe.Del(ctx, "session_pref:"+sessionID)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return w.removeFromStoppedSets(ctx, sessionID)
}

func (w *CleanupWorker) removeFromStoppedSets(ctx context.Context, sessionID string) error {
	setKeys, _, err := w.stoppedSetKeys(ctx)
	if err != nil {
		return err
	}
	for _, setKey := range setKeys {
		if err := w.redis.SRem(ctx, setKey, sessionID).Err(); err != nil && err != redis.Nil {
			return err
		}
	}
	return nil
}

func (w *CleanupWorker) stoppedSetKeys(ctx context.Context) ([]string, bool, error) {
	exists, err := w.redis.Exists(ctx, stoppedSessionIndexKey).Result()
	if err != nil {
		return nil, false, err
	}
	if exists == 0 {
		return nil, false, nil
	}
	setKeys, err := w.redis.SMembers(ctx, stoppedSessionIndexKey).Result()
	if err != nil {
		return nil, true, err
	}
	filtered := make([]string, 0, len(setKeys))
	for _, setKey := range setKeys {
		if setKey != stoppedSessionIndexSentinel {
			filtered = append(filtered, setKey)
		}
	}
	return filtered, true, nil
}

func stringSliceToAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}
