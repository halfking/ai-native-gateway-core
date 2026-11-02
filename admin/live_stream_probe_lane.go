package admin

// live_stream_probe_lane.go — SSE 探测频道订阅侧（U2）。
//
// ★ 写侧已分流：探测 tile → llmgw:live:probe:*（liveRequestQueueKeys）。
// ★ 读侧 HTTP 已有：GET /api/admin/probe/lane/live（ReplayLane "probe"）。
// ★ 本文件补 SSE 订阅侧缺口：
//   · 连接声明 ?probe=1（或 ?lanes=… 含 probe）才订探测频道
//   · 未声明 ⇒ 初始回放 / 实时 fan-out 都不下发探测 tile（网络层隔离，
//     不只靠前端 requestTypes 过滤）
//   · 隔离开启时初始回放合并 ReplayLane("probe")；关闭时业务回放里已有
//     is_probe 行，订阅方保留、未订阅方剥掉

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/probelane"
)

// parseLiveStreamWantProbe reads the client's probe-lane subscription.
//
// Accepted forms (any one is enough):
//   - ?probe=1 / true / on / yes
//   - ?lanes=business,probe（含 probe 即订）
//
// Default / absent ⇒ false（只订业务频道）。
func parseLiveStreamWantProbe(r *http.Request) bool {
	if r == nil {
		return false
	}
	q := r.URL.Query()
	if v := strings.TrimSpace(strings.ToLower(q.Get("probe"))); v != "" {
		switch v {
		case "1", "true", "on", "yes":
			return true
		case "0", "false", "off", "no":
			return false
		}
	}
	if raw, ok := q["lanes"]; ok && len(raw) > 0 {
		for _, part := range strings.Split(raw[0], ",") {
			if strings.EqualFold(strings.TrimSpace(part), "probe") {
				return true
			}
		}
	}
	return false
}

// filterLiveRequestsByProbeSub keeps or drops IsProbe rows per subscription.
func filterLiveRequestsByProbeSub(items []LiveRequest, wantProbe bool) []LiveRequest {
	if wantProbe || len(items) == 0 {
		return items
	}
	out := make([]LiveRequest, 0, len(items))
	for _, item := range items {
		if item.IsProbe {
			continue
		}
		out = append(out, item)
	}
	return out
}

// filterSnapshotByProbeSub strips probe tiles from a snapshot when the
// client did not subscribe to the probe channel. When wantProbe is true
// the snapshot is returned unchanged (probe tiles may live in business
// dim queues when PhysicalEnabled is off).
func filterSnapshotByProbeSub(sn *LiveStreamSnapshot, wantProbe bool) *LiveStreamSnapshot {
	if sn == nil || wantProbe {
		return sn
	}
	out := *sn
	if sn.Dimensions != nil {
		dims := make(map[string][]LiveStreamLane, len(sn.Dimensions))
		for dim, lanes := range sn.Dimensions {
			trimmed := make([]LiveStreamLane, 0, len(lanes))
			for _, lane := range lanes {
				lane.Requests = filterTilesByProbeSub(lane.Requests, false)
				trimmed = append(trimmed, lane)
			}
			dims[dim] = trimmed
		}
		out.Dimensions = dims
	}
	return &out
}

func filterTilesByProbeSub(tiles []LiveStreamTile, wantProbe bool) []LiveStreamTile {
	if wantProbe || len(tiles) == 0 {
		return tiles
	}
	out := make([]LiveStreamTile, 0, len(tiles))
	for _, tile := range tiles {
		if tile.IsProbe {
			continue
		}
		out = append(out, tile)
	}
	return out
}

// mergeLiveRequestsByTs merges two ASC-ish slices and re-sorts by Ts.
// Dedupes on RequestID (business wins on collision — should not happen
// across lanes when PhysicalEnabled, but keep deterministic).
func mergeLiveRequestsByTs(a, b []LiveRequest) []LiveRequest {
	if len(b) == 0 {
		return a
	}
	if len(a) == 0 {
		return b
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]LiveRequest, 0, len(a)+len(b))
	for _, item := range a {
		if item.RequestID == "" {
			out = append(out, item)
			continue
		}
		if _, ok := seen[item.RequestID]; ok {
			continue
		}
		seen[item.RequestID] = struct{}{}
		out = append(out, item)
	}
	for _, item := range b {
		if item.RequestID == "" {
			out = append(out, item)
			continue
		}
		if _, ok := seen[item.RequestID]; ok {
			continue
		}
		seen[item.RequestID] = struct{}{}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := parseLiveRequestTs(out[i].Ts), parseLiveRequestTs(out[j].Ts)
		return ti.Before(tj)
	})
	return out
}

func parseLiveRequestTs(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	return time.Time{}
}

// probeLanePhysicalOn is a thin wrapper so tests can stub if needed.
// Production always reads the env SSOT.
func probeLanePhysicalOn() bool {
	return probelane.PhysicalEnabled()
}

// fanOutScopePayloadKey groups clients that share the same snapshot shape
// (slim/dims/wantProbe) so fanOutScope marshals each variant at most once.
func fanOutScopePayloadKey(c *liveStreamClient) string {
	if c == nil {
		return "legacy"
	}
	probe := "biz"
	if c.wantProbe {
		probe = "probe"
	}
	if c.slim || c.dims != nil {
		return "slim|" + liveStreamDimsKey(c.dims) + "|" + probe
	}
	return "legacy|" + probe
}
