package boardcache

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/stats"
	"github.com/redis/go-redis/v9"
)

// Service manages Redis board baseline + delta + fold.
type Service struct {
	rdb    *redis.Client
	build  BaselineBuilder
	cancel context.CancelFunc
	done   chan struct{}
}

func New(rdb *redis.Client) *Service {
	return &Service{rdb: rdb, done: make(chan struct{})}
}

func (s *Service) SetBaselineBuilder(fn BaselineBuilder) {
	s.build = fn
}

// Record increments Redis delta buckets for global and tenant scopes.
func (s *Service) Record(entry *telemetry.RequestLogEntry) {
	if s == nil || s.rdb == nil || entry == nil {
		return
	}
	main, dims, _, ok := stats.FromTelemetryEntry(entry, time.Now().UTC())
	if !ok {
		return
	}
	now := time.Now().UTC()
	bucketID := bucketIDFor(now)
	ttl := deltaTTL()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	scopes := ScopesForEntry(main.TenantID)
	pipe := s.rdb.Pipeline()
	for _, scope := range scopes {
		key := deltaKey(scope, bucketID)
		pipe.HIncrBy(ctx, key, fieldReq, 1)
		if main.SuccessCount > 0 {
			pipe.HIncrBy(ctx, key, fieldSuccess, main.SuccessCount)
		}
		if main.FailureCount > 0 {
			pipe.HIncrBy(ctx, key, fieldFailure, main.FailureCount)
		}
		pipe.HIncrBy(ctx, key, fieldPrompt, main.PromptTokens)
		pipe.HIncrBy(ctx, key, fieldCompletion, main.CompletionTokens)
		pipe.HIncrBy(ctx, key, fieldTotalTok, main.TotalTokens)
		pipe.HIncrBy(ctx, key, fieldCredits, main.CreditsCharged)
		pipe.HIncrBy(ctx, key, fieldLatency, main.LatencyMsSum)
		pipe.HIncrByFloat(ctx, key, fieldCostUSD, main.CostUSD)
		for i := range dims {
			d := dims[i]
			pipe.HIncrBy(ctx, key, dimField(d.DimType, d.DimKey, "requests"), d.Requests)
			pipe.HIncrBy(ctx, key, dimField(d.DimType, d.DimKey, "tokens"), d.TotalTokens)
			pipe.HIncrBy(ctx, key, dimField(d.DimType, d.DimKey, "credits"), d.CreditsCharged)
			pipe.HIncrByFloat(ctx, key, dimField(d.DimType, d.DimKey, "cost_usd"), d.CostUSD)
		}
		pipe.Expire(ctx, key, ttl)
		pipe.SAdd(ctx, dirtyKey(scope), bucketID)
		pipe.Expire(ctx, dirtyKey(scope), ttl)
	}
	if entry.RequestID != "" {
		pipe.ZAdd(ctx, keyQPSWindow, redis.Z{Score: float64(now.Unix()), Member: entry.RequestID})
		cutoff := strconv.FormatInt(now.Add(-5*time.Minute).Unix(), 10)
		pipe.ZRemRangeByScore(ctx, keyQPSWindow, "-inf", cutoff)
		pipe.Expire(ctx, keyQPSWindow, 10*time.Minute)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("boardcache delta increment failed", "error", err)
	}
}

func bucketIDFor(ts time.Time) string {
	if foldUnit() == "minute" {
		return ts.Format("200601021504")
	}
	return strconv.FormatInt(ts.Unix(), 10)
}
