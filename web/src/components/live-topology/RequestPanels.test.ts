// RequestPanels.test.ts — 左右请求面板 + 放大面板判据（2026-10-17）
//
// 维护者要求：左显示客户端请求摘要、右显示供应商端响应、**可点击放大**。
// 钉住的行为：
//  1. 两栏按状态着色，探测请求有独立标记；
//  2. 缺省字段（无时延/无 token/无 session）渲染「—」，**禁止 0 冒充**
//     —— 这是本仓反复吃过的亏（假零 = 假数据）；
//  3. 放大是 emit 事件交给父层渲染内联面板，不在这里弹窗。

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ClientRequestPanel from './ClientRequestPanel.vue'
import ProviderResponsePanel from './ProviderResponsePanel.vue'
import RequestZoomPanel from './RequestZoomPanel.vue'
import type { ClientSummary, PanelEntry } from '../../composables/useLiveTopology'
import type { ActionEvent } from '../../composables/liveStreamStore'
import zhCN from '../../locales/zh-CN/liveTopology'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

function entry(over: Partial<PanelEntry> = {}): PanelEntry {
  return {
    requestId: 'r-1',
    ts: '2026-10-17T12:00:00Z',
    client: 'claude-code',
    provider: 'deepseek',
    nodeId: 'cred:7',
    model: 'glm-4.5',
    status: 'success',
    latencyMs: 1200,
    tokens: 900,
    promptTokens: 600,
    completionTokens: 300,
    costUsd: 0.0021,
    isProbe: false,
    errorKind: null,
    failureStage: null,
    sessionId: 's-1',
    requestType: 'chat',
    ...over,
  }
}

const SUMMARY: ClientSummary = {
  client: 'claude-code',
  requests: 4,
  success: 3,
  failure: 1,
  inFlight: 0,
  tokens: 900,
  topModel: 'glm-4.5',
}

describe('ClientRequestPanel', () => {
  it('renders one summary row per client and one row per request', () => {
    const wrapper = mount(ClientRequestPanel, {
      props: {
        summaries: [SUMMARY, { ...SUMMARY, client: 'codex', requests: 1, success: 1 }],
        requests: [entry(), entry({ requestId: 'r-2', status: 'failure', errorKind: 'upstream_timeout' })],
      },
      global: { plugins: [i18n] },
    })
    expect(wrapper.findAll('.cp__summary-row')).toHaveLength(2)
    expect(wrapper.findAll('.cp__item')).toHaveLength(2)
    expect(wrapper.text()).toContain('claude-code')
    expect(wrapper.text()).toContain('upstream_timeout')
    // 成功率按聚合口径算（3/4 = 75%），不是随手编的数字
    expect(wrapper.find('.cp__summary-stats').text()).toContain('75%')
  })

  it('emits the request id when a row is clicked', async () => {
    const wrapper = mount(ClientRequestPanel, {
      props: { summaries: [SUMMARY], requests: [entry()] },
      global: { plugins: [i18n] },
    })
    await wrapper.get('.cp__item').trigger('click')
    expect(wrapper.emitted('select-request')?.[0]).toEqual(['r-1'])
  })

  it('says "no requests" instead of rendering an empty box', () => {
    const wrapper = mount(ClientRequestPanel, {
      props: { summaries: [], requests: [] },
      global: { plugins: [i18n] },
    })
    expect(wrapper.text()).toContain('窗口内暂无请求')
  })

  it('renders a missing latency as an em dash, not 0ms', () => {
    const wrapper = mount(ClientRequestPanel, {
      props: { summaries: [SUMMARY], requests: [entry({ latencyMs: null })] },
      global: { plugins: [i18n] },
    })
    expect(wrapper.get('.cp__latency').text()).toBe('—')
    expect(wrapper.get('.cp__latency').text()).not.toContain('0ms')
  })

  it('★ header count always matches the rows actually rendered', () => {
    // 表头原来直接显示 summaries.length（上限 60），正文只画 8 行 ——
    // 表头在宣传不存在的行，同时把真实客户端总数藏起来。
    const many = Array.from({ length: 12 }, (_, i) => ({ ...SUMMARY, client: `c-${i}` }))
    const wrapper = mount(ClientRequestPanel, {
      props: { summaries: many, requests: [] },
      global: { plugins: [i18n] },
    })
    const rows = wrapper.findAll('.cp__summary-row').length
    const header = wrapper.get('.cp__count').text().trim()
    expect(rows).toBe(8)
    // 8 / 12：渲染行数与真实总数两个数都看得见
    expect(header).toBe('8 / 12')
    expect(header).not.toBe('12')
  })

  it('shows a plain count when every client fits', () => {
    const wrapper = mount(ClientRequestPanel, {
      props: { summaries: [SUMMARY, { ...SUMMARY, client: 'codex' }], requests: [] },
      global: { plugins: [i18n] },
    })
    expect(wrapper.findAll('.cp__summary-row')).toHaveLength(2)
    expect(wrapper.get('.cp__count').text().trim()).toBe('2')
  })
})

