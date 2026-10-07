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
 * ## 判据本身的口径：从 UI 出发的**传递**可达性

「孤儿」= `src/api/*.ts`（排除 `.test.ts` / `.spec.ts`）里，
**从 `src/` 下任何 UI 文件（视图 / 组件 / store / 配置 / 路由）出发，沿 import 边走不到**的那个模块。

★ 第一版只查**直接**引用，并且显式跳过 `src/api/` 整个目录。两处都错了：

1. **跳过整个 `src/api/` 是错的。** 第���零二批消解 `nodeHealth.ts` / `nodeHealthTimeline.ts`
   这对副本时，靠的是**API 层内部的委托**（`nodeHealth.ts` 改成 import 厚的那个）。
   直接引用口径下，委托方在 `src/api/` 里 ⇒ 被跳过 ⇒ **计数一点没降**，
   而事实上重复契约已经合并、模块已经可达。
2. **只看直接引用也是错的。** 第九十九批的 `taskTypeCorrectionStats` 从 `./taskProfile` 导入
   `unwrapCorrectionStat` —— 那是**同结构复用**，本身不是 UI 入口；
   但如果它自己也变成可达的，那它**传递可达**（UI → taskTypeCorrectionStats → taskProfile）。
   ⇒ 第一版靠「排除整个 api 目录」来避开这个假阳性，
   **代价是把所有 API 层内部委托一并排除了**。

⇒ ⇒ ★★★ **正确口径是传递可达性**：种子是「被 UI 文件直接 import 的模块」，
再沿 api 模块之间的 import 边做闭包。
⇒ 这样 api 内部互相引用**不会**凭空产生可达性（种子不从那里起），
而真正的委托**会**被算进去 —— 两个问题一次性解决，不需要例外条款。

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
  'opsOverview',
  'platformSettings',
  'probeTriStateTasks',
  'reportRollup',
  'requestActions',
  'selfCheck',
  'sessionAnalyticsFilterOptions',
  'slidingWindow',
  'storageAndLogConfig',
  'storageMigrationState',
  'subscriptionTiers',
  'turnsFilterOptions',
  'usageEnhanced',
  'usageTrendSeries',
  'userUsageStats',
  'v1DataHorizon',
  'workTypes',
]

const ROOT = join(process.cwd(), 'src')
const API_DIR = join(ROOT, 'api')

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
        if (full === API_DIR) continue // 种子必须从 UI 侧起，api 内部不产生可达性
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

/** 某个模块自己的源码（用于找它 import 了谁）。 */
function moduleSource(name: string): string {
  return readFileSync(join(API_DIR, `${name}.ts`), 'utf8')
}

function importsOf(blob: string): string[] {
  // ★ 两类 import 形式都必须认：
  //   ① `@/api/<name>` / `…/api/<name>` —— UI 侧与少数 api 侧用这种；
  //   ② `./<name>` —— **api 模块之间**用的就是这种相对形式。
  //   ★★ 第一版只写了 ① ⇒ `nodeHealth.ts` 对 `./nodeHealthTimeline` 的委托**完全不可见**
  //   ⇒ 计数不降。**量具自己也有盲区，而盲区恰好落在本批要修的那件事上。**
  const abs = [...blob.matchAll(/from\s+['"][^'"]*api\/([A-Za-z0-9_]+)['"]/g)].map((m) => m[1] as string)
  const rel = [...blob.matchAll(/from\s+['"]\.\/(?![.])([A-Za-z0-9_]+)['"]/g)].map((m) => m[1] as string)
  return [...abs, ...rel]
}

/**
 * ★ 从 UI 出发做传递闭包。
 *   注意 `importsOf` 用的是**相对宽松**的 `…api/<name>` 模式，
 *   所以 `./foo`（api 内部引用）匹配不到 —— 这正是我们要的：
 *   api 内部的边只能**传递**可达性，不能**凭空**产生可达性。
 *   ⇒ 而 uiSourceBlob 里出现的都是 `@/api/<name>` 形式，匹配得到。
 */
function reachableFromUi(): Set<string> {
  const all = new Set(apiModules())
  const seen = new Set<string>()
  const queue = importsOf(uiSourceBlob()).filter((n) => all.has(n))
  while (queue.length > 0) {
    const name = queue.shift() as string
    if (seen.has(name)) continue
    seen.add(name)
    for (const dep of importsOf(moduleSource(name))) {
      if (all.has(dep) && !seen.has(dep)) queue.push(dep)
    }
  }
  return seen
}

function currentOrphans(): string[] {
  const seen = reachableFromUi()
  return apiModules().filter((m) => !seen.has(m))
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

  it('★ 已接线的哨兵：routingPolicy 不在孤儿里（第一百零一批）', () => {
    // ★ 第一百零一批把路由策略配置面接上了 ⇒ 棘轮清单同步删掉那一行。
    expect(currentOrphans()).not.toContain('routingPolicy')
  })

  it('★★ 已接线的哨兵：nodeHealthTimeline 传递可达（第一百零二批）', () => {
    // ★ 它是被**委托**接上的：NodeHealthView → nodeHealth.ts → ./nodeHealthTimeline
    //   中间那一跳在 src/api/ 里且用相对导入 —— 两种口径都测得出这一点。
    expect(currentOrphans()).not.toContain('nodeHealthTimeline')
  })

  it('★★★ 反向护栏：api 内部的互相引用不得凭空产生可达性', () => {
    // ★ 种子只从 UI 侧起。若把 api 内部互相 import 也算成种子，
    //   第九十九批那个假阳性就会回来（taskTypeCorrectionStats 从 ./taskProfile 导入
    //   unwrapCorrectionStat 会被当成「taskProfile 已接上」）。
    const orphans = currentOrphans()
    expect(orphans).toContain('turnsFilterOptions')
    expect(orphans).toContain('storageMigrationState')
  })
})