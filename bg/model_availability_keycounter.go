package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// AvailabilityKeyCounter periodically SCANs the llmgw:avail:* keyspace
// and refreshes the llmgw_availability_keys_count Prometheus gauge.
//
// It is intentionally independent of the per-write incremental gauge
// update: writers can race, writers can crash before the post-write
// gauge update, the cache can be FLUSHDB'd by an operator, or the
// Redis cluster can fail over to a node with a different cardinality.
// A periodic SCAN reconciles all of those without coupling to the
// hot path.
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

// Start launches the periodic scan loop. Call Stop to terminate.
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

// CountOnce performs a single SCAN-based count and updates the gauge.
// Exposed so tests and the admin /cache-state path can refresh on
// demand. Uses SCAN with COUNT=500 to avoid blocking Redis on a
// large keyspace; the upper bound on the count is the number of
// match keys, so it is at most the cardinality of llmgw:avail:*.
func (k *AvailabilityKeyCounter) CountOnce(ctx context.Context) {
	if k == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	count, err := scanAvailabilityKeys(ctx, k.redis)
	if err != nil {
		slog.Warn("availability key counter: SCAN failed", "error", err)
		return
	}
	recordAvailabilityKeysAbsolute(count)
}

// scanAvailabilityKeys uses SCAN over the llmgw:avail:* pattern with
// COUNT=500 to count matching keys without blocking Redis. Returns
// the total number of matches.
func scanAvailabilityKeys(ctx context.Context, client *redis.Client) (int, error) {
	iter := client.Scan(ctx, 0, "llmgw:avail:*", 500).Iterator()
	count := 0
	for iter.Next(ctx) {
		count++
	}
	if err := iter.Err(); err != nil {
		return 0, err
	}
	return count, nil
}
