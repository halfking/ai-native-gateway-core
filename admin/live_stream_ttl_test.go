package admin

import (
	"testing"
	"time"
)

// TestLiveStreamTTLConstants 验证精细化 TTL 常量值。
//
// 2026-07-23 精细化分层后:
//   - LiveStreamRecordRetention    = 4h  (请求详情: 一般请求 4h 内完成)
//   - LiveStreamLaneQueueRetention = 24h (泳道队列: 泳道存在 ≤ 1 天)
//   - LiveStreamActivityRetention  = 1h  (活跃度: 1h 无变化应清除)
//   - LiveStreamMainQueueRetention = 2h  (主队列/租户集)
//
// 这个测试是为了防止后续修改时意外覆盖了这些精细化值。
// 任何对这些值的修改必须同步更新 docs/changelogs/2026-07-23-redis-ttl-layered.md
// 并通知团队评审。
func TestLiveStreamTTLConstants(t *testing.T) {
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"LiveStreamRecordRetention", LiveStreamRecordRetention, 4 * time.Hour},
		{"LiveStreamLaneQueueRetention", LiveStreamLaneQueueRetention, 24 * time.Hour},
		{"LiveStreamActivityRetention", LiveStreamActivityRetention, 1 * time.Hour},
		{"LiveStreamMainQueueRetention", LiveStreamMainQueueRetention, 2 * time.Hour},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v (see docs/changelogs/2026-07-23-redis-ttl-layered.md)",
				c.name, c.got, c.want)
		}
	}
}
