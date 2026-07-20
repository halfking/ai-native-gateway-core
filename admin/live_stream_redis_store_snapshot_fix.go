package admin

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/redis/go-redis/v9"
)

// SnapshotFromDimensionQueues builds a snapshot by reading directly from
// dimension queues rather than reading from the main queue and grouping.
// This fixes the swim lane flickering issue where high-traffic vendors
// would crowd out low-traffic vendors in the main queue's 200-request window.
//
// Design:
//   - Scan all dimension queue keys: llmgw:live:dim:{vendor|provider|model}:*
//   - Read the last 20 requests from each dimension queue
//   - Merge and deduplicate across dimensions
//   - Build snapshot from the merged set
//
// This ensures each vendor/provider/model gets its full 20-tile allocation
// regardless of traffic distribution in the main queue.
func (s *LiveStreamRedisStore) SnapshotFromDimensionQueues(ctx context.Context, tenantID string, isSuper bool) (*LiveStreamSnapshot, error) {
	if s == nil || s.rdb == nil {
		return nil, nil
	}

	tenantID = normalizeLiveStreamTenant(tenantID)

	// Step 1: Discover all dimension queue keys
	dimKeys, err := s.discoverDimensionQueues(ctx, tenantID, isSuper)
	if err != nil {
		return nil, fmt.Errorf("discover dimension queues failed: %w", err)
	}

	if len(dimKeys) == 0 {
		// No dimension queues exist yet, fall back to main queue
		return s.Snapshot(ctx, tenantID, isSuper, liveStreamReplayLimit)
	}

	// Step 2: Read last N requests from each dimension queue.
	//
	// 2026-07-20: Do NOT dedupe across dimKeys here. The same request_id
	// intentionally appears in three Redis sorted-sets (vendor / provider /
	// model). When we dedup at this stage, whichever dimKey redis SCAN returns
	// first "claims" the request and later dimKeys silently lose it. Because
	// SCAN order is not stable across calls, provider/model lanes end up with
	// different request sets on consecutive snapshots even when Redis data did
	// not materially change, which is the root cause of the swim-lane flicker.
	//
	// We now append every dimKey member verbatim and defer dedupe to the
	// consumers:
	//   - BuildLiveStreamSnapshot / buildStatusLegends dedupe Summary counts
	//     and legend counts by request_id.
	//   - buildLiveStreamLanes dedupes per (dimension,lane,request_id) so a
	//     request still appears only once inside a given lane.
	var allRequests []LiveRequest

	for _, key := range dimKeys {
		requestIDs, err := s.rdb.ZRevRange(ctx, key, 0, int64(LiveStreamLaneVisibleLimit-1)).Result()
		if err != nil {
			slog.Debug("snapshot: failed to read dimension queue", "key", key, "err", err.Error())
			continue
		}

		for _, requestID := range requestIDs {
			// Load request detail
			detailKey := liveStreamGlobalRequestDetailKey(requestID)
			if !isSuper && tenantID != "" {
				detailKey = liveStreamRequestDetailKey(tenantID, requestID)
			}

			data, err := s.rdb.Get(ctx, detailKey).Result()
			if err == redis.Nil {
				continue // request detail expired
			}
			if err != nil {
				slog.Debug("snapshot: failed to load request detail", "request_id", requestID, "err", err.Error())
				continue
			}

			req, err := unmarshalLiveRequestRedisPayload(data)
			if err != nil {
				slog.Debug("snapshot: failed to unmarshal request", "request_id", requestID, "err", err.Error())
				continue
			}

			// Tenant filtering
			if !isSuper && tenantID != "" && req.TenantID != tenantID {
				continue
			}

			allRequests = append(allRequests, req)
		}
	}

	// 2026-07-20: Sort ASC by timestamp so grouped[key] inside
	// buildLiveStreamLanes is also ASC (oldest first, newest at the
	// tail). lastTiles() then picks the trailing window = "newest N"
	// consistently across snapshots.
	sort.SliceStable(allRequests, func(i, j int) bool {
		// Ts is RFC3339 — lexicographic compare matches chronological order,
		// no need to parse to time.Time (which would also be ~10× slower).
		if allRequests[i].Ts != allRequests[j].Ts {
			return allRequests[i].Ts < allRequests[j].Ts
		}
		return allRequests[i].RequestID < allRequests[j].RequestID
	})

	if len(allRequests) == 0 {
		// No valid requests found, return empty snapshot
		return &LiveStreamSnapshot{
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
		}, nil
	}

	// Step 3: Build snapshot from merged requests
	return BuildLiveStreamSnapshot(allRequests), nil
}

// discoverDimensionQueues scans Redis for all dimension queue keys.
// Returns the list of keys to read from.
func (s *LiveStreamRedisStore) discoverDimensionQueues(ctx context.Context, tenantID string, isSuper bool) ([]string, error) {
	var patterns []string

	if isSuper {
		// Super admin sees global dimension queues
		patterns = []string{
			liveStreamDimPrefix + "vendor:*",
			liveStreamDimPrefix + "provider:*",
			liveStreamDimPrefix + "model:*",
		}
	} else {
		// Tenant admin sees tenant-scoped queues
		tenantPrefix := "llmgw:live:tenant:" + tenantID + ":dim:"
		patterns = []string{
			tenantPrefix + "vendor:*",
			tenantPrefix + "provider:*",
			tenantPrefix + "model:*",
		}
	}

	var allKeys []string
	for _, pattern := range patterns {
		keys, err := s.scanKeysWithPattern(ctx, pattern)
		if err != nil {
			return nil, fmt.Errorf("scan keys pattern=%s: %w", pattern, err)
		}
		allKeys = append(allKeys, keys...)
	}

	return allKeys, nil
}

// scanKeysWithPattern uses SCAN to find keys matching a pattern.
// Safer than KEYS * in production.
func (s *LiveStreamRedisStore) scanKeysWithPattern(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	var cursor uint64
	const maxIterations = 100 // safety limit

	for i := 0; i < maxIterations; i++ {
		var scanKeys []string
		var err error
		scanKeys, cursor, err = s.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, scanKeys...)
		if cursor == 0 {
			break
		}
	}

	// Filter out activity tracking keys (not queue keys)
	var queueKeys []string
	for _, key := range keys {
		if !strings.Contains(key, ":activity:") {
			queueKeys = append(queueKeys, key)
		}
	}

	return queueKeys, nil
}
