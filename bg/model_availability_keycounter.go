package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// AvailabilityKeyCounter periodically refreshes the availability key gauge.
//
// The index is maintained with each cache write. A one-time legacy scan only
// seeds the index after an upgrade, so periodic reconciliation never walks the
// shared Redis database.
type AvailabilityKeyCounter struct {
	redis    *redis.Client
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewAvailabilityKeyCounter constructs a key counter. Returns nil
// when the redis client is missing so main.go wiring is a no-op.
func NewAvailabilityKeyCounter(redisClient *redis.Client, interval time.Duration) *AvailabilityKeyCounter {
	if redisClient == nil {
		return nil
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &AvailabilityKeyCounter{
		redis:    redisClient,
		interval: interval,
		done:     make(chan struct{}),
	}
}

// Start launches the periodic refresh loop. Call Stop to terminate.
func (k *AvailabilityKeyCounter) Start(ctx context.Context) {
	if k == nil {
		return
	}
	ctx, k.cancel = context.WithCancel(ctx)
	go k.run(ctx)
	slog.Info("availability key counter started", "interval", k.interval)
}

// Stop cancels the worker and waits for it to exit.
func (k *AvailabilityKeyCounter) Stop() {
	if k == nil || k.cancel == nil {
		return
	}
	k.cancel()
	<-k.done
}

func (k *AvailabilityKeyCounter) run(ctx context.Context) {
	defer close(k.done)
	ticker := time.NewTicker(k.interval)
	defer ticker.Stop()

	// Initial sweep so the gauge is non-zero on first /metrics scrape.
	k.CountOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			k.CountOnce(ctx)
		}
	}
}

// CountOnce reads the availability index and updates the gauge.
// Exposed so tests and the admin /cache-state path can refresh on
// demand. A compatibility SCAN is used only while initializing a missing
// index after an upgrade or Redis flush.
func (k *AvailabilityKeyCounter) CountOnce(ctx context.Context) {
	if k == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	keys, err := loadAvailabilityIndex(ctx, k.redis)
	if err != nil {
		slog.Warn("availability key counter: index read failed", "error", err)
		return
	}
	recordAvailabilityKeysAbsolute(len(keys))
}

func loadAvailabilityIndex(ctx context.Context, client *redis.Client) ([]string, error) {
	keys, err := client.SMembers(ctx, availabilityIndexKey).Result()
	if err != nil {
		return nil, err
	}
	ready, err := client.Exists(ctx, availabilityIndexReadyKey).Result()
	if err != nil {
		return nil, err
	}
	if ready == 0 {
		iter := client.Scan(ctx, 0, "llmgw:avail:*", 500).Iterator()
		for iter.Next(ctx) {
			keys = append(keys, iter.Val())
		}
		if err := iter.Err(); err != nil {
			return nil, err
		}
		if len(keys) > 0 {
			pipe := client.Pipeline()
			pipe.SAdd(ctx, availabilityIndexKey, keys)
			pipe.Expire(ctx, availabilityIndexKey, modelAvailabilityCacheTTL)
			if _, err := pipe.Exec(ctx); err != nil {
				return nil, err
			}
		}
		if err := client.Set(ctx, availabilityIndexReadyKey, "1", modelAvailabilityCacheTTL).Err(); err != nil {
			return nil, err
		}
	}

	active := make([]string, 0, len(keys))
	stale := make([]string, 0)
	for _, key := range keys {
		exists, err := client.Exists(ctx, key).Result()
		if err != nil {
			return nil, err
		}
		if exists == 1 {
			active = append(active, key)
		} else {
			stale = append(stale, key)
		}
	}
	if len(stale) > 0 {
		if err := client.SRem(ctx, availabilityIndexKey, stale).Err(); err != nil {
			return nil, err
		}
	}
	return active, nil
}
