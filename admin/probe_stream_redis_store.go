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

// recordTransitionSrc is the Lua source for recordTransitionScript, kept as a
// package-level constant so tests can assert on its invariants without a live
// Redis (see TestRecordTransitionScript_Loaded).
const recordTransitionSrc = `
local taskID = ARGV[1]
local ts = tonumber(ARGV[2])
local statusKey = ARGV[3]
local source = ARGV[4]
local statusTTL = tonumber(ARGV[5])
local mainKey = ARGV[6]
local mainTTL = tonumber(ARGV[7])
local taskStatusHash = ARGV[8]
local taskSourceHash = ARGV[9]
local dimIndexKey = ARGV[10]
local detailKey = ARGV[11]
local detailTTL = tonumber(ARGV[12])
local keepLimit = tonumber(ARGV[13])

-- Main queue.
redis.call('ZADD', mainKey, ts, taskID)
redis.call('EXPIRE', mainKey, mainTTL)

-- Status lane: evict from previous, add to current.
local prevStatus = redis.call('HGET', taskStatusHash, taskID)
if prevStatus and prevStatus ~= '' and prevStatus ~= statusKey then
  redis.call('ZREM', prevStatus, taskID)
end
redis.call('ZADD', statusKey, ts, taskID)
redis.call('EXPIRE', statusKey, statusTTL)
redis.call('HSET', taskStatusHash, taskID, statusKey)
redis.call('EXPIRE', taskStatusHash, statusTTL)

-- Source dimension lane.
if source ~= '' then
  local dimKey = 'llmgw:probe:dim:source:' .. source
  redis.call('ZADD', dimKey, ts, taskID)
  redis.call('EXPIRE', dimKey, statusTTL)
  redis.call('SADD', dimIndexKey, source)
  redis.call('EXPIRE', dimIndexKey, statusTTL)
  local prevSrc = redis.call('HGET', taskSourceHash, taskID)
  if prevSrc and prevSrc ~= '' and prevSrc ~= source then
    redis.call('ZREM', 'llmgw:probe:dim:source:' .. prevSrc, taskID)
  end
  redis.call('HSET', taskSourceHash, taskID, source)
  redis.call('EXPIRE', taskSourceHash, statusTTL)
  -- Trim to newest keepLimit members (rank 0 = lowest score = oldest).
  redis.call('ZREMRANGEBYRANK', dimKey, 0, -(keepLimit + 1))
end

return 1
`

// recordTransitionScript atomically moves a taskID from its previous status
// lane to the new one, updates the per-task status/source indexes, and trims
// the lane — all in a single Redis EVAL. This closes the read-modify-write
// race that existed when the prev-status HGet ran outside the pipeline: two
// concurrent transitions on the same taskID could both read the old prev,
// both ZREM the wrong key, and overwrite each other's HSet, leaving stale
// members in the abandoned lane.
//
// 2026-08-12 second-pass audit fix.
var recordTransitionScript = redis.NewScript(recordTransitionSrc)

