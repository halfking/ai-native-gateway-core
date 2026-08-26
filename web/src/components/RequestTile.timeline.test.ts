// RequestTile 动作时间线弹层 — 2026-08-15 OBS-FE2（26号 §2/§6）
// 组件守卫：
//   1) 大形态卡片渲染 ≈{n} 轨迹按钮；计数来自 request_lifecycle 推送索引。
//   2) 点击弹出 Teleport 面板（body 直挂，防泳道裁剪），内容为
//      ActionTimeline；事件未到时显示空态文案。
//   3) 小形态不渲染轨迹按钮（ActionTimeline 属大形态）。

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

function mountTile(tile: RequestTileType, mode: 'small' | 'large' = 'large') {
  return mount(RequestTile, {
    props: { tile, groupBy: 'model', isHighlighted: false, isDimmed: false, mode },
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
  it('shows the ≈{n} badge with the pushed action count in large mode', () => {
    pushActions('req-tl-1',
      { request_id: 'req-tl-1', seq: 1, action: 'arrive', ts: '2026-08-15T12:00:01Z' },
      { request_id: 'req-tl-1', seq: 2, action: 'reply', ts: '2026-08-15T12:00:02Z' },
    )
    const wrapper = mountTile(baseTile())

    const badge = wrapper.find('.request-tile__timeline-badge')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('≈2')
    wrapper.unmount()
  })

  it('renders a muted badge that still opens the empty-state panel when nothing was pushed', async () => {
    const wrapper = mountTile(baseTile())
    const badge = wrapper.find('.request-tile__timeline-badge')
    expect(badge.classes()).toContain('request-tile__timeline-badge--empty')

    await badge.trigger('click')
    expect(document.body.textContent).toContain('动作轨迹 0')
    expect(document.body.textContent).toContain('暂无动作事件推送')
    wrapper.unmount()
  })

  it('opens the timeline panel with ordered rows on click', async () => {
    pushActions('req-tl-1',
      { request_id: 'req-tl-1', seq: 2, action: 'first_byte', ts: '2026-08-15T12:00:02Z', ttfb_ms: 45 } as ActionEvent,
      { request_id: 'req-tl-1', seq: 1, action: 'arrive', ts: '2026-08-15T12:00:01Z', model: 'glm-5.2' },
    )
    const wrapper = mountTile(baseTile())
    await wrapper.find('.request-tile__timeline-badge').trigger('click')

    const panel = document.body.querySelector('.request-tile__timeline-panel')
    expect(panel).not.toBeNull()
    const names = Array.from(panel!.querySelectorAll('.at-name')).map(n => n.textContent)
    expect(names).toEqual(['到达', '首字节'])
    expect(panel!.textContent).toContain('TTFB 45')
    wrapper.unmount()
  })

  it('does not render the timeline badge in small mode', () => {
    const wrapper = mountTile(baseTile(), 'small')
    expect(wrapper.find('.request-tile__timeline-badge').exists()).toBe(false)
    wrapper.unmount()
  })
})
