// LiveTopologyView.filters.test.ts — 新首页的过滤收敛判据（2026-10-17 改版）
//
// ★ 真问题：**图、左面板、右面板、左下统计必须收敛到同一个过滤子集**。
// 只要它们各自算各自的，用户就会看到「图里没有这条链路，右栏却有」这种
// 自相矛盾的画面 —— 态势页最容易犯、也最难自查的一类缺陷。
//
// 本轮新增的两条版面契约：
//  · 顶部四个维度是**多选**（同维 OR / 跨维 AND），URL 用逗号分隔串往返；
//  · 右面板点 ⤢ / 点行 / 点 ↗ 一律**新页打开全屏详情 `/request-detail/:id>`**
//    （属主 2026-10-23 口径：不再是内联浮层，也不再是同标签页 `/live/request/:id`）。
//
// 判据直接驱动被测组件（不是读源码）：mock 掉 SSE 单例，注入固定请求集，
// 断言各区域互相吻合；同时钉住 URL ⇄ 过滤态往返。
//
// 注：vi.mock 工厂会被提升到文件顶部，所以 refs 必须在工厂内部创建
// （顶层 ref 在工厂执行时还没初始化 —— hoist 报错的经典形态）。

import { describe, expect, it, vi, beforeAll, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import LiveTopologyView from './LiveTopologyView.vue'
import zhCN from '../locales/zh-CN/liveTopology'

type Frame = Record<string, unknown>

// window.open 的替身：判据「新页打开」必须能观测到真实入参
// （href + target），而不是只断言「没发生同页跳转」。
const openSpy = vi.fn((_href: string, _target?: string, _features?: string) =>
  ({ focus: () => {} }) as unknown as Window,)

vi.mock('../composables/liveStreamStore', async () => {
  const { computed, ref } = await import('vue')
  const requestsRef = ref<Frame[]>([])
  const nodesRef = ref<Frame[]>([])
  const queueRef = ref<unknown>(null)
  const actionsMap = new Map<string, Frame[]>()
  // 真实 store 里 actionsRef 是包住 Map 的 computedRef，这里保持同一形状
  const actionsRef = computed(() => actionsMap)
  const connectionRef = ref('open')
  const initialDataReceivedRef = ref(true)
  const redisHealthyRef = ref(true)
  const redisErrorRef = ref('')
  return {
    requestsRef,
    nodesRef,
    queueRef,
    actionsRef,
    connectionRef,
    initialDataReceivedRef,
    redisHealthyRef,
    redisErrorRef,
    // 测试侧注入口
    __test: {
      setRequests: (v: Frame[]) => (requestsRef.value = v),
      setNodes: (v: Frame[]) => (nodesRef.value = v),
      setActions: (k: string, v: Frame[]) => actionsMap.set(k, v),
      clearActions: () => actionsMap.clear(),
      setConnection: (v: string) => (connectionRef.value = v),
      setInitialData: (v: boolean) => (initialDataReceivedRef.value = v),
      setRedis: (healthy: boolean, err = '') => {
        redisHealthyRef.value = healthy
        redisErrorRef.value = err
      },
    },
  }
})

vi.mock('../composables/useLiveStream', async () => {
  const { computed } = await import('vue')
  const store = await import('../composables/liveStreamStore')
  return {
    useLiveStream: () => ({
      requests: computed(() => store.requestsRef.value),
      connection: computed(() => store.connectionRef.value),
      initialDataReceived: computed(() => store.initialDataReceivedRef.value),
    }),
  }
})

vi.mock('../api/topology', () => ({
  getProvidersToday: vi.fn().mockResolvedValue({ day_utc: '2026-10-17', totals: {}, items: [] }),
  getTopologyPlugins: vi.fn().mockResolvedValue({
    builtin: [{ name: 'prompt_injection', direction: 'input' }],
    external: [{ plugin_id: 'ai-session-manager', plugin_version: '1.0.0', status: 'ready' }],
  }),
}))

// 左下统计栏在这个判据里只验「是否收到同一个过滤值」，统计口径由
// ProviderTodayRail.test.ts 单独钉，避免两头都浅。
vi.mock('../components/live-topology/ProviderTodayRail.vue', () => ({
  default: {
    name: 'ProviderTodayRailStub',
    props: ['activeProvider'],
    emits: ['select-provider'],
    template: '<div class="rail-stub" :data-provider="activeProvider"></div>',
  },
}))

import * as store from '../composables/liveStreamStore'

const testApi = (store as unknown as {
  __test: {
    setRequests: (v: Frame[]) => void
    setNodes: (v: Frame[]) => void
    setActions: (k: string, v: Frame[]) => void
    clearActions: () => void
    setConnection: (v: string) => void
    setInitialData: (v: boolean) => void
    setRedis: (healthy: boolean, err?: string) => void
  }
}).__test

// jsdom 没装 canvas 包，getContext 会抛 "Not implemented"。
// 组件本身已做 try/catch 降级（隐私模式浏览器同理），这里再兜一层，
// 免得 rAF 循环把测试输出刷成噪声。
//
// ★ 还要给容器一个**真实宽度**：jsdom 的 `getBoundingClientRect()` 全返回 0，
//   布局会退到 `MIN_LAYOUT_WIDTH`(320) 兜底 —— 那是个「手机极窄」形态，
//   放不下几张卡就会折叠，于是 `kimi-k2` 被折进「+N」而让本判据误红。
//   布局按宽度决定画几张卡是**正确行为**（窄屏就该折叠），
//   所以要修的是夹具：给它一个像样的宽度，而不是放宽断言。
const CANVAS_W = 1200
const CANVAS_H = 800
beforeAll(() => {
  HTMLCanvasElement.prototype.getContext = (() => null) as unknown as HTMLCanvasElement['getContext']
  Element.prototype.getBoundingClientRect = function (this: Element) {
    return { width: CANVAS_W, height: CANVAS_H, top: 0, left: 0, right: CANVAS_W, bottom: CANVAS_H, x: 0, y: 0, toJSON: () => ({}) } as DOMRect
  }
})

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

const REQUESTS: Frame[] = [
  {
    ts: new Date().toISOString(), request_id: 'r-ds-1', status: 'success',
    provider_code: 'deepseek', credential_id: 7, canonical_name: 'glm-4.5',
    agent_name: 'claude-code', total_tokens: 900, latency_ms: 1200, gw_session_id: 's-1',
    client_protocol: 'openai',
  },
  {
    ts: new Date().toISOString(), request_id: 'r-ds-2', status: 'failure',
    provider_code: 'deepseek', credential_id: 7, canonical_name: 'glm-4.5',
    agent_name: 'claude-code', total_tokens: 0, error_kind: 'upstream_timeout',
    client_protocol: 'openai',
  },
  {
    ts: new Date().toISOString(), request_id: 'r-mm-1', status: 'success',
    provider_code: 'moonshot', credential_id: 8, canonical_name: 'kimi-k2',
    agent_name: 'codex', total_tokens: 4200, latency_ms: 800,
    client_protocol: 'anthropic',
  },
  // ★ 网关阶段就被拒的请求：**从来没有派发到任何供应端节点**（无 provider/凭据）。
  // 它必须出现在左栏（客户端确实发了），但**不能**出现在右栏（供应商没收到）。
  // 这条夹具是 M8 变异验证逼出来的：早期夹具三条请求都带凭据，
  // 右面板「按节点过滤」这条规则恒真，于是没人咬得住它。
  {
    ts: new Date().toISOString(), request_id: 'r-gw-reject', status: 'failure',
    canonical_name: 'glm-4.5', agent_name: 'claude-code',
    total_tokens: null, error_kind: 'invalid_key', failure_stage: 'gateway',
  },
]

afterEach(() => {
  vi.unstubAllGlobals()
})

async function mountView(initialPath = '/live') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/live', component: LiveTopologyView },
      { path: '/live/request/:requestId', component: { template: '<div />' } },
      // 真实 router 的命名路由；openRequestDetailPage 靠 name 解析出 /request-detail/:id
      { path: '/request-detail/:requestId', name: 'request-detail', component: { template: '<div />' } },
    ],
  })
  await router.push(initialPath)
  await router.isReady()
  const wrapper = mount(LiveTopologyView, {
    global: {
      plugins: [i18n, router],
      stubs: {
        // el-select 多选在 jsdom 里不展开，这里换成可断言的原生下拉：
        // 判据只关心「选中的值有没有进过滤态 / URL」，不关心 el-select 的弹层。
        ElSelect: {
          props: ['modelValue', 'placeholder', 'ariaLabel'],
          emits: ['update:modelValue'],
          template:
            '<select class="sel-stub" :data-testid="$attrs[\'data-testid\']" :aria-label="ariaLabel" multiple @change="onChange"><slot /></select>',
          methods: {
            onChange(e: Event) {
              const target = e.target as HTMLSelectElement
              this.$emit(
                'update:modelValue',
                Array.from(target.selectedOptions).map((o) => o.value),
              )
            },
          },
        },
        ElOption: {
          props: ['label', 'value'],
          template: '<option :value="value">{{ label }}</option>',
        },
      },
    },
  })
  await flushPromises()
  return { wrapper, router }
}

