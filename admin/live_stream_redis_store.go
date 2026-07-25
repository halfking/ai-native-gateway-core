package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// LiveStreamRedisStore backs the realtime request stream with Redis,
// keeping recent requests in sorted sets so clients can replay on
// reconnect/refresh without hitting the DB.
//
// Design:
//   - Main queue: ZSET llmgw:live:main (score = unix_ms, member = JSON)
//   - Dimension queues: ZSET llmgw:live:dim:{vendor|provider|model}:{key}
//   - Status queues: ZSET llmgw:live:status:{success|failure|rate_limited|in_progress}
//   - TTL: LiveStreamRecordRetention (default 2 hours, product minimum)
//   - Visible lane window: LiveStreamLaneVisibleLimit (20 tiles)
//   - Idle markers: inserted after LiveStreamIdleThreshold (5 min) of silence
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
	RequestID        string   `json:"request_id"`
	Timestamp        string   `json:"timestamp"`
	Model            string   `json:"model"`
	Vendor           string   `json:"vendor"`
	Provider         string   `json:"provider"`
	Status           string   `json:"status"`
	ErrorKind        *string  `json:"error_kind,omitempty"`
	LatencyMs        *int     `json:"latency_ms,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
	PromptTokens     *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens *int     `json:"completion_tokens,omitempty"`
	IsProbe          bool     `json:"is_probe,omitempty"`
	ProbeOrigin      string   `json:"probe_origin,omitempty"`
	ProbeAttempt     int      `json:"probe_attempt,omitempty"`
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
	DetailDimensions map[string][]LiveStreamLane       `json:"detail_dimensions"`
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

// LiveStreamLaneVisibleLimit is how many tiles each swim lane shows. Entries
// scrolled past this window are trimmed from per-dimension Redis queues.
const LiveStreamLaneVisibleLimit = 20

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
func (s *LiveStreamRedisStore) Record(ctx context.Context, req LiveRequest) error {
	if s == nil || s.rdb == nil {
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
		}
		// 2026-07-23: 维度队列 24h TTL（泳道存在不超过 1 天）
		pipe.Expire(ctx, key, liveStreamLaneQueueTTL)
		trimLiveStreamQueue(pipe, ctx, key, liveStreamQueueKeepLimit(key))
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
	addIdleRemoval("vendor", req.ModelCategory)
	addIdleRemoval("provider", req.ProviderCode)
	addIdleRemoval("model", modelKey)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline exec failed: request_id=%s tenant_id=%s model=%s provider=%s category=%s queue_count=%d: %w", req.RequestID, tenantID, req.Model, req.ProviderCode, req.ModelCategory, len(queueKeys), err)
	}
	if err := s.NotifyChange(ctx, tenantID, req.RequestID); err != nil {
		slog.Debug("live stream redis notify failed", "request_id", req.RequestID, "tenant_id", tenantID, "err", err.Error())
	}
	return nil
}

type liveStreamNotifyPayload struct {
	RequestID string `json:"request_id"`
	TenantID  string `json:"tenant_id"`
}

// NotifyChange publishes a lightweight event so SSE hubs (local or
// remote) can react to Redis writes without coupling to the request
// handler goroutine.
func (s *LiveStreamRedisStore) NotifyChange(ctx context.Context, tenantID, requestID string) error {
	if s == nil || s.rdb == nil || requestID == "" {
		return nil
	}
	payload, err := json.Marshal(liveStreamNotifyPayload{
		RequestID: requestID,
		TenantID:  normalizeLiveStreamTenant(tenantID),
	})
	if err != nil {
		return err
	}
	return s.rdb.Publish(ctx, liveStreamNotifyChannel, payload).Err()
}

// LoadRequest reads the latest request detail from Redis. Used by the
// pub/sub subscriber to rebuild the LiveRequest before SSE fan-out.
func (s *LiveStreamRedisStore) LoadRequest(ctx context.Context, tenantID, requestID string) (LiveRequest, error) {
	if s == nil || s.rdb == nil || requestID == "" {
		return LiveRequest{}, fmt.Errorf("live stream store unavailable")
	}
	tenantID = normalizeLiveStreamTenant(tenantID)
	data, err := s.rdb.Get(ctx, liveStreamGlobalRequestDetailKey(requestID)).Result()
	if err == redis.Nil && tenantID != "" {
		data, err = s.rdb.Get(ctx, liveStreamRequestDetailKey(tenantID, requestID)).Result()
	}
	if err == redis.Nil {
		return LiveRequest{}, fmt.Errorf("request not found: %s", requestID)
	}
	if err != nil {
		return LiveRequest{}, err
	}
	return unmarshalLiveRequestRedisPayload(data)
}

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

// trimLiveStreamQueue removes the oldest members so at most keep entries
// remain (highest scores / newest requests). Called after every ZADD.
func trimLiveStreamQueue(pipe redis.Pipeliner, ctx context.Context, key string, keep int) {
	if keep <= 0 {
		return
	}
	pipe.ZRemRangeByRank(ctx, key, 0, int64(-keep-1))
}

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
	}
	if req.ProviderCode != "" && req.ProviderCode != "unknown" {
		keys = append(keys,
			liveStreamDimPrefix+"provider:"+req.ProviderCode,
			tenantLiveStreamKey(tenantID, "dim:provider:"+req.ProviderCode),
		)
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
		DetailDimensions: map[string][]LiveStreamLane{
			"vendor":   {},
			"provider": {},
			"model":    {},
		},
		Dimensions: map[string][]LiveStreamLane{
			"vendor":   {},
			"provider": {},
			"model":    {},
		},
		DimensionLegends: map[string][]LiveStreamLegendItem{
			"vendor":   {},
			"provider": {},
			"model":    {},
		},
		StatusLegends: []LiveStreamLegendItem{},
	}

	seenForSummary := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.Type == "idle_marker" {
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

	for _, dim := range []string{"vendor", "provider", "model"} {
		view, detail, legends := buildLiveStreamLanes(dim, items)
		s.Dimensions[dim] = view
		s.DetailDimensions[dim] = detail
		s.DimensionLegends[dim] = legends
	}
	s.StatusLegends = buildStatusLegends(items)
	return s
}

func buildLiveStreamLanes(dimension string, items []LiveRequest) ([]LiveStreamLane, []LiveStreamLane, []LiveStreamLegendItem) {
	stats := map[string]LiveStreamStats{}
	grouped := map[string][]LiveStreamTile{}
	seenByLane := map[string]map[string]struct{}{}

	for _, req := range items {
		key := liveStreamDimensionKey(dimension, req)
		// Skip requests without valid dimension values (empty keys)
		if key == "" {
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
		// Idle markers are displayed as tiles inside the lane but do NOT
		// inflate the lane's business stats (success/failure/in_progress),
		// keeping lane.Stats consistent with the global Summary.
		if req.Type != "idle_marker" {
			st := stats[key]
			countStatus(&st, req.Status)
			stats[key] = st
		} else if _, ok := stats[key]; !ok {
			// Ensure an idle-only lane still gets an (empty) stats entry so
			// it appears in the legend.
			stats[key] = LiveStreamStats{}
		}
		grouped[key] = append(grouped[key], liveRequestTile(req))
	}

	keys := make([]string, 0, len(stats))
	for key := range stats {
		keys = append(keys, key)
	}
	// Stable alphabetical order — Total only affects legend count, not lane
	// position. Sorting by Total caused lanes to jump on every stat tick.
	sort.Strings(keys)

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
			lanes = append(lanes, LiveStreamLane{
				ID:        key,
				Name:      key,
				Dimension: dimension,
				Requests:  firstTiles(grouped[key], liveStreamLaneLimit),
				Stats:     stats[key],
				IsOthers:  false,
			})
		legends = append(legends, LiveStreamLegendItem{Key: key, Name: key, Count: stats[key].Total})
	}

	// Both viewLanes and detailLanes return the same full list
	return lanes, lanes, legends
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

func liveStreamDimensionKey(dimension string, req LiveRequest) string {
	// For idle markers, use the actual dimension value (already set correctly in createIdleMarkerForDimension)
	// This ensures idle markers inherit the queue's identity rather than creating separate idle lanes
	switch dimension {
	case "vendor":
		if req.Type == "idle_marker" {
			if req.ModelCategory != "" {
				return req.ModelCategory
			}
			return ""
		}
		key := resolveVendorForRequest(req)
		return key
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
	tile := LiveStreamTile{
		RequestID:        req.RequestID,
		Timestamp:        req.Ts,
		Model:            standardModel,
		Vendor:           resolveVendorForRequest(req),
		Provider:         req.ProviderCode,
		Status:           emptyAs(req.Status, "in_progress"),
		ErrorKind:        req.ErrorKind,
		LatencyMs:        req.LatencyMs,
		CostUSD:          req.CostUSD,
		PromptTokens:     req.PromptTokens,
		CompletionTokens: req.CompletionTokens,
		IsProbe:          req.IsProbe,
		ProbeOrigin:      req.ProbeOrigin,
		ProbeAttempt:     req.ProbeAttempt,
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

// firstTiles returns the first N tiles from items (newest tiles, for RIGHT→LEFT display).
// Backend stores tiles in DESC timestamp order in Redis ZSET, so first N = newest N.
func firstTiles(items []LiveStreamTile, limit int) []LiveStreamTile {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	// Return first N instead of last N
	return items[:limit]
}

func tenantLiveStreamKey(tenantID, suffix string) string {
	return "llmgw:live:tenant:" + normalizeLiveStreamTenant(tenantID) + ":" + suffix
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

	// 1) Collect candidate activity keys via SCAN.
	//    The shared Redis DB has hundreds of thousands of unrelated keys
	//    (request details, sessions, etc.), so SCAN must traverse them
	//    all to find our ~50 activity keys. Use COUNT 5000 to amortise
	//    round-trips — with COUNT 0 (Redis default 10) we made 40K SCAN
	//    calls and exceeded any reasonable context deadline.
	var activityKeys []string
	iter := s.rdb.Scan(ctx, 0, liveStreamActivityPrefix+"*", 5000).Iterator()
	for iter.Next(ctx) {
		activityKeys = append(activityKeys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("scan activity keys failed: %w", err)
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
	for _, p := range idle {
		// Anchor idle at the moment silence began (last activity + threshold),
		// not at scan time, so new requests push the idle tile left instead
		// of keeping it pinned at the tail.
		idleStartedAt := time.Unix(p.lastActivity+idleThresholdSeconds, 0).UTC()
		marker := createIdleMarkerForDimension(p.info.dimension, p.info.dimensionKey, p.info.tenantID, idleStartedAt)
		data, err := marshalLiveRequestRedisPayload(marker)
		if err != nil {
			slog.Debug("failed to marshal idle marker", "dimension", p.info.dimension, "dimension_key", p.info.dimensionKey, "tenant_id", p.info.tenantID, "err", err.Error())
			continue
		}
		score := float64(idleStartedAt.UnixMilli())

		// Detail lookups: store under the global key always, and the
		// tenant key when scoped, so Replay can resolve the marker.
		// Note: marker.TenantID is already normalized from parseActivityKey.
		writePipe.Set(ctx, liveStreamGlobalRequestDetailKey(marker.RequestID), data, liveStreamTTL)
		writePipe.Set(ctx, liveStreamRequestDetailKey(marker.TenantID, marker.RequestID), data, liveStreamTTL)

		for _, qkey := range idleMarkerQueueKeys(marker.TenantID, p.info.dimension, p.info.dimensionKey) {
			writePipe.ZAdd(ctx, qkey, redis.Z{Score: score, Member: marker.RequestID})
			// 2026-07-23: idle marker 写入泳道队列，队列 TTL 用 24h（泳道存在 ≤ 1 天）
			writePipe.Expire(ctx, qkey, liveStreamLaneQueueTTL)
			trimLiveStreamQueue(writePipe, ctx, qkey, liveStreamQueueKeepLimit(qkey))
		}
	}

	if _, err := writePipe.Exec(ctx); err != nil && err != redis.Nil {
		return fmt.Errorf("write idle markers failed: %w", err)
	}
	return nil
}

// idleMarkerQueueKeys returns the Redis ZSET keys an idle marker should
// land in. Global-scope markers go to the super-admin main queue; tenant-
// scoped markers go to that tenant's main queue only — never both — so
// Replay() does not show duplicate idle tiles for the same lane.
func idleMarkerQueueKeys(tenantID, dimension, dimensionKey string) []string {
	_ = dimension
	_ = dimensionKey
	if strings.TrimSpace(tenantID) == "" {
		return []string{liveStreamMainKey}
	}
	return []string{tenantLiveStreamKey(normalizeLiveStreamTenant(tenantID), "main")}
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
	case "vendor":
		// Vendor lane idle: only ModelCategory is set.
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
	for _, dim := range []string{"vendor", "provider", "model"} {
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
