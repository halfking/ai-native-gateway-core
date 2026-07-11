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
// keeping the most recent 1 hour of requests in a sorted set so that
// clients can replay on reconnect/refresh without hitting the DB.
//
// Design:
//   - Main queue: ZSET llmgw:live:main (score = unix_ms, member = JSON)
//   - Dimension queues: ZSET llmgw:live:dim:{vendor|provider|model}:{key}
//   - Status queues: ZSET llmgw:live:status:{success|failure|in_progress}
//   - TTL: 1 hour (3600s)
//   - Idle markers: Type="idle_marker" entries inserted every 1 minute of silence
//
// Graceful degradation: all write errors are logged but not surfaced;
// the hub falls back to DB replay when Redis is unavailable.
type LiveStreamRedisStore struct {
	rdb *redis.Client
}

type LiveStreamStats struct {
	Total      int `json:"total"`
	Success    int `json:"success"`
	Failure    int `json:"failure"`
	InProgress int `json:"in_progress"`
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
}

const (
	liveStreamMainKey        = "llmgw:live:main"
	liveStreamDimPrefix      = "llmgw:live:dim:"
	liveStreamStatPrefix     = "llmgw:live:status:"
	liveStreamTenantSet      = "llmgw:live:tenants"
	liveStreamActivityPrefix = "llmgw:live:activity:"
	liveStreamTTL            = 28800 * time.Second // 8 hours
	liveStreamLaneLimit      = 30
	liveStreamReplayLimit    = 200 // 默认回放请求数：泳道中请求的有效期靠 TTL(8h)保证，数量上限放宽到 200，让请求“直到被挤出去”而非被过小的 replay 上限提前丢弃
	idleThresholdSeconds     = 60  // 1 minute
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
	if v, err := s.rdb.Get(ctx, liveStreamRequestDetailKey(tenantID, req.RequestID)).Result(); err == nil {
		oldData = v
	} else if err != redis.Nil {
		return fmt.Errorf("failed to fetch old request data: request_id=%s tenant_id=%s: %w", req.RequestID, tenantID, err)
	}
	data, err := marshalLiveRequestRedisPayload(req)
	if err != nil {
		return fmt.Errorf("marshal live request failed: request_id=%s tenant_id=%s model=%s provider=%s category=%s: %w", req.RequestID, tenantID, req.Model, req.ProviderCode, req.ModelCategory, err)
	}

	ts, err := time.Parse(time.RFC3339, req.Ts)
	if err != nil {
		slog.Warn("live stream record: invalid timestamp, using now", "request_id", req.RequestID, "ts", req.Ts, "err", err.Error())
		ts = time.Now().UTC()
	}
	score := float64(ts.UnixMilli())

	pipe := s.rdb.Pipeline()
	if oldData != "" {
		var oldReq LiveRequest
		if oldReq, err = unmarshalLiveRequestRedisPayload(oldData); err == nil {
			removeLiveRequestFromQueues(ctx, pipe, normalizeLiveStreamTenant(oldReq.TenantID), oldReq)
		} else {
			slog.Warn("live stream record: failed to unmarshal old request", "request_id", req.RequestID, "err", err.Error())
		}
	}

	pipe.SAdd(ctx, liveStreamTenantSet, tenantID)
	pipe.Expire(ctx, liveStreamTenantSet, liveStreamTTL)

	// Track last activity time for each dimension queue. Use the request
	// timestamp (not current time) so replayed historical data updates
	// activity correctly.
	activityUnix := ts.Unix()
	if req.ModelCategory != "" {
		pipe.Set(ctx, liveStreamActivityKey("", "vendor", req.ModelCategory), activityUnix, liveStreamTTL)
		pipe.Set(ctx, liveStreamActivityKey(tenantID, "vendor", req.ModelCategory), activityUnix, liveStreamTTL)
	}
	if req.ProviderCode != "" {
		pipe.Set(ctx, liveStreamActivityKey("", "provider", req.ProviderCode), activityUnix, liveStreamTTL)
		pipe.Set(ctx, liveStreamActivityKey(tenantID, "provider", req.ProviderCode), activityUnix, liveStreamTTL)
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
		pipe.Set(ctx, liveStreamActivityKey("", "model", modelActivityKey), activityUnix, liveStreamTTL)
		pipe.Set(ctx, liveStreamActivityKey(tenantID, "model", modelActivityKey), activityUnix, liveStreamTTL)
	}
	// Track main queue activity
	pipe.Set(ctx, liveStreamActivityKey("", "main", ""), activityUnix, liveStreamTTL)
	pipe.Set(ctx, liveStreamActivityKey(tenantID, "main", ""), activityUnix, liveStreamTTL)

	queueKeys := liveRequestQueueKeys(tenantID, req)
	slog.Debug("live stream record: adding to queues", "request_id", req.RequestID, "tenant_id", tenantID, "model", req.Model, "provider", req.ProviderCode, "category", req.ModelCategory, "queue_count", len(queueKeys))

	for _, key := range queueKeys {
		pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: req.RequestID})
		pipe.Expire(ctx, key, liveStreamTTL)
	}
	pipe.Set(ctx, liveStreamRequestDetailKey(tenantID, req.RequestID), data, liveStreamTTL)
	pipe.Set(ctx, liveStreamGlobalRequestDetailKey(req.RequestID), data, liveStreamTTL)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline exec failed: request_id=%s tenant_id=%s model=%s provider=%s category=%s queue_count=%d: %w", req.RequestID, tenantID, req.Model, req.ProviderCode, req.ModelCategory, len(queueKeys), err)
	}
	return nil
}

