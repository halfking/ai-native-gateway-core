// RequestTile action timeline panel — OBS-FE2
// Live-stream cards hide ≈N by default; detail / explicit opt-in still show it.

import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it, beforeEach } from 'vitest'
import RequestTile from './RequestTile.vue'
import type { RequestTile as RequestTileType } from '../types/swimlane'
import { __testing, type ActionEvent, type LiveStreamEnvelope } from '../composables/liveStreamStore'

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
    request_id: 'req-tl-1',
    timestamp: new Date().toISOString(),
    model: 'glm-5.2',
    vendor: 'zhipu',
    provider: 'zhipu-official',
    status: 'in_progress',
    ...overrides,
  }
}

function mountTile(
  tile: RequestTileType,
  mode: 'small' | 'large' = 'large',
  showTimelineBadge?: boolean,
) {
  return mount(RequestTile, {
    props: {
      tile,
      groupBy: 'model',
      isHighlighted: false,
      isDimmed: false,
      mode,
      ...(showTimelineBadge !== undefined ? { showTimelineBadge } : {}),
    },
    attachTo: document.body,
    global: { plugins: [i18n] },
  })
}

function pushActions(requestId: string, ...actions: ActionEvent[]) {
  __testing.handleEnvelope({
    type: 'request_lifecycle',
    ts: '2026-08-15T12:00:00Z',
    action: actions,
  } as unknown as LiveStreamEnvelope)
}

beforeEach(() => {
  __testing.resetStream()
  document.body.innerHTML = ''
})

describe('RequestTile action timeline panel (OBS-FE2)', () => {
  it('hides the ≈{n} badge by default on live-stream cards', () => {
    pushActions('req-tl-1',
      { request_id: 'req-tl-1', seq: 1, action: 'arrive', ts: '2026-08-15T12:00:01Z' },
      { request_id: 'req-tl-1', seq: 2, action: 'reply', ts: '2026-08-15T12:00:02Z' },
    )
    const wrapper = mountTile(baseTile())
    expect(wrapper.find('.request-tile__timeline-badge').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows the ≈{n} badge when showTimelineBadge is explicitly enabled', () => {
    pushActions('req-tl-1',
      { request_id: 'req-tl-1', seq: 1, action: 'arrive', ts: '2026-08-15T12:00:01Z' },
      { request_id: 'req-tl-1', seq: 2, action: 'reply', ts: '2026-08-15T12:00:02Z' },
    )
    const wrapper = mountTile(baseTile(), 'large', true)
    const badge = wrapper.find('.request-tile__timeline-badge')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('≈2')
    wrapper.unmount()
  })

  it('opens the empty-state panel when badge is enabled and nothing was pushed', async () => {
    const wrapper = mountTile(baseTile(), 'large', true)
    const badge = wrapper.find('.request-tile__timeline-badge')
    expect(badge.classes()).toContain('request-tile__timeline-badge--empty')

    await badge.trigger('click')
    expect(document.body.textContent).toContain('动作轨迹 0')
    expect(document.body.textContent).toContain('暂无动作事件推送（等待 request_lifecycle）')
    wrapper.unmount()
  })

  it('opens the timeline panel with ordered rows when badge is enabled', async () => {
    pushActions('req-tl-1',
      { request_id: 'req-tl-1', seq: 2, action: 'first_byte', ts: '2026-08-15T12:00:02Z', ttfb_ms: 45 } as ActionEvent,
      { request_id: 'req-tl-1', seq: 1, action: 'arrive', ts: '2026-08-15T12:00:01Z', model: 'glm-5.2' },
    )
    const wrapper = mountTile(baseTile(), 'large', true)
    await wrapper.find('.request-tile__timeline-badge').trigger('click')

    const panel = document.body.querySelector('.request-tile__timeline-panel')
    expect(panel).not.toBeNull()
    const names = Array.from(panel!.querySelectorAll('.at-name')).map(n => n.textContent)
    expect(names).toEqual(['到达', '首字节'])
    expect(panel!.textContent).toContain('TTFB 45')
    wrapper.unmount()
  })

  it('does not render the timeline badge in small mode even when enabled', () => {
    const wrapper = mountTile(baseTile(), 'small', true)
    expect(wrapper.find('.request-tile__timeline-badge').exists()).toBe(false)
    wrapper.unmount()
  })
})
