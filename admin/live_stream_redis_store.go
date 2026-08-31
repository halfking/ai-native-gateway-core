package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// LiveStreamRedisStore backs the realtime request stream with Redis,
// keeping recent requests in sorted sets so clients can replay on
// reconnect/refresh without hitting the DB.
//
// Design:
//   - Main queue: ZSET llmgw:live:main (score = unix_ms, member = bare request_id)
//   - Dimension queues: ZSET llmgw:live:dim:{vendor|provider|model}:{key}
//     (member = slim tile JSON: rid/ts/st/ek/p — see LiveStreamTileSlim)
//   - Status queues: ZSET llmgw:live:status:{success|failure|rate_limited|in_progress}
//   - Request detail: STRING llmgw:live:req:{id} (+ tenant-scoped variant),
//     holds the full LiveRequest payload for detail/tooltip rendering.
//   - TTL: layered — detail 4h (LiveStreamRecordRetention), dimension/status
//     queues 24h (LiveStreamLaneQueueRetention), activity keys 1h, tenant set 2h.
//   - Visible lane window: LiveStreamLaneVisibleLimit (20 tiles)
//   - Idle markers: inserted after LiveStreamIdleThreshold (5 min) of silence
//   - Concurrency: Record() holds a per-request_id SETNX lock
//     (llmgw:live:lock:record:{id}) for the read-modify-write so that the
//     in_progress + terminal updates for one request cannot interleave and
//     leave duplicate ZSET members.
//
// Graceful degradation: all write errors are logged but not surfaced;
// the hub falls back to DB replay when Redis is unavailable.
type LiveStreamRedisStore struct {
	rdb *redis.Client
}

type LiveStreamStats struct {
	Total       int `json:"total"`
	Success     int `json:"success"`
	Failure     int `json:"failure"`
	RateLimited int `json:"rate_limited"`
	InProgress  int `json:"in_progress"`
}

type LiveStreamTile struct {
	RequestID string `json:"request_id"`
	Timestamp string `json:"timestamp"`
	Model     string `json:"model"`
	Vendor    string `json:"vendor"`
	Provider  string `json:"provider"`
	Status    string `json:"status"`
	// 2026-08-26: 凭据维度泳道（替换原厂维度）。credential_id 是稳定身份，
	// credential_label 是显示名（凭据标签，无标签时为空串，回退
	// "凭据 #ID" 由 liveStreamCredentialKey / 前端负责）。
	CredentialID     int      `json:"credential_id,omitempty"`
	CredentialLabel  string   `json:"credential_label,omitempty"`
	ErrorKind        *string  `json:"error_kind,omitempty"`
	LatencyMs        *int     `json:"latency_ms,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
	PromptTokens     *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens *int     `json:"completion_tokens,omitempty"`
	IsProbe          bool     `json:"is_probe,omitempty"`
	ProbeOrigin      string   `json:"probe_origin,omitempty"`
	ProbeAttempt     int      `json:"probe_attempt,omitempty"`
	// StageCategory is the coarse UI phase for in-flight tiles:
	// routing = received / routing; llm = already sent upstream, waiting.
	// Empty for terminal / idle tiles.
	StageCategory string `json:"stage_category,omitempty"`
}

// LiveStreamTileSlim is a lightweight version of LiveStreamTile for Redis queue storage.
// Full details remain in the request detail hash; this minimal format reduces memory by ~90%.
type LiveStreamTileSlim struct {
	RequestID string  `json:"rid"`
	Timestamp int64   `json:"ts"`           // Unix milliseconds
	Status    string  `json:"st"`           // success, failure, in_progress, idle
	ErrorKind *string `json:"ek,omitempty"` // 5xx, 4xx, timeout, etc.
	IsProbe   bool    `json:"p,omitempty"`  // true if this is a probe request
}

// marshalTileSlim converts a LiveStreamTile to slim JSON format.
func marshalTileSlim(tile LiveStreamTile) (string, error) {
	ts, err := time.Parse(time.RFC3339, tile.Timestamp)
	if err != nil {
		return "", fmt.Errorf("invalid timestamp: %w", err)
	}

	slim := LiveStreamTileSlim{
		RequestID: tile.RequestID,
		Timestamp: ts.UnixMilli(),
		Status:    tile.Status,
		ErrorKind: tile.ErrorKind,
		IsProbe:   tile.IsProbe,
	}

	data, err := json.Marshal(slim)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// unmarshalTileSlim converts slim JSON back to LiveStreamTile.
func unmarshalTileSlim(data string) (LiveStreamTile, error) {
	var slim LiveStreamTileSlim
	if err := json.Unmarshal([]byte(data), &slim); err != nil {
		return LiveStreamTile{}, err
	}

	ts := time.UnixMilli(slim.Timestamp).UTC()

	return LiveStreamTile{
		RequestID: slim.RequestID,
		Timestamp: ts.Format(time.RFC3339),
		Status:    slim.Status,
		ErrorKind: slim.ErrorKind,
		IsProbe:   slim.IsProbe,
		// Other fields loaded from detail hash when needed
	}, nil
}

type LiveStreamLane struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Dimension string           `json:"dimension"`
	Requests  []LiveStreamTile `json:"requests"`
	Stats     LiveStreamStats  `json:"stats"`
	IsOthers  bool             `json:"isOthers"`
}

type LiveStreamLegendItem struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type LiveStreamSnapshot struct {
	Summary          LiveStreamStats                   `json:"summary"`
	Dimensions       map[string][]LiveStreamLane       `json:"dimensions"`
	DimensionLegends map[string][]LiveStreamLegendItem `json:"dimension_legends"`
	StatusLegends    []LiveStreamLegendItem            `json:"status_legends"`
	LatestRequestTs  string                            `json:"latest_request_ts,omitempty"`
}

type LiveStreamDelta struct {
	Summary          LiveStreamStats                   `json:"summary"`
	ChangedLanes     map[string][]LiveStreamLane       `json:"changed_lanes"`
	DimensionLegends map[string][]LiveStreamLegendItem `json:"dimension_legends"`
	StatusLegends    []LiveStreamLegendItem            `json:"status_legends"`
}

type liveRequestRedisPayload struct {
	Type             string   `json:"type,omitempty"`
	RequestID        string   `json:"request_id"`
	Ts               string   `json:"ts"`
	TenantID         string   `json:"tenant_id,omitempty"`
	GwSessionID      string   `json:"gw_session_id,omitempty"`
	Model            string   `json:"model,omitempty"`
	CanonicalName    string   `json:"canonical_name,omitempty"`
	ModelCategory    string   `json:"model_category,omitempty"`
	ProviderCode     string   `json:"provider_code,omitempty"`
	Status           string   `json:"status,omitempty"`
	LatencyMs        *int     `json:"latency_ms,omitempty"`
	PromptTokens     *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens *int     `json:"completion_tokens,omitempty"`
	TotalTokens      *int     `json:"total_tokens,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
	ErrorKind        *string  `json:"error_kind,omitempty"`
	FailureStage     *string  `json:"failure_stage,omitempty"`
	// 2026-07-13: 主动探测标记
	IsProbe        bool   `json:"is_probe,omitempty"`
	ProbeOrigin    string `json:"probe_origin,omitempty"`
	ProbeAttempt   int    `json:"probe_attempt,omitempty"`
	ClientProfile  string `json:"client_profile,omitempty"`
	IdentityHash   string `json:"identity_hash,omitempty"`
	CreditsCharged *int   `json:"credits_charged,omitempty"`
	// OBS-BE2 (V3.3-OBS, 2026-08-15): 主从请求关联随 Redis 持久化，remote hub
	// 经 pub/sub 重建请求时不丢 child_request 所需的 parent/type 元数据。
	ParentRequestID string `json:"parent_request_id,omitempty"`
	RequestType     string `json:"request_type,omitempty"`
	// 2026-08-26: 凭据维度泳道 —— 凭据身份必须随 Redis payload 持久化，
	// 否则 replay/snapshot 重建后凭据泳道退化为 "凭据 #ID" 或整条消失。
	CredentialID    int    `json:"credential_id,omitempty"`
	CredentialLabel string `json:"credential_label,omitempty"`
}

// 2026-07-23: 精细化分层 TTL
//
// LiveStream 缓存有三类 key，每类业务生命周期不同：
//   - Request detail (live:req:*): 单次请求生命周期，正常 4h 内完成
//   - Lane dim queue (live:dim:*): 泳道队列，泳道存在不超过 1 天
//   - Activity key (live:activity:*): 泳道活跃时间戳，无变化 1h 应清除
//   - Main queue (live:main): 跨租户主队列
//   - Tenant set (live:tenants): 已知租户集合
const (
	// LiveStreamRecordRetention: 请求详情 hash 的 TTL。一般请求从开始到完成不超过 4h。
	LiveStreamRecordRetention = 4 * time.Hour

	// LiveStreamLaneQueueRetention: 泳道维度队列 (vendor/provider/model) 的 TTL。
	// 泳道最多保留 24 小时，超过即视为过期泳道，让 snapshot 自动跳过。
	LiveStreamLaneQueueRetention = 24 * time.Hour

	// LiveStreamActivityRetention: 泳道活跃时间戳的 TTL。
	// 当泳道 1h 内没有任何新请求，应该被从缓存移除（sortKeysByActivity 会跳过）。
	LiveStreamActivityRetention = 1 * time.Hour

	// LiveStreamMainQueueRetention: 主队列和租户集合的 TTL。
	LiveStreamMainQueueRetention = 2 * time.Hour
)

// LiveStreamLaneVisibleLimit is how many tiles each swim lane keeps in its
// per-dimension Redis queue. 2026-08-26: 20 → 100。显示层（前端
// SwimLane.maxVisibleTiles）已按泳道轨道宽度动态裁剪显示数量（小模式 9px
// 竖条 / 大模式 80px 卡片），此常量只作为"数据供给窗口"上限 —— 必须 ≥
// 最宽屏在 small 模式下的可显示数，否则显示层"取不到足够 tile"。前端合并
// 侧有同口径常量 LANE_TILE_CAP（web/src/composables/liveStreamStore.ts）。
const LiveStreamLaneVisibleLimit = 100

// LiveStreamIdleThreshold is how long a lane must be silent before an idle
// marker is written into the stream.
const LiveStreamIdleThreshold = 5 * time.Minute

const idleMarkerErrorKind = "no_traffic_5min"
const idleMarkerFailureStage = "idle"

// LiveStreamLaneRetention is the default for in-memory cached snapshot
// eviction in the SSE hub (not Redis record TTL).
const LiveStreamLaneRetention = 4 * time.Hour