func removeLiveRequestFromQueues(ctx context.Context, pipe redis.Pipeliner, tenantID string, req LiveRequest) {
	for _, key := range liveRequestQueueKeys(tenantID, req) {
		pipe.ZRem(ctx, key, req.RequestID)
	}
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
	}, nil
}

func liveStreamGlobalRequestDetailKey(requestID string) string {
	return "llmgw:live:req:" + requestID
}

func liveStreamRequestDetailKey(tenantID, requestID string) string {
	return tenantLiveStreamKey(tenantID, "req:"+requestID)
}

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

	for _, item := range items {
		if item.Type == "idle_marker" {
			continue
		}
		countStatus(&s.Summary, item.Status)
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

	for _, req := range items {
		key := liveStreamDimensionKey(dimension, req)
		// Skip requests without valid dimension values (empty keys)
		if key == "" {
			continue
		}
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
	sort.Slice(keys, func(i, j int) bool {
		if stats[keys[i]].Total == stats[keys[j]].Total {
			return keys[i] < keys[j]
		}
		return stats[keys[i]].Total > stats[keys[j]].Total
	})

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
			Requests:  lastTiles(grouped[key], liveStreamLaneLimit),
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
	for _, req := range items {
		if req.Type == "idle_marker" {
			counts["idle"]++
			continue
		}
		counts[emptyAs(req.Status, "in_progress")]++
	}
	order := []string{"success", "in_progress", "failure", "idle"}
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
		// 返回 model name / "其他" 也算合法 key —— 不能在这里返回 ""，
		// 否则 resolveVendorForRequest 的最后兜底"用模型名称"会被丢弃，
		// 反而出现"原厂"泳道被过滤消失。
		key := resolveVendorForRequest(req)
		// idle marker 永远不进入 vendor / provider / model 维度
		if req.Type == "idle_marker" {
			return ""
		}
		return key
	case "provider":
		// 设计原则（2026-07-09 审计修正）：provider(供应商) 维度只认凭据反查的结果
		// （telemetry.CredentialID → credentials JOIN providers → display_name）。
		// 之前当 ProviderCode 为空时用 CanonicalName/Model 兜底，会把模型名
		// （如 "minimax-m3"）塞进 provider 维度，于是同一个供应商同时出现
		// "MiniMax"（凭据反查）和 "minimax-m3"（模型名兜底）两个泳道。
		//
		// 现在：ProviderCode 为空/未知时返回 ""，该请求不在 provider 维度显示，
		// 模型名永远只出现在 model 维度。ProviderCode 为空的两种真实场景：
		//   1) credential_id == 0：请求未到达凭据选择（auth/路由失败/探测记录）；
		//   2) 凭据已被删除，credential→provider JOIN 查不到。
		// 这两种情况下"供应商"本身就没有意义，不应凭空造一个模型名泳道。
		if req.Type == "idle_marker" {
			return ""
		}
		pc := strings.TrimSpace(req.ProviderCode)
		if pc == "" || pc == "unknown" || pc == "__unknown__" {
			return ""
		}
		return pc
	case "model":
		// Use CanonicalName for aggregation so the same model from different
		// credentials (with different outbound names) aggregates into one lane.
		// Fallback to Model for backward compatibility when CanonicalName is empty.
		//
		// Case-insensitive: canonical name and outbound model may differ in
		// casing across credentials ("MiniMax-M3" vs "minimax-m3"); we fold
		// to a lower-case trimmed form so both spellings fall into the
		// same lane. The original case is preserved in the rendered label
		// because lane display goes through the canonical name field, not
		// this dimension key.
		if req.Type == "idle_marker" {
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
	tile := LiveStreamTile{
		RequestID:        req.RequestID,
		Timestamp:        req.Ts,
		Model:            req.Model,
		Vendor:           resolveVendorForRequest(req),
		Provider:         req.ProviderCode,
		Status:           emptyAs(req.Status, "in_progress"),
		ErrorKind:        req.ErrorKind,
		LatencyMs:        req.LatencyMs,
		CostUSD:          req.CostUSD,
		PromptTokens:     req.PromptTokens,
		CompletionTokens: req.CompletionTokens,
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
	default:
		stats.InProgress++
	}
}

func lastTiles(items []LiveStreamTile, limit int) []LiveStreamTile {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
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
// markers for queues that have been idle for more than idleThresholdSeconds.
// Each idle marker is written ONLY to the queue it pertains to (plus its
// tenant-scoped twin), so an idle vendor marker never pollutes the model
// lane or the main queue.
func (s *LiveStreamRedisStore) ScanAndRecordIdleMarkers(ctx context.Context, ts time.Time) error {
	if s == nil || s.rdb == nil {
		return nil
	}

	nowUnix := ts.Unix()

	// 1) Collect candidate activity keys via SCAN.
	var activityKeys []string
	iter := s.rdb.Scan(ctx, 0, liveStreamActivityPrefix+"*", 0).Iterator()
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
		info activityKeyInfo
		key  string
	}
	var idle []pending
	refreshPipe := s.rdb.Pipeline()
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
		idle = append(idle, pending{info: info, key: k})
		// Refresh the activity timestamp so we do not re-emit on every tick.
		refreshPipe.Set(ctx, k, nowUnix, liveStreamTTL)
	}

	if len(idle) == 0 {
		// Still flush the refresh pipeline (no-op if empty).
		if _, err := refreshPipe.Exec(ctx); err != nil && err != redis.Nil {
			return fmt.Errorf("refresh activity timestamps failed: %w", err)
		}
		return nil
	}

	// 3) Build + persist idle markers, writing only to the relevant lane(s).
	writePipe := s.rdb.Pipeline()
	for _, p := range idle {
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
			writePipe.Expire(ctx, qkey, liveStreamTTL)
		}
	}

	if _, err := writePipe.Exec(ctx); err != nil && err != redis.Nil {
		return fmt.Errorf("write idle markers failed: %w", err)
	}
	if _, err := refreshPipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.Debug("refresh activity timestamps failed (non-fatal)", "err", err.Error())
	}
	return nil
}