// RecordWithOrigin writes a probe task transition to the dimension/status/main
// queues, publishes a notify message tagged with originInstanceID so the same
// instance's subscriber can drop its own events, and removes the taskID from
// any previous status queue so the lane tile reflects the current stage only.
//
// 2026-08-12 audit fixes:
//   - originInstanceID tag stops Redis pub/sub echo back to the same hub.
//   - cross-status ZREM ensures a task only ever sits in ONE status lane
//     (previously a `pending` tile lingered in the pending queue even after
//     the task moved to in-flight or completed, so initial_data replay showed
//     stale copies of every transition).
//   - second-pass: the prev-status/source read + ZREM + ZADD + HSET now run
//     inside a single Lua script (recordTransitionScript) so concurrent
//     transitions on the same taskID can no longer race on the HGet-then-HSet
//     window.
func (s *ProbeRedisStore) RecordWithOrigin(ctx context.Context, task ProbeStreamTask, originInstanceID string) error {
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
	statusKey := probeStatusKey(task.Status)

	// Atomic lane transition (status/source evict+add+trim in one EVAL).
	_, _ = recordTransitionScript.Run(ctx, s.rdb, []string{},
		task.ID, tsMs, statusKey, task.Source,
		int64(probeQueueTTL.Seconds()), probeMainKey, int64(probeMainTTL.Seconds()),
		probeTaskStatusKey, probeTaskSourceKey, probeDimIndexKey,
		probeDetailKey(task.ID), int64(probeDetailTTL.Seconds()), probeQueueKeepLimit,
	).Result()

	// Detail payload + notify (best-effort, separate from the atomic core so
	// a marshal/Publish failure never blocks the lane transition).
	full, _ := json.Marshal(task)
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, probeDetailKey(task.ID), string(full), probeDetailTTL)
	notify := buildProbeNotify(task.ID, task.Source, task.Status, originInstanceID)
	pipe.Publish(ctx, probeNotifyChannel, notify)
	if _, err := pipe.Exec(ctx); err != nil && !isRedisCancelErr(err) {
		slog.Warn("probe_stream redis record (detail/notify) failed", "error", err, "task_id", task.ID)
	}
	return nil
}

// Lane trimming is inlined into recordTransitionScript (Lua ZREMRANGEBYRANK)
// so it runs atomically with the lane transition.

func probeStatusKey(status string) string  { return probeKeyPrefix + ":status:" + status }
func probeDimSourceKey(src string) string  { return probeKeyPrefix + ":dim:source:" + src }
func probeDetailKey(id string) string      { return probeKeyPrefix + ":task:" + id }
func probeActivityKey(scope string) string { return probeKeyPrefix + ":activity:" + scope }

// buildProbeNotify formats the Redis Pub/Sub payload. Exposed for tests so
// they can pin the instance_id tag without spinning up a real broker.
func buildProbeNotify(taskID, source, status, instanceID string) string {
	return fmt.Sprintf(`{"task_id":%q,"source":%q,"status":%q,"instance_id":%q}`,
		taskID, source, status, instanceID)
}

// Per-task status/source hashes used by RecordWithOrigin to evict stale
// state from the previous lane when the task moves on.
const (
	probeTaskStatusKey = probeKeyPrefix + ":task_status"
	probeTaskSourceKey = probeKeyPrefix + ":task_source"
)

// SnapshotFromDimQueues reads the newest task IDs across all known source lanes
// and emits a slim tile per task (built from the per-task detail hash so the
// status is always current, not a stale snapshot from when the tile was first
// ZADDed). Tasks are returned newest-first. Used by HandleStream to build the
// large-form initial_data envelope on connect.
//
// 2026-08-12 audit fix: previous implementation ZADDed a JSON tile whose
// `status` was the status at write time. A subsequent in-flight or terminal
// transition ZADDed a new JSON tile alongside, so the lane kept both. We now
// store taskID as the member and resolve the status from the detail payload
// here (one read per task, in a single pipeline).
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
	seen := make(map[string]struct{}, limit*2)
	var merged []ProbeStreamTileSlim
	for _, src := range sources {
		mem, err := s.rdb.ZRevRange(ctx, probeDimSourceKey(src), 0, int64(limit-1)).Result()
		if err != nil && !isRedisCancelErr(err) {
			slog.Warn("probe_stream snapshot lane read failed", "source", src, "error", err)
			continue
		}
		for _, taskID := range mem {
			if _, ok := seen[taskID]; ok {
				continue
			}
			seen[taskID] = struct{}{}
			raw, err := s.rdb.Get(ctx, probeDetailKey(taskID)).Result()
			if err != nil || raw == "" {
				continue
			}
			var t ProbeStreamTask
			if json.Unmarshal([]byte(raw), &t) != nil {
				continue
			}
			merged = append(merged, ProbeStreamTileSlim{
				ID:     t.ID,
				Ts:     t.TsUnixMilli(),
				Status: t.Status,
				Source: t.Source,
			})
			if len(merged) >= limit*len(sources) {
				break
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