const (
	liveStreamMainKey        = "llmgw:live:main"
	liveStreamNotifyChannel  = "llmgw:live:events"
	liveStreamDimPrefix      = "llmgw:live:dim:"
	liveStreamStatPrefix     = "llmgw:live:status:"
	liveStreamTenantSet      = "llmgw:live:tenants"
	liveStreamActivityPrefix = "llmgw:live:activity:"
	// 2026-08-04 (方案C): dimension-queue index SETs. discoverDimensionQueues
	// previously SCANned the whole keyspace (300k+ keys) on every snapshot read,
	// and those `scan count 10000` calls dominated the Redis slowlog and slowed
	// the shared instance. Each Record()/idle-marker write now SADDs its dim
	// queue keys into one of these SETs so the reader can SMEMBERS instead of
	// SCAN. One SET per scope: super reads the global set, tenant reads its own.
	liveStreamDimIndexPrefix = "llmgw:live:dim:index:"
	// 2026-07-23: liveStreamTTL 仍指向 request detail 保持向后兼容
	liveStreamTTL = LiveStreamRecordRetention
	// 新增：分层 TTL 常量
	liveStreamLaneQueueTTL = LiveStreamLaneQueueRetention // 24h 泳道队列
	liveStreamActivityTTL  = LiveStreamActivityRetention  // 1h 活跃度
	liveStreamMainQueueTTL = LiveStreamMainQueueRetention // 2h 主队列
	liveStreamLaneLimit    = LiveStreamLaneVisibleLimit
	liveStreamReplayLimit  = 200 // main-queue replay cap; per-lane display capped at liveStreamLaneLimit
)

// normalizeModelKey returns a case-insensitive, whitespace-trimmed
// canonical key for use in Redis dimension queues. Without this, the
// upstream pipeline may emit the same logical model with mixed
// casing (e.g. "MiniMax-M3" vs "minimax-m3") and end up split into
// separate Redis queues, which in turn shows up as duplicate lanes
// on the live request swim lane.
//
// The conversion is intentionally conservative:
//   - trim leading/trailing whitespace
//   - collapse internal whitespace runs to a single space
//   - fold all characters to lower case (ASCII + Unicode via ToLower)
//   - preserve non-alphanumeric characters so slashes / dashes / dots
//     in model IDs are still distinguished ("gpt-4o" vs "gpt/4o")
//
// The original case is preserved in the rendered lane label; only
// the Redis key is lower-cased so cross-case requests aggregate.
func normalizeModelKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Collapse internal whitespace runs to a single space (defensive;
	// upstream callers already strip, but a stray \n would otherwise
	// create a different queue key).
	s = collapseWhitespace(s)
	return strings.ToLower(s)
}

// collapseWhitespace replaces runs of Unicode whitespace with a
// single ASCII space. Kept private to this file because it is only
// meaningful in the context of Redis dimension key normalisation.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

// NewLiveStreamRedisStore constructs a store. Safe to pass nil client
// (the store becomes a no-op).
func NewLiveStreamRedisStore(rdb *redis.Client) *LiveStreamRedisStore {
	return &LiveStreamRedisStore{rdb: rdb}
}

// Record persists one LiveRequest to Redis queues. Returns nil on
// success or when Redis is unavailable (graceful degradation).
// Record persists a request to Redis and publishes a notify. instanceID, when
// non-empty, is stamped onto the notify so the originating hub can skip its own
// echo (the local fan-out in Publish already delivered the request). Callers
// that are not a live-stream hub pass "".
func (s *LiveStreamRedisStore) Record(ctx context.Context, req LiveRequest, instanceID string) error {
	if s == nil || s.rdb == nil {
		slog.Warn("live stream record: store or Redis client is nil, skipping write",
			"store_nil", s == nil, "rdb_nil", s != nil && s.rdb == nil,
			"request_id", req.RequestID)
		// 2026-08-31 (P2-2 observability): surface silent drops to Prometheus
		// so operators can distinguish "Redis is down" from "Redis is fine
		// and nothing is happening" on the live stream hub.
		metrics.Global().RecordLiveStreamRecordDropped("store_unconfigured")
		return nil
	}
	if req.RequestID == "" {
		req.RequestID = fmt.Sprintf("request-%d", time.Now().UnixNano())
	}
	tenantID := normalizeLiveStreamTenant(req.TenantID)
	req.TenantID = tenantID

	// Enhanced logging for debugging missing dimension values
	if req.ModelCategory == "" {
		slog.Debug("live stream record: missing model_category", "request_id", req.RequestID, "model", req.Model, "tenant_id", tenantID)
	}
	if req.ProviderCode == "" {
		slog.Debug("live stream record: missing provider_code", "request_id", req.RequestID, "model", req.Model, "tenant_id", tenantID)
	}
	if req.Model == "" {
		slog.Debug("live stream record: missing model", "request_id", req.RequestID, "tenant_id", tenantID)
	}

	// 2026-07-27: Per-request_id Redis lock. Record() is a read-modify-write
	// (GET old payload → compute ZREM set → ZADD new slim tile). Without
	// serialization, two concurrent Records for the SAME request_id — e.g. an
	// in_progress insert and the terminal update, which the telemetry batcher
	// emits near-simultaneously — both read the SAME old payload, each
	// compute the same ZREM set, and each ZADD their own new slim tile. The
	// slim tile carries status + error_kind, which differ between in_progress
	// and terminal, so the two ZADDs insert two DISTINCT JSON members into the
	// same dimension ZSET → the request appears twice on the swim lane (the
	// "jump"/"duplicate" operators reported).
	//
	// The lock serializes the read-modify-write per request_id, making it
	// atomic across instances. The existing pipeline/TTL/idle/vendor logic is
	// left untouched. If the lock cannot be acquired (Redis down OR contention
	// exhausted), we return nil WITHOUT writing: the request is dropped from
	// the live stream rather than written in a raced state. Dropping one tile
	// is far less visible than a persistent duplicate; the canonical record
	// still lives in request_logs and arrives via the next snapshot refresh.
	release, locked := s.acquireLiveStreamRecordLock(ctx, req.RequestID)
	if !locked {
		slog.Warn("live stream record: lock not acquired, dropping request to preserve dedup",
			"request_id", req.RequestID, "tenant_id", tenantID)
		// 2026-08-31 (P2-2 observability): the per-request_id SETNX lock
		// failed (Redis down OR contention exhausted). Without this counter,
		// the operator cannot tell that tiles are being lost while the
		// canonical record still lands in request_logs.
		metrics.Global().RecordLiveStreamRecordDropped("redis_unavailable")
		return nil
	}
	defer release()

	return s.recordLocked(ctx, req, tenantID, instanceID)
}

