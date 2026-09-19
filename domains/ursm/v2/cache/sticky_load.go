package cache

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// StickyLoadKey 是每凭据 sticky 会话滑窗 ZSET 的 Redis key：
// ursm:v2:stickyload:{credentialID}。
func StickyLoadKey(credentialID int) string {
	return keyf("ursm:v2:stickyload:%d", credentialID)
}

// stickyLoadTTLSlack 是滑窗之外的额外 TTL 余量：key 在最后一次观察后
// window+slack 自毁，空闲凭据的 ZSET 不会在 Redis 里无限堆积。
const stickyLoadTTLSlack = 2 * time.Minute

// StickyLoadStore 用每凭据一个 ZSET 维护"最近 window 内 sticky 到该凭据的
// 会话集合"（2026-09-19 sticky-session load balancing）。
//
//   - member = 会话标识（L1 sticky key），score = 该会话最近一次观察的 unix 秒；
//   - 写路径（Observe）先裁剪过期 member 再 ZADD，保证窗口语义；
//   - 读路径（LoadBatch）先 ZREMRANGEBYSCORE 惰性裁剪，再 ZCARD 计数、
//     ZRANGE(-1,-1) 取最高分 = 该凭据最近一次会话活跃时间。
//
// 会话完成与否无法可靠判定，窗口即语义：5 分钟内有活动的会话计入，超过
// 5 分钟自然跌出（用户规格：超过 5 分钟丢弃，5 分钟内全部计数）。
// 会话迁移到新凭据后，旧凭据 ZSET 里的 member 最多残留一个窗口——与规格
// 的滑窗容忍一致，不做跨凭据删除。
//
// 双活蓝绿实例共享同一 Redis，ZSET 计数天然是跨实例真值。
type StickyLoadStore struct {
	rdb *redis.Client
}

func NewStickyLoadStore(rdb *redis.Client) *StickyLoadStore {
	return &StickyLoadStore{rdb: rdb}
}

// Observe 记录"sessionKey 会话在 ts 时刻仍活跃于 credentialID"，
// 单次 pipeline 往返：裁剪 → ZADD → EXPIRE。裁剪先于 ZADD，
// 使当前 member 不会被同一命令组的窗口边界误删。
func (s *StickyLoadStore) Observe(ctx context.Context, credentialID int, sessionKey string, ts time.Time, window time.Duration) error {
	if s == nil || s.rdb == nil || sessionKey == "" || window <= 0 {
		return nil
	}
	key := StickyLoadKey(credentialID)
	cutoff := strconv.FormatInt(ts.Add(-window).Unix(), 10)
	pipe := s.rdb.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", cutoff)
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(ts.Unix()), Member: sessionKey})
	pipe.Expire(ctx, key, window+stickyLoadTTLSlack)
	_, err := pipe.Exec(ctx)
	return err
}

// Remove 会话离开某凭据（绑定被改写/删除）时主动移除 member，
// 加速滑窗收敛——留给 admin 清理路径用，热路径不依赖。
func (s *StickyLoadStore) Remove(ctx context.Context, credentialID int, sessionKey string) error {
	if s == nil || s.rdb == nil || sessionKey == "" {
		return nil
	}
	return s.rdb.ZRem(ctx, StickyLoadKey(credentialID), sessionKey).Err()
}

// LoadBatch 一次 pipeline 取回一批凭据的滑窗计数与最近活跃时间。
// 返回的 map 缺失该凭据 = 无记录（计数 0）。整体失败时两个 map 均为 nil，
// 调用方据此回退到进程内镜像（fail-open：读不到不影响路由，只是信号变差）。
func (s *StickyLoadStore) LoadBatch(ctx context.Context, credentialIDs []int, window time.Duration) (sessions map[int]int, lastSeen map[int]int64) {
	if s == nil || s.rdb == nil || len(credentialIDs) == 0 || window <= 0 {
		return map[int]int{}, map[int]int64{}
	}
	now := time.Now()
	cutoff := strconv.FormatInt(now.Add(-window).Unix(), 10)

	type slot struct {
		card *redis.IntCmd
		last *redis.ZSliceCmd
	}
	slots := make([]slot, len(credentialIDs))
	pipe := s.rdb.Pipeline()
	for i, id := range credentialIDs {
		key := StickyLoadKey(id)
		pipe.ZRemRangeByScore(ctx, key, "-inf", cutoff)
		slots[i].card = pipe.ZCard(ctx, key)
		slots[i].last = pipe.ZRangeWithScores(ctx, key, -1, -1)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, nil
	}

	sessions = make(map[int]int, len(credentialIDs))
	lastSeen = make(map[int]int64, len(credentialIDs))
	for i, id := range credentialIDs {
		if err := slots[i].card.Err(); err != nil && err != redis.Nil {
			// 单个 key 失败不影响其余凭据的读数。
			continue
		}
		sessions[id] = int(slots[i].card.Val())
		if zs := slots[i].last.Val(); len(zs) > 0 {
			lastSeen[id] = int64(zs[0].Score)
		}
	}
	return sessions, lastSeen
}
