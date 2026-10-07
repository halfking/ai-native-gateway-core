import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

/**
 * ★★★★★★ **孤儿棘轮门**（2026-10-08，第一百批）。
 *
 * ## 为什么要有这道门
 *
 * 批 98/99 把 `/api/admin/task-profile` 与
 * `/api/admin/task-profile/corrections/stats` 的 API 层做完了
 * （128 + 214 条判据、87 条变异全有牙、文档 §11.134/§11.135），
 * **但两个模块都没有被任何 UI 引用**。
 * ⇒ 全仓 93 个 api 模块里有 **30 个**是「做完了但用户点不到」的孤儿。
 * ⇒ ★★★ **API 层做完 ≠ 功能复制到移动端。** 用户点不到的东西不算数。
 *
 * 这道门把孤儿清单**显式登记**下来，于是：
 * - 新增一个 api 模块却忘了接线 ⇒ 立刻红（清单多了一项）；
 * - 接上了一项 ⇒ 也红（清单少了一项），**逼着人手工删掉那一行** ——
 *   这正是我们要的：清单只能变小，且每次变小都是一次有记录的收口。
 *
 * ★ 与「现状盘点类数字必然腐烂」不冲突：那张说的是**把数字写进判据**。
 *   这里登记的是**一个集合**，且集合的每次变化都会经过一次显式的 diff ——
 *   「变红」正是它的工作方式，不是它的缺陷。
 *
 * ## 判据本身的口径
 *
 * 「孤儿」= `src/api/*.ts`（排除 `.test.ts` / `.spec.ts`）里，
 * 在 **`src/` 下除 `src/api/` 之外**的任何文件（视图 / 组件 / store / 配置 / 路由）
 * 都找不到 `from '.../api/<模块名>'` 的那个模块。
 * ★ 排除 `src/api/` 自身是关键的：`taskTypeCorrectionStats.ts` 从 `./taskProfile`
 *   导入 `unwrapCorrectionStat`，那是**模块间的复用**，不是「接上了 UI」。
 *   把 api 内部互相引用算进去，批 98 那个模块会被误判成已接线。
 */

const ROOT = join(process.cwd(), 'src')
const API_DIR = join(ROOT, 'api')

/** 当前已登记的孤儿模块（**只能减少**）。接线一个就删一行。 */
const KNOWN_ORPHANS: readonly string[] = [
  'boardOperational',
  'connectionRegistry',
  'dataLifecycleStorage',
  'dispatchJournal',
  'errorsTrend',
  'formatAnomalies',
  'modelHistory',
  'modelIQ',
  'modelIntegrityDrift',
  'nativeTransport',
  'nodeHealthTimeline',
  'opsOverview',
  'platformSettings',
  'probeTriStateTasks',
  'reportRollup',
  'requestActions',
  'routingPolicy',
  'selfCheck',
  'sessionAnalyticsFilterOptions',
  'slidingWindow',
  'storageAndLogConfig',
  'storageMigrationState',
  'subscriptionTiers',
  'transport',
  'turnsFilterOptions',
  'usageEnhanced',
  'usageTrendSeries',
  'userUsageStats',
  'v1DataHorizon',
  'workTypes',
]

function apiModules(): string[] {
  return readdirSync(API_DIR)
    .filter((f) => f.endsWith('.ts') && !f.endsWith('.test.ts') && !f.endsWith('.spec.ts'))
    .map((f) => f.slice(0, -3))
    .sort()
}

/** 把 src 下除 src/api/ 之外的所有源码拼成一个字符串。 */
function uiSourceBlob(): string {
  const out: string[] = []
  const walk = (dir: string): void => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name)
      if (entry.isDirectory()) {
        if (full === API_DIR) continue // ★ 关键：api 内部的互相引用不算接线
        walk(full)
        continue
      }
      if (!/\.(ts|vue)$/.test(entry.name)) continue
      if (entry.name.endsWith('.spec.ts')) continue
      out.push(readFileSync(full, 'utf8'))
    }
  }
  walk(ROOT)
  return out.join('\n')
}

function currentOrphans(): string[] {
  const blob = uiSourceBlob()
  return apiModules().filter((m) => !new RegExp(`from\\s+['"][^'"]*api/${m}['"]`).test(blob))
}

describe('孤儿棘轮', () => {
  it('★ 作用面自证：扫描到的 api 模块数不为 0（塌缩到 0 或 1 必须红）', () => {
    expect(apiModules().length).toBeGreaterThan(50)
  })

  it('★ 作用面自证：UI 源码体量不为 0（万一拼接失败必须红）', () => {
    expect(uiSourceBlob().length).toBeGreaterThan(10000)
  })

  it('★ 当前孤儿集合与登记清单逐项相同', () => {
    expect(currentOrphans()).toEqual([...KNOWN_ORPHANS])
  })

  it('★ 登记清单本身无重复（防止清单写错掩盖变化）', () => {
    expect(new Set(KNOWN_ORPHANS).size).toBe(KNOWN_ORPHANS.length)
  })

  it('★ 登记的每一项都真的是 api 模块（防止写了已不存在的名字）', () => {
    const modules = new Set(apiModules())
    for (const m of KNOWN_ORPHANS) expect(modules.has(m)).toBe(true)
  })

  it('★ 已接线的哨兵：taskProfile / taskTypeCorrectionStats 不在孤儿里', () => {
    // ★ 这两条是本批接线的目标。若有人把抽屉席或路由删了，这条立刻红。
    const orphans = currentOrphans()
    expect(orphans).not.toContain('taskProfile')
    expect(orphans).not.toContain('taskTypeCorrectionStats')
  })
})