// recordLocked is the body of Record(), executed while holding the
// per-request_id lock when locking succeeded. It performs the read-modify-write
// against Redis.
func (s *LiveStreamRedisStore) recordLocked(ctx context.Context, req LiveRequest, tenantID, instanceID string) error {
	var oldData string
	var oldReq LiveRequest
	hasOldReq := false
	if v, err := s.rdb.Get(ctx, liveStreamRequestDetailKey(tenantID, req.RequestID)).Result(); err == nil {
		oldData = v
	} else if err != redis.Nil {
		return fmt.Errorf("failed to fetch old request data: request_id=%s tenant_id=%s: %w", req.RequestID, tenantID, err)
	}

	ts, err := time.Parse(time.RFC3339, req.Ts)
	if err != nil {
		slog.Warn("live stream record: invalid timestamp, using now", "request_id", req.RequestID, "ts", req.Ts, "err", err.Error())
		ts = time.Now().UTC()
	}
	if oldData != "" {
		if oldReq, err = unmarshalLiveRequestRedisPayload(oldData); err == nil {
			hasOldReq = true
			if oldTs, oldTsErr := time.Parse(time.RFC3339, oldReq.Ts); oldTsErr == nil && oldTs.After(ts) {
				// Keep request timestamps monotonic so a delayed/stale update cannot
				// push the same request backwards in the visible lane window.
				ts = oldTs
				req.Ts = oldReq.Ts
			}
		} else {
			slog.Warn("live stream record: failed to unmarshal old request", "request_id", req.RequestID, "err", err.Error())
		}
	}
	data, err := marshalLiveRequestRedisPayload(req)
	if err != nil {
		return fmt.Errorf("marshal live request failed: request_id=%s tenant_id=%s model=%s provider=%s category=%s: %w", req.RequestID, tenantID, req.Model, req.ProviderCode, req.ModelCategory, err)
	}
	score := float64(ts.UnixMilli())

	pipe := s.rdb.Pipeline()
	trimKeys := make([]string, 0, 16)
	if hasOldReq {
		removeLiveRequestFromQueues(ctx, pipe, normalizeLiveStreamTenant(oldReq.TenantID), oldReq)
	}

	// 2026-07-23: 精细化分层 TTL
	// - tenant set: 2h (liveStreamMainQueueTTL)
	// - activity key: 1h (liveStreamActivityTTL) — "1h 无变化应清除"
	// - dim queue: 24h (liveStreamLaneQueueTTL) — "泳道存在不超过 1 天"
	// - request detail: 4h (liveStreamTTL)
	pipe.SAdd(ctx, liveStreamTenantSet, tenantID)
	pipe.Expire(ctx, liveStreamTenantSet, liveStreamMainQueueTTL)

	// Track last activity time for each dimension queue. Use the request
	// timestamp (not current time) so replayed historical data updates
	// activity correctly.
	activityUnix := ts.Unix()
	if req.ModelCategory != "" {
		pipe.Set(ctx, liveStreamActivityKey("", "vendor", req.ModelCategory), activityUnix, liveStreamActivityTTL)
		pipe.Set(ctx, liveStreamActivityKey(tenantID, "vendor", req.ModelCategory), activityUnix, liveStreamActivityTTL)
	}
	if credKey := liveStreamCredentialKey(req); credKey != "" {
		pipe.Set(ctx, liveStreamActivityKey("", "credential", credKey), activityUnix, liveStreamActivityTTL)
		pipe.Set(ctx, liveStreamActivityKey(tenantID, "credential", credKey), activityUnix, liveStreamActivityTTL)
	}
	if req.ProviderCode != "" {
		pipe.Set(ctx, liveStreamActivityKey("", "provider", req.ProviderCode), activityUnix, liveStreamActivityTTL)
		pipe.Set(ctx, liveStreamActivityKey(tenantID, "provider", req.ProviderCode), activityUnix, liveStreamActivityTTL)
	}
	// Use CanonicalName for model dimension activity keys when available.
	// This ensures idle markers generated from these keys use the standard
	// Normalise the model name so the activity key and the idle-marker
	// key are guaranteed to use the same form as the queue / lane key
	// in liveStreamDimensionKey. Without this, the same model under
	// mixed casing (e.g. "MiniMax-M3" vs "minimax-m3") would create
	// two separate activity keys, so the idle scanner would never
	// recognise them as the same lane and would emit one idle marker
	// per casing variant.
	modelActivityKey := normalizeModelKey(emptyAs(req.CanonicalName, req.Model))
	if modelActivityKey != "" {
		pipe.Set(ctx, liveStreamActivityKey("", "model", modelActivityKey), activityUnix, liveStreamActivityTTL)
		pipe.Set(ctx, liveStreamActivityKey(tenantID, "model", modelActivityKey), activityUnix, liveStreamActivityTTL)
	}
	// Track main queue activity
	pipe.Set(ctx, liveStreamActivityKey("", "main", ""), activityUnix, liveStreamActivityTTL)
	pipe.Set(ctx, liveStreamActivityKey(tenantID, "main", ""), activityUnix, liveStreamActivityTTL)

	queueKeys := liveRequestQueueKeys(tenantID, req)
	slog.Debug("live stream record: adding to queues", "request_id", req.RequestID, "tenant_id", tenantID, "model", req.Model, "provider", req.ProviderCode, "category", req.ModelCategory, "queue_count", len(queueKeys))

	// 2026-07-26: Slim tile storage - store lightweight JSON in dimension queues
	// to reduce Redis memory by 72.5% (244B → 67B per tile).
	// The main queue still stores request_id only (backward compat);
	// dimension queues store slim tiles for display.
	slimData, slimErr := marshalTileSlim(liveRequestTile(req))
	if slimErr != nil {
		slog.Debug("live stream: slim tile marshal failed, falling back to request_id", "request_id", req.RequestID, "err", slimErr.Error())
	}

	for _, key := range queueKeys {
		// Main queues (global/tenant) keep request_id for backward compatibility
		// with existing Replay logic that loads from detail hash
		if strings.HasSuffix(key, ":main") || key == liveStreamMainKey {
			pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: req.RequestID})
		} else {
			// Dimension queues use slim tile format to save memory
			if slimData != "" {
				pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: slimData})
			} else {
				// Fallback to request_id if slim marshal fails
				pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: req.RequestID})
			}
			// 2026-08-04 (方案C): register this dim queue key in the scope's
			// index SET so discoverDimensionQueues can SMEMBERS instead of
			// SCANning the whole keyspace. Global dim keys go to the global
			// index; tenant dim keys go to the tenant index. Idempotent (SADD
			// on an existing member is a no-op) and TTL-refreshed per write so
			// the index outlives any single lane queue. See isDimensionQueueKey.
			if isDimensionQueueKey(key) {
				indexKey := liveStreamDimIndexKey(tenantID, isGlobalDimKey(key))
				pipe.SAdd(ctx, indexKey, key)
				pipe.Expire(ctx, indexKey, liveStreamLaneQueueTTL)
			}
		}
		// 2026-07-23: 维度队列 24h TTL（泳道存在不超过 1 天）
		pipe.Expire(ctx, key, liveStreamLaneQueueTTL)
		trimKeys = append(trimKeys, key)
	}
	// 2026-07-23: 请求详情 4h TTL（一般请求不会跨 4 小时）
	pipe.Set(ctx, liveStreamRequestDetailKey(tenantID, req.RequestID), data, liveStreamTTL)
	pipe.Set(ctx, liveStreamGlobalRequestDetailKey(req.RequestID), data, liveStreamTTL)

	// Remove stale idle markers for the same lane — when a lane that
	// previously had an idle marker becomes active again, its idle
	// marker must not persist in the queue (otherwise the next delta
	// would still include the idle tile alongside real requests).
	modelKey := normalizeModelKey(emptyAs(req.CanonicalName, req.Model))
	addIdleRemoval := func(dim, val string) {
		if val == "" {
			return
		}
		pipe.ZRem(ctx, liveStreamMainKey, idleMarkerRequestID("", dim, val))
		if tenantID != "" {
			pipe.ZRem(ctx, tenantLiveStreamKey(tenantID, "main"), idleMarkerRequestID(tenantID, dim, val))
		}
	}
	if req.ModelCategory != "" {
		addIdleRemoval("vendor", req.ModelCategory)
	}
	addIdleRemoval("credential", liveStreamCredentialKey(req))
	addIdleRemoval("provider", req.ProviderCode)
	addIdleRemoval("model", modelKey)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline exec failed: request_id=%s tenant_id=%s model=%s provider=%s category=%s queue_count=%d: %w", req.RequestID, tenantID, req.Model, req.ProviderCode, req.ModelCategory, len(queueKeys), err)
	}
	for _, key := range trimKeys {
		if trimErr := selectiveTrimLiveStreamQueue(ctx, s.rdb, key, liveStreamQueueKeepLimit(key)); trimErr != nil {
			slog.Debug("live stream selective trim failed", "key", key, "err", trimErr.Error())
		}
	}
	if err := s.NotifyChange(ctx, tenantID, req.RequestID, instanceID); err != nil {
		slog.Debug("live stream redis notify failed", "request_id", req.RequestID, "tenant_id", tenantID, "err", err.Error())
	}
	return nil
}

// liveStreamRecordLockTTL bounds how long a per-request_id lock may be held.
// A Record() pipeline completes in low tens of milliseconds under normal load;
// 5s is ample headroom and also bounds staleness if the holder crashes mid-write.
const liveStreamRecordLockTTL = 5 * time.Second

// liveStreamRecordLockRetry bounds contention handling. Same-request_id
// contention is rare in production (only the in_progress + terminal pair from
// the batcher), so a bounded backoff is cheap. We DO NOT proceed unlocked on
// exhaustion — that would reintroduce the duplicate-member race this lock
// exists to prevent. Only ctx cancellation aborts the wait.
const (
	// 2026-08-26: 64 → 200（上限 ~8s）。实测 onair 场景下同一个 request 的
	// in_progress 与 terminal update 相邻到达，锁竞争耗尽导致整条 terminal
	// 更新被静默丢弃（tile 永久停在 in_progress —— 用户反馈 "请求已完成但
	// 显示进行中" 的根因之一）。把重试窗口拉长到远大于 recordLocked 的典型
	// pipeline 耗时（数十 ms），耗尽只应发生在锁持有者崩溃/僵死的极端场景。
	liveStreamRecordLockRetryAttempts = 200
	liveStreamRecordLockRetrySleep    = 10 * time.Millisecond
)

// liveStreamRecordLockKey is the Redis key for the per-request_id lock. It is
// global (not tenant-scoped) because request_id is globally unique, so the
// same request racing itself across instances must contend on one lock.
func liveStreamRecordLockKey(requestID string) string {
	return "llmgw:live:lock:record:" + requestID
}

// acquireLiveStreamRecordLock takes a short-lived SETNX lock keyed by
// request_id, retrying with bounded backoff until acquired or ctx is cancelled.
// Returns a release func (call always, even on error) and a flag indicating
// whether the lock was acquired. The caller must NOT proceed with the write
// unless locked is true — otherwise the read-modify-write race that produces
// duplicate ZSET members would resurface. Redis errors (unavailable) are the
// only case that returns (nil, false) without the lock; Record treats those as
// a graceful no-op since the store is unusable anyway.
func (s *LiveStreamRedisStore) acquireLiveStreamRecordLock(ctx context.Context, requestID string) (release func(), locked bool) {
	if s == nil || s.rdb == nil {
		return nil, false
	}
	key := liveStreamRecordLockKey(requestID)
	token := fmt.Sprintf("%d-%d", time.Now().UnixNano(), requestIDHash(requestID))

	backoff := liveStreamRecordLockRetrySleep
	for attempt := 0; attempt < liveStreamRecordLockRetryAttempts; attempt++ {
		ok, err := s.rdb.SetNX(ctx, key, token, liveStreamRecordLockTTL).Result()
		if err != nil {
			// Redis error — store is unusable; let Record decide (it no-ops).
			slog.Debug("live stream record: lock SetNX failed", "request_id", requestID, "err", err.Error())
			return nil, false
		}
		if ok {
			release := func() {
				// Only delete if we still own the lock (token matches) — a Lua
				// compare-and-delete avoids releasing someone else's lock after
				// our TTL expired. Best-effort: ignore errors.
				_, _ = s.rdb.Eval(ctx,
					`if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) else return 0 end`,
					[]string{key}, token).Result()
			}
			return release, true
		}
		// Contention: another Record for the same request_id holds the lock.
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(backoff):
		}
		// Gentle backoff capped at ~50ms.
		if backoff < 50*time.Millisecond {
			backoff += 5 * time.Millisecond
		}
	}
	// Exhausted retries WITHOUT ctx cancellation: surface as a hard error so
	// the caller does NOT write without the lock. This path should be
	// essentially unreachable (5s lock TTL vs ~tens-of-ms pipeline); reaching
	// it indicates a stuck lock holder and the request is better dropped than
	// written raced.
	slog.Warn("live stream record: lock contention exhausted, dropping request to preserve dedup", "request_id", requestID)
	return nil, false
}

// requestIDHash returns a non-cryptographic 63-bit hash of the request id, used
// only to make the lock token unique per caller without depending on a UUID.
func requestIDHash(s string) int64 {
	var h int64 = 1469598103934665603
	for _, c := range s {
		h ^= int64(c)
		h *= 1099511628211
	}
	if h < 0 {
		h = -h
	}
	return h
}

type liveStreamNotifyPayload struct {
	RequestID  string `json:"request_id"`
	TenantID   string `json:"tenant_id"`
	InstanceID string `json:"instance_id,omitempty"`
}

// NotifyChange publishes a lightweight event so SSE hubs (local or
// remote) can react to Redis writes without coupling to the request
// handler goroutine. instanceID, when non-empty, lets the originating hub
// skip its own notify (it already fanned the request out locally) and thus
// avoid duplicating child_request frames.
func (s *LiveStreamRedisStore) NotifyChange(ctx context.Context, tenantID, requestID, instanceID string) error {
	if s == nil || s.rdb == nil || requestID == "" {
		return nil
	}
	payload, err := json.Marshal(liveStreamNotifyPayload{
		RequestID:  requestID,
		TenantID:   normalizeLiveStreamTenant(tenantID),
		InstanceID: instanceID,
	})
	if err != nil {
		return err
	}
	return s.rdb.Publish(ctx, liveStreamNotifyChannel, payload).Err()
}

