package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/bootstrap"
	"github.com/redis/go-redis/v9"
)

const (
	// bootstrapAttempts is how many times authoritative startup re-runs the
	// legacy→v2 node bootstrap before it gives up and degrades to ModeOff.
	//
	// 2026-10-04: a single transient failure (PG slower than the 60s budget, a
	// momentarily unreachable Redis) used to degrade the process permanently.
	// The runtime recovery path has always had a rebuild-and-retry; startup
	// had none, so a one-off blip cost a full URSM v2 restart cycle.
	bootstrapAttempts = 2

	bootstrapAttemptTimeout = 60 * time.Second

	// bootstrapRetryBackoff separates the two attempts. Long enough that a
	// genuinely-down dependency is not hammered, short enough to stay well
	// inside a deploy's patience.
	bootstrapRetryBackoff = 2 * time.Second
)

// applyBootstrapWithRetry runs the authoritative coverage bootstrap against the
// caller-supplied client — the same client the v2 manager was built on.
//
// The client is a parameter rather than redisClientForCache on purpose: reads
// (coverage validation) and writes (node hashes + manifest) of one subsystem
// must never be able to land on two different Redis databases. That drift is
// not a hypothetical — it is what made the URSM_V2_REDIS_DB cutover fail on
// 154 while 245 looked healthy.
func applyBootstrapWithRetry(pool *pgxpool.Pool, rdb *redis.Client, cfg ursmv2.Config) (bootstrap.Result, error) {
	return retryBootstrap(bootstrapAttempts, bootstrapRetryBackoff, func() (bootstrap.Result, error) {
		ctx, cancel := context.WithTimeout(context.Background(), bootstrapAttemptTimeout)
		defer cancel()
		return bootstrap.Apply(ctx, bootstrap.Options{
			Pool:        pool,
			Redis:       rdb,
			KeyPrefix:   cfg.RedisKeyPrefix,
			CoolSeconds: cfg.CoolSeconds,
			SchemaMode:  cfg.KeySchemaMode,
		})
	})
}

// retryBootstrap is the seam the startup contract is tested through: it counts
// attempts so a test can prove a transient failure is retried rather than
// degrading the process on the first error.
func retryBootstrap(attempts int, backoff time.Duration, apply func() (bootstrap.Result, error)) (bootstrap.Result, error) {
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			time.Sleep(backoff)
			slog.Warn("ursm.v2: retrying authoritative bootstrap", "attempt", attempt, "of", attempts)
		}
		result, err := apply()
		if err == nil {
			return result, nil
		}
		lastErr = err
		slog.Warn("ursm.v2: authoritative bootstrap attempt failed",
			"attempt", attempt, "of", attempts, "error", err)
	}
	return bootstrap.Result{}, fmt.Errorf("bootstrap failed after %d attempts: %w", attempts, lastErr)
}