describe('ProviderResponsePanel', () => {
  it('lists provider responses and marks probe traffic', () => {
    const wrapper = mount(ProviderResponsePanel, {
      props: { responses: [entry(), entry({ requestId: 'r-2', isProbe: true, status: 'in_progress' })] },
      global: { plugins: [i18n] },
    })
    expect(wrapper.findAll('.pp__row')).toHaveLength(2)
    // 探测请求要能与真实业务请求区分（左栏有 is-probe，右栏曾经漏了）
    expect(wrapper.findAll('.pp__row')[1].classes()).toContain('is-probe')
    expect(wrapper.findAll('.pp__row')[0].classes()).not.toContain('is-probe')
  })

  it('emits zoom with the whole entry so the parent can render the inline panel', async () => {
    const wrapper = mount(ProviderResponsePanel, {
      props: { responses: [entry()] },
      global: { plugins: [i18n] },
    })
    await wrapper.get('.pp__zoom').trigger('click')
    expect(wrapper.emitted('zoom')?.[0]?.[0]).toMatchObject({ requestId: 'r-1', provider: 'deepseek' })
    // 放大不是弹窗：组件自身不得产生 teleport / overlay
    expect(wrapper.find('.el-overlay').exists()).toBe(false)
  })

  it('renders a missing token count as an em dash', () => {
    const wrapper = mount(ProviderResponsePanel, {
      props: { responses: [entry({ tokens: null })] },
      global: { plugins: [i18n] },
    })
    expect(wrapper.get('.pp__tokens').text()).toContain('—')
  })

  it('★ never renders a missing token count with a unit suffix', () => {
    // 「— tok」把「没有数据」说成了一个零值单位。缺省时必须只剩破折号。
    const wrapper = mount(ProviderResponsePanel, {
      props: { responses: [entry({ tokens: null })] },
      global: { plugins: [i18n] },
    })
    const text = wrapper.get('.pp__tokens').text().trim()
    expect(text).toBe('—')
    expect(text).not.toContain('tok')
  })

  it('keeps the unit on a real token count', () => {
    const wrapper = mount(ProviderResponsePanel, {
      props: { responses: [entry({ tokens: 900 })] },
      global: { plugins: [i18n] },
    })
    expect(wrapper.get('.pp__tokens').text()).toBe('900 tok')
  })
})

describe('RequestZoomPanel', () => {
  it('renders every field with an explicit em dash when absent', () => {
    const wrapper = mount(RequestZoomPanel, {
      props: {
        entry: entry({
          latencyMs: null, costUsd: null, sessionId: undefined, requestType: undefined,
          tokens: null, promptTokens: null, completionTokens: null, errorKind: null,
        }),
        decisions: [],
      },
      global: { plugins: [i18n] },
    })
    const rows: Array<[string, string]> = wrapper.findAll('.zoom__row').map((r) => [
      r.get('.zoom__label').text(),
      r.get('.zoom__value').text(),
    ])
    const byLabel = new Map<string, string>(rows)
    // 缺省字段逐个点名，不靠「有几个 —」的模糊计数：
    // 漏标一个字段的「退化」正是这套判据要挡的事。
    for (const label of ['延迟', '费用', '会话', '任务类型', '错误']) {
      expect(byLabel.get(label)).toBe('—')
    }
    expect(byLabel.get('Token（入 / 出 / 合计）')).toBe('— / — / —')
    // 有值的字段仍然照常显示（不能因为要防假零就全变「—」）
    expect(byLabel.get('客户端')).toBe('claude-code')
    expect(byLabel.get('模型')).toBe('glm-4.5')
    expect(wrapper.text()).not.toContain('$0.0000')
    expect(wrapper.text()).not.toContain('0 ms')
  })

  it('lists the routing decision timeline for the zoomed request', () => {
    const decisions: ActionEvent[] = [
      { seq: 1, action: 'resolve', stage_category: 'routing', auto_decision: 'weight' },
      { seq: 2, action: 'upstream', stage_category: 'llm', ttfb_ms: 2100, error_kind: 'upstream_timeout' },
      { seq: 3, action: 'terminal', stage_category: 'terminal', status: 'failure', error_kind: 'upstream_timeout' },
    ]
    const wrapper = mount(RequestZoomPanel, {
      props: { entry: entry({ errorKind: 'upstream_timeout', failureStage: 'upstream' }), decisions },
      global: { plugins: [i18n] },
    })
    expect(wrapper.findAll('.zoom__step')).toHaveLength(3)
    expect(wrapper.text()).toContain('upstream_timeout (upstream)')
    expect(wrapper.text()).toContain('ttfb 2100ms')
  })

  it('says there is no decision event rather than an empty list', () => {
    const wrapper = mount(RequestZoomPanel, {
      props: { entry: entry(), decisions: [] },
      global: { plugins: [i18n] },
    })
    expect(wrapper.text()).toContain('该请求暂无判定事件')
  })

  it('emits close', async () => {
    const wrapper = mount(RequestZoomPanel, {
      props: { entry: entry(), decisions: [] },
      global: { plugins: [i18n] },
    })
    await wrapper.get('.zoom__close').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('★ labels a missing stage as unreported, never as routing', () => {
    // 早先两处都回落成 'routing'，等于给没上报阶段的事件盖上「路由」章。
    // 缺省就是缺省，渲染成中性的「未上报阶段」。
    const decisions: ActionEvent[] = [
      { seq: 1, action: 'resolve' },
      { seq: 2, action: 'upstream', stage_category: 'llm' },
    ]
    const wrapper = mount(RequestZoomPanel, {
      props: { entry: entry(), decisions },
      global: { plugins: [i18n] },
    })
    const stages = wrapper.findAll('.zoom__step .zoom__stage').map((s) => s.text())
    expect(stages[0]).toBe('未上报阶段')
    // 有阶段时照常显示真实值（不能为了防编造把真值也抹掉）
    expect(stages[1]).toBe('调用')
    expect(stages[0]).not.toBe('路由')
  })
})
