// LiveTopologyView.filters.test.ts — 新首页的过滤收敛判据（2026-10-17 改版）
//
// ★ 真问题：**图、左面板、右面板、左下统计必须收敛到同一个过滤子集**。
// 只要它们各自算各自的，用户就会看到「图里没有这条链路，右栏却有」这种
// 自相矛盾的画面 —— 态势页最容易犯、也最难自查的一类缺陷。
//
// 本轮新增的两条版面契约：
//  · 顶部四个维度是**多选**（同维 OR / 跨维 AND），URL 用逗号分隔串往返；
//  · 右面板点 ⤢ 出现**内联放大面板**，缺省字段渲染「—」而不是 0。
//
// 判据直接驱动被测组件（不是读源码）：mock 掉 SSE 单例，注入固定请求集，
// 断言各区域互相吻合；同时钉住 URL ⇄ 过滤态往返。
//
// 注：vi.mock 工厂会被提升到文件顶部，所以 refs 必须在工厂内部创建
// （顶层 ref 在工厂执行时还没初始化 —— hoist 报错的经典形态）。

import { describe, expect, it, vi, beforeAll, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import LiveTopologyView from './LiveTopologyView.vue'
import zhCN from '../locales/zh-CN/liveTopology'

type Frame = Record<string, unknown>

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
  },
  {
    ts: new Date().toISOString(), request_id: 'r-ds-2', status: 'failure',
    provider_code: 'deepseek', credential_id: 7, canonical_name: 'glm-4.5',
    agent_name: 'claude-code', total_tokens: 0, error_kind: 'upstream_timeout',
  },
  {
    ts: new Date().toISOString(), request_id: 'r-mm-1', status: 'success',
    provider_code: 'moonshot', credential_id: 8, canonical_name: 'kimi-k2',
    agent_name: 'codex', total_tokens: 4200, latency_ms: 800,
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

async function mountView(initialPath = '/live') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/live', component: LiveTopologyView }],
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

  it('opens an inline zoom panel from the right panel and renders missing fields as em dash', async () => {
    const { wrapper } = await mountView()
    const rows = wrapper.findAll('.pp__row')
    // 最新在上 → r-mm-1；它没带 gw_session_id，放大后必须显示「—」而不是 0
    await rows[0].get('.pp__zoom').trigger('click')
    await flushPromises()
    const zoom = wrapper.get('.zoom')
    expect(zoom.text()).toContain('请求详情')
    expect(zoom.text()).toContain('r-mm-1')
    expect(zoom.findAll('.zoom__value').some((v) => v.text() === '—')).toBe(true)

    await zoom.get('.zoom__close').trigger('click')
    await flushPromises()
    expect(wrapper.find('.zoom').exists()).toBe(false)
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
