// baseline-ratchet.mjs —— 基线棘轮写回的**共用纯函数**（2026-11-03 12h 审计 P2-3）。
//
// ★ 为什么抽出来：两把运行时尺（design-metric-skeleton / design-nested-card）
//   的写回口径必须**逐字同源**，而此前它们各写一份 ⇒ 改一处忘一处 = 门与门
//   不同源 ⇒ 而且**没有任何门覆盖写回口径**（只覆盖「跑起来没报错」）。
//   抽成纯函数后，`web/src/baseline-ratchet.test.ts` 可直接验语义。
//
// 三条口径（每条都对应一次真实缺陷）：
//
// ① 棘轮：写回值 = min(旧值, 实测值) ⇒ 只许降不许升。
//    （实测 > 旧值 ⇒ **保持旧值**；曾有一版写反成「只许升不许降」，
//      被真仓变异臂 M6 当场抓出来。）
// ② 新页按实测值登记 —— 新页必须被棘轮看见。
// ③ **未量到的页保留旧条目** ⇒ 「没量到」≠「已删除」。
//    ⇒ 基线是**约束集合**（每条 = 一个页的棘轮上限）：删条目 = 悄悄摘掉那些页
//      的约束 ⇒ 门在那几页上从此失效。
//
// 日期口径：审计日期用**本地时区**。`toISOString()` 是 UTC ⇒ 在 UTC+8 的
// 00:00–08:00 之间会把日期写成**前一天** ⇒ 变更史出现错日期。

/**
 * localDate 返回本地时区的 YYYY-MM-DD。
 * @param {Date} [d]
 * @returns {string}
 */
export function localDate(d = new Date()) {
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/**
 * mergeRatchetBaseline 合并旧基线与本次实测，得到写回值。
 *
 * @param {Record<string, number>} prevCounts 旧基线（页 → 上限）
 * @param {Record<string, number>} measuredCounts 本次实测（页 → 实测值）
 * @returns {{next: Record<string, number>, keptHigher: Array<{page: string, measured: number, kept: number}>, preserved: string[], added: string[]}}
 *   next       —— 写回的基线（口径 ①②③ 全部落实）
 *   keptHigher —— 本次实测想调高、却被棘轮按旧值挡住的页
 *   preserved  —— 本次**没量到**、因而沿用旧值的页（不是删除）
 *   added      —— 本次新登记的页
 */
export function mergeRatchetBaseline(prevCounts = {}, measuredCounts = {}) {
  const prev = prevCounts ?? {}
  const next = {}
  const keptHigher = []

  for (const [page, now] of Object.entries(measuredCounts ?? {})) {
    const old = prev[page]
    if (typeof old === 'number' && Number.isFinite(old)) {
      if (now > old) {
        // ① 棘轮：实测想升 ⇒ 保持旧值（只许降不许升）。
        next[page] = old
        keptHigher.push({ page, measured: now, kept: old })
        continue
      }
      next[page] = now
      continue
    }
    // ② 新页按实测值登记。
    next[page] = now
  }

  // ③ 未量到的页保留旧条目（「没量到」≠「已删除」）。
  const preserved = []
  for (const [page, old] of Object.entries(prev)) {
    if (!(page in next)) {
      next[page] = old
      preserved.push(page)
    }
  }

  const added = Object.keys(next).filter((k) => !(k in prev))
  return { next, keptHigher, preserved, added }
}
