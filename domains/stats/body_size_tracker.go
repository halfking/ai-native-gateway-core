package stats

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// Redis keys for body size statistics (ephemeral, survives until Redis restart).
const (
	keyBodyReqSum    = "llmgw:stats:body:request:sum"
	keyBodyReqCount  = "llmgw:stats:body:request:count"
	keyBodyReqMax    = "llmgw:stats:body:request:max"
	keyBodyRespSum   = "llmgw:stats:body:response:sum"
	keyBodyRespCount = "llmgw:stats:body:response:count"
	keyBodyRespMax   = "llmgw:stats:body:response:max"
)

// BodySizeTracker records request/response body size statistics to Redis
// for real-time dashboard display. Data is ephemeral (lost on Redis restart).
//
// 2026-07-25: Added to support request body size monitoring on homepage dashboard.
type BodySizeTracker struct {
	rdb *redis.Client
}

// NewBodySizeTracker creates a new body size tracker backed by Redis.
func NewBodySizeTracker(rdb *redis.Client) *BodySizeTracker {
	return &BodySizeTracker{rdb: rdb}
}

// Record updates Redis statistics with the request/response body sizes
// from a telemetry entry. Called via telemetry hook after each request.
func (t *BodySizeTracker) Record(entry *telemetry.RequestLogEntry) {
	if t.rdb == nil || entry == nil {
		return
	}

	ctx := context.Background()
	pipe := t.rdb.Pipeline()

	// Record request body size
	if entry.RequestBytes != nil && *entry.RequestBytes > 0 {
		reqSize := int64(*entry.RequestBytes)
		pipe.IncrBy(ctx, keyBodyReqSum, reqSize)
		pipe.Incr(ctx, keyBodyReqCount)

		// Update max using Lua script for atomic compare-and-set
		pipe.Eval(ctx, `
			local max = redis.call('GET', KEYS[1])
			if not max or tonumber(ARGV[1]) > tonumber(max) then
				redis.call('SET', KEYS[1], ARGV[1])
			end
		`, []string{keyBodyReqMax}, reqSize)
	}

	// Record response body size
	if entry.ResponseBytes != nil && *entry.ResponseBytes > 0 {
		respSize := int64(*entry.ResponseBytes)
		pipe.IncrBy(ctx, keyBodyRespSum, respSize)
		pipe.Incr(ctx, keyBodyRespCount)

		pipe.Eval(ctx, `
			local max = redis.call('GET', KEYS[1])
			if not max or tonumber(ARGV[1]) > tonumber(max) then
				redis.call('SET', KEYS[1], ARGV[1])
			end
		`, []string{keyBodyRespMax}, respSize)
	}

	// Execute pipeline (fire-and-forget, errors are non-critical)
	_, _ = pipe.Exec(ctx)
}

// BodySizeStats contains aggregated body size statistics.
type BodySizeStats struct {
	AvgRequestBytes  int64 `json:"avg_request_bytes"`
	MaxRequestBytes  int64 `json:"max_request_bytes"`
	AvgResponseBytes int64 `json:"avg_response_bytes"`
	MaxResponseBytes int64 `json:"max_response_bytes"`
}

// GetStats retrieves current body size statistics from Redis.
// Returns zero values if Redis is unavailable or no data exists yet.
func (t *BodySizeTracker) GetStats(ctx context.Context) (*BodySizeStats, error) {
	if t.rdb == nil {
		return nil, fmt.Errorf("redis client not available")
	}

	pipe := t.rdb.Pipeline()
	reqSum := pipe.Get(ctx, keyBodyReqSum)
	reqCount := pipe.Get(ctx, keyBodyReqCount)
	reqMax := pipe.Get(ctx, keyBodyReqMax)
	respSum := pipe.Get(ctx, keyBodyRespSum)
	respCount := pipe.Get(ctx, keyBodyRespCount)
	respMax := pipe.Get(ctx, keyBodyRespMax)

	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return nil, err
	}

	stats := &BodySizeStats{}

	// Calculate averages
	if reqSumVal, _ := reqSum.Int64(); reqSumVal > 0 {
		if reqCountVal, _ := reqCount.Int64(); reqCountVal > 0 {
			stats.AvgRequestBytes = reqSumVal / reqCountVal
		}
	}
	if respSumVal, _ := respSum.Int64(); respSumVal > 0 {
		if respCountVal, _ := respCount.Int64(); respCountVal > 0 {
			stats.AvgResponseBytes = respSumVal / respCountVal
		}
	}

	stats.MaxRequestBytes, _ = reqMax.Int64()
	stats.MaxResponseBytes, _ = respMax.Int64()

	return stats, nil
}