// LoadRequest reads the latest request detail from Redis. Used by the
// pub/sub subscriber to rebuild the LiveRequest before SSE fan-out.
//
// Sentinel errors:
//   - ErrLiveStreamStoreUnavailable — store not configured or nil receiver
//   - ErrLiveStreamRequestNotFound  — request_id absent in Redis (wrapped with the id)
//
// Callers should use errors.Is to detect, never string-match on the message.
func (s *LiveStreamRedisStore) LoadRequest(ctx context.Context, tenantID, requestID string) (LiveRequest, error) {
	if s == nil || s.rdb == nil || requestID == "" {
		return LiveRequest{}, ErrLiveStreamStoreUnavailable
	}
	tenantID = normalizeLiveStreamTenant(tenantID)
	data, err := s.rdb.Get(ctx, liveStreamGlobalRequestDetailKey(requestID)).Result()
	if err == redis.Nil && tenantID != "" {
		data, err = s.rdb.Get(ctx, liveStreamRequestDetailKey(tenantID, requestID)).Result()
	}
	if err == redis.Nil {
		return LiveRequest{}, fmt.Errorf("%w: %s", ErrLiveStreamRequestNotFound, requestID)
	}
	if err != nil {
		return LiveRequest{}, err
	}
	return unmarshalLiveRequestRedisPayload(data)
}

// Sentinel errors returned by LoadRequest. Callers MUST use errors.Is
// instead of string-matching on Error() output; the readable ": <id>"
// suffix on ErrLiveStreamRequestNotFound is preserved for logs only.
var (
	// ErrLiveStreamStoreUnavailable indicates the live store is not configured
	// or the caller passed a nil receiver / empty request id. Treated as a
	// cache miss by adapters so they can fall through to DB-backed lookups.
	ErrLiveStreamStoreUnavailable = errors.New("live stream store unavailable")

	// ErrLiveStreamRequestNotFound indicates the request id was absent in
	// Redis at lookup time (TTL expired, never written, or wrong tenant scope).
	// Returned wrapped with the request id so slog can include it; callers
	// detect via errors.Is(err, ErrLiveStreamRequestNotFound).
	ErrLiveStreamRequestNotFound = errors.New("request not found")
)

func removeLiveRequestFromQueues(ctx context.Context, pipe redis.Pipeliner, tenantID string, req LiveRequest) {
	// 2026-07-26: Remove from both main queues (by request_id) and dimension
	// queues (by slim tile JSON) to support the slim tile storage format.
	slimData, _ := marshalTileSlim(liveRequestTile(req))
	for _, key := range liveRequestQueueKeys(tenantID, req) {
		if strings.HasSuffix(key, ":main") || key == liveStreamMainKey {
			// Main queues store request_id
			pipe.ZRem(ctx, key, req.RequestID)
		} else if slimData != "" {
			// Dimension queues store slim tile JSON
			pipe.ZRem(ctx, key, slimData)
		}
	}
}

// liveStreamQueueKeepLimit returns how many members to retain in a Redis
// sorted-set queue. Main queues keep enough history for multi-lane replay;
// dimension/status queues trim to the visible swim-lane window (20).
func liveStreamQueueKeepLimit(key string) int {
	if key == liveStreamMainKey || strings.HasSuffix(key, ":main") {
		return liveStreamReplayLimit
	}
	return LiveStreamLaneVisibleLimit
}

// selectiveTrimLiveStreamQueue is invoked post-exec after queue writes; see
// live_stream_queue_trim.go.

func liveRequestQueueKeys(tenantID string, req LiveRequest) []string {
	tenantID = normalizeLiveStreamTenant(tenantID)
	keys := []string{
		liveStreamMainKey,
		tenantLiveStreamKey(tenantID, "main"),
		liveStreamStatPrefix + emptyAs(req.Status, "in_progress"),
		tenantLiveStreamKey(tenantID, "status:"+emptyAs(req.Status, "in_progress")),
	}
	// Only add dimension keys when the dimension value is present and valid
	// Use resolveVendorForRequest to get the resolved vendor through the full fallback chain
	vendor := resolveVendorForRequest(req)
	if vendor != "" && vendor != "__unknown__" && vendor != "other" {
		keys = append(keys,
			liveStreamDimPrefix+"vendor:"+vendor,
			tenantLiveStreamKey(tenantID, "dim:vendor:"+vendor),
		)
	} else {
		slog.Debug("live stream: vendor dimension skipped",
			"request_id", req.RequestID, "tenant_id", tenantID,
			"vendor", vendor, "model_category", req.ModelCategory,
			"provider_code", req.ProviderCode, "model", req.Model)
	}
	// 2026-08-26: 凭据(credential)维度作为新增泳道（与原厂维度并行）。泳道
	// key = 凭据标签(label 优先，否则 "凭据 #ID")，见 liveStreamCredentialKey。
	// 无凭据身份的请求（如鉴权/路由前置失败）不出现在凭据维度。
	if credKey := liveStreamCredentialKey(req); credKey != "" {
		keys = append(keys,
			liveStreamDimPrefix+"credential:"+credKey,
			tenantLiveStreamKey(tenantID, "dim:credential:"+credKey),
		)
	} else {
		slog.Debug("live stream: credential dimension skipped",
			"request_id", req.RequestID, "tenant_id", tenantID,
			"credential_id", req.CredentialID, "credential_label", req.CredentialLabel,
			"provider_code", req.ProviderCode, "model", req.Model)
	}
	if req.ProviderCode != "" && req.ProviderCode != "unknown" {
		keys = append(keys,
			liveStreamDimPrefix+"provider:"+req.ProviderCode,
			tenantLiveStreamKey(tenantID, "dim:provider:"+req.ProviderCode),
		)
	} else {
		slog.Debug("live stream: provider dimension skipped",
			"request_id", req.RequestID, "tenant_id", tenantID,
			"provider_code", req.ProviderCode)
	}
	// Use CanonicalName for model dimension queue keys when available,
	// matching the aggregation logic in liveStreamDimensionKey. This ensures
	// a request is placed in the same queue it will be grouped into during
	// lane building, preventing requests from appearing in wrong lanes.
	//
	// Case-insensitive aggregation: upstream callers (probe / replay) can
	// emit the same logical model with mixed casing (e.g. "MiniMax-M3" and
	// "minimax-m3"). We normalise to a lower-case trimmed form BEFORE
	// composing the Redis key so both spellings land in the same ZSET,
	// while the original casing is preserved in the rendered lane label.
	modelKey := normalizeModelKey(emptyAs(req.CanonicalName, req.Model))
	if modelKey != "" && modelKey != "unknown" {
		keys = append(keys,
			liveStreamDimPrefix+"model:"+modelKey,
			tenantLiveStreamKey(tenantID, "dim:model:"+modelKey),
		)
	} else {
		slog.Debug("live stream: model dimension skipped",
			"request_id", req.RequestID, "tenant_id", tenantID,
			"canonical_name", req.CanonicalName, "model", req.Model,
			"model_key", modelKey)
	}
	return keys
}

// Replay fetches the most recent N requests from the main queue in
// ascending timestamp order. Returns empty slice (not error) when
// Redis is unavailable.
func (s *LiveStreamRedisStore) Replay(ctx context.Context, tenantID string, isSuper bool, limit int) ([]LiveRequest, error) {
	if s == nil || s.rdb == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = liveStreamReplayLimit
	}

	key := liveStreamMainKey
	if !isSuper && tenantID != "" {
		key = tenantLiveStreamKey(normalizeLiveStreamTenant(tenantID), "main")
	}

	// Fetch last N request ids (ZREVRANGE returns DESC, so we reverse later)
	requestIDs, err := s.rdb.ZRevRange(ctx, key, 0, int64(limit-1)).Result()
	if err != nil {
		return nil, fmt.Errorf("redis zrevrange failed: key=%s tenant_id=%s is_super=%v: %w", key, tenantID, isSuper, err)
	}

	out := make([]LiveRequest, 0, len(requestIDs))
	for i := len(requestIDs) - 1; i >= 0; i-- {
		detailKey := liveStreamGlobalRequestDetailKey(requestIDs[i])
		if !isSuper && tenantID != "" {
			detailKey = liveStreamRequestDetailKey(normalizeLiveStreamTenant(tenantID), requestIDs[i])
		}
		data, err := s.rdb.Get(ctx, detailKey).Result()
		if err != nil {
			slog.Debug("failed to fetch live request detail", "request_id", requestIDs[i], "tenant_id", tenantID, "detail_key", detailKey, "err", err.Error())
			continue
		}
		req, err := unmarshalLiveRequestRedisPayload(data)
		if err != nil {
			slog.Debug("failed to unmarshal live request payload", "request_id", requestIDs[i], "tenant_id", tenantID, "err", err.Error())
			continue
		}
		// Tenant filtering (same logic as DB replay)
		if !isSuper && tenantID != "" && req.TenantID != tenantID {
			continue
		}
		out = append(out, req)
	}
	return out, nil
}

