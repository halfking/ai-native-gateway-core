// LiveTopologyPreview.test.ts —— DEV 夹具预览页判据（2026-10-16）
//
// 这个页面唯一的「行为」就是：把夹具写进 liveStreamStore 单例，让拓扑页
// 在没有登录态/真实 SSE 的情况下也能肉眼验走动效与排版；卸载时必须把
// 原状态**还原**（否则同一个 SPA 会话里切走再回来，预览夹具会污染真实数据）。
// 这两点不测，就等于往仓库里塞了一段没被覆盖的全局状态写入代码。

import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { createI18n } from 'vue-i18n'
// 预览页真正的视图在本仓另有 8 条判据（LiveTopologyView.filters.test.ts），
// 这里把子视图替换掉：本用例只管「夹具写入 + 卸载还原」这一条契约，
// 避免为了渲染真实视图而给这个用例塞一套 router/i18n 脚手架。
vi.mock('./LiveTopologyView.vue', () => ({
  default: defineComponent({ name: 'LiveTopologyViewStub', setup: () => () => h('div', 'topology') }),
}))
import LiveTopologyPreview from './LiveTopologyPreview.vue'
import zhCN from '../locales/zh-CN/liveTopology'
import { liveStreamState } from '../composables/liveStreamStore'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

describe('LiveTopologyPreview fixture seeding', () => {
  it('seeds requests, nodes and actions, then restores the previous state on unmount', async () => {
    const before = {
      requests: liveStreamState.requests,
      nodes: liveStreamState.nodes,
      actions: liveStreamState.actions,
      connection: liveStreamState.connection,
      initialDataReceived: liveStreamState.initialDataReceived,
    }

    const wrapper = mount(LiveTopologyPreview, {
      global: {
        plugins: [i18n],
        stubs: { RouterLink: true },
      },
    })
    await wrapper.vm.$nextTick()

    expect(liveStreamState.requests.length).toBeGreaterThan(10)
    expect(liveStreamState.nodes.length).toBeGreaterThan(0)
    expect(liveStreamState.connection).toBe('open')
    // 夹具必须覆盖图要画的三种状态，否则预览看不出真实差距
    const statuses = new Set(liveStreamState.requests.map((r) => r.status))
    expect(statuses.has('success')).toBe(true)
    expect(statuses.has('failure')).toBe(true)
    expect(statuses.has('in_progress')).toBe(true)
    // 供应端节点要覆盖健康/告警/熔断三档
    const healths = new Set(liveStreamState.nodes.map((n) => n.health_status))
    expect(healths.has('healthy')).toBe(true)
    expect(healths.has('warning')).toBe(true)
    expect(healths.has('unreachable')).toBe(true)
    // 至少一条请求带路由判定动作（底部时间线才有东西可看）
    expect(liveStreamState.actions.size).toBeGreaterThan(0)

    wrapper.unmount()

    // 数组/Map 经 reactive 代理后按引用比会假失败（写入再读出是新代理），
    // 语义上要验的是「内容还原」，所以用 toEqual；原始类型才用 toBe。
    expect(liveStreamState.requests).toEqual(before.requests)
    expect(liveStreamState.nodes).toEqual(before.nodes)
    expect(liveStreamState.actions).toEqual(before.actions)
    expect(liveStreamState.connection).toBe(before.connection)
    expect(liveStreamState.initialDataReceived).toBe(before.initialDataReceived)
  })
})
