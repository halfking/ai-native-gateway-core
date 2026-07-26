package admin

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/redis/go-redis/v9"
)

func requestIDFromDimensionQueueMember(member string) string {
	if member == "" {
		return ""
	}
	if strings.HasPrefix(member, "{") {
		if tile, err := unmarshalTileSlim(member); err == nil && tile.RequestID != "" {
			return tile.RequestID
		}
	}
	return member
}

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
		// 2026-07-26: dimension queues store slim tile JSON members (see
		// Record()), while main queues still store bare request ids. Decode
		// each member before building the request-detail key.
		members, err := s.rdb.ZRevRange(ctx, key, 0, int64(LiveStreamLaneVisibleLimit-1)).Result()
		if err != nil {
			slog.Debug("snapshot: failed to read dimension queue", "key", key, "err", err.Error())
			continue
		}

		for _, member := range members {
			requestID := requestIDFromDimensionQueueMember(member)
			if requestID == "" {
				continue
			}

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

	// 2026-07-21: Log snapshot details for debugging
	slog.Info("snapshot from dimension queues built",
		"tenant_id", tenantID,
		"is_super", isSuper,
		"total_requests", len(allRequests),
		"dimension_keys_scanned", len(dimKeys),
		"first_request_ts", func() string {
			if len(allRequests) > 0 {
				return allRequests[0].Ts
			}
			return ""
		}(),
		"last_request_ts", func() string {
			if len(allRequests) > 0 {
				return allRequests[len(allRequests)-1].Ts
			}
			return ""
		}(),
	)

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
// Returns the list of keys to read from, sorted by most recent activity
// to ensure stable snapshot windows across calls.
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

	// 2026-07-20: Sort keys by last activity timestamp to stabilize snapshot
	// windows. Redis SCAN order is non-deterministic, causing the same request
	// to appear/disappear across consecutive snapshots when different dimension
	// queues are scanned in different orders (the "swim-lane rolling" issue
	// reported on 245). Sorting by recency ensures we consistently prioritize
	// active lanes, and the deduplication logic produces stable results.
	allKeys, err := s.sortKeysByActivity(ctx, allKeys)
	if err != nil {
		// Fallback to alphabetical sort if activity lookup fails
		slog.Debug("failed to sort by activity, using lexicographic order", "err", err.Error())
		sort.Strings(allKeys)
	}

	// 2026-07-21: Log dimension queue discovery details
	slog.Info("dimension queues discovered",
		"tenant_id", tenantID,
		"is_super", isSuper,
		"total_keys", len(allKeys),
		"first_3_keys", func() string {
			if len(allKeys) > 3 {
				return strings.Join(allKeys[:3], ", ")
			}
			return strings.Join(allKeys, ", ")
		}(),
	)

	return allKeys, nil
}

// scanKeysWithPattern uses SCAN to find keys matching a pattern.
// Safer than KEYS * in production.
//
// 2026-07-23: SCAN COUNT 必须用较大值（默认 1000），
// 否则 Redis 在大 keyspace（30万+ keys）下跳跃扫描几乎找不到匹配项。
// 实测：DBSIZE=347850，COUNT=100 时 101 次迭代中 100 次返回空，
// 仅找到 1 个 key；COUNT=10000 时 1-2 次迭代就能找到 27 个 key。
func (s *LiveStreamRedisStore) scanKeysWithPattern(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	var cursor uint64
	const maxIterations = 1000 // 允许足够多的迭代

	for i := 0; i < maxIterations; i++ {
		var scanKeys []string
		var err error
		// COUNT=10000：避免大 keyspace 下跳跃扫描漏掉 keys
		scanKeys, cursor, err = s.rdb.Scan(ctx, cursor, pattern, 10000).Result()
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

// sortKeysByActivity sorts dimension queue keys by their most recent request
// timestamp (descending), ensuring stable snapshot windows. Keys with no
// activity fall back to alphabetical order at the end.
func (s *LiveStreamRedisStore) sortKeysByActivity(ctx context.Context, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return keys, nil
	}

	type keyWithScore struct {
		key   string
		score float64 // timestamp of most recent request, or 0 if empty
	}

	keysWithScores := make([]keyWithScore, 0, len(keys))

	// Batch read last activity timestamp from each queue
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.ZSliceCmd, len(keys))
	for i, key := range keys {
		// Get the most recent entry (highest score = latest timestamp)
		cmds[i] = pipe.ZRevRangeWithScores(ctx, key, 0, 0)
	}

	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return keys, fmt.Errorf("pipeline exec failed: %w", err)
	}

	// Collect scores
	for i, cmd := range cmds {
		result, err := cmd.Result()
		if err == redis.Nil || len(result) == 0 {
			// Empty queue, use 0 as score (will sort to end)
			keysWithScores = append(keysWithScores, keyWithScore{key: keys[i], score: 0})
		} else if err != nil {
			// Error reading this key, use 0
			keysWithScores = append(keysWithScores, keyWithScore{key: keys[i], score: 0})
		} else {
			// Use the timestamp of the most recent entry
			keysWithScores = append(keysWithScores, keyWithScore{key: keys[i], score: result[0].Score})
		}
	}

	// Sort: highest score first (most recent activity), then alphabetically
	sort.SliceStable(keysWithScores, func(i, j int) bool {
		if keysWithScores[i].score != keysWithScores[j].score {
			return keysWithScores[i].score > keysWithScores[j].score // descending
		}
		return keysWithScores[i].key < keysWithScores[j].key // alphabetical tie-breaker
	})

	// Extract sorted keys
	sortedKeys := make([]string, len(keysWithScores))
	for i, kws := range keysWithScores {
		sortedKeys[i] = kws.key
	}

	return sortedKeys, nil
}
