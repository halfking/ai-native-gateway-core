import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { TitleResolver } from '@/hyper/navigation/titleResolver'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'

/**
 * 深链直达受限页时 **`document.title` 会泄露受限页面的真名**（第一百三十四批）。
 *
 * ## 怎么测出来的（先量后改，不是读代码猜的）
 *
 * `hyper/navigation/titleResolver.ts` 的优先级链是「命中即停」：
 * `overlay → page(registered) → route meta → appName`，
 * 而 `hyper/runtime.ts:189-191` 把 `routeTitle` 取自 `to.meta.titleKey`。
 *
 * ⇒ **两条独立来源**，而 15 个受限视图的 `useHyperPage({ title: () => t('x.title') })`
 *   是**无条件**的（`Hyper.registerPage` 在 `onMounted` 里跑，与 `allowed` 无关）。
 *
 * 实测（下面第 ⑦ 组把量到的四条读数钉住）：
 *   A 注册真名            ⇒ 「请求侧异常」 ← registered
 *   B 注册**空串**        ⇒ 「请求侧异常」 ← **route**   ← ★ 空串堵不住！
 *   C 注册「无权访问此页」 ⇒ 「无权访问此页」            ← ★ 这一路能堵
 *
 * ⇒ 修法：`title: () => (allowed.value ? t('x.title') : t('common.pageDenied'))`
 *   —— 注册一个**非空**的兜底标题，`registered` 那一档就永远轮不到 `route meta`。
 *
 * ⚠️⚠️ 而这正是**壳层缺口**唯一的实测代价：
 *   `hyper-wiring.ts` 的 `beforeEach` 不消费 `meta.requiresRole`，
 *   所以路由照样解析、视图照样挂载 —— 批 130~133 给 15 个面补的自身兜底
 *   挡住了**内容**，但挡不住**页面名字**。
 *   ⇒ 本批用**视图层**堵掉它，不去改壳层的导航语义（那是口径决策，另议）。
 */

/** 当前全族：18 条带 `requiresRole` 的路由 → 视图 → 标题键（批150 扩两席：format-anomalies / model-integrity-drift）。 */
const FAMILY: ReadonlyArray<{ path: string; view: string; key: string }> = [
  { path: '/sanitize-rules', view: 'SanitizeRulesView', key: 'sr.title' },
  { path: '/request-anomalies', view: 'RequestAnomaliesView', key: 'anomalies.title' },
  { path: '/errors-trend', view: 'ErrorsTrendView', key: 'et.title' },
  { path: '/model-history', view: 'ModelHistoryView', key: 'mh.title' },
  { path: '/sliding-window', view: 'SlidingWindowView', key: 'sw.title' },
  { path: '/model-iq', view: 'ModelIQView', key: 'miq.title' },
  { path: '/ops-overview', view: 'OpsOverviewView', key: 'ops.title' },
  { path: '/request-actions', view: 'RequestActionsView', key: 'ra.title' },
  { path: '/subscription-tiers', view: 'SubscriptionTiersView', key: 'sts.title' },
  { path: '/platform-settings', view: 'PlatformSettingsView', key: 'ps.title' },
  { path: '/usage-enhanced', view: 'UsageEnhancedView', key: 'ue.title' },
  { path: '/data-lifecycle-storage', view: 'DataLifecycleStorageView', key: 'dls.title' },
  { path: '/storage-log-config', view: 'StorageAndLogConfigView', key: 'slc.title' },
  { path: '/storage-migration-state', view: 'StorageMigrationStateView', key: 'sms.title' },
  { path: '/turns-filter-options', view: 'TurnsFilterOptionsView', key: 'tfo.title' },
  { path: '/session-analytics-filter-options', view: 'SessionAnalyticsFilterOptionsView', key: 'saf.title' },
  { path: '/format-anomalies', view: 'FormatAnomaliesView', key: 'fa.title' },
  { path: '/model-integrity-drift', view: 'ModelIntegrityDriftView', key: 'mid.title' },
]

const src = (p: string): string => readFileSync(join(process.cwd(), p), 'utf8')

// ── ⑦ 实测出来的契约（把量到的读数钉住，别让它悄悄变）─────────────────────

describe('⑦ ★★★★★★ titleResolver 优先级链：实测契约', () => {
  const mk = (registered: string | undefined, routeTitle: string | undefined) =>
    new TitleResolver({
      getPage: () => (registered === undefined ? null : { id: 'page-1', title: () => registered }),
      getOverlays: () => [],
      getRouteTitle: () => routeTitle,
      getAppName: () => 'LLM Gateway',
    })

  it('⑦-1 ★★★★★ A 无条件注册真名 ⇒ 真名胜出（这就是泄漏本身）', () => {
    expect(mk('请求侧异常', '请求侧异常').resolve()).toMatchObject({
      title: '请求侧异常',
      source: 'registered',
    })
  })

  it('⑦-2 ★★★★★★ B 注册**空串** ⇒ 落到 route meta（★ 空字符串堵不住）', () => {
    // ★★ 这条是修法形状的**依据**：若以为「返回空串就行」，
    //   实测会落到 route 那一档，页面真名照样显示。
    expect(mk('', '请求侧异常').resolve()).toMatchObject({ title: '请求侧异常', source: 'route' })
  })

  it('⑦-3 ★★★★★★ C 注册「无权访问」⇒ 真名不再出现在结果里', () => {
    const out = mk('无权访问此页', '请求侧异常').resolve()
    expect(out.title).toBe('无权访问此页')
    expect(out.title).not.toContain('请求侧异常')
  })

  it('⑦-4 ★★★ D 两个来源都空 ⇒ 落到应用名（最坏不退化成空白）', () => {
    expect(mk(undefined, undefined).resolve()).toMatchObject({ source: 'document' })
  })
})

