/**
 * navCoverage — 菜单可达性守卫（R85-C P1 配套）
 *
 * ## 为什么要有这道门
 *
 * 2026-10-01 R85 审计发现：后端 `/api/admin/session-analytics` 三端点与真库
 * `session_dim`（121 万行）均已就绪，但 `router.ts:257-258` 的
 * `/admin/session-analytics/users` 及其子路由**没有任何菜单入口**，只能手敲 URL。
 * 8 个语种的 `nav.item.sessionAnalytics` 文案**早已存在**却零引用 —— 典型的
 * 「可交付未交付」。
 *
 * 更糟的是 `appNav.test.ts` 21/21 全绿却**一行业务都没守**（全测可见性逻辑，
 * 无 labelKey×语种断言、无菜单↔路由断言）。本门补上这个缺口。
 *
 * ## 判据的语义（重要：不要改回「只看菜单」）
 *
 * 「孤儿路由」= **既没有菜单入口，也没有任何视图内的导航链接指向它**。
 *
 * 第一版只按菜单前缀匹配，得出 46 条「孤儿」——那是**代理量不是语义量**：
 * `/request-detail/:requestId`、`/admin/sessions/:id`、`/admin/approvals/:id`
 * 这类详情页本来就不该有菜单项，它们由列表页内部跳转到达。
 * 收紧为「菜单可达 **或** 视图内导航可达」后才落到真实的 17 条。
 *
 * 判定「视图内可达」要求路径字面量出现在**导航上下文**中
 * （`to=` / `:to=` / `href=` / `router.push` / `router.replace` / `$router` /
 * `window.location`）的同一行 —— 仅仅在 API 调用或注释里出现该字符串**不算**
 * （第一版就是栽在这里，把 5 条真孤儿判成了可达）。
 *
 * @see docs/全面审计v3/2026-10-01/59-R85-三域审计本体与主代理重新定级.md §3
 */

import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const srcRoot = join(here, '..')

const readSrc = (rel: string) => readFileSync(join(srcRoot, rel), 'utf8')

/**
 * 已知孤儿路由登记处。
 *
 * 每条必须写**具体**理由（≥ 20 字符且含可核对的细节）——沿用 `internal/metricguard`
 * 的教训：白名单里写「同上」等于没有约束。
 *
 * 白名单条目失效（router 里已删）会让门转红 —— 否则白名单会腐烂成一张
 * 「什么都豁免」的废纸，门对已修复项失去约束。
 */
const KNOWN_ORPHANS: Record<string, string> = {
  '/forbidden': '403 页面，仅由路由守卫在无权限时跳转进入，不应出现在菜单',
  '/routing': '纯 redirect 路由（→ /routing-v2?tab=resolve），无 component；供旧书签兼容，不占菜单项',
  '/maintain/:pathMatch(.*)*': 'ai-native-maintain SPA 的同源子路径兜底；由 appNav 的 external 入口整体进入，非单条菜单项',
  '/:pathMatch(.*)*': 'vue-router 404 兜底路由，匹配一切未命中路径；按设计永不可从菜单到达',
  '/catalog': '遗留目录页，appNav 无对应项；R85 未确认是否仍有消费者',
  '/customer/activate': '租户门户独立入口（customer 域），与控制台菜单是两套导航',
  '/customer/agreement': '租户门户独立入口（customer 域），与控制台菜单是两套导航',
  '/customer/site': '租户门户独立入口（customer 域），与控制台菜单是两套导航',
  '/dev/waterfall-preview': '开发期瀑布图预览页，刻意不暴露给生产菜单导航；仅供本地调试时手敲 URL 进入',
  '/maas/account': 'MaaS 门户独立入口（maas 域），由 /maas/models 进入',
  '/maas/models': 'MaaS 门户独立入口（maas 域），与控制台菜单是两套导航',
  '/maas/orders/:id': 'MaaS 门户子页，由 /maas/orders 内部跳转',
  '/maas/pricing': 'MaaS 门户独立入口（maas 域），与控制台菜单是两套导航',
  '/maas/usage': 'MaaS 门户子页，由 /maas/models 内部跳转',
  '/system-monitor': '已折叠进 /dashboard?tab=selfcheck（见 appNav.ts 2026-09-07 注释自证）',
  '/routing-decisions': 'R85 判 P3：路由决策页可交付未挂菜单，待产品确认归属后挂载',
  '/routing/overrides/audit': 'R85 判 P3：路由覆写审计页可交付未挂菜单，待产品确认归属后挂载',
  '/quality-correlations': 'R85 判 P3：质量关联页可交付未挂菜单，待产品确认归属后挂载',
  '/admin/output-compliance': 'R85 判 P3：输出合规页可交付未挂菜单，待产品确认归属后挂载',
  '/admin/usage': 'R85 判 P3：用量成本页可交付未挂菜单，R84 已证与 report-rollup 数字不同源',
}

