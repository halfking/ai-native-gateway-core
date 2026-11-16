package autoroute

// reselect.go —— auto 模式的**宽退路池**（2026-11-17 auto 模式可用性轮 · 根修 5）。
//
// ★ 为什么要宽退路池：
//
// 决策池在默认路径下只有 **48 小时热门 top-3**（recommend_v2.go:
// getHotTop3Canonicals ⇒ candidatePool 只装 hot top3）。于是：
//
//   · auto 选中的模型若此刻不可路由（凭据配额耗尽 / 探针退避 / 模型生命周期
//     过期 / 供应商停用……），可选的退路**也只有那 2-3 个同类模型**；
//   · 211 真机实测（2026-11-17 00:24 与 01:20 两轮）：auto 选中的模型
//     连续两次都**零候选**（`qwen2.5-7b-instruct` → `kimi-k3`），而平台上
//     明明有 **871** 个此刻可路由的模型。
//
// ★ 本文件产出的是「**此刻真的能路由**」的候选名单，供网关在零候选时换一个
// 还有节点的模型。
//
// ── ★★★ 第一版踩过的坑（别再踩）★★★ ──
//
// 第一版的退路池取自**同一张 48h 热度榜**（LIMIT 32）。它在生产上**恒空**：
// 211 真库实测 `session_turns_hot` 近 48h 有 **5338** 行，但 `canonical_id`
// **5338 行全为 NULL** ⇒ 热度榜 SQL（`WHERE canonical_id IS NOT NULL
// GROUP BY canonical_id`）返回 **0 行** ⇒
//
//	· hot top3 为空 ⇒ 决策池退化成「按 canonical id 升序回填」⇒ auto 选的
//	  永远是 **canonical id 最小的那个模型**（kimi-k3，id 2067363）——
//	  这才是「auto 总是选那个冷门模型、然后它不可用」的真正机制；
//	· 退路池建在同一张空榜上 ⇒ **同样为空** ⇒ 零候选时**无链可走** ⇒
//	  重选静默返回 ⇒ 仍然 503（211 live 验收实测：重选日志 0 条）。
//
// ⇒ **退路池不能依赖那张榜**。现在的实现改为：
//
//	索引快照（`credential_model_index`，含各模型的绑定与打分）
//	  → 按 canonical 去重
//	  → 走**实时可用过滤**（`filterCurrentlyAvailable`，与决策面同一套闸）
//	  → 输出 canonical 名列表
//
// 这样退路池的内容来自**「此刻可路由」**这个事实本身，而不是任何一张可能
// 空掉的统计表。
//
// ★ 与根修 6（决策面/候选面同闸）是互补的两层：
//
//	根修 6 ⇒ 不再选中「候选面必然滤空」的模型（事前）；
//	根修 5 ⇒ 真到了零候选时，有一条够宽的退路（事后）。

import (
	"context"
	"log/slog"
	"time"
)

// ReselectPoolLimit 是宽退路池的默认宽度（canonical 个数上限）。
//
// 32 的取舍：够宽（决策池只有 3，32 倍冗余），又不至于让「最坏情况下的
// 逐个实时验证」变成几十次串行 DB 查询——重选是**异常路径**（正常请求
// 不会走到这里），但它仍然占用请求的 deadline。
const ReselectPoolLimit = 32

// reselectCandidatesForAccessor is the IndexAccessor-facing wrapper.
//
// V1 keeps its index behind the IndexAccessor interface (so tests can inject
// fakes), while the ladder is implemented on *Index. A type assertion keeps
// the interface untouched — a fake accessor simply yields no ladder,
// i.e. the pre-existing narrow behaviour.
func reselectCandidatesForAccessor(ctx context.Context, accessor IndexAccessor, chosen string, limit int) []string {
	idx, ok := accessor.(*Index)
	if !ok || idx == nil {
		return nil
	}
	return reselectCandidateNames(ctx, idx, chosen, limit)
}

