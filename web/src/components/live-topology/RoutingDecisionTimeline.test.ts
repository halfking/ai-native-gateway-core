// RoutingDecisionTimeline.test.ts — 路由判定折叠/展开判据（2026-11-06）
//
// ★ 核心不变量：
//  1. 默认展开：判定时间线是排查的主视图，收起它=隐藏用户主动要看的东西；
//  2. 标题即按钮（StatsPanel 同族）：点击切换折叠，aria-expanded 与
//     caret 旋转态同步，可访问性不能掉；
//  3. 折叠收掉的是「内容」不是「入口」：列表与空态文案都随折叠消失，
//     但标题按钮常驻 —— 折叠后必须还能点回来；
//  4. 「空数据」与「折叠」是两件事：有数据时折叠隐藏列表；无数据时
//     空态文案只在展开态渲染（折叠态不渲染任何内容行）。

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import RoutingDecisionTimeline from './RoutingDecisionTimeline.vue'
import zhCN from '../../locales/zh-CN/liveTopology'

// 用真实的 zh-CN 文案（组件走的是 useI18n() 组合式 API）—— 缺键当场红。
const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

function evt(over: Record<string, unknown> = {}) {
  return {
    seq: 1,
    action: 'dispatch',
    stage_category: 'llm',
    ...over,
  } as never
}

function mountTimeline(decisions: unknown[]) {
  return mount(RoutingDecisionTimeline, {
    props: { decisions: decisions as never[] },
    global: { plugins: [i18n] },
  })
}

describe('RoutingDecisionTimeline 折叠/展开', () => {
  it('默认展开：列表可见、toggle 的 aria-expanded 为 true', () => {
    const w = mountTimeline([evt()])
    expect(w.find('[data-testid="decisions-toggle"]').exists()).toBe(true)
    expect(w.find('[data-testid="decisions-toggle"]').attributes('aria-expanded')).toBe('true')
    expect(w.find('.decisions__list').exists()).toBe(true)
    expect(w.findAll('.decisions__row').length).toBe(1)
  })

  it('点击折叠：列表消失、aria-expanded=false；再点展开恢复列表', async () => {
    const w = mountTimeline([evt(), evt({ seq: 2, action: 'resolve', stage_category: 'routing' })])
    const toggle = () => w.find('[data-testid="decisions-toggle"]')
    await toggle().trigger('click')
    expect(toggle().attributes('aria-expanded')).toBe('false')
    expect(w.find('.decisions__list').exists()).toBe(false)
    // 折叠态不渲染空态文案（那是「无数据」的形态，不是「收起」的形态）
    expect(w.find('.decisions__empty').exists()).toBe(false)
    // 标题按钮仍可点 —— 折叠收掉的是内容，不是入口
    await toggle().trigger('click')
    expect(toggle().attributes('aria-expanded')).toBe('true')
    expect(w.findAll('.decisions__row').length).toBe(2)
  })

  it('空数据 + 展开：渲染空态文案；折叠后空态也消失', async () => {
    const w = mountTimeline([])
    expect(w.find('.decisions__empty').exists()).toBe(true)
    expect(w.find('.decisions__list').exists()).toBe(false)
    await w.find('[data-testid="decisions-toggle"]').trigger('click')
    expect(w.find('.decisions__empty').exists()).toBe(false)
    expect(w.find('[data-testid="decisions-toggle"]').attributes('aria-expanded')).toBe('false')
  })

  it('i18n 键真实存在：collapse/expand 取得到（缺键会渲染字面键名，当场红）', async () => {
    const w = mountTimeline([evt()])
    const toggle = () => w.find('[data-testid="decisions-toggle"]')
    expect(toggle().attributes('aria-label')).toBe('收起判定时间线')
    await toggle().trigger('click')
    expect(toggle().attributes('aria-label')).toBe('展开判定时间线')
  })
})
