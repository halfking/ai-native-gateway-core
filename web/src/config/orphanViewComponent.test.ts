/**
 * orphanViewComponent —— 「孤儿组件」守卫（2026-11-23 L14 轮）
 *
 * ## 为什么要有这道门
 *
 * ★★ L14 轮实测撞出的形态：上游 `95e44dc15`（2026-11-23「整合 Dashboard 会话统计与
 * Top10 用户排行到用户分析页面」）把 `/admin/session-analytics/users` 的
 * `component:` 从 `UserProfileListView` 改指成 `SessionAnalyticsUsersView`
 * ⇒ ★ **`UserProfileListView.vue` 变成「被 router import 却没有任何路由指向」的
 * 孤儿组件**（router.ts 里那行 `const UserProfileListView = () => import(...)`
 * 还在，只是没人用它）。
 *
 * ★ 后果实测（静态闭包证明，见 docs/audit/2026-11-20-会话分析系统前端实施报告.md
 * §7.25）：上游 maestro `13-session-analytics-users.yaml` 的列表页段**恒红**
 * —— 它的 5 个断言点（canonical 视图名出处行 / 窗口用量五列 / 帮助抽屉 /
 * 「画像详情」按钮 / 展开行模型矩阵）在现役页面 `SessionAnalyticsUsersView`
 * 的渲染闭包（17 个 .vue + 23 个 .ts）里**一个都不存在**
 * ⇒ 第一层 flow 恒红、第二层 `assert-users-usage.mjs` 的页面段恒红而它的
 * **后端段仍绿**（`/api/admin/session-analytics/users` 仍回
 * `usage_meta.source_view = request_logs_with_current_month`）
 * ⇒ ⇒ **能力在服务端活着，在前端死了**，而 CI 里没有任何一道门会红。
 *
 * ★★ **为什么已有的三道门都看不见它**（这才是本门存在的理由）：
 *   ① `navCoverage.test.ts`（菜单可达性）—— 判据是**路由**的可达性：
 *      「既无菜单入口、又无其它视图导航链接指向」⇒ ★ `/admin/session-analytics/users`
 *      有菜单入口（appNav.ts）⇒ 门绿 ⇒ ★★ **它判的是路由，不是组件**
 *      ⇒ 「组件被 import 但没路由」对它不可见。
 *   ② `pageHelpRegistry.test.ts`（帮助正文完整性）—— 判据是**存在性**：
 *      每个 id 有内容文件、每个文件被注册、无孤儿文件 ⇒ ★★ **正文与页面实际形态
 *      是否一致不在判据里**（实测 `session-users.zh-CN.md` 描述的页面形态与现役页
 *      完全对不上，也没有门会红）。
 *   ③ `maestro/tools/audit-route-coverage.mjs`（路由覆盖三方对拍）—— 判据是
 *      **路径**的三方对拍（router 路由 / flow 打开过的 URL / README 登记）
 *      ⇒ ★★ **同一个 URL 换了组件、断言随之失效，它看不见**。
 *
 * ⇒⇒ 三条守卫都在、都在跑，但**都判「存在 / 可达」，不判「一致」**
 * ⇒ ⇒ L14 这个形态恰好落在**三条守卫的盲区交集**里 ⇒ 必须补一条
 * **判「组件 ⇒ 路由」的可达性**的守卫。
 *
 * ## 判据（不要改宽）
 *
 * 「孤儿组件」= ★ **router.ts 里被 import（懒加载或静态）却没有任何一条路由的
 * `component:` 指向它**。
 *
 * ★ 为什么只看 router.ts 不看全仓：`router.ts` 是**唯一**能挂载路由表的地方
 * （Vue Router 的 routes 数组只在此）⇒ 「组件可达」等价于「被某条路由引用」。
 *
 * ★ 为什么不用「文件是否被任何地方 import」：那样会把「被别的视图当子组件
 * 复用」的合法形态判成孤儿（噪音大 ⇒ 门被当噪音关掉 ⇒ 比没有门更糟）。
 * ★ 本判据只问「**有没有路由用它**」，不问「有没有人 import 它」。
 *
 * ★ 豁免表**双向生效**：
 *   - 新增孤儿（实际孤儿 − 豁免表 ≠ ∅）⇒ 红；
 *   - 豁免表条目已不是孤儿（豁免表 − 实际孤儿 ≠ ∅）⇒ 红
 *     ⇒ ★ **豁免表不能当废纸**（沿用 `navCoverage.test.ts` 的 KNOWN_ORPHANS 约定：
 *     白名单条目失效必须让门转红，否则白名单会腐烂成「什么都豁免」的废纸）。
 *
 * @see docs/audit/2026-11-20-会话分析系统前端实施报告.md §7.25（L14 取证主档）
 */

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const webRoot = join(here, '..', '..')
const routerSrc = readFileSync(join(webRoot, 'src', 'router.ts'), 'utf8')

/**
 * 已知孤儿组件登记处。
 *
 * 每条必须写**具体**理由（≥ 20 字符且含可核对的细节）——沿用 `navCoverage.test.ts`
 * 的 KNOWN_ORPHANS 约定：白名单里写「同上」等于没有约束。
 */