func marshalLiveRequestRedisPayload(req LiveRequest) (string, error) {
	p := liveRequestRedisPayload{
		Type:             req.Type,
		RequestID:        req.RequestID,
		Ts:               req.Ts,
		TenantID:         req.TenantID,
		GwSessionID:      req.GwSessionID,
		Model:            req.Model,
		CanonicalName:    req.CanonicalName,
		ModelCategory:    req.ModelCategory,
		ProviderCode:     req.ProviderCode,
		Status:           req.Status,
		LatencyMs:        req.LatencyMs,
		PromptTokens:     req.PromptTokens,
		CompletionTokens: req.CompletionTokens,
		TotalTokens:      req.TotalTokens,
		CostUSD:          req.CostUSD,
		ErrorKind:        req.ErrorKind,
		FailureStage:     req.FailureStage,
		IsProbe:          req.IsProbe,
		ProbeOrigin:      req.ProbeOrigin,
		ProbeAttempt:     req.ProbeAttempt,
		ClientProfile:    req.ClientProfile,
		IdentityHash:     req.IdentityHash,
		CreditsCharged:   req.CreditsCharged,
		ParentRequestID:  req.ParentRequestID,
		RequestType:      req.RequestType,
		CredentialID:     req.CredentialID,
		CredentialLabel:  req.CredentialLabel,
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalLiveRequestRedisPayload(data string) (LiveRequest, error) {
	var p liveRequestRedisPayload
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return LiveRequest{}, err
	}
	return LiveRequest{
		Type:             p.Type,
		RequestID:        p.RequestID,
		Ts:               p.Ts,
		TenantID:         p.TenantID,
		GwSessionID:      p.GwSessionID,
		Model:            p.Model,
		CanonicalName:    p.CanonicalName,
		ModelCategory:    p.ModelCategory,
		ProviderCode:     p.ProviderCode,
		Status:           p.Status,
		LatencyMs:        p.LatencyMs,
		PromptTokens:     p.PromptTokens,
		CompletionTokens: p.CompletionTokens,
		TotalTokens:      p.TotalTokens,
		CostUSD:          p.CostUSD,
		ErrorKind:        p.ErrorKind,
		FailureStage:     p.FailureStage,
		IsProbe:          p.IsProbe,
		ProbeOrigin:      p.ProbeOrigin,
		ProbeAttempt:     p.ProbeAttempt,
		ClientProfile:    p.ClientProfile,
		IdentityHash:     p.IdentityHash,
		CreditsCharged:   p.CreditsCharged,
		ParentRequestID:  p.ParentRequestID,
		RequestType:      normalizeLiveRequestType(p.RequestType),
		CredentialID:     p.CredentialID,
		CredentialLabel:  p.CredentialLabel,
	}, nil
}

func liveStreamGlobalRequestDetailKey(requestID string) string {
	return "llmgw:live:req:" + requestID
}

func liveStreamRequestDetailKey(tenantID, requestID string) string {
	return tenantLiveStreamKey(tenantID, "req:"+requestID)
}

// Snapshot builds a snapshot from the main queue (legacy method).
// DEPRECATED: Use SnapshotFromDimensionQueues for stable per-lane tile counts.
// This method is kept for backward compatibility and fallback scenarios.
func (s *LiveStreamRedisStore) Snapshot(ctx context.Context, tenantID string, isSuper bool, limit int) (*LiveStreamSnapshot, error) {
	items, err := s.Replay(ctx, tenantID, isSuper, limit)
	if err != nil {
		return nil, err
	}
	return BuildLiveStreamSnapshot(items), nil
}

func BuildLiveStreamSnapshot(items []LiveRequest) *LiveStreamSnapshot {
	s := &LiveStreamSnapshot{
		Dimensions: map[string][]LiveStreamLane{
			"credential": {},
			"vendor":     {},
			"provider":   {},
			"model":      {},
		},
		DimensionLegends: map[string][]LiveStreamLegendItem{
			"credential": {},
			"vendor":     {},
			"provider":   {},
			"model":      {},
		},
		StatusLegends: []LiveStreamLegendItem{},
	}

	seenForSummary := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.Type == "idle_marker" {
			continue
		}
		// Diagnostic client-cancel probes remain visible as tiles, but they do
		// not represent an upstream outcome and must not affect failure rates.
		if isClientCancelProbe(item) {
			continue
		}
		if _, ok := seenForSummary[item.RequestID]; ok {
			continue
		}
		seenForSummary[item.RequestID] = struct{}{}
		countStatus(&s.Summary, item.Status)
		// Track the latest request timestamp for versioning
		if item.Ts > s.LatestRequestTs {
			s.LatestRequestTs = item.Ts
		}
	}

	for _, dim := range []string{"credential", "vendor", "provider", "model"} {
		lanes, legends := buildLiveStreamLanes(dim, items)
		s.Dimensions[dim] = lanes
		s.DimensionLegends[dim] = legends
	}
	s.StatusLegends = buildStatusLegends(items)
	return s
}

func buildLiveStreamLanes(dimension string, items []LiveRequest) ([]LiveStreamLane, []LiveStreamLegendItem) {
	stats := map[string]LiveStreamStats{}
	grouped := map[string][]LiveStreamTile{}
	seenByLane := map[string]map[string]struct{}{}
	latestIdleByLane := map[string]LiveStreamTile{}

	for _, req := range items {
		key := liveStreamDimensionKey(dimension, req)
		// Skip requests without valid dimension values (empty keys)
		if key == "" {
			continue
		}

		// Idle markers from different scopes can have different RequestIDs
		// while representing the same rendered lane (for example, a global
		// marker and a legacy tenant marker in a super-admin snapshot). Keep
		// one idle tile per lane, choosing the newest timestamp. This also
		// cleans up duplicates already left in Redis by older writers.
		if req.Type == "idle_marker" {
			candidate := liveRequestTile(req)
			previous, exists := latestIdleByLane[key]
			if !exists || candidate.Timestamp > previous.Timestamp ||
				(candidate.Timestamp == previous.Timestamp && candidate.RequestID < previous.RequestID) {
				latestIdleByLane[key] = candidate
			}
			if _, ok := stats[key]; !ok {
				// Ensure an idle-only lane still gets an (empty) stats entry so it
				// appears in the legend.
				stats[key] = LiveStreamStats{}
			}
			continue
		}

		laneSeen := seenByLane[key]
		if laneSeen == nil {
			laneSeen = map[string]struct{}{}
			seenByLane[key] = laneSeen
		}
		if _, exists := laneSeen[req.RequestID]; exists {
			continue
		}
		laneSeen[req.RequestID] = struct{}{}
		st := stats[key]
		if !isClientCancelProbe(req) {
			countStatus(&st, req.Status)
		}
		stats[key] = st
		grouped[key] = append(grouped[key], liveRequestTile(req))
	}

	for key, idleTile := range latestIdleByLane {
		grouped[key] = append(grouped[key], idleTile)
	}

	// Product FIFO: oldest left → newest right. SwimLaneTrack paints index
	// 0 leftmost and lastTiles() keeps the tail (newest N). Sorting here
	// makes SnapshotFromDimensionQueues and Replay agree regardless of
	// caller order.
	for key := range grouped {
		tiles := grouped[key]
		sort.SliceStable(tiles, func(i, j int) bool {
			// Ts is RFC3339 — lexicographic compare matches chronological
			// order. Tie-break on RequestID so equal timestamps stay stable
			// across snapshots and don't trip lanesChanged.
			if tiles[i].Timestamp != tiles[j].Timestamp {
				return tiles[i].Timestamp < tiles[j].Timestamp
			}
			return tiles[i].RequestID < tiles[j].RequestID
		})
		grouped[key] = tiles
	}

	keys := make([]string, 0, len(stats))
	for key := range stats {
		keys = append(keys, key)
	}

	// 2026-08-27: credential lanes key on the numeric credential_id. During
	// the 24h TTL migration window, idle markers written by the legacy
	// "provider/label" keying still resolve to non-numeric lane keys; those
	// idle-only lanes have no data and no display name — drop them instead of
	// rendering ghost lanes.
	displayNames := make(map[string]string, len(keys))
	if dimension == "credential" {
		numeric := keys[:0]
		for _, key := range keys {
			credentialID, err := strconv.Atoi(key)
			if err != nil || credentialID <= 0 {
				continue
			}
			numeric = append(numeric, key)
			displayNames[key] = resolveCredentialDisplayName(key, items)
		}
		keys = numeric
	}

	if dimension == "credential" {
		// Sort credential lanes by display name (provider/label), tie-broken
		// by numeric credential_id. Handoff requires name-first ordering with a
		// stable ID tie-break; plain sort.Strings would order "10" before "9".
		sort.SliceStable(keys, func(i, j int) bool {
			if displayNames[keys[i]] != displayNames[keys[j]] {
				return displayNames[keys[i]] < displayNames[keys[j]]
			}
			idI, _ := strconv.Atoi(keys[i])
			idJ, _ := strconv.Atoi(keys[j])
			return idI < idJ
		})
	} else {
		// Stable alphabetical order — Total only affects legend count, not lane
		// position. Sorting by Total caused lanes to jump on every stat tick.
		sort.Strings(keys)
	}

	// Build lanes - no more top N or others aggregation, return all lanes
	// Skip empty keys and unknown/other categories
	lanes := make([]LiveStreamLane, 0, len(keys))
	legends := make([]LiveStreamLegendItem, 0, len(keys))

	for _, key := range keys {
		// 过滤历史脏数据：仅过滤真正的"未知"标记。我们用"其他"作为最终兜底，
		// 因此不再把 literal "other" 视为污染（"其他"是合法的兜底显示）。
		if key == "" || key == "unknown" || key == "__unknown__" || key == "__idle__" {
			continue
		}

		// 2026-08-27: For credential dimension, Lane.ID is the stable credential_id,
		// Lane.Name is the display string "provider/label" resolved from the newest
		// request in this lane. Other dimensions use key for both ID and Name.
		displayName := key
		if dimension == "credential" {
			displayName = displayNames[key]
		}

		lanes = append(lanes, LiveStreamLane{
			ID:        key,
			Name:      displayName,
			Dimension: dimension,
			Requests:  lastTiles(grouped[key], liveStreamLaneLimit),
			Stats:     stats[key],
			IsOthers:  false,
		})
		legends = append(legends, LiveStreamLegendItem{Key: key, Name: displayName, Count: stats[key].Total})
	}

	return lanes, legends
}

func buildStatusLegends(items []LiveRequest) []LiveStreamLegendItem {
	counts := map[string]int{}
	seen := map[string]struct{}{}
	for _, req := range items {
		if _, ok := seen[req.RequestID]; ok {
			continue
		}
		seen[req.RequestID] = struct{}{}
		if req.Type == "idle_marker" {
			counts["idle"]++
			continue
		}
		counts[emptyAs(req.Status, "in_progress")]++
	}
	order := []string{"success", "in_progress", "rate_limited", "failure", "idle"}
	legends := make([]LiveStreamLegendItem, 0, len(order))
	for _, key := range order {
		legends = append(legends, LiveStreamLegendItem{Key: key, Name: key, Count: counts[key]})
	}
	return legends
}

// liveStreamCredentialKey returns the credential lane identity and display name.
//
// 2026-08-27: Identity strategy. A credential's ID is globally unique and stable
// across renames; label is a mutable display string. The lane key must be stable
// so rename operations do not break the Redis queue identity or the frontend
// Vue TransitionGroup key.
//
// Lane identity (Redis key, Lane.ID, Legend.Key): credential_id (integer)
// Display name (Lane.Name, Legend.Name): "provider/label" or "provider/凭据 #id"
//
// Idle markers inherit the lane key in CredentialLabel so both real requests
// and idle markers resolve to the same lane.
func liveStreamCredentialKey(req LiveRequest) string {
	// Idle markers carry the stable lane key in CredentialLabel (set by createIdleMarkerForDimension).
	// Re-applying the provider/label fallback would create a different lane identity.
	if req.Type == "idle_marker" {
		return strings.TrimSpace(req.CredentialLabel)
	}

	// Real requests: use credential_id as the stable lane identity.
	// The display name (provider/label) is resolved separately in buildLiveStreamLanes.
	if req.CredentialID <= 0 {
		return "" // Skip requests without credential binding
	}

	return fmt.Sprintf("%d", req.CredentialID)
}

// resolveCredentialDisplayName builds the display name "provider/label" for a
// credential lane. It picks the NEWEST real request carrying the lane's
// credential_id so a renamed credential shows its latest label instead of a
// stale first-seen snapshot. Falls back to "未知供应商/凭据 #<id>" when the
// lane has no real request (idle-only lane) or fields are missing.
func resolveCredentialDisplayName(laneKey string, items []LiveRequest) string {
	// strconv.Atoi (not Sscanf) rejects suffixes: "42abc" must not resolve to 42.
	credentialID, err := strconv.Atoi(laneKey)
	if err != nil || credentialID <= 0 {
		// Legacy-format key ("provider/label") — caller filters these out;
		// return as-is for any residual defensive call.
		return laneKey
	}

	var newest LiveRequest
	found := false
	for _, req := range items {
		if req.Type == "idle_marker" || req.CredentialID != credentialID {
			continue
		}
		if !found || req.Ts > newest.Ts || (req.Ts == newest.Ts && req.RequestID > newest.RequestID) {
			newest = req
			found = true
		}
	}
	if !found {
		return fmt.Sprintf("未知供应商/凭据 #%d", credentialID)
	}
	return credentialDisplayNameFrom(newest)
}

