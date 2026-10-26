// RequestPanels.test.ts — 左右请求面板 + 请求详情内容判据（2026-10-17 起，
// 2026-10-24 改挂 RequestDetailView）
//
// 维护者要求：左显示客户端请求摘要、右显示供应商端响应、**可点击放大**。
// 钉住的行为：
//  1. 两栏按状态着色，探测请求有独立标记；
//  2. 缺省字段（无时延/无 token/无 session）渲染「—」，**禁止 0 冒充**
//     —— 这是本仓反复吃过的亏（假零 = 假数据）；
//  3. 放大是 emit 事件交给父层；父层先弹内联摘要浮层（RequestZoomPanel），
//     浮层里的「查看全景」才开新页 —— **两步交互**（owner 2026-10-27 口径）。
//
// ★ 2026-10-24 审计改挂的理由（重要，不是搬家）：
//   这组「详情内容」断言原本挂在 RequestZoomPanel.vue 上，但那个组件在
//   2026-10-23「详情改开新页」之后已经**没有任何生产引用**（只被本测试引用）
//   ⇒ 它是死代码。而它断言的类名（zoom__row / zoom__label / zoom__value /
//   zoom__step）**全部住在 RequestDetailView.vue** 里 —— 也就是说：
//   这 7 条断言实际测的是 **RequestDetailView**，只是经由一个死壳。
//   ⇒ 直接删整块 = 悄悄丢掉**活组件**（/live/request/:id 与 /request-detail/:id
//   两处路由都在用它）的全部覆盖。
//   ⇒ 正确处置：**断言改挂到 RequestDetailView 本身**，死壳连同它自己那两条
//     外壳断言（close 按钮 / open-page 按钮）一起删。

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import ClientRequestPanel from './ClientRequestPanel.vue'
import ProviderResponsePanel from './ProviderResponsePanel.vue'
import RequestDetailView from './RequestDetailView.vue'
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
    expect(wrapper.text()).toContain('Claude Code')
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

describe('ClientRequestPanel 分页（owner 2026-10-18：单页 ≤50 条）', () => {
  // 行内不显示 requestId，用互不相同的 client 名来验证翻页取的是哪一段。
  const many = Array.from({ length: 120 }, (_, i) => entry({ requestId: `r-${i}`, client: `client-${i}` }))

  function mountClient(requests: PanelEntry[] = many) {
    return mount(ClientRequestPanel, {
      props: { summaries: [SUMMARY], requests },
      global: { plugins: [i18n] },
    })
  }

  it('★ renders at most 50 rows per page with a pager', () => {
    const wrapper = mountClient()
    expect(wrapper.findAll('.cp__item')).toHaveLength(50)
    expect(wrapper.find('[data-testid="client-request-pager"]').exists()).toBe(true)
    expect(wrapper.get('.cp__page-info').text()).toContain('1/3')
    expect(wrapper.get('.cp__page-info').text()).toContain('120')
  })

  it('pages through older requests with prev/next (page 2 = 条目 51-100)', async () => {
    const wrapper = mountClient()
    // 第 1 页第一条 = 最新（c-0）
    expect(wrapper.findAll('.cp__item')[0].text()).toContain('Client 0')
    await wrapper.get('[data-testid="client-page-next"]').trigger('click')
    const page2 = wrapper.findAll('.cp__item')
    expect(page2).toHaveLength(50)
    // 第 2 页第一条 = 第 51 条（c-50）
    expect(page2[0].text()).toContain('Client 50')
    // next 到第 3 页只有 20 条；prev 应回到第 2 页
    await wrapper.get('[data-testid="client-page-next"]').trigger('click')
    expect(wrapper.findAll('.cp__item')).toHaveLength(20)
    await wrapper.get('[data-testid="client-page-prev"]').trigger('click')
    expect(wrapper.findAll('.cp__item')).toHaveLength(50)
  })

  it('disables prev on the first page and next on the last page', async () => {
    const wrapper = mountClient()
    expect((wrapper.get('[data-testid="client-page-prev"]').element as HTMLButtonElement).disabled).toBe(true)
    await wrapper.get('[data-testid="client-page-next"]').trigger('click')
    await wrapper.get('[data-testid="client-page-next"]').trigger('click')
    expect((wrapper.get('[data-testid="client-page-next"]').element as HTMLButtonElement).disabled).toBe(true)
    expect((wrapper.get('[data-testid="client-page-prev"]').element as HTMLButtonElement).disabled).toBe(false)
  })

  it('★ fewer than 50 entries fit on one page without a pager split', () => {
    const wrapper = mountClient(Array.from({ length: 30 }, (_, i) => entry({ requestId: `r-${i}` })))
    expect(wrapper.findAll('.cp__item')).toHaveLength(30)
    expect(wrapper.get('.cp__page-info').text()).toContain('1/1')
  })

  it('★ clicking a row opens the detail (zoom) in addition to selecting it', async () => {
    const wrapper = mountClient([entry()])
    await wrapper.get('.cp__item').trigger('click')
    expect(wrapper.emitted('select-request')?.[0]).toEqual(['r-1'])
    expect(wrapper.emitted('zoom')?.[0]?.[0]).toMatchObject({ requestId: 'r-1', client: 'claude-code' })
  })

  it('★ renders the human-readable client name (claude-code → Claude Code)', () => {
    const wrapper = mountClient([entry()])
    expect(wrapper.get('.cp__client').text()).toBe('Claude Code')
    // 摘要行同样走易读名（同一显示口径）
    expect(wrapper.get('.cp__summary-name').text()).toBe('Claude Code')
  })

  it('★ emits open-page with the entry when the ↗ link is clicked (全页详情入口)', async () => {
    const wrapper = mountClient([entry()])
    await wrapper.get('.cp__open').trigger('click')
    expect(wrapper.emitted('open-page')?.[0]?.[0]).toMatchObject({ requestId: 'r-1' })
    // ↗ 是独立按钮：不得顺带触发选中/放大（那是主行的行为）
    expect(wrapper.emitted('zoom')).toBeUndefined()
    expect(wrapper.emitted('select-request')).toBeUndefined()
  })
})

