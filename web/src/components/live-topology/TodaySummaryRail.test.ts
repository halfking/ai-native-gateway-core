// TodaySummaryRail.test.ts — 左栏「当天汇总」（供应商+模型两组）判据
// （2026-10-28 当天汇总轮；由 ProviderTodayRail.test.ts 升级迁移而来）
//
// ★ 核心不变量（沿用 2026-10-16 轮）：**「统计不可用」与「当天 0 请求」
// 是两件事**。聚合表未迁移 / 端点失败时必须渲染成不可用横幅，绝不能
// 渲染成一张全是 0 的表（同 2026-10-03 抽屉降级教训）。
// ★ 本轮新增：两组独立降级（一组挂另一组照常）；组可折叠（折叠后组头
// 合计仍在）；模型组点行联动 models 过滤；缓存数在行内与组头可见。

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import TodaySummaryRail from './TodaySummaryRail.vue'
import { getProvidersToday, getTopologyModelsToday } from '../../api/topology'
import zhCN from '../../locales/zh-CN/liveTopology'

vi.mock('../../api/topology', () => ({
  getProvidersToday: vi.fn(),
  getTopologyModelsToday: vi.fn(),
}))

const mockedProviders = vi.mocked(getProvidersToday)
const mockedModels = vi.mocked(getTopologyModelsToday)

// 用真实的 zh-CN 文案（组件走的是 useI18n() 组合式 API，$t mock 不生效）
const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

type RailProps = { activeProvider?: string; activeModel?: string }

// 后端 totals 是全字段返回的；夹具也写全，避免测试里出现「半个信封」
// 这种真实线上不会发生的形状（缺字段时组件行为与线上不一致 = 假绿）。
function totals(over: Partial<Record<string, number>> = {}) {
  return {
    requests: 0, success: 0, failure: 0, rate_limited: 0,
    prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0,
    cache_write_tokens: 0, total_tokens: 0, credits_charged: 0, cost_usd: 0,
    ...over,
  } as never
}

function modelItem(over: Record<string, unknown> = {}) {
  return {
    canonical_id: 1, canonical_name: 'glm-4.5', raw_model_name: '',
    requests: 100, success: 98, failure: 2, rate_limited: 0,
    prompt_tokens: 10, completion_tokens: 5, cache_read_tokens: 40, cache_write_tokens: 10,
    total_tokens: 15, credits_charged: 1, cost_usd: 0.1, share: 1,
    ...over,
  } as never
}

function mountRail(props: RailProps = {}) {
  return mount(TodaySummaryRail, { props: props as never, global: { plugins: [i18n] } })
}

describe('TodaySummaryRail 供应商组（迁移自 ProviderTodayRail）', () => {
  beforeEach(() => {
    mockedProviders.mockReset()
    mockedModels.mockReset()
    mockedModels.mockResolvedValue({ day_utc: '2026-10-16', items: [] })
  })

  it('renders the degraded banner — not a zero-filled table — when aggregation is unavailable', async () => {
    mockedProviders.mockResolvedValue({ degraded: true, error_code: 'STATS_NOT_MIGRATED', items: [] })
    const w = mountRail()
    await flushPromises()

    expect(w.find('[data-testid="rail-group-providers"] .rail__degraded').exists()).toBe(true)
    expect(w.find('[data-testid="rail-group-providers"] .rail__list').exists()).toBe(false)
  })

  it('renders the degraded banner when the endpoint itself fails', async () => {
    mockedProviders.mockRejectedValue(new Error('503'))
    const w = mountRail()
    await flushPromises()

    expect(w.find('[data-testid="rail-group-providers"] .rail__degraded').exists()).toBe(true)
    expect(w.text()).toContain('统计服务不可用')
  })

  it('renders real rows plus the data watermark when the day bucket is available', async () => {
    mockedProviders.mockResolvedValue({
      day_utc: '2026-10-16',
      source_high_watermark: new Date('2026-10-16T11:59:00Z').toISOString(),
      rollup_cadence: '1h',
      totals: totals({ requests: 1291, total_tokens: 2234000 }),
      items: [
        {
          provider_id: 12, catalog_code: 'deepseek', display_name: 'DeepSeek', enabled: true, known: true,
          requests: 1284, success: 1270, failure: 14, rate_limited: 3,
          prompt_tokens: 1, completion_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0,
          total_tokens: 2234000, credits_charged: 42, cost_usd: 3.5, share: 0.99,
        },
      ],
    })
    const w = mountRail()
    await flushPromises()

    expect(w.find('.rail__degraded').exists()).toBe(false)
    expect(w.findAll('[data-testid="rail-group-providers"] .rail__row')).toHaveLength(1)
    expect(w.text()).toContain('DeepSeek')
    expect(w.text()).toContain('2.23M')
    // 水位是「数据可能滞后 ≤1h」的显式提示，不是脚注
    expect(w.text()).toContain('数据水位')
  })

  it('converges to the selected provider only (left rail shares the graph filter)', async () => {
    mockedProviders.mockResolvedValue({
      day_utc: '2026-10-16',
      totals: totals({ requests: 2000, total_tokens: 10 }),
      items: [
        { provider_id: 12, catalog_code: 'deepseek', display_name: 'DeepSeek', enabled: true, known: true, requests: 1000, success: 1000, failure: 0, rate_limited: 0, prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, total_tokens: 5, credits_charged: 0, cost_usd: 1, share: 0.5 },
        { provider_id: 13, catalog_code: 'moonshot', display_name: 'Moonshot', enabled: true, known: true, requests: 1000, success: 1000, failure: 0, rate_limited: 0, prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, total_tokens: 5, credits_charged: 0, cost_usd: 1, share: 0.5 },
      ],
    })
    const w = mountRail({ activeProvider: 'deepseek' })
    await flushPromises()

    const rows = w.findAll('[data-testid="rail-group-providers"] .rail__row')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('DeepSeek')
  })

  it('shows an empty state only when the day bucket is genuinely empty', async () => {
    mockedProviders.mockResolvedValue({ day_utc: '2026-10-16', totals: totals(), items: [] })
    const w = mountRail()
    await flushPromises()

    expect(w.find('[data-testid="rail-group-providers"] .rail__empty').exists()).toBe(true)
    expect(w.find('[data-testid="rail-group-providers"] .rail__degraded').exists()).toBe(false)
  })
})

