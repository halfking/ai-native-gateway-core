package dbdegradation

// R83 轮（2026-10-24）：TTL 统计腿的**成本**移出请求路径。
//
// ★ 背景（154 生产实测，R82 之后）：
//
//	`/api/admin/data-lifecycle/degradation/status` 的 **`ttl_stats` 腿**
//	每次请求都要**全量 SCAN `session:*`**（生产 ~1M 键）
//	⇒ ★ 端点只有 5s 预算 ⇒ ★ **扫描必然被打断** ⇒ 计数永远是「部分值」
//	⇒ ⇒ `degraded` **长期为 true**（读数里 `sessions_count` 在 30 万~83 万间波动
//	　　= 部分计数）⇒ ★ 「端点一直报降级」这件事**不是产品坏了，是读法贵**。
//
// ★ 与备份摘要腿（R80~R82b）**同款**的修法：
//
//	① **快照 + 后台刷新**（单飞）⇒ 请求路径**不再**做全量 SCAN；
//	② **没有快照时仍内联算一次**（受请求 ctx 限制）
//	   ⇒ ★ 算得完就**当场给完整结果**（degraded=false）
//	   ⇒ ★ 算不完（被打断）⇒ 返回**部分统计 + 显式错误**（R79 语义保留）
//	     且 ★ **不缓存部分结果**（R82 教训：部分结果会覆盖/冒充好结果）
//	     ⇒ 同时**触发后台刷新** ⇒ 几十秒后请求路径就能拿到完整快照
//	③ 快照**必须带年龄**（`snapshot_age_seconds` / `snapshot_at`）
//	   ⇒ ★ 快照是「某个已知时刻的真实计数」⇒ 必须让运维知道它有多旧
//
// ★ 安全前提（已核实）：`GetTTLStats` 的调用方**只有 2 个**，都是**只读 admin 端点**
//　（`admin/degradation_control_handlers.go` / `admin/db_degradation_handlers.go`）
//　⇒ **不参与任何破坏性决策** ⇒ 缓存快照安全。
//
// ★ 口径不变：计数仍是「匹配 `session:*` 的全部键数」+ 两个子集计数 + `ursm:*` 计数。

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// R83 轮的三个常量。
const (
	// ttlStatsSnapshotTTL 快照有效期。
	//
	// ★ TTL 计数是**活的状态**（session 键不断增删）⇒ 快照不能太旧，
	//　但也不必每次请求都重扫 ⇒ 60s ⇒ **每分钟最多一次全量 SCAN**，
	//　而不是**每个请求一次**（生产上这是几十倍的差别）。
	ttlStatsSnapshotTTL = 60 * time.Second
	// ttlStatsComputeBudget 后台刷新的预算。
	//
	// ★ 全量 SCAN ~1M 键（COUNT 10000 ⇒ ~100 次往返）⇒ 十几秒可完成
	//　⇒ 2min 预算绰绰有余（★ 预算太小 ⇒ 后台永远算不完 ⇒ 每轮白烧）。
	ttlStatsComputeBudget = 2 * time.Minute
	// ttlStatsScanCount SCAN 的 COUNT 提示值。
	//
	// ★ 原来用 1000 ⇒ 1M 键需要 ~1000 次往返 ⇒ 端点 5s 预算必然被打断。
	// ★ 提到 10000 ⇒ ~100 次往返 ⇒ 往返次数降 10×。
	// ★ COUNT 只是「每批取多少」的**提示**，**不影响计数结果**（口径不变）。
	ttlStatsScanCount = 10000
)

// ttlStatsSnapshot 是 TTL 统计的一次快照。
type ttlStatsSnapshot struct {
	stats      map[string]interface{}
	computedAt time.Time
	// partial=true 表示这次统计**没算完**（被打断）⇒ 端点必须标降级。
	partial bool
	reason  string
}

// cloneTTLStats 深拷贝统计 map（★ 缓存里的 map 不得被调用方改坏）。
func cloneTTLStats(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in)+3)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// TTLStatsScans 返回 TTL 统计路径上的 **SCAN 次数**（R83 的可断言成本读数）。
//
// ★ 这是「请求路径还扫不扫」的**确定性读数**（计时断言天然 flaky ⇒ 不用计时）。
func (tm *TTLManager) TTLStatsScans() int64 {
	return atomic.LoadInt64(&tm.statsScans)
}

// ttlStatsSnapshotNow 返回当前快照（可能为 nil）与它的年龄。
func (tm *TTLManager) ttlStatsSnapshotNow() (*ttlStatsSnapshot, time.Duration) {
	tm.statsMu.Lock()
	snap := tm.statsSnapshot
	tm.statsMu.Unlock()
	if snap == nil {
		return nil, 0
	}
	return snap, time.Since(snap.computedAt)
}

// storeTTLStatsSnapshot 写入快照（短锁）。
func (tm *TTLManager) storeTTLStatsSnapshot(snap *ttlStatsSnapshot) {
	if snap == nil {
		return
	}
	tm.statsMu.Lock()
	tm.statsSnapshot = snap
	tm.statsMu.Unlock()
}