const KNOWN_ORPHAN_COMPONENTS: Record<string, string> = {
  UserProfileListView:
    '2026-11-23 L14 轮登记：上游 95e44dc15（2026-11-23 整合 Dashboard 会话统计与 Top10 用户排行到用户分析页面）把 /admin/session-analytics/users 的 component 由 UserProfileListView 改指 SessionAnalyticsUsersView ⇒ 本组件成为「被 import 却无路由指向」的孤儿；上游 maestro 13-session-analytics-users.yaml 列表页段因此恒红（断言点在现役页渲染闭包内 0 命中）。★ 修法二选一（恢复列表页路由 / 重写 flow 段改断言聚合页）＝**产品口径待 owner 裁决**，本轮只登记不擅改别列车的 flow 口径。详见报告 §7.25',
}

/** router.ts 里所有「懒加载组件常量」：const X = () => import('./path.vue') */
const LAZY_IMPORTED = new Map<string, string>()
for (const m of routerSrc.matchAll(
  /const\s+([A-Za-z0-9_]+)\s*=\s*\(\s*\)\s*=>\s*import\(\s*['"]([^'"]+)['"]\s*\)/g,
)) {
  LAZY_IMPORTED.set(m[1], m[2])
}

/** router.ts 里所有「静态 import 的组件」：import X from './path.vue' */
for (const m of routerSrc.matchAll(/^import\s+([A-Za-z0-9_]+)\s+from\s+['"]([^'"]+)['"]/gm)) {
  if (!LAZY_IMPORTED.has(m[1])) LAZY_IMPORTED.set(m[1], m[2])
}

/** 被某条路由的 component: 指向的组件名集合。 */
const ROUTE_USED = new Set<string>()
for (const m of routerSrc.matchAll(/component:\s*([A-Za-z0-9_]+)/g)) {
  ROUTE_USED.add(m[1])
}

/** 孤儿组件 = 被 import 但没有任何路由指向。 */
const orphans = [...LAZY_IMPORTED.entries()]
  .filter(([name]) => !ROUTE_USED.has(name))
  .map(([name, path]) => ({ name, path }))
  .sort((a, b) => a.name.localeCompare(b.name))

describe('orphanViewComponent —— 孤儿组件守卫', () => {
  it('解析器不得腐化：router.ts 的组件数不得掉到量级下限以下', () => {
    // ★ 防「正则坏了 ⇒ 恒绿」：实测 router.ts 有 98 个被 import 的组件、97 个被路由使用。
    // ★ 阈值取 50（远低于实测值，远高于「解析器彻底坏掉」的值）。
    expect(LAZY_IMPORTED.size, `router.ts 被 import 的组件数 = ${LAZY_IMPORTED.size}`).toBeGreaterThan(50)
    expect(ROUTE_USED.size, `router.ts 路由使用的组件数 = ${ROUTE_USED.size}`).toBeGreaterThan(50)
  })

  it('没有未登记的孤儿组件（新增孤儿 ⇒ 红）', () => {
    const unregistered = orphans.filter((o) => !KNOWN_ORPHAN_COMPONENTS[o.name])
    expect(
      unregistered.map((o) => `${o.name}（${o.path}）`).join(', '),
      `未登记的孤儿组件：${unregistered.length} 条。\n` +
        '★ 孤儿组件 = router.ts 里被 import 却没有任何路由的 component: 指向它。\n' +
        '★ 处置二选一：① 给它一条路由（产品口径）；② 确认它确实废弃 ⇒ 删除该组件与其\n' +
        '  专属钉测文件（不留半死代码）；③ 若它只是「上游刚改指、待 owner 裁决」⇒\n' +
        '  登记进 KNOWN_ORPHAN_COMPONENTS 并写明具体理由（不许写「同上」）。\n' +
        '★ 另请核对：上游 maestro flow 若仍以该 URL 断言本页独有元素，\n' +
        '  那条 flow 段此刻正在恒红（见报告 §7.25）。',
    ).toBe('')
  })

  it('豁免表不得腐烂：登记的条目必须仍然是孤儿（已修复 ⇒ 红）', () => {
    const stale = Object.keys(KNOWN_ORPHAN_COMPONENTS).filter((n) => !orphans.some((o) => o.name === n))
    expect(
      stale.join(', '),
      `豁免表里这些条目已不是孤儿组件（组件已被路由指向或已删除）：${stale.join(', ')}\n` +
        '★ 请从 KNOWN_ORPHAN_COMPONENTS 里删掉它们 —— 白名单条目失效必须让门转红，\n' +
        '  否则白名单会腐烂成「什么都豁免」的废纸，门对已修复项失去约束。',
    ).toBe('')
  })

  it('豁免表每条理由必须具体（≥ 20 字符，禁止「同上」类写法）', () => {
    for (const [name, reason] of Object.entries(KNOWN_ORPHAN_COMPONENTS)) {
      expect(reason.length, `豁免表条目 ${name} 的理由过短`).toBeGreaterThanOrEqual(20)
      expect(reason, `豁免表条目 ${name} 的理由禁止写「同上」`).not.toMatch(/^(同上|同上。|同前|as above)/)
    }
  })

  it('孤儿登记必须指向可核对的来源（本轮 L14 的具体事实）', () => {
    // ★ 这条不是「内容门」而是「口径门」：它钉住本门**当前**唯一的豁免条目，
    // ★ 防止有人把 UserProfileListView 从豁免表里删掉（删了就红 ⇒ 必须同时修路由）。
    expect(Object.keys(KNOWN_ORPHAN_COMPONENTS), '本门当前唯一的已知孤儿是 UserProfileListView').toEqual([
      'UserProfileListView',
    ])
    const entry = KNOWN_ORPHAN_COMPONENTS.UserProfileListView
    expect(entry, '理由必须写明改指的那一笔提交').toContain('95e44dc15')
    expect(entry, '理由必须写明恒红的后果').toContain('恒红')
  })
})
