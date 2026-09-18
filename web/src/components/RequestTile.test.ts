import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import RequestTile from './RequestTile.vue'
import type { RequestTile as RequestTileType } from '../types/swimlane'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      dashboard: {
        liveStream: {
          probeDirect: '主动探测',
          probeGateway: '网关探测',
          probeScheduled: '周期探测',
          probeGeneric: '探测',
          tileIdle: '空闲',
        },
      },
    },
  },
})

function baseTile(overrides: Partial<RequestTileType> = {}): RequestTileType {
  return {
    request_id: 'req-probe-1',
    timestamp: new Date().toISOString(),
    model: 'glm-5.2',
    vendor: 'zhipu',
    provider: 'zhipu-official',
    status: 'failure',
    error_kind: '5xx',
    is_probe: true,
    probe_origin: 'direct',
    ...overrides,
  }
}

function mountTile(tile: RequestTileType) {
  return mount(RequestTile, {
    props: {
      tile,
      groupBy: 'vendor',
      isHighlighted: false,
      isDimmed: false,
      // 2026-08-06: force the large-card render path so the tests can assert
      // .request-tile__time / .request-tile__probe-badge selectors. The default
      // mode is 'small' (vertical bar in production); SwimLaneTrack always
      // passes mode='large' when rendering inside DashboardView.
      mode: 'large',
    },
    global: { plugins: [i18n] },
  })
}

describe('RequestTile display format', () => {
  it('renders time on the first row for normal tiles', () => {
    const wrapper = mountTile(baseTile({ is_probe: false, status: 'success' }))
    const time = wrapper.find('.request-tile__time')
    expect(time.exists()).toBe(true)
    expect(time.text()).toMatch(/\d{2}:\d{2}/)
  })

  it('renders radar probe badge in the top-left for probe tiles', () => {
    const wrapper = mountTile(baseTile())
    expect(wrapper.find('.request-tile__probe-badge').exists()).toBe(true)
    expect(wrapper.find('.request-tile__probe-icon').exists()).toBe(true)
    expect(wrapper.find('.request-tile__probe-badge').text()).not.toContain('T')
  })

  it('shows idle time on the first row and idle reason on the third row', () => {
    const fiveMinAgo = new Date(Date.now() - 6 * 60 * 1000).toISOString()
    const wrapper = mountTile(baseTile({
      is_probe: false,
      status: 'idle',
      error_kind: 'no_traffic_5min',
      timestamp: fiveMinAgo,
    }))
    expect(wrapper.find('.request-tile__time').exists()).toBe(true)
    expect(wrapper.find('.request-tile__reason--idle').exists()).toBe(true)
  })

  it('marks large in-progress tiles as routing vs llm by stage_category', () => {
    const routing = mountTile(baseTile({
      is_probe: false,
      status: 'in_progress',
      stage_category: 'routing',
    }))
    expect(routing.classes()).toContain('request-tile--routing')
    expect(routing.find('.request-tile__stage-indicator--routing').exists()).toBe(true)
    routing.unmount()

    const llm = mountTile(baseTile({
      is_probe: false,
      status: 'in_progress',
      stage_category: 'llm',
    }))
    expect(llm.classes()).toContain('request-tile--llm')
    expect(llm.find('.request-tile__stage-indicator--llm').exists()).toBe(true)
    llm.unmount()
  })

  it('marks small bars with routing / llm stage classes', () => {
    const routing = mount(RequestTile, {
      props: {
        tile: baseTile({ is_probe: false, status: 'in_progress', stage_category: 'routing' }),
        groupBy: 'vendor',
        isHighlighted: false,
        isDimmed: false,
        mode: 'small',
      },
      global: { plugins: [i18n] },
    })
    expect(routing.classes()).toContain('request-bar--routing')
    routing.unmount()

    const llm = mount(RequestTile, {
      props: {
        tile: baseTile({ is_probe: false, status: 'in_progress', stage_category: 'llm' }),
        groupBy: 'vendor',
        isHighlighted: false,
        isDimmed: false,
        mode: 'small',
      },
      global: { plugins: [i18n] },
    })
    expect(llm.classes()).toContain('request-bar--llm')
    llm.unmount()
  })
})

// ─── 2026-09-18: 缓存命中 + 会话 ID tooltip（实时请求流字段补齐） ───────────
describe('RequestTile cache & session tooltip', () => {
  const cacheI18n = createI18n({
    legacy: false,
    locale: 'zh-CN',
    messages: {
      'zh-CN': {
        dashboard: {
          liveStream: {
            legend: { success: '成功' },
            tooltip: {
              tokens: 'Token',
              tokenFormat: '{p} + {c}',
              cache: '缓存',
              cacheFormat: '读{r}/写{w}（命中率{pct}）',
              sessionId: '会话',
            },
          },
        },
      },
    },
  })

  function mountLarge(tile: RequestTileType) {
    return mount(RequestTile, {
      props: { tile, groupBy: 'model', isHighlighted: false, isDimmed: false, mode: 'large' },
      global: { plugins: [cacheI18n] },
    })
  }

  it('renders cache read/write tokens and hit ratio (read/(read+prompt))', () => {
    const w = mountLarge(baseTile({
      is_probe: false, status: 'success',
      prompt_tokens: 738, completion_tokens: 210,
      cache_read_tokens: 1200, cache_write_tokens: 340,
      gw_session_id: 'gw_cache_case_1',
    }))
    const title = w.attributes('title') ?? ''
    // 1200 / (1200 + 738) = 61.9% → 62%
    expect(title).toContain('1200')
    expect(title).toContain('340')
    expect(title).toContain('62%')
    expect(title).toContain('gw_cache_case_1')
    w.unmount()
  })

  it('omits cache/session lines when fields are not reported (never fakes zeros)', () => {
    const w = mountLarge(baseTile({ is_probe: false, status: 'success', prompt_tokens: 5, completion_tokens: 2 }))
    const title = w.attributes('title') ?? ''
    expect(title).not.toContain('缓存')
    expect(title).not.toContain('会话')
    expect(title).not.toContain('0%')
    w.unmount()
  })

  it('renders cache line without ratio when denominator is zero', () => {
    const w = mountLarge(baseTile({
      is_probe: false, status: 'success',
      cache_read_tokens: 0, cache_write_tokens: 4, gw_session_id: 'gw_zero_read',
    }))
    const title = w.attributes('title') ?? ''
    expect(title).toContain('缓存')
    expect(title).toContain('—')
    expect(title).toContain('gw_zero_read')
    w.unmount()
  })

  // R44: prompt_tokens 缺省时不得把缺省当 0 —— 只报 cache_read 的上游会
  // 冒充 100% 命中率，与"缺省不冒充"承诺同源。
  it('renders cache line without ratio when prompt_tokens is missing (never fakes 100%)', () => {
    const w = mountLarge(baseTile({
      is_probe: false, status: 'success',
      cache_read_tokens: 500, cache_write_tokens: 0, gw_session_id: 'gw_no_prompt',
    }))
    const title = w.attributes('title') ?? ''
    expect(title).toContain('缓存')
    expect(title).toContain('—')
    expect(title).not.toContain('100%')
    w.unmount()
  })
})