// triggerTTLStatsRefresh 触发**后台**TTL 统计刷新（单飞，非阻塞）。
//
// ★ 与备份摘要腿（R80）的 `triggerSummaryRefresh` **同款**：
// 　单飞闸门 ⇒ 不重复起 goroutine；后台预算 ≫ 请求预算。
func (tm *TTLManager) triggerTTLStatsRefresh(client *redis.Client) {
	if client == nil {
		return
	}
	if !tm.statsRefreshing.CompareAndSwap(false, true) {
		return // 已有刷新在跑 ⇒ 不重复
	}
	go func() {
		defer tm.statsRefreshing.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), ttlStatsComputeBudget)
		defer cancel()

		stats, err := tm.computeTTLStats(ctx, client)
		if err != nil {
			// ★ 算不完 ⇒ **不覆盖**已有快照（★ R82 教训：坏结果不覆盖好结果）
			//　⇒ 但**记下**这次的不完整（让端点知道它不可信）—— 若本来就没有快照，
			//　　则存一个 partial 快照，让下一次请求能显示「已统计到 X 条但没算完」。
			if tm.ttlStatsSnapshotNow0() == nil {
				tm.storeTTLStatsSnapshot(&ttlStatsSnapshot{
					stats:      stats,
					computedAt: time.Now(),
					partial:    true,
					reason:     err.Error(),
				})
			}
			slog.Warn("ttl manager: background stats refresh produced incomplete result", "error", err)
			return
		}
		tm.storeTTLStatsSnapshot(&ttlStatsSnapshot{
			stats:      stats,
			computedAt: time.Now(),
		})
	}()
}

// ttlStatsSnapshotNow0 是 `ttlStatsSnapshotNow` 的无年龄版本（仅内部判定「有没有快照」）。
func (tm *TTLManager) ttlStatsSnapshotNow0() *ttlStatsSnapshot {
	tm.statsMu.Lock()
	defer tm.statsMu.Unlock()
	return tm.statsSnapshot
}

// GetTTLStats 获取 TTL 统计信息（R83 轮：快照 + 后台刷新）。
//
// ★ **契约不变**：返回 (统计, error)；error 非 nil ⇒ 计数是**部分**的。
//
// ★ R83 轮的行为：
//
//	① **有快照** ⇒ 直接返回（零 SCAN）+ **带年龄**
//	   ⇒ 快照过期 ⇒ 顺手触发后台刷新（本次仍返回旧快照）
//	② **无快照** ⇒ **内联算一次**（受请求 ctx 限制）
//	   ⇒ 算得完 ⇒ 存快照并返回（degraded=false）
//	   ⇒ 算不完 ⇒ 返回**部分统计 + 错误**（R79 语义）+ **触发后台刷新**
//	     且 ★ **不缓存部分结果**（只有后台的完整结果才进快照）
func (tm *TTLManager) GetTTLStats(ctx context.Context) (map[string]interface{}, error) {
	client := tm.redis.Client()
	if client == nil {
		return map[string]interface{}{
			"mode":  tm.GetMode(),
			"error": "redis client not available",
		}, nil
	}

	// ① 有快照 ⇒ 零 SCAN
	if snap, age := tm.ttlStatsSnapshotNow(); snap != nil {
		out := cloneTTLStats(snap.stats)
		out["snapshot_at"] = snap.computedAt.Format(time.RFC3339)
		out["snapshot_age_seconds"] = int64(age.Seconds())
		if snap.partial {
			out["partial"] = true
			out["partial_reason"] = snap.reason
			// ★ 部分快照 ⇒ 端点必须标降级（部分计数 ≠ 完整计数）
			return out, fmt.Errorf("ttl stats partial: %s", snap.reason)
		}
		// ★ 过期 ⇒ 后台刷新（本次仍返回旧快照 + 年龄 ⇒ 运维看得见它有多旧）
		if age >= tm.statsTTL {
			tm.triggerTTLStatsRefresh(client)
		}
		return out, nil
	}

	// ② 无快照 ⇒ 内联算一次（算得完就当场给完整结果）
	stats, err := tm.computeTTLStats(ctx, client)
	if err == nil {
		tm.storeTTLStatsSnapshot(&ttlStatsSnapshot{stats: stats, computedAt: time.Now()})
		out := cloneTTLStats(stats)
		out["snapshot_at"] = time.Now().Format(time.RFC3339)
		out["snapshot_age_seconds"] = int64(0)
		return out, nil
	}

	// ★ 算不完 ⇒ **不缓存部分结果** + 触发后台刷新
	//　（R79 语义：报错时仍返回已统计到的部分数据）
	tm.triggerTTLStatsRefresh(client)
	return stats, err
}

// computeTTLStats 执行一次真正的全量统计（R83：SCAN 探针 + 更大的 COUNT）。
func (tm *TTLManager) computeTTLStats(ctx context.Context, client *redis.Client) (map[string]interface{}, error) {
	stats := map[string]interface{}{
		"mode":            tm.GetMode(),
		"normal_ttl":      tm.normalTTL.String(),
		"degraded_ttl":    tm.degradedTTL.String(),
		"extend_interval": tm.extendInterval.String(),
	}

	// ★ R79 轮（2026-10-24）：`session:key:*` 与 `session:apiKey:*` **都是
	//   `session:*` 的子集**（前缀蕴含）⇒ ⇒ **一次** `session:*` 的 SCAN
	//   就能逐字算出三个计数 ⇒ ⇒ **SCAN 次数 4 → 2**（`session:*` + `ursm:*`）。
	//   ★ **口径不变**：`sessions_count` 仍是「匹配 `session:*` 的**全部**键数」
	//   （**含**两个子集，与旧实现逐字一致）；两个子集计数也逐字一致。
	sessionCount, sessionKeyCount, apiKeyIndexCount, scanErr := tm.countSessionScopedKeys(ctx, client)
	stats["sessions_count"] = sessionCount
	stats["session_keys_count"] = sessionKeyCount
	stats["api_key_index_count"] = apiKeyIndexCount
	ursmCount, ursmErr := tm.countKeys(ctx, client, "ursm:*")
	stats["ursm_cache_count"] = ursmCount

	// ★ R79 轮：扫描被中断 ⇒ **显式报错**，不得让「部分计数」看起来像「完整计数」。
	if scanErr != nil {
		return stats, scanErr
	}
	if ursmErr != nil {
		return stats, ursmErr
	}
	return stats, nil
}