describe('TodaySummaryRail 模型组 + 折叠（2026-10-28 当天汇总轮）', () => {
  beforeEach(() => {
    mockedProviders.mockReset()
    mockedModels.mockReset()
    mockedProviders.mockResolvedValue({ day_utc: '2026-10-16', totals: totals(), items: [] })
  })

  it('★ groups degrade independently: one failing endpoint must not blank the other group', async () => {
    mockedModels.mockRejectedValue(new Error('503'))
    const w = mountRail()
    await flushPromises()

    // 模型组显示「统计服务不可用」；供应商组不受牵连（走「真没有流量」
    // 分支，不是降级）
    expect(w.find('[data-testid="rail-group-models"] .rail__degraded').exists()).toBe(true)
    expect(w.find('[data-testid="rail-group-providers"] .rail__degraded').exists()).toBe(false)
    expect(w.find('[data-testid="rail-group-providers"] .rail__empty').exists()).toBe(true)
  })

  it('★ collapses the models group by default; the group-head summary stays visible', async () => {
    mockedModels.mockResolvedValue({
      day_utc: '2026-10-16',
      source_high_watermark: new Date('2026-10-16T11:59:00Z').toISOString(),
      items: [modelItem({ requests: 900, total_tokens: 1234000, cache_read_tokens: 8000, cache_write_tokens: 2000 })],
    })
    const w = mountRail()
    await flushPromises()

    const modelsGroup = w.find('[data-testid="rail-group-models"]')
    // 默认收起：行不渲染，但组头合计（请求 · token · 缓存）还在
    expect(modelsGroup.find('.rail__list').exists()).toBe(false)
    expect(modelsGroup.find('.rail__group-sum').text()).toContain('900')
    expect(modelsGroup.find('.rail__group-sum').text()).toContain('1.23M')
    expect(modelsGroup.find('.rail__group-sum').text()).toContain('缓存')
    // aria-expanded 如实反映折叠态
    expect(modelsGroup.find('.rail__group-head').attributes('aria-expanded')).toBe('false')
  })

  it('★ expands on group-head click and renders model rows with cache counts', async () => {
    mockedModels.mockResolvedValue({
      day_utc: '2026-10-16',
      items: [
        modelItem({ canonical_name: 'glm-4.5', requests: 900, cache_read_tokens: 8000, cache_write_tokens: 2000 }),
        modelItem({ canonical_id: 2, canonical_name: 'deepseek-chat', requests: 300, cache_read_tokens: 0, cache_write_tokens: 0 }),
      ],
    })
    const w = mountRail()
    await flushPromises()

    await w.find('[data-testid="rail-group-models"] .rail__group-head').trigger('click')
    expect(w.find('[data-testid="rail-group-models"] .rail__group-head').attributes('aria-expanded')).toBe('true')

    const rows = w.findAll('[data-testid="rail-group-models"] .rail__row')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('glm-4.5')
    // 有缓存量的行显示「缓存 N」；无缓存量的行不显示「缓存 —」（假零禁令：
    // 0 是真值可以不标，缺省才用 —；这里 cache=0 是真没有，干脆不出现该段）
    expect(rows[0].text()).toContain('缓存')
    expect(rows[0].text()).toContain('10.0K')
    expect(rows[1].text()).not.toContain('缓存')
  })

  it('★ model rows toggle the graph model filter (select-model event)', async () => {
    mockedModels.mockResolvedValue({
      day_utc: '2026-10-16',
      items: [modelItem({ canonical_name: 'glm-4.5' })],
    })
    const w = mountRail()
    await flushPromises()
    await w.find('[data-testid="rail-group-models"] .rail__group-head').trigger('click')

    await w.findAll('[data-testid="rail-group-models"] .rail__row-btn')[0].trigger('click')
    expect(w.emitted('select-model')?.[0]).toEqual(['glm-4.5'])
  })

  it('falls back to raw_model_name when canonical_name is empty (same口径 as the graph modelKeyOf)', async () => {
    mockedModels.mockResolvedValue({
      day_utc: '2026-10-16',
      items: [modelItem({ canonical_name: '', raw_model_name: 'vendor-raw-name' })],
    })
    const w = mountRail()
    await flushPromises()
    await w.find('[data-testid="rail-group-models"] .rail__group-head').trigger('click')

    expect(w.findAll('[data-testid="rail-group-models"] .rail__row')[0].text()).toContain('vendor-raw-name')
  })
})