describe('ProviderResponsePanel 分页（owner 2026-10-18：单页 ≤50 条）', () => {
  const many = Array.from({ length: 110 }, (_, i) => entry({ requestId: `r-${i}` }))

  function mountProvider(responses: PanelEntry[] = many) {
    return mount(ProviderResponsePanel, {
      props: { responses },
      global: { plugins: [i18n] },
    })
  }

  it('★ renders at most 50 rows per page (110 → 50/50/10)', async () => {
    const wrapper = mountProvider()
    expect(wrapper.findAll('.pp__row')).toHaveLength(50)
    expect(wrapper.get('.pp__page-info').text()).toContain('1/3')
    await wrapper.get('[data-testid="provider-page-next"]').trigger('click')
    expect(wrapper.findAll('.pp__row')).toHaveLength(50)
    await wrapper.get('[data-testid="provider-page-next"]').trigger('click')
    expect(wrapper.findAll('.pp__row')).toHaveLength(10)
  })

  it('★ clicking the main row opens the detail panel (zoom), not just selection', async () => {
    const wrapper = mountProvider([entry()])
    await wrapper.get('.pp__main').trigger('click')
    expect(wrapper.emitted('zoom')?.[0]?.[0]).toMatchObject({ requestId: 'r-1' })
  })

  it('hides the pager when there is nothing to page (empty state)', () => {
    const wrapper = mountProvider([])
    expect(wrapper.find('[data-testid="provider-response-pager"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('窗口内暂无请求')
  })

  it('★ emits open-page when the ↗ link is clicked (全页详情入口)', async () => {
    const wrapper = mountProvider([entry()])
    await wrapper.get('.pp__page').trigger('click')
    expect(wrapper.emitted('open-page')?.[0]?.[0]).toMatchObject({ requestId: 'r-1' })
  })
})

describe('RequestDetailView', () => {
  it('renders every field with an explicit em dash when absent', () => {
    const wrapper = mount(RequestDetailView, {
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
    expect(byLabel.get('客户端')).toBe('Claude Code')
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
    const wrapper = mount(RequestDetailView, {
      props: { entry: entry({ errorKind: 'upstream_timeout', failureStage: 'upstream' }), decisions },
      global: { plugins: [i18n] },
    })
    expect(wrapper.findAll('.zoom__step')).toHaveLength(3)
    expect(wrapper.text()).toContain('upstream_timeout (upstream)')
    expect(wrapper.text()).toContain('ttfb 2100ms')
  })

  it('★ renders a status label for every enum value, never the raw i18n key', async () => {
    // 2026-10-18 浏览器验收抓到：in_progress 直接拼 `filter.in_progress`
    // 渲染成原始键名（文案键是 camelCase）。逐枚举值点名，谁再拼错立刻红。
    for (const status of ['success', 'in_progress', 'failure', 'rate_limited']) {
      const wrapper = mount(RequestDetailView, {
        props: { entry: entry({ status }), decisions: [] },
        global: { plugins: [i18n] },
      })
      const rows: Array<[string, string]> = wrapper.findAll('.zoom__row').map((r) => [
        r.get('.zoom__label').text(),
        r.get('.zoom__value').text(),
      ])
      const value = new Map(rows).get('状态')
      expect(value, `status=${status}`).toBeTruthy()
      expect(value, `status=${status} 不得渲染原始键`).not.toContain('liveTopology.')
    }
  })

  it('says there is no decision event rather than an empty list', () => {
    const wrapper = mount(RequestDetailView, {
      props: { entry: entry(), decisions: [] },
      global: { plugins: [i18n] },
    })
    expect(wrapper.text()).toContain('该请求暂无判定事件')
  })

  it('★ keeps every detail row after the shared-view extraction', async () => {
    // 内容抽到 RequestDetailView 后字段不能少（两宿主共享一份实现，
    // 但抽取本身可能漏搬 —— 逐字段点名）。
    const wrapper = mount(RequestDetailView, {
      props: { entry: entry(), decisions: [] },
      global: { plugins: [i18n] },
    })
    const labels = wrapper.findAll('.zoom__label').map((l) => l.text())
    for (const need of ['请求 ID', '客户端协议', '状态', 'Token（入 / 出 / 合计）', '费用', '会话', '任务类型', '探测任务', '错误']) {
      expect(labels, `缺字段 ${need}`).toContain(need)
    }
  })

  it('★ labels a missing stage as unreported, never as routing', () => {
    // 早先两处都回落成 'routing'，等于给没上报阶段的事件盖上「路由」章。
    // 缺省就是缺省，渲染成中性的「未上报阶段」。
    const decisions: ActionEvent[] = [
      { seq: 1, action: 'resolve' },
      { seq: 2, action: 'upstream', stage_category: 'llm' },
    ]
    const wrapper = mount(RequestDetailView, {
      props: { entry: entry(), decisions },
      global: { plugins: [i18n] },
    })
    const stages = wrapper.findAll('.zoom__step .zoom__stage').map((s) => s.text())
    expect(stages[0]).toBe('未上报阶段')
    // 有阶段时照常显示真实值（不能为了防编造把真值也抹掉）
    expect(stages[1]).toBe('调用')
    expect(stages[0]).not.toBe('路由')
  })

  it('★ 摘要浮层（RequestZoomPanel）已按属主 2026-10-27 口径恢复 —— 点行弹摘要，浮层里「查看全景」开新窗', () => {
    // 2026-10-23「详情改开新页」曾把点行变成直接跳全页（跳过摘要）；
    // 属主实测后要求改回旧版两步流 ⇒ 这条判据从「钉退役」翻转为「钉恢复」：
    //   · 浮层文件存在，且被拓扑页**生产引用**（不是只被测试引用的死壳）；
    //   · 浮层渲染的是共享的 RequestDetailView（与全页详情字段必然一致）。
    //
    // ⚠ 路径一律按 cwd（= web/）解析，不用 import.meta.url：这套 vitest 配置下
    //   import.meta.url **不是 file scheme**（new URL() 直接抛
    //   "The URL must be of scheme file"）——同仓 honesty.test.ts 早就踩过。
    const zoomPanelPath = resolve(process.cwd(), 'src/components/live-topology/RequestZoomPanel.vue')
    expect(existsSync(zoomPanelPath), '摘要浮层已恢复，不应缺失').toBe(true)

    const viewPath = resolve(process.cwd(), 'src/views/LiveTopologyView.vue')
    const viewSource = readFileSync(viewPath, 'utf8')
    expect(viewSource).toMatch(/import\s+RequestZoomPanel/)
    expect(viewSource).toMatch(/<RequestZoomPanel/)

    // 内容本体 = 共享详情（zoom__row 住在 RequestDetailView 里）
    const wrapper = mount(RequestZoomPanel, {
      props: { entry: entry(), decisions: [] },
      global: { plugins: [i18n] },
    })
    expect(wrapper.findAll('.zoom__row').length).toBeGreaterThan(0)
    expect(wrapper.get('[data-testid="zoom-open-page"]').text()).toContain('查看全景')
  })

  it('★ 浮层外壳的两个出口：「查看全景」emit open-page、✕ emit close（开新窗的裁决在父层）', async () => {
    const wrapper = mount(RequestZoomPanel, {
      props: { entry: entry(), decisions: [] },
      global: { plugins: [i18n] },
    })
    await wrapper.get('[data-testid="zoom-open-page"]').trigger('click')
    expect(wrapper.emitted('open-page')?.[0]?.[0]).toMatchObject({ requestId: 'r-1' })
    await wrapper.get('.zoom__close').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
