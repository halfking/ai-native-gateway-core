// GatewayCoreBox.test.ts — 网关核心「大方块」判据（2026-10-17）
//
// 维护者要求：九个内部模块放进**一个大方块**表示一个整体。要钉住的是：
//  1. 九格都在，且落在框内；
//  2. **主链路 vs 旁路的视觉语义**分开 —— 错误记录/探测任务/任务识别是旁路，
//     画成实线会让人误读成「每个请求依次走完九步」；
//  3. 没有数据 = 0 且画成 idle，**不能**用 0 冒充「有流量但没命中」；
//  4. 点格子 emit 模块 key（父层据此做视觉聚焦）。

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import GatewayCoreBox from './GatewayCoreBox.vue'
import zhCN from '../../locales/zh-CN/liveTopology'
import { GATEWAY_BRANCH_MODULES, GATEWAY_MODULES, type GatewayModuleKey } from '../../types/liveTopology'
import { computeGatewayBox } from '../../lib/liveTopologyLayout'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

const BOX = computeGatewayBox(1200, 700)

function statsOf(values: Partial<Record<GatewayModuleKey, { count: number; errors: number }>> = {}) {
  const out = {} as Record<GatewayModuleKey, { count: number; errors: number }>
  for (const m of GATEWAY_MODULES) out[m.key] = values[m.key] ?? { count: 0, errors: 0 }
  return out
}

function mountBox(stats = statsOf(), selected: GatewayModuleKey | null = null) {
  return mount(GatewayCoreBox, { props: { box: BOX, stats, selected }, global: { plugins: [i18n] } })
}

describe('GatewayCoreBox', () => {
  it('renders exactly nine module cells inside one box', () => {
    const wrapper = mountBox()
    expect(wrapper.findAll('.gw__cell')).toHaveLength(9)
    expect(wrapper.find('.gw').attributes('aria-label')).toBe('网关核心')
    // 九个模块名都要出现（一个都不能靠色块暗示）
    const text = wrapper.text()
    for (const name of ['缓存', '脱敏', '路由', '节点状态管理', '会话数据文件存储', '数据会话写入', '错误记录', '探测任务', '任务识别']) {
      expect(text).toContain(name)
    }
  })

  it('dashes the bypass modules and keeps the main pipeline solid', () => {
    const wrapper = mountBox()
    const branchCells = wrapper.findAll('.gw__cell.is-branch')
    expect(branchCells).toHaveLength(3)
    expect(branchCells.map((c) => c.attributes('aria-label')?.split(' ')[0])).toEqual(
      ['错误记录', '探测任务', '任务识别'],
    )
    expect(GATEWAY_BRANCH_MODULES.size).toBe(3)
    expect(wrapper.findAll('.gw__cell:not(.is-branch)')).toHaveLength(6)
  })

  it('shows zero as idle rather than painting it as an active or failed stage', () => {
    const wrapper = mountBox(statsOf({ route: { count: 0, errors: 0 } }))
    const routeCell = wrapper.findAll('.gw__cell').find((c) => c.attributes('aria-label')?.startsWith('路由'))
    expect(routeCell?.classes()).toContain('is-idle')
    expect(routeCell?.classes()).not.toContain('is-error')
    expect(routeCell?.text()).toContain('0')
  })

  it('turns a cell red only when it has both volume and errors', () => {
    const wrapper = mountBox(statsOf({ errorRecord: { count: 3, errors: 2 }, cache: { count: 5, errors: 0 } }))
    const errorCell = wrapper.findAll('.gw__cell').find((c) => c.attributes('aria-label')?.startsWith('错误记录'))
    const cacheCell = wrapper.findAll('.gw__cell').find((c) => c.attributes('aria-label')?.startsWith('缓存'))
    expect(errorCell?.classes()).toContain('is-error')
    expect(cacheCell?.classes()).toContain('is-active')
    expect(cacheCell?.classes()).not.toContain('is-error')
  })

  it('marks the focused module without recoloring it', () => {
    const wrapper = mountBox(statsOf(), 'route')
    const routeCell = wrapper.findAll('.gw__cell').find((c) => c.attributes('aria-label')?.startsWith('路由'))
    expect(routeCell?.classes()).toContain('is-selected')
    // 聚焦是中性高亮，不是「故障红」
    expect(routeCell?.classes()).not.toContain('is-error')
  })

  it('emits the module key when a cell is clicked', async () => {
    const wrapper = mountBox()
    const cell = wrapper.findAll('.gw__cell').find((c) => c.attributes('aria-label')?.startsWith('缓存'))!
    await cell.trigger('click')
    expect(wrapper.emitted('select-module')?.[0]).toEqual(['cache'])
  })
})
