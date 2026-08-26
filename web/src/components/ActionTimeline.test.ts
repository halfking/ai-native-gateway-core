// ActionTimeline — 2026-08-15 OBS-FE2（26号 §2/§6、24号 §7）
// 组件守卫：动作时间线按 seq 升序渲染、BE2 摊平的 detail 字段进 chips、
// retry/error 特别标、无推送时空态文案。数据仅通过
// liveStreamStore.__testing.handleEnvelope 注入（SSE 推送索引）。

import { mount } from '@vue/test-utils'
import { describe, expect, it, beforeEach } from 'vitest'
import ActionTimeline from './ActionTimeline.vue'
import { __testing, type ActionEvent, type LiveStreamEnvelope } from '../composables/liveStreamStore'

function pushActions(...actions: ActionEvent[]) {
  __testing.handleEnvelope({
    type: 'request_lifecycle',
    ts: '2026-08-15T12:00:00Z',
    action: actions,
  } as unknown as LiveStreamEnvelope)
}

beforeEach(() => {
  __testing.resetStream()
})

describe('ActionTimeline (OBS-FE2)', () => {
  it('renders actions ordered by seq with zh labels', () => {
    // 乱序注入（数组内 seq 降序）：组件必须按 seq 升序展示。
    pushActions(
      { request_id: 'req-1', seq: 3, action: 'first_byte', ts: '2026-08-15T12:00:03Z', ttfb_ms: 87 },
      { request_id: 'req-1', seq: 1, action: 'arrive', ts: '2026-08-15T12:00:01Z', model: 'glm-5.2' },
      { request_id: 'req-1', seq: 2, action: 'route_resolved', ts: '2026-08-15T12:00:02Z' },
    )

    const wrapper = mount(ActionTimeline, { props: { requestId: 'req-1' } })
    const names = wrapper.findAll('.at-name').map(n => n.text())

    expect(names).toEqual(['到达', '路由完成', '首字节'])
    expect(wrapper.text()).toContain('glm-5.2')
  })

  it('flattens BE2 detail fields into chips with zh labels', () => {
    // BE2 摊平的 detail 值在线上是字符串（map[string]string）；
    // 类型上先落 unknown 再转 ActionEvent。
    pushActions(
      { request_id: 'req-2', seq: 1, action: 'model_enqueued', ts: '2026-08-15T12:00:00Z', queue_depth: '4' } as unknown as ActionEvent,
      { request_id: 'req-2', seq: 2, action: 'first_byte', ts: '2026-08-15T12:00:01Z', ttfb_ms: 120 } as unknown as ActionEvent,
    )

    const wrapper = mount(ActionTimeline, { props: { requestId: 'req-2' } })
    const chips = wrapper.findAll('.at-chip').map(c => c.text())

    expect(chips).toContain('队列深度 4')
    expect(chips).toContain('TTFB 120')
  })

  it('marks retry and error rows, terminal actions get success marker', () => {
    pushActions(
      { request_id: 'req-3', seq: 1, action: 'arrive', ts: '2026-08-15T12:00:00Z' },
      { request_id: 'req-3', seq: 2, action: 'node_switch', ts: '2026-08-15T12:00:01Z', retry: true, retry_seq: 2 },
      { request_id: 'req-3', seq: 3, action: 'reply', ts: '2026-08-15T12:00:02Z', error_kind: 'rate_limit' },
    )

    const wrapper = mount(ActionTimeline, { props: { requestId: 'req-3' } })

    expect(wrapper.find('.at-row--retry').exists()).toBe(true)
    expect(wrapper.text()).toContain('⭐r2')
    expect(wrapper.find('.at-row--error').exists()).toBe(true)
    expect(wrapper.text()).toContain('rate_limit')
    expect(wrapper.find('.at-row--terminal').exists()).toBe(true)
  })

  it('shows credential and unknown detail keys verbatim', () => {
    pushActions(
      {
        request_id: 'req-4', seq: 1, action: 'node_selected', ts: '2026-08-15T12:00:00Z',
        credential_id: 7, sticky: 'true',
      } as unknown as ActionEvent,
    )

    const wrapper = mount(ActionTimeline, { props: { requestId: 'req-4' } })
    // 2026-08-23 凭据显示：标签缓存未命中时回退「凭据 #ID」便于人工排查。
    expect(wrapper.text()).toContain('凭据 #7')
    expect(wrapper.text()).toContain('粘滞 true')
  })

  it('renders the empty state when no lifecycle events were pushed', () => {
    const wrapper = mount(ActionTimeline, { props: { requestId: 'req-none' } })
    expect(wrapper.find('.at-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('暂无动作事件推送')
    expect(wrapper.findAll('.at-row')).toHaveLength(0)
  })
})
