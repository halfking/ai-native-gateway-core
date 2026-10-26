package admin

// live_stream_tile_delta.go — tile-granular slim delta for /api/admin/live-stream.
//
// 带宽问题（2026-10-27）：ComputeDelta 的粒度是【整条泳道】—— 一个维度里
// 任何一条 tile 变化，就把该维度的【全部】泳道（每条最多 20 tile）整体重发；
// 4 个维度 × tenant/super 双 scope，稳态流量下每 2s tick 近乎重发整个快照。
// 这是 /live 与 /dashboard 页面流量（~200GB/天）的主要来源。
//
// slim 模式（?slim=1，由前端主动携带）把 delta 粒度降到【tile】：
//   - changed_tiles[dim][laneID] = { name, stats, upserts, removed }
//     只携带自上次基线以来真正变化（新增/字段变化/离开泳道）的 tile；
//   - removed_lanes[dim] = 从快照里消失的泳道 ID 列表；
//   - 摘要/图例照旧（小）。
//
// 兼容性：legacy 客户端（不带 slim）的线上字节【一字节不变】—— slim 路径
// 只在客户端显式携带 ?slim=1 时启用，两条格式互不影响。前端
// web/src/composables/liveStreamStore.ts 的 mergeTileDelta 与本文件判据
// 一一对应（upsert 按 request_id 原位替换/尾部追加；removed 按 request_id
// 过滤；removed_lanes 整条删除）。

import (
	"net/http"
	"sort"
	"strings"
)

// liveStreamLanePatch is one lane's tile-level change set.
//
// Stats/Name always ride along: stats derive from the same tiles that
// produced the patch, so a lane with any tile change has fresh stats, and
// Name is short enough to send unconditionally (it also carries renames).
type liveStreamLanePatch struct {
	Name    string           `json:"name,omitempty"`
	Stats   LiveStreamStats  `json:"stats"`
	Upserts []LiveStreamTile `json:"upserts,omitempty"`
	// Removed carries request_ids that left this lane (rotated out of the
	// visible window, moved to another lane, or became invalid). The legacy
	// wire had no removal channel — whole-lane replacement handled it.
	Removed []string `json:"removed,omitempty"`
}

// LiveStreamTileDelta is the slim alternative to LiveStreamDelta.
// Tile upserts reuse the exact LiveStreamTile shape so the frontend merge
// is shape-identical to what a full snapshot would deliver.
type LiveStreamTileDelta struct {
	Summary      LiveStreamStats                           `json:"summary"`
	ChangedTiles map[string]map[string]liveStreamLanePatch `json:"changed_tiles,omitempty"`
	RemovedLanes map[string][]string                       `json:"removed_lanes,omitempty"`
	// DimensionLegends is emitted only for dimensions that had at least one
	// lane patch (same condition as the legacy delta's legend emission).
	DimensionLegends map[string][]LiveStreamLegendItem `json:"dimension_legends,omitempty"`
	StatusLegends    []LiveStreamLegendItem            `json:"status_legends,omitempty"`
}

// liveStreamDimensions is the canonical dimension order shared by snapshot
// build, legacy delta and slim delta.
var liveStreamDimensions = []string{"credential", "vendor", "provider", "model"}

// ComputeTileDelta diffs two snapshots at tile granularity. old == nil means
// "no baseline" (first tick / cold cache): every lane becomes a full upsert
// patch, mirroring the legacy delta's ChangedLanes = new.Dimensions fallback.
func ComputeTileDelta(old, new *LiveStreamSnapshot) *LiveStreamTileDelta {
	if new == nil {
		return nil
	}
	td := &LiveStreamTileDelta{
		Summary:          new.Summary,
		StatusLegends:    new.StatusLegends,
		DimensionLegends: map[string][]LiveStreamLegendItem{},
	}
	if old == nil {
		td.ChangedTiles = map[string]map[string]liveStreamLanePatch{}
		td.DimensionLegends = map[string][]LiveStreamLegendItem{}
		for _, dim := range liveStreamDimensions {
			patches := map[string]liveStreamLanePatch{}
			for _, lane := range new.Dimensions[dim] {
				patches[lane.ID] = liveStreamLanePatch{
					Name:    lane.Name,
					Stats:   lane.Stats,
					Upserts: lane.Requests,
				}
			}
			if len(patches) > 0 {
				td.ChangedTiles[dim] = patches
				td.DimensionLegends[dim] = new.DimensionLegends[dim]
			}
		}
		return td
	}

	td.ChangedTiles = map[string]map[string]liveStreamLanePatch{}
	td.RemovedLanes = map[string][]string{}
	for _, dim := range liveStreamDimensions {
		oldLanes := laneMapByID(old.Dimensions[dim])
		patches := map[string]liveStreamLanePatch{}
		for _, lane := range new.Dimensions[dim] {
			prev := oldLanes[lane.ID]
			patch := diffLaneTiles(prev, &lane)
			if patch != nil {
				patches[lane.ID] = *patch
			}
			delete(oldLanes, lane.ID)
		}
		// Lanes present in the baseline but gone from the new snapshot.
		for id := range oldLanes {
			td.RemovedLanes[dim] = append(td.RemovedLanes[dim], id)
		}
		if len(td.RemovedLanes[dim]) == 0 {
			delete(td.RemovedLanes, dim)
		}
		if len(patches) > 0 {
			td.ChangedTiles[dim] = patches
			td.DimensionLegends[dim] = new.DimensionLegends[dim]
		}
	}
	if len(td.ChangedTiles) == 0 {
		td.ChangedTiles = nil
	}
	if len(td.RemovedLanes) == 0 {
		td.RemovedLanes = nil
	}
	return td
}

