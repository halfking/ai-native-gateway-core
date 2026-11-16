package autoroute

// reselect.go —— auto 模式的**宽退路池**（2026-11-17 auto 模式可用性轮 · 根修 5）。
//
// ★ 为什么要宽退路池：
//
// 决策池在默认路径下**只有 48 小时热门 top-3**（recommend_v2.go:
// getHotTop3Canonicals ⇒ candidatePool 只装 hot top3）。于是：
//
//   · auto 选中的模型若此刻不可路由（凭据配额耗尽 / 探针退避 / 模型生命周期
//     过期 / 供应商停用……），可选的退路**也只有那 2-3 个同类模型**；
//   · 211 真机实测（2026-11-17 00:24）：auto 选到 qwen2.5-7b-instruct，
//     该模型候选面零候选 ⇒ 退路 glm-4.7 / glm-4.5-air **此刻也全是零候选**
//     ⇒ 请求 503「原来的模型不可用」，而平台上明明有 871 个此刻可路由的模型。
//
// ★ 本文件提供的是**同一张 48h 成功流量榜的更宽版本**（LIMIT 32）：
//
//   · 与决策池同源（真实成功流量）⇒ 退路模型不是「随便挑的冷门模型」；
//   · 比决策池宽（32 vs 3）⇒ 选中模型不可路由时，大概率还有一个有节点；
//   · **零额外打分开销**：不重新打分，只多一次带 2 分钟 TTL 缓存的 SQL；
//   · **不改变决策**：本文件只产出「备选名单」，winner 仍由既有流程决定。
//
// ★ 与根修 6（决策面/候选面同闸）是互补的两层：
//
//	根修 6 ⇒ 不再选中「候选面必然滤空」的模型（事前）；
//	根修 5 ⇒ 真到了零候选时，有一条够宽的退路（事后）。

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/internal/dbrows"
)

// ReselectPoolLimit 是宽退路池的默认宽度（48h 热度榜的 LIMIT）。
//
// 32 的取舍：够宽（决策池只有 3，32 倍冗余），又不至于让「最坏情况下的
// 逐个实时验证」变成几十次串行 DB 查询——重选是**异常路径**（正常请求
// 不会走到这里），但它仍然占用请求的 deadline。
const ReselectPoolLimit = 32

// getHotReselectCanonicals returns the wide 48h hot ladder (canonical ids,
// hottest first). Same source as getHotTop3Canonicals, wider LIMIT.
//
// Failure semantics: like its top-3 sibling, a query error yields an empty
// list rather than an error — the caller (Decision.ReselectCandidates) then
// simply has no wide ladder and the request keeps the old narrow behaviour.
func (idx *Index) getHotReselectCanonicals(ctx context.Context, limit int) []int {
	if limit <= 0 {
		limit = ReselectPoolLimit
	}

	// Test seam: same pattern as correctionLoader / availabilityFilter.
	if idx.hotReselectLoader != nil {
		return idx.hotReselectLoader(ctx, limit)
	}

	idx.hotCanonicalMu.RLock()
	if time.Since(idx.hotReselectCanonicalsTS) < idx.hotCanonicalsTTL && len(idx.hotReselectCanonicals) > 0 {
		cached := make([]int, len(idx.hotReselectCanonicals))
		copy(cached, idx.hotReselectCanonicals)
		idx.hotCanonicalMu.RUnlock()
		return cached
	}
	idx.hotCanonicalMu.RUnlock()

	// 2026-07-27 concurrency fix (same as getHotTop3Canonicals): read the
	// pool pointer under RLock — SetPool writes it under the write lock.
	idx.mu.RLock()
	pool := idx.pool
	idx.mu.RUnlock()
	if pool == nil {
		return nil
	}

	rows, err := pool.Query(ctx, `
		SELECT rl.canonical_id, count(*) as usage_count
		FROM `+db.SessionFamilyTurnsSourceSQL()+` rl
		WHERE rl.ts > NOW() - INTERVAL '48 hours'
		  AND rl.success = TRUE
		  AND rl.canonical_id IS NOT NULL
		GROUP BY rl.canonical_id
		ORDER BY usage_count DESC
		LIMIT $1
	`, limit)
	if err != nil {
		slog.Warn("autoroute: wide hot ladder query failed; reselect ladder stays narrow",
			"error", err, "limit", limit)
		return nil
	}
	defer rows.Close()

	var result []int
	for rows.Next() {
		var id int
		var count int64
		if err := rows.Scan(&id, &count); err != nil {
			dbrows.WarnRowSkip("autoroute.Index.getHotReselectCanonicals", err)
			continue
		}
		result = append(result, id)
	}
	if err := rows.Err(); err != nil {
		// Same discipline as the top-3 ladder: a truncated result must not be
		// cached, otherwise a partial ladder sticks for the whole TTL.
		slog.Warn("autoroute: wide hot ladder row iteration aborted; result truncated, cache not updated",
			"error", err, "collected", len(result))
		return result
	}

	idx.hotCanonicalMu.Lock()
	idx.hotReselectCanonicals = result
	idx.hotReselectCanonicalsTS = time.Now()
	idx.hotCanonicalMu.Unlock()

	return result
}

// reselectCandidatesForAccessor is the IndexAccessor-facing wrapper.
//
// V1 keeps its index behind the IndexAccessor interface (so tests can inject
// fakes), while the wide ladder is implemented on *Index. A type assertion
// keeps the interface untouched — a fake accessor simply yields no ladder,
// i.e. the pre-existing narrow behaviour.
func reselectCandidatesForAccessor(ctx context.Context, accessor IndexAccessor, chosen string, limit int) []string {
	idx, ok := accessor.(*Index)
	if !ok || idx == nil {
		return nil
	}
	return reselectCandidateNames(ctx, idx, chosen, limit)
}

// reselectCandidateNames maps the wide hot ladder to canonical model names,
// dropping the chosen model and duplicates. Returns nil when no ladder is
// available (no pool / query failed) — the caller then keeps the narrow
// (top-3) ladder, i.e. behaviour is unchanged from before this feature.
func reselectCandidateNames(ctx context.Context, idx *Index, chosen string, limit int) []string {
	if idx == nil || limit <= 0 {
		return nil
	}
	hot := idx.getHotReselectCanonicals(ctx, limit)
	if len(hot) == 0 {
		return nil
	}

	entries := idx.Snapshot()
	byCanonical := make(map[int]string, len(entries))
	for _, c := range entries {
		if c.CanonicalName == "" || c.CanonicalID <= 0 {
			continue
		}
		if _, ok := byCanonical[c.CanonicalID]; !ok {
			byCanonical[c.CanonicalID] = c.CanonicalName
		}
	}

	out := make([]string, 0, len(hot))
	seen := make(map[string]struct{}, len(hot))
	for _, id := range hot {
		name := byCanonical[id]
		if name == "" || name == chosen {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
		if len(out) >= limit {
			break
		}
	}
	return out
}
