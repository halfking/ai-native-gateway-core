// Package admin — probe_stream_redis_store.go
//
// 自检（probe）队列的 Redis 持久层，与 live-stream 的 LiveStreamRedisStore
// 物理隔离（独立 llmgw:probe:* keyspace、独立 notify 通道），避免自检 tile 与
// 业务请求 tile 混在同一泳道。设计参照 live_stream_redis_store.go 的分层 TTL +
// slim tile + dim-index-SET + 限长裁剪技法，但 envelope 为自检领域专用
// （source/status/attempt），不复用 LiveRequest 结构。
//
// 数据模型（每条 ProbeStreamTask）:
//   - 主队列 ZSET    llmgw:probe:main                score=ts_ms  member=taskID
//   - 维度泳道 ZSET  llmgw:probe:dim:source:<src>    score=ts_ms  member=slimTileJSON
//   - 状态 ZSET      llmgw:probe:status:<status>     score=ts_ms  member=taskID
//   - 详情 STRING    llmgw:probe:task:<taskID>       full task JSON
//   - 活跃键 STRING  llmgw:probe:activity:<scope>    = unix秒
//   - 维度索引 SET   llmgw:probe:dim:index           供 reader SMEMBERS（避免 SCAN）
//
// 分层 TTL：详情 4h，维度/状态队列 6h（自检生命周期短于业务请求），主队列 2h。
// 通知：PUBLISH llmgw:probe:events {task_id, source, status}。
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// probeKeyPrefix namespaces all probe-stream keys so they never collide with
// the live-request stream (llmgw:live:*) or system-monitor (llmgw:monitor:*).
const probeKeyPrefix = "llmgw:probe"

const (
	probeMainKey        = probeKeyPrefix + ":main"
	probeNotifyChannel  = probeKeyPrefix + ":events"
	probeDimIndexKey    = probeKeyPrefix + ":dim:index"
	probeDetailTTL      = 4 * time.Hour
	probeQueueTTL       = 6 * time.Hour
	probeMainTTL        = 2 * time.Hour
	probeActivityTTL    = 1 * time.Hour
	probeQueueKeepLimit = 200 // trim each lane to newest N members
)

// ProbeRedisStore persists probe lifecycle events to Redis dimension queues,
// mirroring the live-stream store's pattern but in an isolated keyspace.
type ProbeRedisStore struct {
	rdb *redis.Client
}

// NewProbeRedisStore returns a store backed by rdb. rdb may be nil — Record is
// then a no-op (the hub falls back to in-memory fan-out only).
func NewProbeRedisStore(rdb *redis.Client) *ProbeRedisStore {
	return &ProbeRedisStore{rdb: rdb}
}

// Enabled reports whether Redis persistence is available.
func (s *ProbeRedisStore) Enabled() bool { return s != nil && s.rdb != nil }

// ProbeStreamTileSlim is the compact lane tile (stored as the ZSET member so a
// lane read returns everything needed without an extra detail round-trip).
// Mirrors LiveStreamTileSlim's role in live_stream_redis_store.go.
type ProbeStreamTileSlim struct {
	ID     string `json:"id"`
	Ts     int64  `json:"ts"`     // unix milliseconds
	Status string `json:"st"`     // pending|in-flight|ok|fail
	Source string `json:"src"`    // integrity|node_probe|selfcheck
}

// Record writes a probe task transition to the dimension/status/main queues
// and publishes a notify message. It is idempotent on taskID+status (ZADD
// updates the score/member in place), so re-emitting the same transition is
// safe.
func (s *ProbeRedisStore) Record(ctx context.Context, task ProbeStreamTask) error {
	if !s.Enabled() {
		return nil
	}
	if task.ID == "" {
		return nil
	}
	tsMs := task.TsUnixMilli()
	if tsMs == 0 {
		tsMs = time.Now().UnixMilli()
	}

	pipe := s.rdb.Pipeline()

	// Main queue (member = bare taskID).
	pipe.ZAdd(ctx, probeMainKey, redis.Z{Score: float64(tsMs), Member: task.ID})
	pipe.Expire(ctx, probeMainKey, probeMainTTL)

	// Status queue.
	statusKey := probeStatusKey(task.Status)
	pipe.ZAdd(ctx, statusKey, redis.Z{Score: float64(tsMs), Member: task.ID})
	pipe.Expire(ctx, statusKey, probeQueueTTL)

	// Source dimension lane (member = slim tile JSON).
	if task.Source != "" {
		slim, _ := json.Marshal(ProbeStreamTileSlim{
			ID: task.ID, Ts: tsMs, Status: task.Status, Source: task.Source,
		})
		dimKey := probeDimSourceKey(task.Source)
		pipe.ZAdd(ctx, dimKey, redis.Z{Score: float64(tsMs), Member: string(slim)})
		pipe.Expire(ctx, dimKey, probeQueueTTL)
		pipe.SAdd(ctx, probeDimIndexKey, task.Source)
		pipe.Expire(ctx, probeDimIndexKey, probeQueueTTL)
		trimProbeQueue(ctx, pipe, dimKey)
	}

	// Detail payload.
	full, _ := json.Marshal(task)
	pipe.Set(ctx, probeDetailKey(task.ID), string(full), probeDetailTTL)

	// Notify (hub subscriber reloads + fans out).
	notify := fmt.Sprintf(`{"task_id":%q,"source":%q,"status":%q}`, task.ID, task.Source, task.Status)
	pipe.Publish(ctx, probeNotifyChannel, notify)

	_, err := pipe.Exec(ctx)
	if err != nil && !isRedisCancelErr(err) {
		slog.Warn("probe_stream redis record failed", "error", err, "task_id", task.ID)
	}
	return nil // best-effort: a Redis hiccup must not break the probe worker
}