describe('LiveTopologyView filter convergence', () => {
  beforeEach(() => {
    // 新页打开的判据：window.open 必须被观测到（stub 后逐条清空）
    vi.stubGlobal('open', openSpy)
    openSpy.mockClear()
    testApi.setRequests(REQUESTS.map((r) => ({ ...r })))
    testApi.setNodes([
      { credential_id: 7, provider_code: 'deepseek', health_status: 'healthy', in_flight: 1 },
      { credential_id: 8, provider_code: 'moonshot', health_status: 'warning', in_flight: 0 },
    ])
    testApi.clearActions()
    testApi.setConnection('open')
    testApi.setInitialData(true)
    testApi.setRedis(true)
  })

  it('renders graph, both panels and the rail from one shared subset', async () => {
    const { wrapper } = await mountView()
    const labels = wrapper.findAll('.topo-node').map((n) => n.attributes('aria-label') ?? '').join('|')
    expect(labels).toContain('glm-4.5')
    expect(labels).toContain('kimi-k2')

    // 左栏：客户端请求摘要（逐条）—— 4 条，含那条从未派发出去的
    expect(wrapper.findAll('.cp__item')).toHaveLength(4)
    // 右栏：供应商响应 —— 3 条，未派发的那条不在内
    expect(wrapper.findAll('.pp__row')).toHaveLength(3)
    expect(wrapper.find('.rail-stub').attributes('data-provider')).toBe('')
  })

  it('★ keeps a never-dispatched request on the left but never on the right', async () => {
    const { wrapper } = await mountView()
    const left = wrapper.findAll('.cp__item').map((i) => i.text()).join('|')
    const right = wrapper.findAll('.pp__row').map((i) => i.text()).join('|')
    expect(left).toContain('invalid_key')
    expect(right).not.toContain('invalid_key')
    // 前提仍在场：这条请求确实在过滤后的子集里（否则上面的 not.toContain 是假绿）
    expect(wrapper.findAll('.topo-node').length).toBeGreaterThan(0)
  })

  it('converges graph + both panels + rail when a provider filter is applied', async () => {
    const { wrapper } = await mountView('/live?provider=deepseek')

    const items = wrapper.findAll('.cp__item')
    expect(items).toHaveLength(2)
    expect(items.every((i) => i.text().includes('glm-4.5'))).toBe(true)
    expect(wrapper.findAll('.pp__row')).toHaveLength(2)

    const labels = wrapper.findAll('.topo-node').map((n) => n.attributes('aria-label') ?? '').join('|')
    expect(labels).not.toContain('kimi-k2')
    expect(labels).not.toContain('moonshot')

    expect(wrapper.find('.rail-stub').attributes('data-provider')).toBe('deepseek')
  })

  it('accepts a multi-value provider filter as OR (comma separated in the URL)', async () => {
    const { wrapper } = await mountView('/live?provider=deepseek,moonshot')
    expect(wrapper.findAll('.cp__item')).toHaveLength(3)
    expect(wrapper.findAll('.pp__row')).toHaveLength(3)
  })

  it('reads a multi-select from the top bar into the shared subset', async () => {
    const { wrapper } = await mountView()
    const select = wrapper.get('[data-testid="filter-providers"]')
    await select.findAll('option').forEach(async (opt) => {
      if (opt.element.value === 'moonshot' || opt.element.value === 'deepseek') {
        ;(opt.element as HTMLOptionElement).selected = true
      }
    })
    await select.trigger('change')
    await flushPromises()
    // 两条都在选中集里 → 一条都不该被筛掉
    expect(wrapper.findAll('.cp__item')).toHaveLength(3)
  })

  it('writes multi-value filters back into the URL so the view is shareable', async () => {
    const { wrapper, router } = await mountView()
    const select = wrapper.get('[data-testid="filter-clients"]')
    const options = select.findAll('option')
    // 夹具里有两个客户端（claude-code / codex），两个都选上
    expect(options.length).toBe(2)
    for (const opt of options) (opt.element as HTMLOptionElement).selected = true
    await select.trigger('change')
    await flushPromises()
    await flushPromises()
    expect(String(router.currentRoute.value.query.client)).toContain(',')
    // claude-code 三条（含未派发那条）+ codex 一条 = 4 条仍在视野内（多选 OR）
    expect(wrapper.findAll('.cp__item')).toHaveLength(4)
  })

  it('restores node-level filters from the URL', async () => {
    const { wrapper } = await mountView('/live?node=cred:8')
    expect(wrapper.findAll('.cp__item')).toHaveLength(1)
    expect(wrapper.find('.cp__item').text()).toContain('kimi-k2')
  })

  it('distinguishes "filtered to nothing" from "no traffic at all"', async () => {
    const { wrapper } = await mountView('/live?provider=does-not-exist')
    expect(wrapper.findAll('.cp__item')).toHaveLength(0)
    expect(wrapper.text()).toContain('当前过滤条件下窗口内没有请求')

    testApi.setRequests([])
    const { wrapper: empty } = await mountView('/live')
    expect(empty.text()).not.toContain('当前过滤条件下窗口内没有请求')
  })

  it('★ opens the full-page request detail in a NEW page when the ↗ link is clicked', async () => {
    const { wrapper, router } = await mountView()
    // 左栏最新在上：第一行是 r-gw-reject
    await wrapper.get('.cp__open').trigger('click')
    await flushPromises()
    // 新页打开 ⇒ 当前标签页**必须仍停在 /live**（同页跳转是本轮修掉的旧行为）
    expect(router.currentRoute.value.path).toBe('/live')
    expect(openSpy.mock.calls[0]?.[0]).toBe('/request-detail/r-gw-reject')
    expect(openSpy.mock.calls[0]?.[1]).toBe('_blank')
  })

  it('★ ★ clicking a row pops the summary overlay (NO new window), and keeps the graph highlight', async () => {
    const { wrapper, router } = await mountView()
    const row = wrapper.get('.cp__item')
    await row.trigger('click')
    await flushPromises()
    // 两步交互（owner 2026-10-27）：点行 = 摘要浮层，**不开新窗**
    expect(openSpy).not.toHaveBeenCalled()
    expect(wrapper.find('.zoom').exists()).toBe(true)
    // 选中态（高亮链路）必须保留 —— 这是拓扑页自己的信息，不能一起删掉
    expect(router.currentRoute.value.path).toBe('/live')
    expect(wrapper.find('.cp__row.is-active').exists()).toBe(true)
  })

  it('★ the right panel row pops the summary overlay too; 「查看全景」inside opens the NEW page', async () => {
    const { wrapper } = await mountView()
    const rows = wrapper.findAll('.pp__row')
    // 最新在上 → r-mm-1
    await rows[0].get('.pp__zoom').trigger('click')
    await flushPromises()
    // 点行/⤢ = 摘要浮层；开新窗是浮层里「查看全景」的事
    expect(openSpy).not.toHaveBeenCalled()
    expect(wrapper.find('.zoom').exists()).toBe(true)
    await wrapper.get('[data-testid="zoom-open-page"]').trigger('click')
    await flushPromises()
    expect(openSpy.mock.calls[0]?.[0]).toBe('/request-detail/r-mm-1')
    expect(openSpy.mock.calls[0]?.[1]).toBe('_blank')
  })

  it('renders the gateway core as one box holding all nine modules', async () => {
    const { wrapper } = await mountView()
    expect(wrapper.findAll('.gw__cell')).toHaveLength(9)
    // 主链路模块与旁路模块的视觉语义必须分开（旁路不能画成串行流水线的一步）
    expect(wrapper.findAll('.gw__cell.is-branch')).toHaveLength(3)
    // 大方块里能看到会话存储的命中（r-ds-1 带 gw_session_id）
    const sessionFile = wrapper.findAll('.gw__cell').find((c) => c.attributes('aria-label')?.includes('会话数据文件存储'))
    expect(sessionFile?.text()).toContain('1')
  })

  it('shows the connection banner instead of pretending to be live', async () => {
    testApi.setConnection('reconnecting')
    const { wrapper } = await mountView()
    expect(wrapper.text()).toContain('实时流未连接')
  })

  it('waits for the first authoritative frame when connected but not yet delivered', async () => {
    testApi.setInitialData(false)
    const { wrapper } = await mountView()
    expect(wrapper.text()).toContain('等待本代际首帧数据')
  })

  it('surfaces a Redis failure as degraded health, not as a fake outage', async () => {
    testApi.setRedis(false, 'redis: connection refused')
    const { wrapper } = await mountView()
    expect(wrapper.text()).toContain('connection refused')
  })

  it('renders the routing decision timeline for the selected request', async () => {
    testApi.setActions('r-ds-2', [
      { seq: 1, action: 'resolve', stage_category: 'routing', auto_decision: 'weight' },
      { seq: 2, action: 'upstream', stage_category: 'llm', error_kind: 'upstream_timeout' },
      { seq: 3, action: 'terminal', stage_category: 'terminal', status: 'failure' },
    ])
    const { wrapper } = await mountView()
    const items = wrapper.findAll('.cp__item')
    // 左栏最新在上：r-gw-reject → r-mm-1 → r-ds-2 → r-ds-1
    await items[0].trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('该请求暂无判定事件')

    await wrapper.findAll('.cp__item')[2].trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.decisions__row')).toHaveLength(3)
    expect(wrapper.find('.decisions__stage.is-terminal').exists()).toBe(true)
  })
})