// credentialDisplayNameFrom composes the "provider/label" display string from
// one request, applying the shared fallbacks (unknown provider → 未知供应商,
// empty label → "凭据 #ID").
func credentialDisplayNameFrom(req LiveRequest) string {
	provider := strings.TrimSpace(req.ProviderCode)
	if provider == "" || provider == "unknown" || provider == "__unknown__" {
		provider = "未知供应商"
	}
	label := strings.TrimSpace(req.CredentialLabel)
	if label == "" {
		label = fmt.Sprintf("凭据 #%d", req.CredentialID)
	}
	return provider + "/" + label
}

func liveStreamDimensionKey(dimension string, req LiveRequest) string {
	// For idle markers, use the actual dimension value (already set correctly in createIdleMarkerForDimension)
	// This ensures idle markers inherit the queue's identity rather than creating separate idle lanes
	switch dimension {
	case "credential":
		// 2026-08-26: 凭据维度。idle marker 的 CredentialLabel 已被
		// createIdleMarkerForDimension 置为泳道 key，liveStreamCredentialKey
		// 的 label 优先规则对真实请求与 idle marker 同时成立。
		return liveStreamCredentialKey(req)
	case "vendor":
		// 2026-08-26: 原厂维度仍在泳道队列里有写入 (liveRequestQueueKeys
		// 写 vendor:<vendor> + tenant dim:vendor:<vendor> 双队列)，保留
		// 是因为：(a) BuildLiveStreamSnapshot 在 credential 之外仍输出
		// vendor 维度让前端 tile 颜色 / 老检查位兼容；(b) CreateIdleMarkerForDimension
		// 的 vendor case 把 ModelCategory 作为泳道 key 注入 → 与真实
		// 请求归到同一泳道，避免出现"空闲块"重复泳道。
		if req.Type == "idle_marker" {
			return req.ModelCategory
		}
		return resolveVendorForRequest(req)
	case "provider":
		if req.Type == "idle_marker" {
			pc := strings.TrimSpace(req.ProviderCode)
			if pc != "" && pc != "unknown" && pc != "__unknown__" {
				return pc
			}
			return ""
		}
		pc := strings.TrimSpace(req.ProviderCode)
		if pc == "" || pc == "unknown" || pc == "__unknown__" {
			return ""
		}
		return pc
	case "model":
		if req.Type == "idle_marker" {
			if req.CanonicalName != "" {
				return normalizeModelKey(req.CanonicalName)
			}
			if req.Model != "" {
				return normalizeModelKey(req.Model)
			}
			return ""
		}
		if req.CanonicalName != "" {
			return normalizeModelKey(req.CanonicalName)
		}
		if req.Model == "" {
			return "" // Skip this dimension if no value
		}
		return normalizeModelKey(req.Model)
	default:
		return ""
	}
}

// resolveVendorForRequest resolves the vendor for a request using the full fallback chain:
// 1. ModelCategory (from DB lookup)
// 2. VendorFromProvider (provider → vendor mapping)
// 3. InferVendorFromModel (固化标准 model → vendor)
// 4. Model name itself (最后兜底，绝对不返回空、绝不出现"未知"字样)
func resolveVendorForRequest(req LiveRequest) string {
	if req.ModelCategory != "" && req.ModelCategory != "__unknown__" && req.ModelCategory != "unknown" && req.ModelCategory != "other" {
		return req.ModelCategory
	}
	if req.ProviderCode != "" {
		if vendor := VendorFromProvider(req.ProviderCode); vendor != "" {
			return vendor
		}
	}
	if req.Model != "" {
		if vendor := InferVendorFromModel(req.Model); vendor != "" {
			return vendor
		}
		// 最后兜底：直接用 model 名作为泳道 key；前端会把它显示为原厂名称。
		// 老板要求"再没有就直接用模型名称，不要用未知或其它来标记"。
		return req.Model
	}
	// 没有任何可识别字段时仍要返回非空串，避免被 lanes 过滤成空。
	// 用 canonical_name → model → 用一个固定的 no-idle 默认值兜底。
	if req.CanonicalName != "" {
		return req.CanonicalName
	}
	return "其他"
}

// InferVendorFromModel infers the vendor from model name patterns.
// This is the last-resort fallback when neither DB lookup nor provider mapping works.
func InferVendorFromModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	switch {
	case strings.Contains(m, "gpt"), strings.Contains(m, "o1"), strings.Contains(m, "o3"), strings.Contains(m, "o4"):
		return "openai"
	case strings.Contains(m, "claude"):
		return "anthropic"
	case strings.Contains(m, "gemini"), strings.Contains(m, "palm"):
		return "google"
	case strings.Contains(m, "qwen"):
		return "alibaba"
	case strings.Contains(m, "glm"):
		return "zhipu"
	case strings.Contains(m, "deepseek"):
		return "deepseek"
	case strings.Contains(m, "doubao"):
		return "bytedance"
	case strings.Contains(m, "ernie"):
		return "baidu"
	case strings.Contains(m, "moonshot"):
		return "moonshot"
	case strings.Contains(m, "yi-"):
		return "01ai"
	case strings.Contains(m, "baichuan"):
		return "baichuan"
	case strings.Contains(m, "llama"):
		return "meta"
	case strings.Contains(m, "mistral"), strings.Contains(m, "mixtral"):
		return "mistral"
	case strings.Contains(m, "minimax"):
		return "minimax"
	case strings.Contains(m, "mimo"):
		return "xiaomi"
	case strings.Contains(m, "phi"):
		return "microsoft"
	case strings.Contains(m, "gemma"):
		return "google"
	default:
		return ""
	}
}

func liveRequestTile(req LiveRequest) LiveStreamTile {
	// 2026-07-24: tile.Model 必须是标准名（canonical），禁止用供应商 raw/outbound
	// 做模型维度展示；与 liveStreamDimensionKey("model") 保持一致。
	standardModel := strings.TrimSpace(req.CanonicalName)
	if standardModel == "" {
		standardModel = strings.TrimSpace(req.Model)
	}
	status := emptyAs(req.Status, "in_progress")
	tile := LiveStreamTile{
		RequestID:        req.RequestID,
		Timestamp:        req.Ts,
		Model:            standardModel,
		Vendor:           resolveVendorForRequest(req),
		Provider:         req.ProviderCode,
		Status:           status,
		CredentialID:     req.CredentialID,
		CredentialLabel:  strings.TrimSpace(req.CredentialLabel),
		ErrorKind:        req.ErrorKind,
		LatencyMs:        req.LatencyMs,
		CostUSD:          req.CostUSD,
		PromptTokens:     req.PromptTokens,
		CompletionTokens: req.CompletionTokens,
		IsProbe:          req.IsProbe,
		ProbeOrigin:      req.ProbeOrigin,
		ProbeAttempt:     req.ProbeAttempt,
	}
	// First paint for in-flight tiles defaults to "routing" so the live
	// stream can distinguish "received / routing" from "waiting on LLM"
	// before the first lifecycle action arrives. Upstream actions flip
	// this to "llm" via SSE request_lifecycle patches.
	if status == "in_progress" {
		tile.StageCategory = "routing"
	}
	// Idle markers carry only their own dimension's identity. Surface a
	// human-readable "[空闲]" label on whichever field is empty so the
	// tile is never blank, while preserving the real value on the field
	// that identifies the lane.
	if req.Type == "idle_marker" {
		tile.Status = "idle"
		if tile.Model == "" {
			tile.Model = "[空闲]"
		}
		if tile.Vendor == "" {
			tile.Vendor = "[空闲]"
		}
		if tile.Provider == "" {
			tile.Provider = "[空闲]"
		}
	}
	return tile
}

func isClientCancelProbe(req LiveRequest) bool {
	return req.FailureStage != nil && *req.FailureStage == "probe" &&
		req.ErrorKind != nil && *req.ErrorKind == "client_cancel"
}

func countStatus(stats *LiveStreamStats, status string) {
	stats.Total++
	switch status {
	case "success":
		stats.Success++
	case "failure":
		stats.Failure++
	case "rate_limited":
		stats.RateLimited++
	case "in_progress":
		stats.InProgress++
	default:
		stats.InProgress++
	}
}

// lastTiles caps a lane at limit tiles by keeping the tail of items.
// buildLiveStreamLanes sorts each lane ASC (oldest first) before calling
// this, so the tail is the newest N and the truncated head is the oldest.
func lastTiles(items []LiveStreamTile, limit int) []LiveStreamTile {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

// firstTiles returns the first N tiles from items (newest tiles, for RIGHT→LEFT display).
// Backend stores tiles in DESC timestamp order in Redis ZSET, so first N = newest N.
func firstTiles(items []LiveStreamTile, limit int) []LiveStreamTile {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[:limit]
}

func tenantLiveStreamKey(tenantID, suffix string) string {
	return "llmgw:live:tenant:" + normalizeLiveStreamTenant(tenantID) + ":" + suffix
}

// liveStreamDimIndexKey returns the Redis SET key that indexes all dimension
// queue keys for one scope (方案C). super-admin reads the global index; a
// tenant reads its own. The SET's members are the full dim queue key names
// (e.g. "llmgw:live:dim:vendor:minimax" or
// "llmgw:live:tenant:default:dim:vendor:minimax").
func liveStreamDimIndexKey(tenantID string, isSuper bool) string {
	if isSuper {
		return liveStreamDimIndexPrefix + "global"
	}
	return liveStreamDimIndexPrefix + "tenant:" + normalizeLiveStreamTenant(tenantID)
}

// isDimensionQueueKey reports whether a Redis key is a swim-lane dimension
// queue (vendor/provider/model). Used by the index writer to decide which
// queue keys to register, and by the reader to filter SET members down to
// the requested dimensions. Only keys containing the ":dim:" segment and a
// known dimension suffix are considered.
func isDimensionQueueKey(key string) bool {
	// Both global ("llmgw:live:dim:vendor:...") and tenant
	// ("llmgw:live:tenant:<id>:dim:credential:...") forms contain ":dim:".
	idx := strings.Index(key, ":dim:")
	if idx < 0 {
		return false
	}
	rest := key[idx+len(":dim:"):]
	// 2026-08-26: credential 替换 vendor。"vendor:" 旧队列保留识别以兼容
	// 存量 Redis 数据（24h TTL 自动过期），但新代码不再写入。
	return strings.HasPrefix(rest, "credential:") ||
		strings.HasPrefix(rest, "provider:") ||
		strings.HasPrefix(rest, "model:") ||
		strings.HasPrefix(rest, "vendor:")
}

// isGlobalDimKey reports whether a dim queue key is the global-scope form
// ("llmgw:live:dim:...") rather than a tenant-scoped form
// ("llmgw:live:tenant:<id>:dim:..."). The index writer routes a key into the
// global vs tenant index SET based on this.
func isGlobalDimKey(key string) bool {
	return strings.HasPrefix(key, liveStreamDimPrefix)
}

func normalizeLiveStreamTenant(tenantID string) string {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return "default"
	}
	return tenantID
}

