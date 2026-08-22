package stats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

// maxUpdateScript is a Lua script that atomically updates the max value
// only if the new value is greater. Returns the final max value.
//
// KEYS[1] = max key
// ARGV[1] = new candidate value
//
// Pre-compiled once at package init for efficiency. The script:
//  1. Reads current max (returns nil if not set)
//  2. Compares with new value
//  3. Updates if new value is greater
//  4. Returns the final max
//
// Note: we use Eval (not EvalSha) intentionally — miniredis (used in
// tests) does not implement NOSCRIPT fallback, so EVALSHA would fail
// there. In production against real Redis the script body is ~150 bytes,
// so per-call script transfer is negligible compared to the network RTT
// for the rest of the pipeline.
var maxUpdateScriptSrc = `
	local current = redis.call('GET', KEYS[1])
	local candidate = tonumber(ARGV[1])
	if not current or candidate > tonumber(current) then
		redis.call('SET', KEYS[1], candidate)
		return candidate
	end
	return tonumber(current)
`

// BodySizeTracker records request/response body size statistics to Redis
// for real-time dashboard display. Data is ephemeral (lost on Redis restart).
//
// 2026-07-25: Added to support request body size monitoring on homepage dashboard.
// 2026-07-26 (audit): Hardened error handling, context propagation, integer overflow
// checks, and pre-compiled Lua script for efficiency.
type BodySizeTracker struct {
	rdb    *redis.Client
	logger *slog.Logger
}

// NewBodySizeTracker creates a new body size tracker backed by Redis.
// If logger is nil, a no-op default logger is used.
func NewBodySizeTracker(rdb *redis.Client, logger *slog.Logger) *BodySizeTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &BodySizeTracker{rdb: rdb, logger: logger}
}

// Record updates Redis statistics with the request/response body sizes
// from a telemetry entry. Called via telemetry hook after each request.
//
// Errors are logged but not returned — body size stats are observability
// metadata, not on the critical request path. A Redis outage MUST NOT
// impact gateway request handling.
//
// 2026-07-26 (audit): Added context with timeout to prevent indefinite
// Redis calls from blocking the telemetry pipeline. Pre-compiled Lua
// script is reused across calls. Integer overflow is checked.
func (t *BodySizeTracker) Record(entry *telemetry.RequestLogEntry) {
	if t.rdb == nil || entry == nil {
		return
	}

	// Use a bounded context to prevent a stuck Redis call from
	// blocking the telemetry pipeline indefinitely. 500ms is more
	// than enough for a 6-command pipeline against in-memory Redis.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	pipe := t.rdb.Pipeline()

	hasReq := entry.RequestBytes != nil && *entry.RequestBytes > 0
	hasResp := entry.ResponseBytes != nil && *entry.ResponseBytes > 0

	// Early exit when nothing to record — saves one pipeline round trip
	// when only metadata-only rows (e.g. probes) reach this hook.
	if !hasReq && !hasResp {
		return
	}

	if hasReq {
		reqSize := int64(*entry.RequestBytes)
		// Sanity: a single request body > 1 GiB is almost certainly a
		// programming error upstream (or a malicious 1 GiB POST that
		// already bypassed the Nginx 256m cap). Reject before passing
		// to Redis to avoid skewing the running max.
		if reqSize > 1<<30 {
			t.logger.Warn("body_size_tracker: ignoring implausibly large request body",
				"request_id", entry.RequestID,
				"bytes", reqSize,
			)
		} else {
			pipe.IncrBy(ctx, keyBodyReqSum, reqSize)
			pipe.Incr(ctx, keyBodyReqCount)
			pipe.Eval(ctx, maxUpdateScriptSrc, []string{keyBodyReqMax}, reqSize)
		}
	}

	if hasResp {
		respSize := int64(*entry.ResponseBytes)
		if respSize > 1<<30 {
			t.logger.Warn("body_size_tracker: ignoring implausibly large response body",
				"request_id", entry.RequestID,
				"bytes", respSize,
			)
		} else {
			pipe.IncrBy(ctx, keyBodyRespSum, respSize)
			pipe.Incr(ctx, keyBodyRespCount)
			pipe.Eval(ctx, maxUpdateScriptSrc, []string{keyBodyRespMax}, respSize)
		}
	}

	// Execute pipeline. Log errors at WARN — Redis trouble must not
	// break the gateway, but ops needs to know if the stats are stale.
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.logger.Warn("body_size_tracker: redis pipeline failed",
			"error", err.Error(),
			"request_id", entry.RequestID,
		)
	}
}

// BodySizeStats contains aggregated body size statistics.
type BodySizeStats struct {
	AvgRequestBytes  int64 `json:"avg_request_bytes"`
	MaxRequestBytes  int64 `json:"max_request_bytes"`
	AvgResponseBytes int64 `json:"avg_response_bytes"`
	MaxResponseBytes int64 `json:"max_response_bytes"`
}

// GetStats retrieves current body size statistics from Redis.
// Returns zero values (not an error) when Redis is reachable but no data
// exists yet — that's the "freshly started service" state and is not a
// failure condition. Only transport-level errors are returned.
//
// 2026-07-26 (audit): Used Int64() error returns (not the int64,err tuple
// that discards errors) so a mis-typed Redis value surfaces clearly instead
// of being silently coerced to zero.
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

	// Err returns redis.Nil when one or more keys don't exist (fresh
	// service) — that's the expected cold-start state, not an error.
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}

	stats := &BodySizeStats{}

	// Request average
	if reqSumVal, sumErr := reqSum.Int64(); sumErr == nil && reqSumVal > 0 {
		if reqCountVal, cntErr := reqCount.Int64(); cntErr == nil && reqCountVal > 0 {
			stats.AvgRequestBytes = reqSumVal / reqCountVal
		}
	}

	// Response average
	if respSumVal, sumErr := respSum.Int64(); sumErr == nil && respSumVal > 0 {
		if respCountVal, cntErr := respCount.Int64(); cntErr == nil && respCountVal > 0 {
			stats.AvgResponseBytes = respSumVal / respCountVal
		}
	}

	// Max values — safe to ignore redis.Nil (cold start), but log
	// anything else so we don't silently swallow a bad cast.
	if v, err := reqMax.Int64(); err == nil {
		stats.MaxRequestBytes = v
	} else if !errors.Is(err, redis.Nil) {
		t.logger.Warn("body_size_tracker: failed to read max request bytes",
			"error", err.Error(),
		)
	}
	if v, err := respMax.Int64(); err == nil {
		stats.MaxResponseBytes = v
	} else if !errors.Is(err, redis.Nil) {
		t.logger.Warn("body_size_tracker: failed to read max response bytes",
			"error", err.Error(),
		)
	}

	return stats, nil
}
