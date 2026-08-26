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
	//
	// 2026-08-04 (方案B): Pipeline the reads. The previous implementation
	// issued one ZRevRange per dimKey (42 round-trips) then one GET per member
	// (~840 round-trips), all serial. Against a shared Redis on a remote host
	// this blew past the 2s context deadline ~40% of the time (production
	// logs), and on timeout the remaining dimKeys were skipped → a "degraded"
	// snapshot whose total dropped from ~258 to single digits → swim lanes
	// jumped 20↔3. Two pipelines collapse the ~882 round-trips into 2, leaving
	// ample headroom under the (now widened) deadline. Per-command errors are
	// still tolerated (continue), preserving the original degradation shape so
	// computeScopeDelta's degraded-snapshot guard still catches any leftover.
	var allRequests []LiveRequest

	// Phase 1: batch ZRevRange all dimension queue keys in a single pipeline.
	pipe := s.rdb.Pipeline()
	zrevCmds := make([]*redis.StringSliceCmd, len(dimKeys))
	for i, key := range dimKeys {
		zrevCmds[i] = pipe.ZRevRange(ctx, key, 0, int64(LiveStreamLaneVisibleLimit-1))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		// A pipeline-level error (e.g. ctx already cancelled) fails every cmd;
		// fall through so per-cmd errors are logged individually below.
		slog.Debug("snapshot: zrevrange pipeline exec failed", "err", err.Error(), "dim_keys", len(dimKeys))
	}

	// Collect (requestID) in dimKey order, preserving original semantics. We
	// dedupe request_ids across dimKeys only for the detail-fetch phase (same
	// request legitimately appears in vendor/provider/model queues — fetching
	// its detail once is correct and faster), while still appending the decoded
	// request once per (dimKey, request_id) so each lane keeps its member.
	type memberRef struct {
		dimIdx    int
		requestID string
	}
	var memberRefs []memberRef
	seenDetail := make(map[string]int) // requestID → index into detail fetch list
	var detailOrder []string           // requestIDs in first-seen order
	for i, cmd := range zrevCmds {
		members, err := cmd.Result()
		if err != nil && err != redis.Nil {
			slog.Debug("snapshot: failed to read dimension queue", "key", dimKeys[i], "err", err.Error())
			continue
		}
		for _, member := range members {
			requestID := requestIDFromDimensionQueueMember(member)
			if requestID == "" {
				continue
			}
			memberRefs = append(memberRefs, memberRef{dimIdx: i, requestID: requestID})
			if _, ok := seenDetail[requestID]; !ok {
				seenDetail[requestID] = len(detailOrder)
				detailOrder = append(detailOrder, requestID)
			}
		}
	}

	// Phase 2: batch GET every distinct request detail in a single pipeline.
	detailPipe := s.rdb.Pipeline()
	detailCmds := make([]*redis.StringCmd, len(detailOrder))
	for i, requestID := range detailOrder {
		detailKey := liveStreamGlobalRequestDetailKey(requestID)
		if !isSuper && tenantID != "" {
			detailKey = liveStreamRequestDetailKey(tenantID, requestID)
		}
		detailCmds[i] = detailPipe.Get(ctx, detailKey)
	}
	if _, err := detailPipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.Debug("snapshot: detail pipeline exec failed", "err", err.Error(), "details", len(detailOrder))
	}

	// Decode each detail once; nil entries mean "expired/missing" (skip, as before).
	details := make([]*LiveRequest, len(detailOrder))
	for i, cmd := range detailCmds {
		data, err := cmd.Result()
		if err == redis.Nil {
			continue // request detail expired
		}
		if err != nil {
			slog.Debug("snapshot: failed to load request detail", "request_id", detailOrder[i], "err", err.Error())
			continue
		}
		req, err := unmarshalLiveRequestRedisPayload(data)
		if err != nil {
			slog.Debug("snapshot: failed to unmarshal request", "request_id", detailOrder[i], "err", err.Error())
			continue
		}
		// Tenant filtering
		if !isSuper && tenantID != "" && req.TenantID != tenantID {
			continue
		}
		details[i] = &req
	}

	// Append one decoded request per member reference. A request missing its
	// detail is skipped here (matching the previous per-member continue), so a
	// lane whose every member expired still contributes nothing.
	allRequests = make([]LiveRequest, 0, len(memberRefs))
	for _, ref := range memberRefs {
		idx := seenDetail[ref.requestID]
		if details[idx] == nil {
			continue
		}
		allRequests = append(allRequests, *details[idx])
	}

	// Sort ASC by timestamp so this function's own output — the
	// first/last_request_ts log fields below — is deterministic across
	// snapshots regardless of Redis SCAN order.
	//
	// 2026-07-26: per-lane display order is NOT set here. buildLiveStreamLanes
	// sorts each lane DESC (newest first) because that is the contract the
	// dashboard renders and firstTiles() truncates against. Previously this
	// ASC order leaked into the lanes, so the 20-tile cap kept the OLDEST
	// tiles and dropped every newer request.
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