func emptyAs(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// Stats returns Redis-based queue sizes for monitoring. Returns zero
// counts when Redis is unavailable.
func (s *LiveStreamRedisStore) Stats(ctx context.Context) map[string]int64 {
	if s == nil || s.rdb == nil {
		return map[string]int64{"main": 0}
	}
	main, _ := s.rdb.ZCard(ctx, liveStreamMainKey).Result()
	return map[string]int64{"main": main}
}

// liveStreamActivityKey builds the Redis key that records the last
// activity timestamp for a (tenant, dimension, dimensionKey) tuple.
// The scope segment ("global" vs "tenant:<id>") is the FIRST field so
// parsing can never be ambiguous even when the dimensionKey contains ":".
func liveStreamActivityKey(tenantID, dimension, key string) string {
	scope := "global"
	if tenantID != "" {
		scope = "tenant:" + normalizeLiveStreamTenant(tenantID)
	}
	if key == "" {
		return fmt.Sprintf("%s%s:%s", liveStreamActivityPrefix, scope, dimension)
	}
	return fmt.Sprintf("%s%s:%s:%s", liveStreamActivityPrefix, scope, dimension, key)
}

// activityKeyInfo decodes a liveStreamActivityKey back into its parts.
// Returns ok=false when the key is malformed. The scope segment is
// consumed first (it never contains ":" outside the "tenant:<id>" form),
// so the remainder is split into dimension + dimensionKey unambiguously.
type activityKeyInfo struct {
	tenantID     string
	dimension    string
	dimensionKey string
}

func parseActivityKey(key string) (activityKeyInfo, bool) {
	rest := strings.TrimPrefix(key, liveStreamActivityPrefix)
	if rest == key { // prefix did not match
		return activityKeyInfo{}, false
	}
	var info activityKeyInfo
	// Scope is either "global" or "tenant:<id>"; take everything up to the first ":" after it.
	if strings.HasPrefix(rest, "tenant:") {
		// tenant:<id>:dimension[:dimKey]
		afterTenant := strings.TrimPrefix(rest, "tenant:")
		idx := strings.Index(afterTenant, ":")
		if idx < 0 {
			return activityKeyInfo{}, false
		}
		info.tenantID = afterTenant[:idx]
		rest = afterTenant[idx+1:]
	} else if strings.HasPrefix(rest, "global:") {
		rest = strings.TrimPrefix(rest, "global:")
	} else {
		return activityKeyInfo{}, false
	}
	// rest is now "dimension" or "dimension:dimKey"
	if idx := strings.Index(rest, ":"); idx >= 0 {
		info.dimension = rest[:idx]
		info.dimensionKey = rest[idx+1:]
	} else {
		info.dimension = rest
	}
	if info.dimension == "" {
		return activityKeyInfo{}, false
	}
	return info, true
}

// ScanAndRecordIdleMarkers scans all dimension queues and inserts idle
// markers for queues that have been idle for longer than idleThreshold.
// Each idle marker is written ONLY to the queue it pertains to (plus its
// tenant-scoped twin), so an idle vendor marker never pollutes the model
// lane or the main queue.
func (s *LiveStreamRedisStore) ScanAndRecordIdleMarkers(ctx context.Context, ts time.Time, idleThreshold time.Duration) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	if idleThreshold <= 0 {
		idleThreshold = LiveStreamIdleThreshold
	}
	idleThresholdSeconds := int64(idleThreshold.Seconds())

	nowUnix := ts.Unix()

	// 1) Derive activity keys from the dimension indexes. The Redis DB also
	// contains hundreds of thousands of session/detail keys; scanning the
	// whole keyspace every 10 seconds starves live request writes and makes
	// provider lanes appear stale. Indexes are populated by Record().
	activityKeys, err := s.activityKeysFromDimensionIndexes(ctx)
	if err != nil {
		return fmt.Errorf("load activity indexes failed: %w", err)
	}
	if len(activityKeys) == 0 {
		// Preserve cold-start compatibility before the first indexed write.
		iter := s.rdb.Scan(ctx, 0, liveStreamActivityPrefix+"*", 5000).Iterator()
		for iter.Next(ctx) {
			activityKeys = append(activityKeys, iter.Val())
		}
		if err := iter.Err(); err != nil {
			return fmt.Errorf("scan activity keys failed: %w", err)
		}
	}
	if len(activityKeys) == 0 {
		return nil
	}

	// 2) Batch-fetch all timestamps in one pipeline (avoid N round-trips).
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.StringCmd, len(activityKeys))
	for i, k := range activityKeys {
		cmds[i] = pipe.Get(ctx, k)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return fmt.Errorf("mget activity timestamps failed: %w", err)
	}

	type pending struct {
		info         activityKeyInfo
		key          string
		lastActivity int64
	}
	var idle []pending
	for i, k := range activityKeys {
		val, err := cmds[i].Result()
		if err != nil {
			continue // key expired between scan and get
		}
		var lastActivity int64
		if _, err := fmt.Sscanf(val, "%d", &lastActivity); err != nil {
			slog.Debug("activity key has non-numeric timestamp", "key", k, "value", val)
			continue
		}
		if nowUnix-lastActivity < idleThresholdSeconds {
			continue
		}
		info, ok := parseActivityKey(k)
		if !ok {
			slog.Debug("skipping malformed activity key", "key", k)
			continue
		}
		// Main-queue activity is a heartbeat only; swim lanes are built from
		// vendor/provider/model dimension keys. Skip "main" so we do not
		// enqueue dimension-less idle markers that never render in a lane.
		if info.dimension == "main" {
			continue
		}
		idle = append(idle, pending{info: info, key: k, lastActivity: lastActivity})
	}

	if len(idle) == 0 {
		return nil
	}

	// 3) Build + persist idle markers, writing only to the relevant lane(s).
	writePipe := s.rdb.Pipeline()
	idleTrimKeys := make([]string, 0, len(idle)*2)
	for _, p := range idle {
		// 2026-07-28 fix: use scan time (ts) for BOTH the marker Ts and
		// the ZSet score. The previous implementation anchored both at
		// `lastActivity+threshold` (the moment silence began), which
		// caused the "update idle time" requirement to be a Redis no-op
		// (ZADD with the same member + same score does nothing, and the
		// detail hash was also overwritten with identical bytes). With
		// the new behavior:
		//   - Each tick is a real ZADD (different score than the previous
		//     tick), so the ZSet key's TTL is refreshed and the marker
		//     visibly moves to the leftmost position.
		//   - The detail hash gets the fresh Ts payload, so any front-end
		//     that reads detail (e.g. via Replay fallback) sees the new
		//     Ts without waiting for a full re-snapshot.
		//   - A new real request at score=now has a higher score than the
		//     idle marker, so the new request goes leftmost and the idle
		//     tile gets pushed right (the "正常记录把 idle 推到最左侧"
		//     requirement from the bug report).
		marker := createIdleMarkerForDimension(p.info.dimension, p.info.dimensionKey, p.info.tenantID, ts)
		data, err := marshalLiveRequestRedisPayload(marker)
		if err != nil {
			slog.Debug("failed to marshal idle marker", "dimension", p.info.dimension, "dimension_key", p.info.dimensionKey, "tenant_id", p.info.tenantID, "err", err.Error())
			continue
		}
		score := float64(ts.UnixMilli())

		// Detail lookups: store under the global key always, and the
		// tenant key when scoped, so Replay can resolve the marker.
		// Note: marker.TenantID is already normalized from parseActivityKey.
		writePipe.Set(ctx, liveStreamGlobalRequestDetailKey(marker.RequestID), data, liveStreamTTL)
		writePipe.Set(ctx, liveStreamRequestDetailKey(marker.TenantID, marker.RequestID), data, liveStreamTTL)

		for _, qkey := range idleMarkerQueueKeys(marker.TenantID, p.info.dimension, p.info.dimensionKey) {
			writePipe.ZAdd(ctx, qkey, redis.Z{Score: score, Member: marker.RequestID})
			// 2026-07-23: idle marker 写入泳道队列，队列 TTL 用 24h（泳道存在 ≤ 1 天）
			writePipe.Expire(ctx, qkey, liveStreamLaneQueueTTL)
			idleTrimKeys = append(idleTrimKeys, qkey)
			// 2026-08-04 (方案C): keep the dim index SET in sync when an idle
			// marker creates a dim queue (idle-only lane). Routes the key to the
			// global vs tenant index, matching the Record() path.
			if isDimensionQueueKey(qkey) {
				indexKey := liveStreamDimIndexKey(marker.TenantID, isGlobalDimKey(qkey))
				writePipe.SAdd(ctx, indexKey, qkey)
				writePipe.Expire(ctx, indexKey, liveStreamLaneQueueTTL)
			}
		}
	}

	if _, err := writePipe.Exec(ctx); err != nil && err != redis.Nil {
		return fmt.Errorf("write idle markers failed: %w", err)
	}
	for _, qkey := range idleTrimKeys {
		if trimErr := selectiveTrimLiveStreamQueue(ctx, s.rdb, qkey, liveStreamQueueKeepLimit(qkey)); trimErr != nil {
			slog.Debug("live stream idle selective trim failed", "key", qkey, "err", trimErr.Error())
		}
	}
	return nil
}

func (s *LiveStreamRedisStore) activityKeysFromDimensionIndexes(ctx context.Context) ([]string, error) {
	global, err := s.rdb.SMembers(ctx, liveStreamDimIndexKey("", true)).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	tenants, err := s.rdb.SMembers(ctx, liveStreamTenantSet).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	keys := make([]string, 0, len(global)+len(tenants)*4)
	appendQueueKeys := func(queueKeys []string) {
		for _, queueKey := range queueKeys {
			if !isDimensionQueueKey(queueKey) {
				continue
			}
			info, ok := dimensionQueueKeyInfo(queueKey)
			if ok {
				keys = append(keys, liveStreamActivityKey(info.tenantID, info.dimension, info.dimensionKey))
			}
		}
	}
	appendQueueKeys(global)
	for _, tenantID := range tenants {
		members, memberErr := s.rdb.SMembers(ctx, liveStreamDimIndexKey(tenantID, false)).Result()
		if memberErr != nil && memberErr != redis.Nil {
			return nil, memberErr
		}
		appendQueueKeys(members)
	}
	return uniqueStrings(keys), nil
}

