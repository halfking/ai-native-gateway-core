// SessionAnalyticsUserDetailView.tabState.spec.ts —— L16「端点 500 被伪装成空态」的钉测
//
// ★ 背景（2026-11-23 实测，见 docs/audit/2026-11-20-会话分析系统前端实施报告.md §7.11/§7.14）：
//   生产上 `/user-ranking/:owner/api-keys` 返回 **500**（PG /dev/shm 不足 ⇒ SQLSTATE 53100），
//   而**页面上看到的是「暂无数据」空态** ⇒ 用户完全看不出后端挂了。
//
// ★ 根因是**两半**，缺一不可（所以本钉测也断两条）：
//   ① **tab 级 loader 的 error/stale 没有出口** —— 页面级 `error` 绑定的是
//      `detailLoader.error`，tab 自己的 loader 报错时**没有任何横幅承接**；
//   ② **`:empty` 条件没排除错误态** —— 加载失败后 `loading=false`、数据数组为空
//      ⇒ `empty=true` ⇒ 渲染「暂无数据」。
//   ⇒ ★ 只修 ① ⇒ 横幅下面还挂着「暂无数据」，自相矛盾；只修 ② ⇒ 表格全空，用户更懵。
//
// ★ 为什么用**源码契约门**而不是组件渲染测试：
//   本条的判据是「**空态的判定条件里有没有排除错误态**」—— 这需要读**表达式**，
//   渲染测试只能看到最终 DOM（横幅在、空态也在，DOM 里两者并存 ⇒ 分不清哪个是缺陷）。
//   ⇒ 所以：★ 组件测试断「渲染出了什么」，源码契约门断「**判据写对了没有**」。
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const REPO = resolve(process.cwd(), '..')
const VIEW = join(REPO, 'web/src/views/admin/session-analytics/SessionAnalyticsUserDetailView.vue')
const src = readFileSync(VIEW, 'utf8')

/** 四个 tab 的数据数组名（每个 tab 各有一个 loader + 一个数组）。 */
const TAB_ARRAYS = ['projects', 'tasks', 'models', 'apiKeys'] as const

describe('L16 · 详情页 tab 级错误态不得被渲染成「暂无数据」', () => {
  it('① 页面必须按当前 tab 暴露 tab 级 error / stale', () => {
    // tab 级 loader 的 error/stale 必须有出口 —— 否则端点 500 时页面上什么都不显示。
    expect(src, '缺 tabLoader（按当前 tab 取它自己的 loader）').toContain('tabLoader')
    expect(src, '缺 tabError（tab 级错误态）').toContain('tabError')
    expect(src, '缺 tabStale（tab 级陈旧态）').toContain('tabStale')
    // ★ 四个 tab 都要被 tabLoader 覆盖 ⇒ 漏一个 ⇒ 那个 tab 的 500 仍然被伪装成空态。
    for (const t of TAB_ARRAYS) {
      expect(src, `tabLoader 未覆盖 tab=${t}`).toMatch(new RegExp(`case '${t}':`))
    }
  })

  it('② 每个 tab 的 `:empty` 条件必须排除错误态与陈旧态', () => {
    // ★ 核心判据：`:empty` 里必须同时出现 `!tabError` 与 `!tabStale`。
    //   少了任一个 ⇒ 加载失败后仍会渲染「暂无数据」⇒ 500 继续被伪装成无数据。
    const empties = [...src.matchAll(/:empty="([^"]*)"/g)].map((m) => m[1])
    expect(empties.length, '没找到任何 :empty 条件（模板结构变了？）').toBeGreaterThanOrEqual(4)
    for (const t of TAB_ARRAYS) {
      const cond = empties.find((c) => c.includes(`${t}.length === 0`))
      expect(cond, `tab ${t} 的 :empty 条件没找到`).toBeTruthy()
      expect(cond, `tab ${t} 的 :empty 未排除错误态 ⇒ 500 会被渲染成「暂无数据」`).toContain('!tabError')
      expect(cond, `tab ${t} 的 :empty 未排除陈旧态`).toContain('!tabStale')
    }
  })

  it('③ tab 级错误/陈旧态必须渲染出可见的横幅', () => {
    // 横幅是「让用户看出后端挂了」的载体 ⇒ 必须真的在模板里。
    const banners = [...src.matchAll(/<SaStateBanner\b([^>]*)>/g)].map((m) => m[1])
    const errorBanner = banners.find((b) => b.includes('tabError'))
    expect(errorBanner, '模板里没有按 tabError 渲染的错误横幅').toBeTruthy()
    expect(errorBanner, '错误横幅的 kind 必须是 error').toContain('kind="error"')
    const staleBanner = banners.find((b) => b.includes('tabStale'))
    expect(staleBanner, '模板里没有按 tabStale 渲染的陈旧横幅').toBeTruthy()
    expect(staleBanner, '陈旧横幅的 kind 必须是 stale').toContain('kind="stale"')
  })

  it('④ ★ 零新增 i18n 键：横幅文案必须复用既有键', () => {
    // ★ 本轮不引入任何新 locale 键 ⇒ 横幅只能引用**已存在**的键。
    //   （新增键要动 8 个语种资产，且 i18n 棘轮/CJK 计数门都会跟着响。）
    for (const key of ['sessionAnalytics.error.userDetailFailed', 'sessionAnalytics.action.retry']) {
      expect(src, `横幅未复用既有键 ${key}`).toContain(key)
    }
  })
})
