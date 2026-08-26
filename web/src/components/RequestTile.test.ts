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
