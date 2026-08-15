// RequestTile lifecycle badges — 2026-08-15 OBS-FE3（26号 §3 / 24号 §1）
// component guards for the request-card enhancement:
//
//   1. stage 徽标：后端上报 stage 时渲染，缺省时不渲染（禁止猜测值冒充）。
//   2. retrySeq 角标：retrySeq >= 1 时显示 ⭐r{n}；retry=0 / 缺省不显示。
//   3. 子请求徽标与面板：计数来自 liveStreamStore 的 SSE child_request
//      索引；点击弹出列表面板；事件未到时面板为空态文案。
//
// 子请求数据通过 liveStreamStore.__testing.handleEnvelope 注入（仅消费
// store 导出，不修改 store、不调后端查询）。

import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { nextTick } from 'vue'
import { describe, expect, it, beforeEach } from 'vitest'
import RequestTile from './RequestTile.vue'
import type { RequestTile as RequestTileType } from '../types/swimlane'
import { __testing, type LiveRequest, type LiveStreamEnvelope } from '../composables/liveStreamStore'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      dashboard: {
        liveStream: {
          probeGeneric: '探测',
          tileIdle: '空闲',
          idleHeartbeat: '心跳',
        },
      },
    },
  },
})

function baseTile(overrides: Partial<RequestTileType> = {}): RequestTileType {
  return {
    request_id: 'req-main-1',
    timestamp: new Date().toISOString(),
    model: 'glm-5.2',
    vendor: 'zhipu',
    provider: 'zhipu-official',
    status: 'in_progress',
    ...overrides,
  }
}

function mountTile(tile: RequestTileType) {
  return mount(RequestTile, {
    props: {
      tile,
      groupBy: 'model',
      isHighlighted: false,
      isDimmed: false,
      // 大卡片（large）是徽标/面板的渲染路径
      mode: 'large',
    },
    attachTo: document.body,
    global: { plugins: [i18n] },
  })
}

/** Seed one SSE child_request frame into the store's child index. */
function pushChild(parentRequestId: string, child: Partial<LiveRequest>) {
  const request: LiveRequest = {
    request_id: child.request_id ?? `child-${Math.random().toString(36).slice(2, 8)}`,
    ts: child.ts ?? '2026-08-15T12:00:00Z',
    model: child.model,
    status: child.status,
    requestType: child.requestType,
  }
  __testing.handleEnvelope({
    type: 'child_request',
    ts: request.ts,
    parent_request_id: parentRequestId,
    request,
  } as unknown as LiveStreamEnvelope)
}

beforeEach(() => {
  __testing.resetStream()
})

