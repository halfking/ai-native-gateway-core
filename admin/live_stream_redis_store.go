package admin

import (
	"context"
	"encoding/json"
	"fmt"
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
	liveStreamMainKey    = "llmgw:live:main"
	liveStreamDimPrefix  = "llmgw:live:dim:"
	liveStreamStatPrefix = "llmgw:live:status:"
	liveStreamTenantSet  = "llmgw:live:tenants"
	liveStreamTTL        = 3600 * time.Second // 1 hour
	liveStreamTopN       = 5
	liveStreamLaneLimit  = 30
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
		return fmt.Errorf("marshal request: %w", err)
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
	for _, key := range liveRequestQueueKeys(tenantID, req) {
		pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: req.RequestID})
		pipe.Expire(ctx, key, liveStreamTTL)
	}
	pipe.Set(ctx, liveStreamRequestDetailKey(tenantID, req.RequestID), data, liveStreamTTL)
	pipe.Set(ctx, liveStreamGlobalRequestDetailKey(req.RequestID), data, liveStreamTTL)

	_, err = pipe.Exec(ctx)
	return err
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

// RecordIdleMarker inserts a 1-minute silence marker into the main queue.
func (s *LiveStreamRedisStore) RecordIdleMarker(ctx context.Context, ts time.Time) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	marker := LiveRequest{
		Type:          "idle_marker",
		RequestID:     fmt.Sprintf("idle-%d", ts.UnixMilli()),
		Ts:            ts.UTC().Format(time.RFC3339),
		Model:         "空闲",
		ModelCategory: "__idle__",
		ProviderCode:  "系统心跳",
		Status:        "idle",
	}
	data, err := marshalLiveRequestRedisPayload(marker)
	if err != nil {
		return err
	}
	score := float64(ts.UnixMilli())
	tenants, err := s.rdb.SMembers(ctx, liveStreamTenantSet).Result()
	if err != nil {
		tenants = nil
	}
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, liveStreamGlobalRequestDetailKey(marker.RequestID), data, liveStreamTTL)
	writeIdleMarkerToPipe(ctx, pipe, marker.RequestID, liveStreamMainKey, liveStreamStatPrefix+"idle", liveStreamDimPrefix+"vendor:__idle__", liveStreamDimPrefix+"provider:系统心跳", liveStreamDimPrefix+"model:空闲", score)
	for _, tenantID := range tenants {
		tenantMarker := marker
		tenantMarker.TenantID = normalizeLiveStreamTenant(tenantID)
		tenantData, mErr := marshalLiveRequestRedisPayload(tenantMarker)
		if mErr != nil {
			continue
		}
		pipe.Set(ctx, liveStreamRequestDetailKey(tenantID, tenantMarker.RequestID), tenantData, liveStreamTTL)
		writeIdleMarkerToPipe(ctx, pipe,
			tenantMarker.RequestID,
			tenantLiveStreamKey(tenantID, "main"),
			tenantLiveStreamKey(tenantID, "status:idle"),
			tenantLiveStreamKey(tenantID, "dim:vendor:__idle__"),
			tenantLiveStreamKey(tenantID, "dim:provider:系统心跳"),
			tenantLiveStreamKey(tenantID, "dim:model:空闲"),
			score,
		)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func writeIdleMarkerToPipe(ctx context.Context, pipe redis.Pipeliner, requestID, mainKey, statusKey, vendorKey, providerKey, modelKey string, score float64) {
	for _, key := range []string{mainKey, statusKey, vendorKey, providerKey, modelKey} {
		pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: requestID})
		pipe.Expire(ctx, key, liveStreamTTL)
	}
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
		return nil, err
	}

	out := make([]LiveRequest, 0, len(requestIDs))
	for i := len(requestIDs) - 1; i >= 0; i-- {
		detailKey := liveStreamGlobalRequestDetailKey(requestIDs[i])
		if !isSuper && tenantID != "" {
			detailKey = liveStreamRequestDetailKey(normalizeLiveStreamTenant(tenantID), requestIDs[i])
		}
		data, err := s.rdb.Get(ctx, detailKey).Result()
		if err != nil {
			continue
		}
		req, err := unmarshalLiveRequestRedisPayload(data)
		if err != nil {
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

	detailLanes := make([]LiveStreamLane, 0, len(keys))
	for _, key := range keys {
		detailLanes = append(detailLanes, LiveStreamLane{
			ID:        key,
			Name:      key,
			Dimension: dimension,
			Requests:  lastTiles(grouped[key], liveStreamLaneLimit),
			Stats:     stats[key],
			IsOthers:  false,
		})
	}

	viewLanes := make([]LiveStreamLane, 0, liveStreamTopN+1)
	legends := make([]LiveStreamLegendItem, 0, liveStreamTopN+1)
	top := keys
	if len(top) > liveStreamTopN {
		top = top[:liveStreamTopN]
	}
	topSet := map[string]struct{}{}
	for _, key := range top {
		topSet[key] = struct{}{}
		viewLanes = append(viewLanes, LiveStreamLane{
			ID:        key,
			Name:      key,
			Dimension: dimension,
			Requests:  lastTiles(grouped[key], liveStreamLaneLimit),
			Stats:     stats[key],
			IsOthers:  false,
		})
		legends = append(legends, LiveStreamLegendItem{Key: key, Name: key, Count: stats[key].Total})
	}

	othersStats := LiveStreamStats{}
	othersReqs := make([]LiveStreamTile, 0)
	for _, key := range keys {
		if _, ok := topSet[key]; ok {
			continue
		}
		mergeStats(&othersStats, stats[key])
		othersReqs = append(othersReqs, grouped[key]...)
	}
	if othersStats.Total > 0 {
		viewLanes = append(viewLanes, LiveStreamLane{
			ID:        "__others__",
			Name:      "其它",
			Dimension: dimension,
			Requests:  lastTiles(othersReqs, liveStreamLaneLimit),
			Stats:     othersStats,
			IsOthers:  true,
		})
		legends = append(legends, LiveStreamLegendItem{Key: "__others__", Name: "其它", Count: othersStats.Total})
	}

	return viewLanes, detailLanes, legends
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

func mergeStats(dst *LiveStreamStats, src LiveStreamStats) {
	dst.Total += src.Total
	dst.Success += src.Success
	dst.Failure += src.Failure
	dst.InProgress += src.InProgress
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