/** 扫描器不得腐化：路由数掉到这个量级说明解析正则坏了（沿用 metricguard「声明数<100 即红」）。 */
const MIN_ROUTER_PATHS = 80
const MIN_NAV_PATHS = 40

/**
 * 判据：一条路由是「孤儿」当且仅当 —— **既无菜单入口，也没有任何其它视图的
 * 导航链接指向它**。
 *
 * ## 三版判据的取舍记录（别改回前两版）
 *
 * **v1 只看菜单** → 报 46 条「孤儿」。那是**代理量不是语义量**：
 * `/request-detail/:requestId`、`/admin/sessions/:id` 这类详情页本就不该有菜单项，
 * 它们由列表页内部跳转。噪音太大 ⇒ 门会被当噪音关掉，比没有门更糟。
 *
 * **v2 无参数顶层页必须有菜单** → 降到 13 条，但**方向反了地制造假阳性**：
 * `/correlations`、`/routing/overrides`、`/admin/approvals` 都是无参数页，
 * 却由各自的父页面内部跳转到达。切分不紧。
 *
 * **v3 传递闭包**（沿 router.push 逐跳展开）→ 方向对，但**静态追不动**：
 * 链接目标常是模板字面量（`/x/users/${owner}`）与运行期拼接。
 *
 * **v4 = 菜单覆盖 ∪ 视图内链接，但排除「自我背书」**（本版）。
 * v1 的唯一真缺陷是**环形 vouch**：`UserProfileListView.vue` 里的
 * `router.push('/admin/session-analytics/users/${owner}')` 让列表页的**子树**
 * 给自己背书，于是「删掉菜单入口」这个真实回归**门是绿的**（变异 M1 未变红）。
 * 修法只需一句话：**判定某路由时，忽略它自己那个视图文件里的链接**。
 * 这样 `/admin/session-analytics/users` 回到孤儿（正确），
 * 而 `/correlations`（由 RoutingView 这个**别的**文件链入）仍算可达（也正确）。
 */
const NAV_CONTEXT = /(to=|:to=|href=|router\.push|router\.replace|\$router|window\.location)/

function collectSourceFiles(dir: string, acc: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry.startsWith('.')) continue
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) collectSourceFiles(full, acc)
    else if (/\.(ts|vue)$/.test(entry) && entry !== 'router.ts') acc.push(full)
  }
  return acc
}

