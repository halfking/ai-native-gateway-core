// ProviderTodayRail.test.ts — 左栏「供应商当天统计」判据（2026-10-16）
//
// ★ 核心不变量：**「统计不可用」与「当天 0 请求」是两件事**。
// 聚合表未迁移 / 端点 503 时必须渲染成不可用横幅，绝不能渲染成一张
// 全是 0 的表 —— 后者会让运维以为今天真的一单没有（同 2026-10-03
// 抽屉降级教训：降级视图缺失时「空列表」不是「没有数据」）。

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ProviderTodayRail from './ProviderTodayRail.vue'
import { getProvidersToday } from '../../api/topology'
import zhCN from '../../locales/zh-CN/liveTopology'

vi.mock('../../api/topology', () => ({ getProvidersToday: vi.fn() }))

const mocked = vi.mocked(getProvidersToday)

// 用真实的 zh-CN 文案（组件走的是 useI18n() 组合式 API，$t mock 不生效）
const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

type RailProps = { activeProvider?: string }

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

function mountRail(props: RailProps = {}) {
  return mount(ProviderTodayRail, { props: props as never, global: { plugins: [i18n] } })
}

describe('ProviderTodayRail', () => {
  beforeEach(() => {
    mocked.mockReset()
  })

  it('renders the degraded banner — not a zero-filled table — when aggregation is unavailable', async () => {
    mocked.mockResolvedValue({ degraded: true, error_code: 'STATS_NOT_MIGRATED', items: [] })
    const w = mountRail()
    await flushPromises()

    expect(w.find('.rail__degraded').exists()).toBe(true)
    expect(w.find('.rail__list').exists()).toBe(false)
    expect(w.text()).not.toContain('请求')
  })

  it('renders the degraded banner when the endpoint itself fails', async () => {
    mocked.mockRejectedValue(new Error('503'))
    const w = mountRail()
    await flushPromises()

    expect(w.find('.rail__degraded').exists()).toBe(true)
    expect(w.text()).toContain('统计服务不可用')
  })

  it('renders real rows plus the data watermark when the day bucket is available', async () => {
    mocked.mockResolvedValue({
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
    expect(w.findAll('.rail__row')).toHaveLength(1)
    expect(w.text()).toContain('DeepSeek')
    expect(w.text()).toContain('2.23M')
    // 水位是「数据可能滞后 ≤1h」的显式提示，不是脚注
    expect(w.text()).toContain('数据水位')
  })

  it('converges to the selected provider only (left rail shares the graph filter)', async () => {
    mocked.mockResolvedValue({
      day_utc: '2026-10-16',
      totals: totals({ requests: 2000, total_tokens: 10 }),
      items: [
        { provider_id: 12, catalog_code: 'deepseek', display_name: 'DeepSeek', enabled: true, known: true, requests: 1000, success: 1000, failure: 0, rate_limited: 0, prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, total_tokens: 5, credits_charged: 0, cost_usd: 1, share: 0.5 },
        { provider_id: 13, catalog_code: 'moonshot', display_name: 'Moonshot', enabled: true, known: true, requests: 1000, success: 1000, failure: 0, rate_limited: 0, prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, total_tokens: 5, credits_charged: 0, cost_usd: 1, share: 0.5 },
      ],
    })
    const w = mountRail({ activeProvider: 'deepseek' })
    await flushPromises()

    const rows = w.findAll('.rail__row')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('DeepSeek')
  })

  it('shows an empty state only when the day bucket is genuinely empty', async () => {
    mocked.mockResolvedValue({ day_utc: '2026-10-16', totals: totals(), items: [] })
    const w = mountRail()
    await flushPromises()

    expect(w.find('.rail__empty').exists()).toBe(true)
    expect(w.find('.rail__degraded').exists()).toBe(false)
  })
})