type dimensionQueueInfo struct {
	tenantID     string
	dimension    string
	dimensionKey string
}

func dimensionQueueKeyInfo(key string) (dimensionQueueInfo, bool) {
	marker := ":dim:"
	idx := strings.Index(key, marker)
	if idx < 0 {
		return dimensionQueueInfo{}, false
	}
	prefix, suffix := key[:idx], key[idx+len(marker):]
	parts := strings.SplitN(suffix, ":", 2)
	// 2026-08-26: credential 替换 vendor；保留 "vendor" 读兼容（存量队列）。
	if len(parts) != 2 || (parts[0] != "credential" && parts[0] != "provider" && parts[0] != "model" && parts[0] != "vendor") || parts[1] == "" {
		return dimensionQueueInfo{}, false
	}
	tenantID := ""
	if strings.HasPrefix(prefix, "llmgw:live:tenant:") {
		tenantID = strings.TrimPrefix(prefix, "llmgw:live:tenant:")
	}
	return dimensionQueueInfo{tenantID: tenantID, dimension: parts[0], dimensionKey: parts[1]}, true
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// idleMarkerQueueKeys returns the Redis ZSET keys an idle marker should
// land in.
//
// Two readers must see the marker:
//   - Replay() / Snapshot() — read the main queue, so we write the
//     marker there for backward compatibility and for the empty-queue
//     fallback path.
//   - SnapshotFromDimensionQueues() (the production reader) — reads
//     only dimension queues. Without a mirror write here, the idle
//     marker is invisible to the dashboard the moment any vendor/
//     provider/model queue has data (the original bug: idle was
//     written but never reached the front-end).
//
// Global-scope markers go to the super-admin main queue AND every
// dim:* key they belong to (no tenant prefix on dim keys). Tenant-
// scoped markers go to that tenant's main queue AND the tenant's
// tenant-scoped dim keys — never the global dim keys — so a
// tenant-scoped marker never pollutes the super-admin view.
//
// The ZADD with stable RequestID + refreshed score is idempotent: a
// lane that has been idle for many ticks carries exactly one marker
// (the last score wins), and the "update idle time, don't add new"
// requirement is satisfied at the Redis level.
func idleMarkerQueueKeys(tenantID, dimension, dimensionKey string) []string {
	if strings.TrimSpace(dimensionKey) == "" {
		// No dimension key (e.g. a "main" activity key that escaped the
		// filter — should not happen because ScanAndRecordIdleMarkers
		// skips dimension=="main", but guard anyway). Fall back to main
		// queue only; there is no dim queue to mirror to.
		if strings.TrimSpace(tenantID) == "" {
			return []string{liveStreamMainKey}
		}
		return []string{tenantLiveStreamKey(normalizeLiveStreamTenant(tenantID), "main")}
	}

	// Build the dim key path. Safe escaping matches the convention used
	// by idleMarkerRequestID and liveStreamDimensionKey: ":" and "/"
	// in dimension values are turned into "_" so the resulting Redis
	// key never has an ambiguous ":" that a downstream parser would
	// split incorrectly.
	safeKey := strings.NewReplacer(":", "_", "/", "_").Replace(dimensionKey)
	dimSuffix := dimension + ":" + safeKey
	var keys []string
	if strings.TrimSpace(tenantID) == "" {
		// Global scope: super-admin main queue + global dim queue.
		keys = []string{
			liveStreamMainKey,
			liveStreamDimPrefix + dimSuffix,
		}
		return keys
	}
	tid := normalizeLiveStreamTenant(tenantID)
	keys = []string{
		tenantLiveStreamKey(tid, "main"),
		// Tenant-scoped dim queue — the production reader looks here
		// for the tenant view.
		tenantLiveStreamKey(tid, "dim:"+dimSuffix),
		// Tenant markers intentionally stay out of global dim queues.
		// The global activity key produces the single global marker used
		// by the super-admin view; mirroring every tenant marker there
		// would create one idle tile per tenant in the same global lane.
	}
	return keys
}

// idleMarkerRequestID returns the stable request_id for an idle marker tile.
// The same lane always produces the same request_id so each idle tick
// updates the same tile (ZADD member) instead of appending a new row.
func idleMarkerRequestID(tenantID, dimension, key string) string {
	scope := "global"
	if tenantID != "" {
		scope = "t-" + tenantID
	}
	safeKey := strings.NewReplacer(":", "_", "/", "_").Replace(key)
	return fmt.Sprintf("idle-%s-%s-%s", scope, dimension, safeKey)
}

func createIdleMarkerForDimension(dimension, key, tenantID string, ts time.Time) LiveRequest {
	requestID := idleMarkerRequestID(tenantID, dimension, key)

	errKind := idleMarkerErrorKind
	failStage := idleMarkerFailureStage
	// 2026-07-28 fix: Ts now reflects the scan time (ts) on every tick.
	// Previously it was anchored at lastActivity+threshold ("the moment
	// silence began"), which made the displayed idle duration grow
	// monotonically but caused the "update idle time" semantics to be a
	// no-op at the Redis level (ZADD same member + same score = nothing
	// happens, TTL is not refreshed). Using scan time for both Ts and
	// the ZSet score means:
	//   1. ZADD is a real write each tick → ZSet memory + key TTL refresh
	//   2. The displayed "空闲 X 分钟" oscillates between 0 and IdleTickInterval
	//      (default 5 min) — accurate "time since the most recent heartbeat"
	//   3. A new real request (score = now) has a higher score than the
	//      idle marker, so DESC ordering places the new request leftmost
	//      and pushes the idle tile right (the "正常记录把 idle 推到
	//      最左侧" requirement).
	marker := LiveRequest{
		Type:         "idle_marker",
		RequestID:    requestID,
		Ts:           ts.UTC().Format(time.RFC3339),
		TenantID:     tenantID,
		Status:       "idle",
		ErrorKind:    &errKind,
		FailureStage: &failStage,
	}

	// Each idle marker carries ONLY the identity of the lane it represents,
	// so it shows up in exactly one dimension view and never leaks into
	// others (e.g. a vendor-idle marker must not spawn a phantom lane in
	// the model view). The carried value doubles as the display label.
	switch dimension {
	case "credential":
		// 2026-08-27: key is the stable credential_id (e.g. "42").
		// CredentialLabel carries the raw key so liveStreamCredentialKey groups
		// the marker into the same lane as real requests; the parsed
		// CredentialID lets liveRequestTile expose the id so frontend legend
		// highlighting also matches idle tiles.
		marker.CredentialLabel = key
		if id, err := strconv.Atoi(key); err == nil {
			marker.CredentialID = id
		}
	case "vendor":
		// 2026-08-26: Vendor lane idle: carry ModelCategory so the marker
		// groups under the same vendor lane as real requests (legacy 兼容,
		// vendor 维度作为 dim 别名保留给 BuildLiveStreamSnapshot 输出 + tile 颜色)。
		marker.ModelCategory = key
	case "provider":
		// Provider lane idle: only ProviderCode is set.
		marker.ProviderCode = key
	case "model":
		// Model lane idle: set both Model (for display) and CanonicalName
		// (for aggregation, matching the logic in liveStreamDimensionKey).
		marker.Model = key
		marker.CanonicalName = key
	default:
		// Main-queue (global) idle: leave all dimension fields empty so it
		// does not appear in any per-dimension lane; it is still rendered
		// as a heartbeat in the flat request list.
	}

	return marker
}

// ComputeDelta compares old and new snapshots, returning only the
// lanes that changed plus the full summary (small). Dimensions with
// no lane changes are omitted from ChangedLanes to minimise payload.
func ComputeDelta(old, new *LiveStreamSnapshot) *LiveStreamDelta {
	if old == nil {
		return &LiveStreamDelta{
			Summary:          new.Summary,
			ChangedLanes:     new.Dimensions,
			DimensionLegends: new.DimensionLegends,
			StatusLegends:    new.StatusLegends,
		}
	}
	delta := &LiveStreamDelta{
		Summary:          new.Summary,
		ChangedLanes:     map[string][]LiveStreamLane{},
		DimensionLegends: map[string][]LiveStreamLegendItem{},
		StatusLegends:    new.StatusLegends,
	}
	for _, dim := range []string{"credential", "vendor", "provider", "model"} {
		oldLanes := old.Dimensions[dim]
		newLanes := new.Dimensions[dim]
		if lanesChanged(oldLanes, newLanes) {
			delta.ChangedLanes[dim] = newLanes
			delta.DimensionLegends[dim] = new.DimensionLegends[dim]
		}
	}
	return delta
}

// lanesChanged reports whether two ordered lane slices differ in any
// field that would invalidate the delta cache. The previous implementation
// compared LiveStreamTile values with `!=`, which compares *struct pointers*;
// liveRequestTile() returns a fresh struct on every snapshot, so the test
// was always true and every delta carried the whole lane array. The new
// check compares by request_id (the stable identity in the UI's
// TransitionGroup key) — a tile whose identity + status + ts match is
// considered unchanged even if the backend re-serialised it.
func lanesChanged(old, new []LiveStreamLane) bool {
	if len(old) != len(new) {
		return true
	}
	for i := range old {
		if old[i].ID != new[i].ID || old[i].IsOthers != new[i].IsOthers ||
			old[i].Stats != new[i].Stats || len(old[i].Requests) != len(new[i].Requests) {
			return true
		}
		for j := range old[i].Requests {
			if old[i].Requests[j].RequestID != new[i].Requests[j].RequestID {
				return true
			}
			if old[i].Requests[j].Status != new[i].Requests[j].Status {
				return true
			}
			// Use tolerance-based comparison instead of exact string match
			if !timestampsEqual(old[i].Requests[j].Timestamp, new[i].Requests[j].Timestamp) {
				return true
			}
		}
	}
	return false
}

// absTimeDiff returns the absolute duration between two timestamps.
func absTimeDiff(a, b time.Time) time.Duration {
	d := a.Sub(b)
	if d < 0 {
		return -d
	}
	return d
}

// timestampsEqual checks if two RFC3339 timestamps are equal within tolerance.
// Tolerance accounts for clock skew and serialization precision loss.
const timestampToleranceMs = 100

func timestampsEqual(ts1, ts2 string) bool {
	t1, err1 := time.Parse(time.RFC3339, ts1)
	t2, err2 := time.Parse(time.RFC3339, ts2)

	if err1 != nil || err2 != nil {
		// If either parse fails, fall back to string comparison
		return ts1 == ts2
	}

	return absTimeDiff(t1, t2) <= timestampToleranceMs*time.Millisecond
}