// ── 全族普查 ───────────────────────────────────────────────────────────────

describe('⑧ ★★★★★★ 16 个受限视图的标题必须随 `allowed` 切换', () => {
  it('⑧-1 ★★★★★★ 逐条核对（★ 本批真正的产出）', () => {
    // ★ 先断言真的扫到了东西 —— 否则下面的循环是恒真的
    expect(FAMILY.length).toBeGreaterThan(10)

    const bad: string[] = []
    for (const m of FAMILY) {
      const code = src(`src/views/${m.view}.vue`)
      const want = `useHyperPage({ title: () => (allowed.value ? t('${m.key}') : t('common.pageDenied')) })`
      if (!code.includes(want)) {
        const unguarded = `useHyperPage({ title: () => t('${m.key}') })`
        bad.push(
          code.includes(unguarded)
            ? `${m.path} → ${m.view}：标题**无条件**取真名（无权限深链时 document.title 泄露页面名）`
            : `${m.path} → ${m.view}：没找到期望的标题写法（既不是新写法也不是旧写法）`,
        )
      }
    }
    expect(bad, `这些受限视图的标题没有随权限切换：\n  ${bad.join('\n  ')}`).toEqual([])
  })

  it('⑧-2 ★★★★★★ 路由 meta 的 titleKey 必须与视图的有权标题是同一个键', () => {
    // 换句话说：兜底不是另编一个名字，而是「有权时那个真名」和「无权时那个提示」。
    const router = src('src/router/index.ts')
    for (const m of FAMILY) {
      const re = new RegExp(`path: '${m.path}',[\\s\\S]{0,400}?titleKey: '${m.key.replace('.', '\\.')}'`)
      expect(router, `${m.path} 的 meta.titleKey 与视图标题键不一致`).toMatch(re)
    }
  })

  it('⑧-3 ★★★★★ common.pageDenied 两语种都在、值 ≠ 键名、且**不含**任何真页面名', () => {
    // ⚠️⚠️ 第一版这里把**提示语当成了期望值**（`expect(cond).toBe('提示语')`），
    //   于是拿 `false === '提示语'` 去比 —— 判据自己写错，不是数据有问题。
    //   ⇒ vitest 的第二参是 `expect(value, message)`，**不是** `toBe(value, message)`。
    const dicts: ReadonlyArray<readonly [string, Record<string, unknown>]> = [
      ['zh-CN', zhCN as unknown as Record<string, unknown>],
      ['en-US', enUS as unknown as Record<string, unknown>],
    ]
    const pick = (root: Record<string, unknown>, key: string): string | undefined => {
      const [a, b] = key.split('.')
      const mid = (root as Record<string, Record<string, unknown>>)[a!] as
        | Record<string, string>
        | undefined
      return mid?.[b!]
    }

    for (const [loc, dict] of dicts) {
      const denied = (dict as Record<string, Record<string, string>>).common?.pageDenied
      expect(typeof denied === 'string' && denied.trim().length > 0, `${loc} 缺 common.pageDenied`).toBe(true)
      expect(denied, `${loc} 的 pageDenied 值 === 键名（等于没翻译）`).not.toBe('common.pageDenied')

      // ★ 兜底标题**不得**包含任何一席的真实页面名 —— 否则换汤不换药：
      //   无权的人仍然能从 document.title 读出「这一页叫什么」。
      for (const m of FAMILY) {
        const real = pick(dict, m.key)
        expect(typeof real === 'string' && real.length > 0, `${loc} 取不到 ${m.key}`).toBe(true)
        expect(
          denied!.includes(real!),
          `${loc} 的 pageDenied「${denied}」里带了 ${m.path} 的真页面名「${real}」`,
        ).toBe(false)
      }
    }
  })

  it('⑧-4 ★★★★ 批 150 合上壳层缺口：beforeEach 消费 `requiresRole`（不再未消费）', () => {
    // ★ 批 150：本批**改了壳层**，合上"meta.requiresRole 全仓无人消费"的缺口。
    //   hyper-wiring.ts 调 `routeAllowedFor(to.path, auth.role)`，不匹配就跳 `/restricted`。
    // ⇒ 这条是反向钉：壳层**不**再"不消费 requiresRole"。
    //   同时钉住 `appNav.ts` 暴露了 `routeAllowedFor`（不只是内部用），
    //   避免有人把它"封装"回去让 hyper-wiring 拿不到。
    const wiring = src('src/hyper-wiring.ts')
    expect(wiring, 'beforeEach 必须真的调 routeAllowedFor ⇒ 路由层真消费 requiresRole').toMatch(/routeAllowedFor/)
    const appNav = src('src/config/appNav.ts')
    expect(appNav, 'routeAllowedFor 必须是 export 出来的公共函数（不能是内联）').toMatch(/export function routeAllowedFor/)
  })
})