describe('RequestTile stage badge (OBS-FE3)', () => {
  it('renders the reported stage when the backend supplied one', () => {
    const wrapper = mountTile(baseTile({ stage: 'forwarding' }))
    const badge = wrapper.find('.request-tile__stage-badge')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('forwarding')
    wrapper.unmount()
  })

  it('renders no stage badge when stage is absent (never guesses)', () => {
    const wrapper = mountTile(baseTile())
    expect(wrapper.find('.request-tile__stage-badge').exists()).toBe(false)
    wrapper.unmount()
  })

  it('renders no stage badge when stage is an empty string', () => {
    const wrapper = mountTile(baseTile({ stage: '' }))
    expect(wrapper.find('.request-tile__stage-badge').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('RequestTile retrySeq corner badge (OBS-FE3)', () => {
  it('shows the ⭐r{n} special mark when retrySeq >= 1', () => {
    const wrapper = mountTile(baseTile({ retrySeq: 2 }))
    const badge = wrapper.find('.request-tile__retry-badge')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toContain('⭐')
    expect(badge.text()).toContain('r2')
    wrapper.unmount()
  })

  it('hides the retry mark when retrySeq is 0 (first attempt)', () => {
    const wrapper = mountTile(baseTile({ retrySeq: 0 }))
    expect(wrapper.find('.request-tile__retry-badge').exists()).toBe(false)
    wrapper.unmount()
  })

  it('hides the retry mark when retrySeq is absent', () => {
    const wrapper = mountTile(baseTile())
    expect(wrapper.find('.request-tile__retry-badge').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('RequestTile children badge & panel (OBS-FE3)', () => {
  it('shows the SSE children count on the top-right badge', async () => {
    pushChild('req-main-1', { request_id: 'child-t1', requestType: 'title', status: 'success', model: 'glm-5.2' })
    pushChild('req-main-1', { request_id: 'child-s1', requestType: 'summary', status: 'in_progress', model: 'glm-5.2' })

    const wrapper = mountTile(baseTile())
    await nextTick()
    const badge = wrapper.find('.request-tile__children-badge')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('2')
    expect(badge.classes()).not.toContain('request-tile__children-badge--empty')
    wrapper.unmount()
  })

  it('opens a panel listing children (type chips T/S/SW) on badge click', async () => {
    pushChild('req-main-1', { request_id: 'child-t1', requestType: 'title', status: 'success', model: 'glm-5.2' })
    pushChild('req-main-1', { request_id: 'child-s1', requestType: 'summary', status: 'in_progress', model: 'glm-5.2' })
    pushChild('req-main-1', { request_id: 'child-sw1', requestType: 'sensitive_word', status: 'failure', model: 'glm-5.2' })

    const wrapper = mountTile(baseTile())
    await nextTick()

    // Panel lives in a Teleport at document.body — assert it is closed first.
    expect(document.querySelector('.request-tile__children-panel')).toBeNull()

    await wrapper.find('.request-tile__children-badge').trigger('click')
    await nextTick()

    const panel = document.querySelector('.request-tile__children-panel')
    expect(panel).not.toBeNull()
    const rows = panel!.querySelectorAll('.request-tile__child-row')
    expect(rows).toHaveLength(3)

    const chips = [...panel!.querySelectorAll('.request-tile__child-type')].map((el) => el.textContent?.trim())
    expect(chips).toEqual(['T', 'S', 'SW'])

    const statuses = [...panel!.querySelectorAll('.request-tile__child-status')].map((el) => el.textContent?.trim())
    expect(statuses).toEqual(['success', 'in_progress', 'failure'])
    wrapper.unmount()
  })

  it('shows the empty state copy when no child_request events arrived yet', async () => {
    const wrapper = mountTile(baseTile())
    await nextTick()

    const badge = wrapper.find('.request-tile__children-badge')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('0')
    expect(badge.classes()).toContain('request-tile__children-badge--empty')

    await badge.trigger('click')
    await nextTick()

    const panel = document.querySelector('.request-tile__children-panel')
    expect(panel).not.toBeNull()
    expect(panel!.querySelectorAll('.request-tile__child-row')).toHaveLength(0)
    expect(panel!.querySelector('.request-tile__children-empty')?.textContent)
      .toContain('暂无子请求事件推送')
    wrapper.unmount()
  })

  it('closes the panel via the close button and toggles aria-expanded', async () => {
    const wrapper = mountTile(baseTile())
    await nextTick()

    const badge = wrapper.find('.request-tile__children-badge')
    expect(badge.attributes('aria-expanded')).toBe('false')

    await badge.trigger('click')
    await nextTick()
    expect(document.querySelector('.request-tile__children-panel')).not.toBeNull()
    expect(wrapper.find('.request-tile__children-badge').attributes('aria-expanded')).toBe('true')

    const closeBtn = document.querySelector<HTMLButtonElement>('.request-tile__children-panel-close')
    closeBtn!.click()
    await nextTick()
    expect(document.querySelector('.request-tile__children-panel')).toBeNull()
    wrapper.unmount()
  })

  it('hides the children badge on idle tiles', () => {
    const wrapper = mountTile(baseTile({ status: 'idle', error_kind: 'no_traffic_5min' }))
    expect(wrapper.find('.request-tile__children-badge').exists()).toBe(false)
    wrapper.unmount()
  })
})