// trimProbeQueue caps a lane to the newest N members (oldest evicted first).
func trimProbeQueue(ctx context.Context, pipe redis.Pipeliner, key string) {
	pipe.ZRemRangeByRank(ctx, key, 0, -probeQueueKeepLimit-1)
}

func probeStatusKey(status string) string  { return probeKeyPrefix + ":status:" + status }
func probeDimSourceKey(src string) string  { return probeKeyPrefix + ":dim:source:" + src }
func probeDetailKey(id string) string      { return probeKeyPrefix + ":task:" + id }
func probeActivityKey(scope string) string { return probeKeyPrefix + ":activity:" + scope }

// SnapshotFromDimQueues reads the newest tiles across all known source lanes,
// returning them newest-first. This powers the large-form initial_data replay.
func (s *ProbeRedisStore) SnapshotFromDimQueues(ctx context.Context, limit int) ([]ProbeStreamTileSlim, error) {
	if !s.Enabled() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	sources, err := s.rdb.SMembers(ctx, probeDimIndexKey).Result()
	if err != nil && !isRedisCancelErr(err) {
		return nil, err
	}
	merged := make([]ProbeStreamTileSlim, 0, limit*2)
	for _, src := range sources {
		mem, err := s.rdb.ZRevRange(ctx, probeDimSourceKey(src), 0, int64(limit-1)).Result()
		if err != nil && !isRedisCancelErr(err) {
			slog.Warn("probe_stream snapshot lane read failed", "source", src, "error", err)
			continue
		}
		for _, m := range mem {
			var t ProbeStreamTileSlim
			if json.Unmarshal([]byte(m), &t) == nil {
				merged = append(merged, t)
			}
		}
	}
	// Sort newest-first.
	for i := 0; i < len(merged); i++ {
		for j := i + 1; j < len(merged); j++ {
			if merged[j].Ts > merged[i].Ts {
				merged[i], merged[j] = merged[j], merged[i]
			}
		}
	}
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}

// LoadDetail returns the full task payload for a taskID (used by the notify
// subscriber to rebuild a complete event after a cross-instance publish).
func (s *ProbeRedisStore) LoadDetail(ctx context.Context, taskID string) (*ProbeStreamTask, error) {
	if !s.Enabled() || taskID == "" {
		return nil, nil
	}
	raw, err := s.rdb.Get(ctx, probeDetailKey(taskID)).Result()
	if err != nil {
		if isRedisCancelErr(err) || err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var t ProbeStreamTask
	if json.Unmarshal([]byte(raw), &t) != nil {
		return nil, nil
	}
	return &t, nil
}

// isRedisCancelErr returns true for context-cancellation/timeout errors from
// go-redis so callers can treat them as soft skips (the probe worker must not
// fail because the Redis op was cancelled at shutdown).
func isRedisCancelErr(err error) bool {
	if err == nil {
		return false
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	s := err.Error()
	return probeContains(s, "context canceled") || probeContains(s, "context deadline exceeded") || probeContains(s, "connection refused")
}

// probeContains is a local strings.Contains to avoid an import-cycle clash
// with a same-named helper in admin/pending_handlers_test.go.
func probeContains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// tsUnixMilli helper kept here so ProbeStreamTask (defined in the SSE file)
// can stay free of time-import noise if moved; see probe_stream_sse.go for the
// method. strconv import retained for future score parsing.
var _ = strconv.Itoa
