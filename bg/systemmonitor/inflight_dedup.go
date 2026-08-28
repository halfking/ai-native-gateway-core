// Package bg/systemmonitor — inflight_dedup.go
//
// 节点探测去重（30s inflight + 5min recent_success）。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3.1 / §4.3
package systemmonitor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/redis/go-redis/v9"
)

// ErrRecentSuccess indicates that the auto-task should be skipped because a
// real request succeeded on the same (cred, model) within the last 5 minutes.
//
// The caller (worker loop) MUST convert this into TaskStatusSkipped with
// SkipReasonRecentRequestSuccess.
var ErrRecentSuccess = errors.New("recent request succeeded within skip window")

// InflightDedup handles the two Redis-backed dedup signals used by the
// worker loop:
//
//	(1) 30s inflight token — prevents the same node from being probed twice
//	    within a 30-second sliding window (set by claim.lua, expires naturally).
//	(2) 5min recent_success marker — feeds the auto-skip rule for automatic
//	    tasks (set by middleware/request_log.go on 2xx response, TTL 300s).
//
// Thread safety: all methods are stateless wrappers around redis.Client
// (which is itself thread-safe).
type InflightDedup struct {
	rdb *redis.Client
}

// NewInflightDedup constructs the dedup helper.
func NewInflightDedup(rdb *redis.Client) *InflightDedup {
	return &InflightDedup{rdb: rdb}
}

// Enabled reports whether the dedup helper has a live Redis backend.
func (d *InflightDedup) Enabled() bool {
	return d != nil && d.rdb != nil
}

// MarkRecentSuccess records that a real request succeeded on (cred, model)
// at the given timestamp. Idempotent: SET ... EX 300 NX is used so the
// FIRST successful request in a 5-minute window wins; subsequent ones
// extend nothing (a subsequent request is itself evidence the node is healthy).
//
// ttl may be overridden via env (design §4.3 decision 3: "5min TTL 写死 +
// env 覆盖"). Phase 1 wires a fixed RedisRecentSuccTTL; Phase 2 introduces
// the env var.
func (d *InflightDedup) MarkRecentSuccess(ctx context.Context, credID int64, rawModel, requestID string, httpStatus int, latencyMs int, at time.Time) error {
	if !d.Enabled() {
		return nil // best-effort: skip silently when Redis is down
	}
	key := fmt.Sprintf("llmgw:monitor:node:recent_success:%d:%s", credID, rawModel)
	fields := map[string]any{
		"ts":          at.UTC().Format(time.RFC3339Nano),
		"http_status": httpStatus,
		"request_id":  requestID,
		"latency_ms":  latencyMs,
	}
	pipe := d.rdb.TxPipeline()
	pipe.HSet(ctx, key, fields)
	pipe.Expire(ctx, key, RedisRecentSuccTTL)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("mark recent success: %w", err)
	}
	return nil
}

// ShouldSkipAutoTask reports whether an automatic task should be skipped
// due to a recent real request success.
//
// Returns:
//
//	(nil)              - no skip; task should proceed
//	(*RecentSuccessInfo, nil)  - skip recommended; fields carry context for the audit row
//	(nil, err)         - Redis failure (caller should treat as "do not skip" + log warn)
//
// Per design §4.3, this ONLY applies to automaticity == automatic. Mandatory
// tasks must always run; the caller MUST short-circuit before calling this
// method if automaticity == mandatory.
func (d *InflightDedup) ShouldSkipAutoTask(ctx context.Context, credID int64, rawModel string) (*RecentSuccessInfo, error) {
	if !d.Enabled() {
		return nil, nil
	}
	key := fmt.Sprintf("llmgw:monitor:node:recent_success:%d:%s", credID, rawModel)
	// audit-24h-20260828-r4 P2: Use SafeHGetAll to prevent WRONGTYPE.
	// ErrKeyNotFound collapses into the existing 'no recent success marker'
	// return; TypedError propagates as the original error path.
	data, err := redissafe.SafeHGetAll(ctx, d.rdb, key)
	if err != nil {
		if errors.Is(err, redissafe.ErrKeyNotFound) {
			return nil, nil // no recent success marker
		}
		return nil, fmt.Errorf("hgetall recent_success: %w", err)
	}
	if len(data) == 0 {
		return nil, nil // no recent success marker
	}

	httpStatusStr := data["http_status"]
	if httpStatusStr == "" {
		return nil, nil // marker exists but is malformed; treat as "no skip"
	}
	httpStatus, err := strconv.Atoi(httpStatusStr)
	if err != nil {
		return nil, nil
	}
	// Only 2xx counts as success; 3xx redirects do not (设计 §4.3).
	if httpStatus < 200 || httpStatus >= 300 {
		return nil, nil
	}

	info := &RecentSuccessInfo{
		RequestID:  data["request_id"],
		HTTPStatus: httpStatus,
		LatencyMs:  Int64FromString(data["latency_ms"]),
	}
	if ts, err := time.Parse(time.RFC3339Nano, data["ts"]); err == nil {
		info.At = ts
	}
	return info, nil
}

// RecentSuccessInfo captures the context of a recent successful request —
// used as audit fields when an auto-task is skipped.
type RecentSuccessInfo struct {
	RequestID  string
	HTTPStatus int
	LatencyMs  int64
	At         time.Time
}

// IsWithinWindow reports whether the recent success is within ttl of now.
// Useful for downstream rules that may want to extend the skip window
// beyond the Redis TTL (Phase 2 stretch goal).
func (r *RecentSuccessInfo) IsWithinWindow(now time.Time, ttl time.Duration) bool {
	if r == nil || r.At.IsZero() {
		return false
	}
	return now.Sub(r.At) <= ttl
}

// MarkInflightSkip refreshes the 30s inflight token even when the task is
// being SKIPPED (not actually executed). Without this, an automatic task
// could be skipped, then 5s later a second automatic task on the same
// node would NOT be dedup'd because inflight is empty.
//
// Implementation: SET inflight_key "skipped" EX 30. The token value is
// informational only; the only consumer is claim.lua which only checks
// EXISTS, not the value.
func (d *InflightDedup) MarkInflightSkip(ctx context.Context, credID int64, rawModel string) error {
	if !d.Enabled() {
		return nil
	}
	key := fmt.Sprintf("llmgw:monitor:inflight:%d:%s", credID, rawModel)
	return d.rdb.Set(ctx, key, "skipped", RedisInflightTTL).Err()
}

// Ping is a 1-second health probe used by SystemMonitor to decide whether
// to enter fallback mode (design §3.4).
//
// Returns nil if the Redis backend is reachable within timeout; an error
// otherwise. Does NOT consume the inflight / recent_success keys.
func (d *InflightDedup) Ping(ctx context.Context) error {
	if !d.Enabled() {
		return errors.New("inflight dedup disabled: redis client is nil")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	return d.rdb.Ping(pingCtx).Err()
}