const routerSrc = readSrc('router.ts')
const navSrc = readSrc('config/appNav.ts')
// 同时剥掉 query 与 hash：视图里常见 :to="`/routing/overrides#${id}`" 这种带片段的链接
const base = (p: string) => p.split(/[?#]/)[0].replace(/\/+$/, '') || '/'

/** router.ts 单行里的 `path: 'X', component: YyyView`。 */
const ROUTE_TO_VIEW = new Map<string, string>()
for (const line of routerSrc.split('\n')) {
  const m = line.match(/path:\s*'([^']+)'.*?component:\s*([A-Za-z0-9_]+)/)
  if (m) ROUTE_TO_VIEW.set(m[1], m[2])
}
/** 组件名 → 懒加载的 .vue 路径。 */
const VIEW_TO_FILE = new Map<string, string>()
for (const m of routerSrc.matchAll(/const\s+([A-Za-z0-9_]+)\s*=\s*\(\)\s*=>\s*import\('([^']+)'\)/g)) {
  VIEW_TO_FILE.set(m[1], m[2])
}

const routerPaths = [...new Set([...routerSrc.matchAll(/path:\s*'([^']+)'/g)].map(m => m[1]))]
const navPaths = [...new Set([...navSrc.matchAll(/path:\s*'([^']+)'/g)].map(m => m[1]))]

/** 路径字面量 → 路由模式。分级：精确 > 参数归一 > 前缀（取最长）。 */
function resolveRoute(literal: string): string | undefined {
  const b = base(literal)
  const exact = routerPaths.find(r => base(r) === b)
  if (exact) return exact
  // **两侧都要归一**：只归一路由侧的 `:owner` 而放过链接侧的 `${owner}`，
  // 会让模板字面量永远匹配不上（本门第一版就栽在这里，报了 7 条假孤儿）。
  const dyn = (seg: string) => seg.startsWith(':') || seg.includes('(') || /^\$\{.*\}$/.test(seg)
  const strip = (s: string) => s.split('/').map(seg => (dyn(seg) ? '*' : seg)).join('/')
  const param = routerPaths.find(r => strip(base(r)) === strip(b))
  if (param) return param
  const cands = routerPaths.filter(r => {
    const rb = base(r)
    return b.startsWith(rb + '/') || rb.startsWith(b + '/')
  })
  if (!cands.length) return undefined
  return cands.sort((x, y) => base(y).length - base(x).length)[0]
}

const navCovers = (target: string) => {
  const t = base(target)
  return navPaths.some(p => {
    const b = base(p)
    return t === b || t.startsWith(b + '/')
  })
}

const corpus = collectSourceFiles(srcRoot).map(p => ({ file: p, text: readFileSync(p, 'utf8') }))

function linkedFromOtherView(target: string): boolean {
  const self = ROUTE_TO_VIEW.get(target)
  const selfFile = self ? VIEW_TO_FILE.get(self) : undefined
  return corpus.some(({ file, text }) => {
    if (selfFile && file.endsWith(selfFile.replace('./', ''))) return false // 排除自我背书
    return text.split('\n').some(line => NAV_CONTEXT.test(line) && resolveRouteFromLine(line, target))
  })
}

/** 该行是否包含指向 target 的路径字面量。 */
function resolveRouteFromLine(line: string, target: string): boolean {
  for (const m of line.matchAll(/['"`]([^'"`]*\/[^'"`]*)['"`]/g)) {
    if (resolveRoute(m[1]) === target) return true
  }
  return false
}

const orphans = routerPaths.filter(p => !navCovers(p) && !linkedFromOtherView(p))

describe('菜单可达性守卫（R85-C）', () => {
  it('扫描器未腐化：路由与菜单条目数在合理量级', () => {
    expect(routerPaths.length).toBeGreaterThanOrEqual(MIN_ROUTER_PATHS)
    expect(navPaths.length).toBeGreaterThanOrEqual(MIN_NAV_PATHS)
  })

  it('无未登记的孤儿路由', () => {
    const unlisted = orphans.filter(p => !(p in KNOWN_ORPHANS))
    expect(
      unlisted,
      `新增孤儿路由 ${unlisted.length} 条：\n` + unlisted.map(p => `  ${p}`).join('\n') +
        '\n请在 appNav.ts 挂载菜单入口，或在 KNOWN_ORPHANS 登记并写明具体理由。',
    ).toEqual([])
  })

  it('白名单条目全部有效：每条仍存在于 router.ts', () => {
    const stale = Object.keys(KNOWN_ORPHANS).filter(p => !routerPaths.includes(p))
    expect(stale, `白名单已失效（路由已删除），应移除：${stale.join(', ')}`).toEqual([])
  })

  it('白名单理由必须具体（≥20 字符，不接受「同上」式敷衍）', () => {
    const weak = Object.entries(KNOWN_ORPHANS)
      .filter(([, reason]) => reason.trim().length < 20)
      .map(([p, r]) => `${p} → "${r}"（${r.trim().length} 字符）`)
    expect(weak, '白名单理由过短，等于没有约束：\n' + weak.join('\n')).toEqual([])
  })

  it('白名单不得凭空登记未被门覆盖的路由（防止门被整体绕过）', () => {
    // 若有人把 nav 覆盖判据改成恒真，orphans 会整体变空、白名单全体失效。
    // 这条断言确保白名单与实际孤儿集合保持同源。
    const listedButNotOrphan = Object.keys(KNOWN_ORPHANS).filter(p => !orphans.includes(p))
    expect(
      listedButNotOrphan,
      `以下白名单条目已不再是孤儿（可能已被菜单或视图链接覆盖），应移除：\n` +
        listedButNotOrphan.map(p => `  ${p}`).join('\n'),
    ).toEqual([])
  })

  it('菜单项无死链：每个 nav path 都能落到某个 router 路径', () => {
    const dead = navPaths.filter(p => {
      const b = base(p)
      return !routerPaths.some(r => {
        const rb = base(r)
        return rb === b || rb.startsWith(b + '/') || b.startsWith(rb + '/')
      })
    })
    expect(dead, `菜单死链 ${dead.length} 条：${dead.join(', ')}`).toEqual([])
  })
})