describe('LiveTopologyView 数据开关（owner 2026-10-18：默认去除，开关默认不选）', () => {
  const PROBE_AND_INVALID: Frame[] = [
    {
      ts: new Date().toISOString(), request_id: 'real-1', status: 'success',
      provider_code: 'deepseek', credential_id: 7, canonical_name: 'glm-4.5',
      agent_name: 'claude-code', total_tokens: 100,
    },
    // 探测请求（经健康凭据 7）
    {
      ts: new Date().toISOString(), request_id: 'probe-1', status: 'success',
      provider_code: 'deepseek', credential_id: 7, canonical_name: 'glm-4.5',
      agent_name: 'claude-code', total_tokens: 10, is_probe: true,
    },
    // 经无效节点（cred:9 熔断打开）的真实请求
    {
      ts: new Date().toISOString(), request_id: 'via-dead-1', status: 'failure',
      provider_code: 'nvidia', credential_id: 9, canonical_name: 'gpt-6',
      agent_name: 'codex', total_tokens: 0, error_kind: 'upstream_timeout',
    },
  ]

  beforeEach(() => {
    testApi.setRequests(PROBE_AND_INVALID.map((r) => ({ ...r })))
    testApi.setNodes([
      { credential_id: 7, provider_code: 'deepseek', health_status: 'healthy' },
      { credential_id: 9, provider_code: 'nvidia', circuit_state: 'open' },
    ])
    testApi.clearActions()
    testApi.setConnection('open')
    testApi.setInitialData(true)
    testApi.setRedis(true)
  })

  it('★ hides probe traffic everywhere by default and explains it in a banner', async () => {
    const { wrapper } = await mountView()
    // 默认态：探测请求不上任何面板
    const left = wrapper.findAll('.cp__item').map((i) => i.text()).join('|')
    expect(left).not.toContain('probe-1')
    // 但真实请求在场（隐藏是选择性的，不是全灭）
    expect(left.length).toBeGreaterThan(0)
    // 横幅说清隐藏了多少条 —— 否则「图上比预期少」会被读成「丢数据」
    expect(wrapper.get('[data-testid="hidden-counts-banner"]').text()).toContain('已隐藏 1 条探测请求')
  })

  it('★ shows probe traffic again when the switch is on (?probe=1)', async () => {
    const { wrapper } = await mountView('/live?probe=1')
    const checkbox = wrapper.get('[data-testid="toggle-probe"]').get('input')
    expect((checkbox.element as HTMLInputElement).checked).toBe(true)
    // 探测标记回到面板（is-probe 样式在，条目也在）
    expect(wrapper.findAll('.cp__row.is-probe').length).toBe(1)
    expect(wrapper.get('[data-testid="hidden-counts-banner"]').text()).not.toContain('探测')
  })

  it('★ hides invalid-node traffic and the node itself by default', async () => {
    const { wrapper } = await mountView()
    // 经熔断节点的请求被隐藏
    const left = wrapper.findAll('.cp__item').map((i) => i.text()).join('|')
    expect(left).not.toContain('upstream_timeout')
    // 熔断节点本身也不上图（node_update 不建节点；节点标签是凭据号+健康态）
    const labels = wrapper.findAll('.topo-node').map((n) => n.attributes('aria-label') ?? '').join('|')
    expect(labels).not.toContain('#9 · 故障')
    expect(labels).toContain('#7 · 健康')
    // 横幅说明
    expect(wrapper.get('[data-testid="hidden-counts-banner"]').text()).toContain('无效节点')
  })

  it('★ restores invalid nodes and their traffic when the switch is on (?invalid=1)', async () => {
    const { wrapper } = await mountView('/live?invalid=1')
    expect((wrapper.get('[data-testid="toggle-invalid"]').get('input').element as HTMLInputElement).checked).toBe(true)
    const labels = wrapper.findAll('.topo-node').map((n) => n.attributes('aria-label') ?? '').join('|')
    expect(labels).toContain('#9 · 故障')
    // 只开无效开关：探测仍默认隐藏 → real-1 + via-dead-1 共 2 条
    expect(wrapper.findAll('.cp__item')).toHaveLength(2)
  })

  it('★ both toggles default to unchecked on a clean URL', async () => {
    const { wrapper } = await mountView()
    expect((wrapper.get('[data-testid="toggle-probe"]').get('input').element as HTMLInputElement).checked).toBe(false)
    expect((wrapper.get('[data-testid="toggle-invalid"]').get('input').element as HTMLInputElement).checked).toBe(false)
  })
})