// reselectCandidateNames returns the canonical model names that are routable
// right now (excluding the chosen model), best-effort.
//
// Source: the in-memory index snapshot (which holds every binding the last
// refresh saw) filtered through the **same live availability filter** the
// decision path uses. It deliberately does NOT use the 48h popularity
// ladder — that ladder is empty in production (see the file header), and a
// ladder built on an empty table is an empty ladder.
//
// Failure semantics: a filter error fails **open** (the unfiltered names are
// returned) — the reselect path verifies every candidate against the
// provider side anyway, so a wider list is safe while an empty list is not.
func reselectCandidateNames(ctx context.Context, idx *Index, chosen string, limit int) []string {
	if idx == nil || limit <= 0 {
		return nil
	}
	pool := idx.cachedReselectPool(ctx, limit)
	out := make([]string, 0, len(pool))
	for _, name := range pool {
		if name == chosen {
			continue
		}
		out = append(out, name)
	}
	return out
}

// reselectPoolTTL is how long the wide ladder may be reused.
//
// The ladder is "which models are routable right now" — a 30s lag is
// harmless because the reselect path re-verifies every candidate against the
// provider side before adopting it. The TTL exists so that the live
// availability query does not run on every single auto request.
const reselectPoolTTL = 30 * time.Second

// cachedReselectPool returns the wide ladder, using the process-local cache
// when it is fresh enough.
func (idx *Index) cachedReselectPool(ctx context.Context, limit int) []string {
	idx.reselectPoolMu.Lock()
	defer idx.reselectPoolMu.Unlock()
	if idx.reselectPoolLimit == limit &&
		len(idx.reselectPool) > 0 &&
		time.Since(idx.reselectPoolTS) < reselectPoolTTL {
		out := make([]string, len(idx.reselectPool))
		copy(out, idx.reselectPool)
		return out
	}

	pool := idx.buildReselectPool(ctx, limit)
	idx.reselectPool = pool
	idx.reselectPoolTS = time.Now()
	idx.reselectPoolLimit = limit
	return pool
}

// buildReselectPool queries the live availability filter over the index
// snapshot and returns the canonical names that are routable right now.
func (idx *Index) buildReselectPool(ctx context.Context, limit int) []string {
	entries := idx.Snapshot()
	if len(entries) == 0 {
		return nil
	}

	// One representative per canonical model — but keep every binding of that
	// canonical in the filter set: availability is per binding, and a
	// canonical whose representative binding is dead may still have a live
	// sibling binding.
	seenCanonical := make(map[string]struct{}, len(entries))
	bindings := make([]Candidate, 0, len(entries))
	// Cap the filter input: the availability query is one round trip with one
	// array parameter, and an unbounded list would make it expensive.
	maxBindings := limit * 4
	for _, c := range entries {
		if c.CanonicalName == "" || c.CredentialID == 0 {
			continue
		}
		if _, ok := seenCanonical[c.CanonicalName]; !ok {
			seenCanonical[c.CanonicalName] = struct{}{}
		}
		bindings = append(bindings, Candidate{CredentialID: c.CredentialID, CanonicalName: c.CanonicalName})
		if len(bindings) >= maxBindings {
			break
		}
	}
	if len(bindings) == 0 {
		return nil
	}

	filtered, err := idx.filterCurrentlyAvailable(ctx, idx.pool, idx.availabilityFilter, bindings)
	if err != nil {
		// Fail open: the reselect path re-verifies every candidate against the
		// provider side, so a slightly wider list is harmless — an empty list
		// would kill the request.
		slog.Warn("autoroute: reselect pool live filter failed; using unfiltered names",
			"error", err, "candidates", len(bindings))
		filtered = bindings
	}

	out := make([]string, 0, limit)
	emitted := make(map[string]struct{}, limit)
	for _, c := range filtered {
		if c.CanonicalName == "" {
			continue
		}
		if _, ok := emitted[c.CanonicalName]; ok {
			continue
		}
		emitted[c.CanonicalName] = struct{}{}
		out = append(out, c.CanonicalName)
		if len(out) >= limit {
			break
		}
	}
	return out
}