// diffLaneTiles produces a patch for one lane, or nil when the lane's tile
// set is unchanged. Identity is request_id; a tile counts as changed when it
// is new or its status/timestamp fields moved.
func diffLaneTiles(prev, lane *LiveStreamLane) *liveStreamLanePatch {
	prevTiles := map[string]LiveStreamTile{}
	if prev != nil {
		for _, t := range prev.Requests {
			prevTiles[t.RequestID] = t
		}
	}
	patch := &liveStreamLanePatch{
		Name:  lane.Name,
		Stats: lane.Stats,
	}
	prevIDs := make(map[string]struct{}, len(prevTiles))
	if prev != nil {
		for _, t := range prev.Requests {
			prevIDs[t.RequestID] = struct{}{}
		}
	}
	newIDs := make(map[string]struct{}, len(lane.Requests))
	for _, t := range lane.Requests {
		newIDs[t.RequestID] = struct{}{}
		old, existed := prevTiles[t.RequestID]
		if !existed || old.Status != t.Status || old.Timestamp != t.Timestamp {
			patch.Upserts = append(patch.Upserts, t)
		}
	}
	if prev != nil {
		for _, t := range prev.Requests {
			if _, ok := newIDs[t.RequestID]; !ok {
				patch.Removed = append(patch.Removed, t.RequestID)
			}
		}
	}
	if len(patch.Upserts) == 0 && len(patch.Removed) == 0 {
		return nil
	}
	return patch
}

func laneMapByID(lanes []LiveStreamLane) map[string]*LiveStreamLane {
	m := make(map[string]*LiveStreamLane, len(lanes))
	for i := range lanes {
		m[lanes[i].ID] = &lanes[i]
	}
	return m
}

// filterTileDeltaForDims narrows a tile delta to the subscribed dimensions.
// dims == nil means "all dimensions" (no filtering). Returns the input when
// nothing needs dropping so the throttle cache is not copied per client.
func filterTileDeltaForDims(td *LiveStreamTileDelta, dims map[string]bool) *LiveStreamTileDelta {
	if td == nil || dims == nil {
		return td
	}
	out := *td
	out.ChangedTiles = filterDimMap(td.ChangedTiles, dims)
	out.RemovedLanes = filterDimSlices(td.RemovedLanes, dims)
	out.DimensionLegends = filterDimLegends(td.DimensionLegends, dims)
	return &out
}

func filterDimMap[T any](m map[string]T, dims map[string]bool) map[string]T {
	if len(m) == 0 {
		return m
	}
	out := make(map[string]T, len(m))
	for dim, v := range m {
		if dims[dim] {
			out[dim] = v
		}
	}
	return out
}

func filterDimSlices(m map[string][]string, dims map[string]bool) map[string][]string {
	if len(m) == 0 {
		return m
	}
	out := make(map[string][]string, len(m))
	for dim, v := range m {
		if dims[dim] {
			out[dim] = v
		}
	}
	return out
}

func filterDimLegends(m map[string][]LiveStreamLegendItem, dims map[string]bool) map[string][]LiveStreamLegendItem {
	if len(m) == 0 {
		return m
	}
	out := make(map[string][]LiveStreamLegendItem, len(m))
	for dim, v := range m {
		if dims[dim] {
			out[dim] = v
		}
	}
	return out
}

// filterSnapshotForDims narrows a full snapshot's Dimensions/DimensionLegends
// to the subscribed set (used for initial_data / snapshot_refresh when the
// client declared ?dims=). dims == nil keeps everything.
func filterSnapshotForDims(sn *LiveStreamSnapshot, dims map[string]bool) *LiveStreamSnapshot {
	if sn == nil || dims == nil {
		return sn
	}
	out := *sn
	out.Dimensions = filterDimMap(sn.Dimensions, dims)
	out.DimensionLegends = filterDimLegends(sn.DimensionLegends, dims)
	return &out
}

// parseLiveStreamDims reads the ?dims= declaration from the SSE request.
//
// Semantics (the frontend relies on all three):
//   - param absent        → (nil, false): subscribe to ALL dimensions.
//   - param present ""    → (empty set, true): subscribe to NONE (queue mode —
//     the UI shows dispatch-queue lanes which do not ride the dimension delta).
//   - param present "a,b" → the set of recognized values, invalid names dropped.
func parseLiveStreamDims(r *http.Request) (map[string]bool, bool) {
	raw, ok := r.URL.Query()["dims"]
	if !ok {
		return nil, false
	}
	dims := map[string]bool{}
	if len(raw) > 0 {
		for _, part := range strings.Split(raw[0], ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			for _, known := range liveStreamDimensions {
				if part == known {
					dims[known] = true
					break
				}
			}
		}
	}
	return dims, true
}

// liveStreamDimsKey is the per-client payload-cache key: sorted subscribed
// dims, "" for the empty set, "all" for no restriction.
func liveStreamDimsKey(dims map[string]bool) string {
	if dims == nil {
		return "all"
	}
	if len(dims) == 0 {
		return ""
	}
	keys := make([]string, 0, len(dims))
	for dim := range dims {
		keys = append(keys, dim)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