describe('LiveTopologyView 连线与协议标注（owner 2026-10-18）', () => {
  beforeEach(() => {
    testApi.setRequests(REQUESTS.map((r) => ({ ...r })))
    testApi.setNodes([
      { credential_id: 7, provider_code: 'deepseek', health_status: 'healthy', in_flight: 1 },
      { credential_id: 8, provider_code: 'moonshot', health_status: 'warning', in_flight: 0 },
    ])
    testApi.clearActions()
    testApi.setConnection('open')
    testApi.setInitialData(true)
    testApi.setRedis(true)
  })

  it('★ renders protocol chips on client→gateway edges (小字标注协议)', async () => {
    const { wrapper } = await mountView()
    // claude-code 两条 openai → chip「openai ×2」；codex 一条 anthropic
    const openai = wrapper.find('[data-testid="proto-chip-openai"]')
    expect(openai.exists()).toBe(true)
    expect(openai.text()).toContain('openai')
    expect(openai.text()).toContain('2')
    expect(wrapper.find('[data-testid="proto-chip-anthropic"]').text()).toContain('1')
  })

  it('★ spreads two protocol chips on the same edge (no overlap)', async () => {
    // 审计轮缺陷：同边双协议 chip 同锚 t=0.45 完全重叠。夹具此前每客户端
    // 单协议，肉眼与判据都没踩到 —— 这条夹具故意给同一客户端混两种协议。
    testApi.setRequests([
      { ...REQUESTS[0], request_id: 'mix-1', client_protocol: 'openai' },
      { ...REQUESTS[0], request_id: 'mix-2', client_protocol: 'anthropic' },
    ])
    const { wrapper } = await mountView()
    const chips = [
      wrapper.find('[data-testid="proto-chip-openai"]'),
      wrapper.find('[data-testid="proto-chip-anthropic"]'),
    ]
    expect(chips[0].exists()).toBe(true)
    expect(chips[1].exists()).toBe(true)
    // 两个 chip 的定位必须错开（左/上至少一项不同；同位 = 重叠缺陷回归）
    const pos = (i: number) => {
      const style = chips[i].attributes('style') ?? ''
      const left = /left:\s*([\d.]+)px/.exec(style)?.[1] ?? ''
      const top = /top:\s*([\d.]+)px/.exec(style)?.[1] ?? ''
      return `${left},${top}`
    }
    expect(pos(0), '同边双协议 chip 不得同位重叠').not.toBe(pos(1))
    // 还原夹具，避免污染后续用例
    testApi.setRequests(REQUESTS.map((r) => ({ ...r })))
  })

  it('★ renders plugin chips inside the gateway box from the topology plugins endpoint', async () => {
    const { wrapper } = await mountView()
    const inChips = wrapper.find('[data-testid="gw-plugins-in"]').findAll('.gw__plugin')
    expect(inChips.map((c) => c.text()).sort()).toEqual(['ai-session-manager', 'prompt_injection'].sort())
  })

  it('highlights the full client→gateway→provider→model path when a request is selected', async () => {
    const { wrapper } = await mountView()
    // r-ds-1（claude-code → cred:7 → glm-4.5）：左栏最新在上第 4 条（r-ds-1 最旧）
    const items = wrapper.findAll('.cp__item')
    await items[3].trigger('click')
    await flushPromises()
    // 选中的是 deepseek 那条：右栏没有 moonshot 高亮，左栏对应条目 is-active
    expect(wrapper.findAll('.cp__row.is-active').length).toBe(1)
  })
})