// idleMarkerQueueKeys returns the Redis ZSET keys an idle marker should
// land in. Because Replay()/Snapshot() only ever read the main queue and
// rebuild lanes in memory from it, an idle marker MUST be written to the
// main queue (plus its tenant-scoped twin) to be visible at all. The old
// implementation wrote only to per-dimension ZSETs that nothing reads,
// making every idle marker invisible dead data.
func idleMarkerQueueKeys(tenantID, dimension, dimensionKey string) []string {
	tenantID = normalizeLiveStreamTenant(tenantID)
	// All idle markers go to main so they are replayed and rendered inside
	// the lane that matches their carried dimension value.
	return []string{liveStreamMainKey, tenantLiveStreamKey(tenantID, "main")}
}

func createIdleMarkerForDimension(dimension, key, tenantID string, ts time.Time) LiveRequest {
	// requestID must be unique per (scope, dimension, key, tick); include
	// the scope to prevent collision between global and tenant-scoped markers.
	scope := "global"
	if tenantID != "" {
		scope = "t-" + tenantID
	}
	requestID := fmt.Sprintf("idle-%s-%s-%s-%d", scope, dimension, key, ts.UnixNano())

	marker := LiveRequest{
		Type:      "idle_marker",
		RequestID: requestID,
		Ts:        ts.UTC().Format(time.RFC3339),
		TenantID:  tenantID,
		Status:    "idle",
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
			if old[i].Requests[j] != new[i].Requests[j] {
				return true
			}
		}
	}
	return false
}