// discoverDimensionQueues returns all swim-lane dimension queue keys for one
// scope, sorted by most recent activity so snapshot windows stay stable across
// calls.
//
// 2026-08-04 (方案C): the primary path now reads the scope's index SET
// (maintained by Record()/ScanAndRecordIdleMarkers) via a single SMEMBERS,
// replacing the per-snapshot SCAN over the whole keyspace that dominated the
// Redis slowlog and slowed the shared instance. If the index is absent
// (cold start, migration, or the SET expired) the method transparently falls
// back to the original SCAN so no lane is ever lost.
func (s *LiveStreamRedisStore) discoverDimensionQueues(ctx context.Context, tenantID string, isSuper bool) ([]string, error) {
	var allKeys []string
	source := "scan"

	if indexKey := liveStreamDimIndexKey(tenantID, isSuper); indexKey != "" {
		members, err := s.rdb.SMembers(ctx, indexKey).Result()
		if err == nil && len(members) > 0 {
			// Filter to genuine dim keys only: the SET is best-effort and may
			// carry stale members (a lane queue can be evicted before the index
			// SET itself expires). isDimensionQueueKey also rules out anything
			// that is not vendor/provider/model. Stale members are harmless —
			// the downstream ZRevRange simply returns empty for a missing key.
			for _, m := range members {
				if isDimensionQueueKey(m) {
					allKeys = append(allKeys, m)
				}
			}
			source = "index"
		} else if err != nil && err != redis.Nil {
			slog.Debug("dimension index SMembers failed, falling back to SCAN",
				"index_key", indexKey, "err", err.Error())
		}
	}

	if len(allKeys) == 0 {
		// Fallback: SCAN the keyspace. Used before any Record() has populated
		// the index (cold start), during a rolling deploy, or if the index SET
		// expired (24h of no traffic).
		scanKeys, err := s.discoverDimensionQueuesByScan(ctx, tenantID, isSuper)
		if err != nil {
			return nil, err
		}
		allKeys = scanKeys
		source = "scan-fallback"
	}

	// 2026-07-20: Sort keys by last activity timestamp to stabilize snapshot
	// windows. Redis SCAN order is non-deterministic, causing the same request
	// to appear/disappear across consecutive snapshots when different dimension
	// queues are scanned in different orders (the "swim-lane rolling" issue
	// reported on 245). Sorting by recency ensures we consistently prioritize
	// active lanes, and the deduplication logic produces stable results.
	var sortErr error
	allKeys, sortErr = s.sortKeysByActivity(ctx, allKeys)
	if sortErr != nil {
		slog.Debug("failed to sort by activity, using lexicographic order", "err", sortErr.Error())
		sort.Strings(allKeys)
	}

	// 2026-07-21: Log dimension queue discovery details
	slog.Info("dimension queues discovered",
		"tenant_id", tenantID,
		"is_super", isSuper,
		"source", source,
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

// discoverDimensionQueuesByScan is the legacy keyspace SCAN used as a fallback
// when the dim index SET is unavailable. Behaviour is identical to the
// pre-方案C discoverDimensionQueues.
func (s *LiveStreamRedisStore) discoverDimensionQueuesByScan(ctx context.Context, tenantID string, isSuper bool) ([]string, error) {
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