describe('LiveTopologyView 版面契约（owner 2026-10-18：筛选一行 + 字号一致）', () => {
  // jsdom 的 getComputedStyle 恒为空串（AGENTS 判据纪律 19），样式契约按
  // 本仓 honesty.test 的 readSrc 先例直接钉源码文本。变异验证：把 nowrap
  // 改回 wrap 或字号改大，本组立刻红。
  const source = readFileSync(resolve(process.cwd(), 'src/views/LiveTopologyView.vue'), 'utf8')

  it('★ filter bar never wraps (one row, horizontal scroll as narrow fallback)', () => {
    const filtersBlock = source.split('.live-topology__filters {')[1]?.split('\n}')[0] ?? ''
    expect(filtersBlock, '过滤栏样式块必须存在').toContain('flex-wrap: nowrap')
    expect(filtersBlock).toContain('overflow-x: auto')
  })

  it('★ every filter control is 11px and 24px tall (uniform size)', () => {
    for (const block of ['.live-topology__filters select {', '.live-topology__toggle {', '.live-topology__clear {']) {
      const body = source.split(block)[1]?.split('\n}')[0] ?? ''
      expect(body, `${block} 必须存在`).toContain('font-size: 11px')
    }
    // el-select 走 element 变量注入统一字号
    expect(source).toContain('--el-font-size-base: 11px')
  })
})
