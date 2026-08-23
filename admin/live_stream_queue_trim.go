package admin

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// liveStreamInflightProtectDuration is how long an in_progress tile remains
// protected from lane eviction. Defaults to 2h (aligned with request-survival
// interactive deadline). Override via LLM_GATEWAY_LIVE_STREAM_INFLIGHT_PROTECT_SECONDS.
var liveStreamInflightProtectDuration = 2 * time.Hour

func liveStreamInflightProtectDeadline() time.Duration {
	if liveStreamInflightProtectDuration > 0 {
		return liveStreamInflightProtectDuration
	}
	return 2 * time.Hour
}

func isMainOrStatusQueueKey(key string) bool {
	return key == liveStreamMainKey ||
		strings.HasSuffix(key, ":main") ||
		strings.HasPrefix(key, liveStreamStatPrefix)
}

func batchLoadRequestStatus(ctx context.Context, rdb redis.Cmdable, members []redis.Z) map[string]string {
	out := make(map[string]string, len(members))
	if rdb == nil || len(members) == 0 {
		return out
	}
	pipe := rdb.Pipeline()
	cmds := make([]*redis.StringCmd, 0, len(members))
	ids := make([]string, 0, len(members))
	for _, z := range members {
		member, ok := z.Member.(string)
		if !ok || member == "" || strings.HasPrefix(member, "idle-") {
			continue
		}
		if _, looksJSON := looksLikeTileSlim(member); looksJSON {
			continue
		}
		ids = append(ids, member)
		cmds = append(cmds, pipe.Get(ctx, liveStreamGlobalRequestDetailKey(member)))
	}
	if len(cmds) == 0 {
		return out
	}
	_, _ = pipe.Exec(ctx)
	for i, id := range ids {
		data, err := cmds[i].Result()
		if err != nil {
			continue
		}
		req, err := unmarshalLiveRequestRedisPayload(data)
		if err != nil || req.Status == "" {
			continue
		}
		out[id] = req.Status
	}
	return out
}

func looksLikeTileSlim(member string) (LiveStreamTileSlim, bool) {
	var slim LiveStreamTileSlim
	if err := json.Unmarshal([]byte(member), &slim); err != nil {
		return LiveStreamTileSlim{}, false
	}
	return slim, slim.RequestID != "" || slim.Status != ""
}

func isLiveStreamTerminalStatus(status string) bool {
	switch status {
	case "success", "failure", "rate_limited", "idle":
		return true
	default:
		return false
	}
}

func isLiveStreamEvictableMember(status string, tileTime time.Time, now time.Time, protect time.Duration) bool {
	if isLiveStreamTerminalStatus(status) {
		return true
	}
	if status != "in_progress" {
		return true
	}
	if protect <= 0 {
		return true
	}
	return now.Sub(tileTime) >= protect
}

func memberStatusAndTime(key, member string, score float64) (status string, tileTime time.Time) {
	tileTime = time.UnixMilli(int64(score)).UTC()
	if strings.HasPrefix(member, "idle-") {
		return "idle", tileTime
	}
	if isDimensionQueueKey(key) {
		if tile, err := unmarshalTileSlim(member); err == nil {
			if tile.Status != "" {
				status = tile.Status
			}
			if tile.Timestamp != "" {
				if ts, err := time.Parse(time.RFC3339, tile.Timestamp); err == nil {
					tileTime = ts.UTC()
				}
			}
			if status == "" {
				status = "in_progress"
			}
			return status, tileTime
		}
	}
	// Main / status queues store bare request_id; treat unknown as in_progress.
	return "in_progress", tileTime
}

// selectiveTrimLiveStreamQueue removes oldest evictable members when the ZSET
// exceeds keep. Fresh in_progress tiles younger than the protect window are
// never removed; completed and expired in_progress tiles are removed first.
func selectiveTrimLiveStreamQueue(ctx context.Context, rdb redis.Cmdable, key string, keep int) error {
	if rdb == nil || keep <= 0 {
		return nil
	}
	count, err := rdb.ZCard(ctx, key).Result()
	if err != nil {
		return err
	}
	if count <= int64(keep) {
		return nil
	}
	members, err := rdb.ZRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil {
		return err
	}
	statusByMember := map[string]string{}
	if isMainOrStatusQueueKey(key) {
		statusByMember = batchLoadRequestStatus(ctx, rdb, members)
	}
	now := time.Now().UTC()
	protect := liveStreamInflightProtectDeadline()
	overflow := int(count) - keep
	toRemove := make([]interface{}, 0, overflow)
	for _, z := range members {
		if overflow <= 0 {
			break
		}
		member, ok := z.Member.(string)
		if !ok || member == "" {
			continue
		}
		status, tileTime := memberStatusAndTime(key, member, z.Score)
		if s, ok := statusByMember[member]; ok && s != "" {
			status = s
		}
		if !isLiveStreamEvictableMember(status, tileTime, now, protect) {
			continue
		}
		toRemove = append(toRemove, member)
		overflow--
	}
	if len(toRemove) == 0 {
		return nil
	}
	return rdb.ZRem(ctx, key, toRemove...).Err()
}
