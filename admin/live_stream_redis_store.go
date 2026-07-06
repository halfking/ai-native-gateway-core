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
	ModelCategory    string   `json:"model_category,omitempty"`
	ProviderCode     string   `json:"provider_code,omitempty"`
	Status           string   `json:"status,omitempty"`
	LatencyMs        *int     `json:"latency_ms,omitempty"`
	PromptTokens     *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens *int     `json:"completion_tokens,omitempty"`
	TotalTokens      *int     `json:"total_tokens,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
	ErrorKind        *string  `json:"error_kind,omitempty"`
}

const (
	liveStreamMainKey        = "llmgw:live:main"
	liveStreamDimPrefix      = "llmgw:live:dim:"
	liveStreamStatPrefix     = "llmgw:live:status:"
	liveStreamTenantSet      = "llmgw:live:tenants"
	liveStreamActivityPrefix = "llmgw:live:activity:"
	liveStreamTTL            = 28800 * time.Second // 8 hours
	liveStreamLaneLimit      = 30
	idleThresholdSeconds     = 60 // 1 minute
)

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
	var oldData string
	if v, err := s.rdb.Get(ctx, liveStreamRequestDetailKey(tenantID, req.RequestID)).Result(); err == nil {
		oldData = v
	} else if err != redis.Nil {
		return err
	}
	data, err := marshalLiveRequestRedisPayload(req)
	if err != nil {
		return fmt.Errorf("marshal live request failed: request_id=%s tenant_id=%s model=%s: %w", req.RequestID, tenantID, req.Model, err)
	}

	ts, err := time.Parse(time.RFC3339, req.Ts)
	if err != nil {
		ts = time.Now().UTC()
	}
	score := float64(ts.UnixMilli())

	pipe := s.rdb.Pipeline()
	if oldData != "" {
		var oldReq LiveRequest
		if oldReq, err = unmarshalLiveRequestRedisPayload(oldData); err == nil {
			removeLiveRequestFromQueues(ctx, pipe, normalizeLiveStreamTenant(oldReq.TenantID), oldReq)
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
	if req.Model != "" {
		pipe.Set(ctx, liveStreamActivityKey("", "model", req.Model), activityUnix, liveStreamTTL)
		pipe.Set(ctx, liveStreamActivityKey(tenantID, "model", req.Model), activityUnix, liveStreamTTL)
	}
	// Track main queue activity
	pipe.Set(ctx, liveStreamActivityKey("", "main", ""), activityUnix, liveStreamTTL)
	pipe.Set(ctx, liveStreamActivityKey(tenantID, "main", ""), activityUnix, liveStreamTTL)

	for _, key := range liveRequestQueueKeys(tenantID, req) {
		pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: req.RequestID})
		pipe.Expire(ctx, key, liveStreamTTL)
	}
	pipe.Set(ctx, liveStreamRequestDetailKey(tenantID, req.RequestID), data, liveStreamTTL)
	pipe.Set(ctx, liveStreamGlobalRequestDetailKey(req.RequestID), data, liveStreamTTL)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline exec failed: request_id=%s tenant_id=%s: %w", req.RequestID, tenantID, err)
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
	if req.ModelCategory != "" {
		keys = append(keys,
			liveStreamDimPrefix+"vendor:"+req.ModelCategory,
			tenantLiveStreamKey(tenantID, "dim:vendor:"+req.ModelCategory),
		)
	}
	if req.ProviderCode != "" {
		keys = append(keys,
			liveStreamDimPrefix+"provider:"+req.ProviderCode,
			tenantLiveStreamKey(tenantID, "dim:provider:"+req.ProviderCode),
		)
	}
	if req.Model != "" {
		keys = append(keys,
			liveStreamDimPrefix+"model:"+req.Model,
			tenantLiveStreamKey(tenantID, "dim:model:"+req.Model),
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
		limit = 50
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
		ModelCategory:    req.ModelCategory,
		ProviderCode:     req.ProviderCode,
		Status:           req.Status,
		LatencyMs:        req.LatencyMs,
		PromptTokens:     req.PromptTokens,
		CompletionTokens: req.CompletionTokens,
		TotalTokens:      req.TotalTokens,
		CostUSD:          req.CostUSD,
		ErrorKind:        req.ErrorKind,
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
		ModelCategory:    p.ModelCategory,
		ProviderCode:     p.ProviderCode,
		Status:           p.Status,
		LatencyMs:        p.LatencyMs,
		PromptTokens:     p.PromptTokens,
		CompletionTokens: p.CompletionTokens,
		TotalTokens:      p.TotalTokens,
		CostUSD:          p.CostUSD,
		ErrorKind:        p.ErrorKind,
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
		st := stats[key]
		countStatus(&st, req.Status)
		stats[key] = st
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
	lanes := make([]LiveStreamLane, 0, len(keys))
	legends := make([]LiveStreamLegendItem, 0, len(keys))

	for _, key := range keys {
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
	if req.Type == "idle_marker" {
		switch dimension {
		case "vendor":
			return "__idle__"
		case "provider":
			return "系统心跳"
		case "model":
			return "空闲"
		}
	}
	switch dimension {
	case "vendor":
		return emptyAs(req.ModelCategory, "__unknown__")
	case "provider":
		return emptyAs(req.ProviderCode, "unknown")
	case "model":
		return emptyAs(req.Model, "unknown")
	default:
		return "unknown"
	}
}

func liveRequestTile(req LiveRequest) LiveStreamTile {
	if req.Type == "idle_marker" {
		return LiveStreamTile{
			RequestID: req.RequestID,
			Timestamp: req.Ts,
			Model:     "空闲",
			Vendor:    "__idle__",
			Provider:  "系统心跳",
			Status:    "idle",
		}
	}
	return LiveStreamTile{
		RequestID:        req.RequestID,
		Timestamp:        req.Ts,
		Model:            emptyAs(req.Model, "unknown"),
		Vendor:           emptyAs(req.ModelCategory, "__unknown__"),
		Provider:         emptyAs(req.ProviderCode, "unknown"),
		Status:           emptyAs(req.Status, "in_progress"),
		ErrorKind:        req.ErrorKind,
		LatencyMs:        req.LatencyMs,
		CostUSD:          req.CostUSD,
		PromptTokens:     req.PromptTokens,
		CompletionTokens: req.CompletionTokens,
	}
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
	tenantID    string
	dimension   string
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
		info    activityKeyInfo
		key     string
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

// idleMarkerQueueKeys returns the Redis ZSET keys an idle marker for the
// given dimension should land in: the global lane and the tenant-scoped
// lane. Crucially it does NOT include the main queue or status queues,
// so a per-lane idle marker stays inside its own lane.
func idleMarkerQueueKeys(tenantID, dimension, dimensionKey string) []string {
	tenantID = normalizeLiveStreamTenant(tenantID)
	switch dimension {
	case "vendor":
		k := liveStreamDimPrefix + "vendor:" + dimensionKey
		return []string{k, tenantLiveStreamKey(tenantID, "dim:vendor:"+dimensionKey)}
	case "provider":
		k := liveStreamDimPrefix + "provider:" + dimensionKey
		return []string{k, tenantLiveStreamKey(tenantID, "dim:provider:"+dimensionKey)}
	case "model":
		k := liveStreamDimPrefix + "model:" + dimensionKey
		return []string{k, tenantLiveStreamKey(tenantID, "dim:model:"+dimensionKey)}
	case "main":
		// Main-queue idle markers DO belong on the main lane.
		return []string{liveStreamMainKey, tenantLiveStreamKey(tenantID, "main")}
	default:
		return nil
	}
}

func createIdleMarkerForDimension(dimension, key, tenantID string, ts time.Time) LiveRequest {
	// requestID must be unique per (scope, dimension, key, tick); include
	// the scope to prevent collision between global and tenant-scoped markers.
	scope := "global"
	if tenantID != "" {
		scope = "t-" + tenantID
	}
	requestID := fmt.Sprintf("idle-%s-%s-%d", scope, dimension, ts.UnixNano())

	marker := LiveRequest{
		Type:      "idle_marker",
		RequestID: requestID,
		Ts:        ts.UTC().Format(time.RFC3339),
		TenantID:  tenantID,
		Status:    "idle",
	}

	switch dimension {
	case "vendor":
		// Vendor lane idle: carry the vendor as ModelCategory so the
		// lane keeps showing its real identity.
		marker.ModelCategory = key
		marker.Model = "空闲"
		marker.ProviderCode = "系统心跳"
	case "provider":
		// Provider lane idle: carry the provider as ProviderCode.
		marker.ProviderCode = key
		marker.Model = "空闲"
		marker.ModelCategory = "__idle__"
	case "model":
		// Model lane idle: carry the model name.
		marker.Model = key
		marker.ModelCategory = "__idle__"
		marker.ProviderCode = "系统心跳"
	default:
		// Main queue idle: generic heartbeat.
		marker.Model = "空闲"
		marker.ModelCategory = "__idle__"
		marker.ProviderCode = "系统心跳"
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
