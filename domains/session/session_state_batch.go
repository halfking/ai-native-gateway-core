package session

import (
	"context"
	"errors"
	"fmt"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
)

// GetBatch 批量读取会话及其统计。
//
// ── 为什么需要它（R166 · 会话列表 N+1）──────────────────────────
//
// 旧的列表路径对每个 session id 依次调用 Get 与 GetStats，而**这两个方法读的是
// 同一个 Redis hash**（`session:{id}`）：
//
//	Get      → redissafe.SafeHGetAll → TYPE + HGETALL（2 次往返）
//	GetStats → redissafe.SafeHGetAll → TYPE + HGETALL（2 次往返）
//
// ⇒ 每会话 **4 次往返**，且**同一份 hash 取了两遍**。列表 limit 上限 200、
// 取数时多取一倍（limit*2）⇒ 单次列表请求最多 400 个 id ⇒ 最多 **1600 次
// 往返**。实测 `/api/admin/sessions` 1311ms，与 400×2 次往返的量级吻合。
//
// 本方法改为：**一次 TYPE 流水线 + 一次 HGETALL 流水线**取回整批 hash
// （往返 4N → 2），再由**同一份快照**分别派生 Session 与 SessionStats
// （不重复读）。仓内已有同形先例：`GetEnrichedSession` 也是「读一次 hash，
// 同时给出 session 与 stats」。
//
// ── 语义对齐（逐键路径 → 本方法）──────────────────────────────
//
//	逐键路径（Get）                   本方法
//	──────────────────────────       ─────────────────────────────
//	key 不存在 → ErrSessionNotFound   该键不进任何 map
//	TYPE 非 hash → 记 mismatch + 跳过  该键不进任何 map（并记 mismatch）
//	hash 为空 → ErrSessionNotFound    该键不进任何 map
//	已过期 → ErrSessionExpired        该键不进任何 map
//	其它 Redis 错误 → 返回 error        返回 error（由调用方决定降级）
//
// ★ 与旧路径有一处**刻意**的差别：旧路径里 Get 成功后 GetStats 是**第二次**
// 读，若两次读之间 key 消失，stats 会是 nil；本方法从同一份快照派生 ⇒
// stats 恒非 nil。这是「同一份数据不许读两遍」的直接结果，也消除了两次读
// 之间的竞态窗口。
//
// ★ 失败粒度：批量路径是**整批失败**，逐键路径是**坏键被跳过、好键照常渲染**。
// 因此调用方**必须**在批量失败时退回逐键读，否则一个坏键就会打空整页列表。
// （错类型键在 TYPE 阶段就被逐键识别，不会导致整批失败；只有「两次流水线
// 之间键的类型翻转」这种极窄竞态才会。）
func (sm *Manager) GetBatch(ctx context.Context, sessionIDs []string) (map[string]*Session, map[string]*SessionStats, error) {
	if sm == nil || sm.redis == nil {
		return nil, nil, ErrSessionNotFound
	}
	client := sm.redis.Client()
	if client == nil {
		return nil, nil, ErrSessionNotFound
	}

	sessions := make(map[string]*Session, len(sessionIDs))
	stats := make(map[string]*SessionStats, len(sessionIDs))
	if len(sessionIDs) == 0 {
		return sessions, stats, nil
	}

	// 去重：同一个 id 重复出现不该产生两次往返。
	uniq := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, id := range sessionIDs {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return sessions, stats, nil
	}

	// key → session id：SafeHGetAllPipeline 回传的是 Redis key。
	keys := make([]string, 0, len(uniq))
	keyToID := make(map[string]string, len(uniq))
	for _, id := range uniq {
		key := "session:" + id
		keys = append(keys, key)
		keyToID[key] = id
	}

	// 分块：DefaultHashBatchSize 是仓内实测的批量工作点（见
	// internal/redis/safe_operations_batch.go 的读数表）。
	for start := 0; start < len(keys); start += redissafe.DefaultHashBatchSize {
		end := start + redissafe.DefaultHashBatchSize
		if end > len(keys) {
			end = len(keys)
		}
		results, err := redissafe.SafeHGetAllPipeline(ctx, client.Pipeline(), keys[start:end])
		if err != nil {
			return nil, nil, fmt.Errorf("batch read session hashes failed: %w", err)
		}
		for _, r := range results {
			if r.Err != nil {
				switch {
				case errors.Is(r.Err, redissafe.ErrKeyNotFound):
					continue // 键不存在：逐键路径同样返回 ErrSessionNotFound
				case errors.Is(r.Err, redissafe.ErrWrongType):
					logSessionTypeMismatch("get_batch", keyToID[r.Key], r.Err)
					continue
				default:
					return nil, nil, fmt.Errorf("batch read session %q failed: %w", keyToID[r.Key], r.Err)
				}
			}
			id := keyToID[r.Key]
			if len(r.Fields) == 0 {
				continue // 空 hash：逐键路径同样返回 ErrSessionNotFound
			}
			sess, err := sessionFromRedisHash(id, r.Fields)
			if err != nil {
				continue // 过期等：逐键路径下 Get 返回错误 ⇒ 调用方跳过该会话
			}
			sessions[id] = sess
			stats[id] = sessionStatsFromRedisHash(r.Fields)
		}
	}

	return sessions, stats, nil
}
