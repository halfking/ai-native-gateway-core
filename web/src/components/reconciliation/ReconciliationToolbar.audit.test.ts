// ReconciliationToolbar.audit.test.ts —— 筛选栏的「候选完整性 + 模型选择器
// 换成路由页同款 ModelPicker」（2026-11-16 审计轮）。
//
// 钉住三件事：
//  1. 模型筛选器是 ModelPicker（与 /routing-v2?tab=resolve 同一个组件），
//     且把「本区间有请求的模型」按请求数传进去（置顶分组的唯一数据源）。
//  2. 供应商候选里 -1（未落定）那一项显示人话「未落定」，不是裸的 "-1"。
//  3. 后端回报候选被截断时，页面**说出来**（旧实现按 500 静默砍）。

import { mount } from '@vue/test-utils'
import { ElOption } from 'element-plus'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it, vi } from 'vitest'
import zhCN from '../../locales/zh-CN'
import ReconciliationToolbar from './ReconciliationToolbar.vue'
import type { DimensionOptions, ReportFilter, ReportView } from '../../api/reportrollup'

const getAvailableModelsMock = vi.fn()
vi.mock('../../api', async () => {
  const actual = await vi.importActual<typeof import('../../api')>('../../api')
  return {
    ...actual,
    getAvailableModels: (...args: unknown[]) => getAvailableModelsMock(...args),
  }
})

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  messages: { 'zh-CN': zhCN },
})

const dims: DimensionOptions = {
  providers: [
    { key: '587', name: 'apiclaude', requests: 25300 },
    { key: '-1', requests: 38035 },
    { key: '95464', name: 'magpie', requests: 0 },
  ],
  credentials: [
    { key: '11', name: '凭据一', requests: 110 },
    { key: '-1', requests: 38035 },
  ],
  api_keys: [
    { key: '248', name: 'apikey-a', requests: 3 },
    { key: '-1', requests: 16 },
  ],
  models: [
    { key: 'MiniMax-M2.5', requests: 25300 },
    { key: 'glm-5.2', requests: 1200 },
  ],
  tenants: [],
  persons: [],
}

function mountToolbar(props: Partial<{
  filters: ReportFilter
  dims: DimensionOptions | null
}> = {}) {
  return mount(ReconciliationToolbar, {
    props: {
      view: 'provider' as ReportView,
      range: { start: '2026-11-09', end: '2026-11-15' },
      filters: { provider_id: undefined, model: undefined } as ReportFilter,
      dims: null,
      dimsLoading: false,
      loading: false,
      exporting: false,
      coverage: '',
      ...props,
    },
    global: { plugins: [i18n] },
  })
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('ReconciliationToolbar 候选完整性（2026-11-16 审计轮）', () => {
  it('模型筛选器是 ModelPicker，且带上了「本区间有请求的模型」清单', () => {
    getAvailableModelsMock.mockResolvedValue({
      families: [], popular: [], total_raw: 0, unmapped: [],
    })
    const wrapper = mountToolbar({ dims })
    // get() 在找不到时会直接抛错 ⇒ 断言「存在」就是「没抛错」。
    wrapper.get('[data-testid="filter-model-picker"]')

    const mp = wrapper.findComponent({ name: 'ModelPicker' })
    expect(mp.exists(), '筛选栏里没有 ModelPicker').toBe(true)
    const ranked = mp.props('requestRanked') as { name: string; requests: number }[]
    expect(ranked.map((r) => r.name)).toEqual(['MiniMax-M2.5', 'glm-5.2'])
    expect(ranked[0].requests).toBe(25300)
    expect(mp.props('requestRankedTitle')).toBeTruthy()
  })

  it('供应商候选：-1（未落定）显示人话，零流量供应商也在列表里', () => {
    const wrapper = mountToolbar({ dims })
    const options = wrapper.findAllComponents(ElOption)
    const labels = options.map((o) => String(o.props('label')))
    // 未落定那一桶（真库占全量流量三成）必须能被选中且看得懂。
    expect(labels.some((l) => l.includes('未落定'))).toBe(true)
    // 零流量供应商（配了却没流量）不能被藏起来。
    expect(labels.some((l) => l.includes('95464'))).toBe(true)
    const values = options.map((o) => o.props('value'))
    expect(values).toContain(-1)
  })

  it('候选被后端截断时页面必须披露（旧实现按 500 静默砍）', () => {
    const wrapper = mountToolbar({
      dims: { ...dims, truncated: { model: 37 } },
    })
    const html = wrapper.html()
    expect(html).toContain('37')
    expect(wrapper.text()).toContain('截断')
  })

  it('没有截断时不显示截断提示（阴性对照）', () => {
    const wrapper = mountToolbar({ dims })
    expect(wrapper.text()).not.toContain('截断')
  })
})

// ★ 2026-11-17 审计轮：凭据 / apikey 两个维度也各有「未落定」桶
// （真库实测凭据未落定 38,035 笔 = 全量 30.8%、apikey 未落定 16 笔）。
// 上一轮只给供应商配了「未落定」文案 ⇒ 另外两个维度的下拉显示裸的
// "-1 · 38,035"，对账人读不懂，也看不出这一行可以筛。
describe('ReconciliationToolbar 三个 id 维度的「未落定」文案（2026-11-17 审计轮）', () => {
  it('凭据与 apikey 下拉里的 -1 都显示人话「未落定」', () => {
    const wrapper = mountToolbar({ dims })
    const options = wrapper.findAllComponents(ElOption)
    const labels = options.map((o) => String(o.props('label')))
    const values = options.map((o) => o.props('value'))
    // 三个维度各有 1 个 -1 候选 ⇒ 共 3 个。
    expect(values.filter((v) => v === -1).length).toBe(3)
    // 三个 -1 都必须说人话，而不是裸 "-1 · N"。
    const unassigned = labels.filter((l) => l.includes('未落定'))
    expect(unassigned.length).toBe(3)
    expect(labels.some((l) => /^-1 ·/.test(l)), '仍有裸 -1 标签').toBe(false)
    // 请求数要跟着走（38,035 那一桶是凭据的未落定桶）。
    expect(labels.some((l) => l.includes('38,035'))).toBe(true)
  })
})
