// LiveTopologyView.honesty.test.ts — 展示诚实性判据（2026-10-17 审计轮）
//
// 这一轮审计修掉的都是**同一族**缺陷：把「不知道」画成了「知道」。
// 共同形态有两种 ——
//   ① 数字层面：把命中/缺失塞进「失败」位，于是正常状态被涂成故障红；
//   ② 呈现层面：算出来的量没人消费（窗口丢弃数），或容器没定位基准
//      （放大面板逃出本列压住别的区域）。
//
// 判据直接驱动真实组件 + mock 掉的 SSE 单例，不读源码。
// 每条判据都必须满足：**把被测逻辑改回审计前的形态，它立刻变红**。
//
// mock 工厂会被提升到文件顶部，refs 必须在工厂内部创建（hoist 报错经典形态）。

import { describe, expect, it, vi, beforeAll, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import LiveTopologyView from './LiveTopologyView.vue'
import zhCN from '../locales/zh-CN/liveTopology'

/** 定位基准写在样式契约里，按仓库相对路径读（cwd = web/）。
 *  不用 import.meta.url：这套 vitest 配置下它不是 file scheme。 */
const readSrc = (rel: string) => readFileSync(resolve(process.cwd(), rel), 'utf8')

type Frame = Record<string, unknown>

vi.mock('../composables/liveStreamStore', async () => {
  const { computed, ref } = await import('vue')
  const requestsRef = ref<Frame[]>([])
  const nodesRef = ref<Frame[]>([])
  const queueRef = ref<unknown>(null)
  const actionsMap = new Map<string, Frame[]>()
  const actionsRef = computed(() => actionsMap)
  const connectionRef = ref('open')
  const initialDataReceivedRef = ref(true)
  const redisHealthyRef = ref(true)
  const redisErrorRef = ref('')
  const liveDataRevision = ref(0)
  return {
    requestsRef,
    nodesRef,
    queueRef,
    actionsRef,
    connectionRef,
    initialDataReceivedRef,
    redisHealthyRef,
    redisErrorRef,
    liveDataRevision,
    __test: {
      setRequests: (v: Frame[]) => (requestsRef.value = v),
      setNodes: (v: Frame[]) => (nodesRef.value = v),
      clearActions: () => actionsMap.clear(),
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
  getTopologyModelsToday: vi.fn().mockResolvedValue({ day_utc: '2026-10-17', items: [] }),
  getTopologyProviderKinds: vi.fn().mockResolvedValue({ items: [] }),
  getTopologyPlugins: vi.fn().mockResolvedValue({ builtin: [], external: [] }),
}))

// 统计面板的统计口径由 StatsPanel.test.ts 单独钉，这里只验它收到的过滤值。
vi.mock('../components/live-topology/StatsPanel.vue', () => ({
  default: {
    name: 'StatsPanelStub',
    props: ['activeClient', 'activeProvider', 'activeModel', 'inFlight', 'queueDepth'],
    emits: ['select-provider', 'select-model'],
    template: '<div class="rail-stub" :data-provider="activeProvider" :data-model="activeModel"></div>',
  },
}))

import * as store from '../composables/liveStreamStore'

const testApi = (store as unknown as {
  __test: {
    setRequests: (v: Frame[]) => void
    setNodes: (v: Frame[]) => void
    clearActions: () => void
  }
}).__test

beforeAll(() => {
  HTMLCanvasElement.prototype.getContext = (() => null) as unknown as HTMLCanvasElement['getContext']
})

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

const nowIso = () => new Date().toISOString()

/** 一小时前 —— 会被 5 分钟默认窗口丢掉。 */
const oldIso = () => new Date(Date.now() - 60 * 60_000).toISOString()

/** 已挂载的组件（★ 每个用例后统一 unmount ⇒ 见下面的 `afterEach`）。 */
const mounted: Array<{ unmount: () => void }> = []

afterEach(() => {
  // ★★ **必须 unmount**：★ 被测组件里有一个 `setInterval`（★ `LiveTopologyView.vue` 的 `spawnTimer`）
  //   ⇒ ⇒ 组件**只在 `onUnmounted` 里**清它 ⇒ ⇒ 测试不 unmount ⇒ ⇒ 定时器活过本文件
  //   ⇒ ⇒ ⇒ jsdom 环境销毁后再触发 ⇒ ⇒ `document is not defined` ⇒ ⇒ vitest 记【未捕获异常】
  //   ⇒ ⇒ ⇒ **用例本身全绿，但整包退出码 = 1**（★ 负载越高越容易撞上 ⇒ ⇒ **看起来像 flaky**）
  //   ⇒ ⇒ ⇒ 仓内先例：★ `src/composables/useLiveStreamUrl.test.ts` 逐个 `wrapper.unmount()`。
  while (mounted.length) {
    const w = mounted.pop()
    try {
      w?.unmount()
    } catch {
      // ★ 已经卸载过 ⇒ ⇒ 忽略（★ 清理是【卫生问题】，不该变成【判据问题】）
    }
  }
})

async function mountView(initialPath = '/live') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/live', component: LiveTopologyView }],
  })
  await router.push(initialPath)
  await router.isReady()
  const wrapper = mount(LiveTopologyView, { global: { plugins: [i18n, router] } })
  mounted.push(wrapper)
  await flushPromises()
  return { wrapper, router }
}

/** 大方块里某个模块格的 DOM 文本（「模块名 + 计数」）。 */
function cellText(wrapper: Awaited<ReturnType<typeof mountView>>['wrapper'], prefix: string): string {
  const cell = wrapper.findAll('.gw__cell').find((c) => c.attributes('aria-label')?.startsWith(prefix))
  return cell?.get('.gw__cell-count').text() ?? ''
}

function cellClasses(wrapper: Awaited<ReturnType<typeof mountView>>['wrapper'], prefix: string): string[] {
  const cell = wrapper.findAll('.gw__cell').find((c) => c.attributes('aria-label')?.startsWith(prefix))
  return cell?.classes() ?? []
}

describe('LiveTopologyView 展示诚实性（本轮审计修复）', () => {
  beforeEach(() => {
    testApi.setNodes([
      { credential_id: 7, provider_code: 'deepseek', health_status: 'healthy' },
      { credential_id: 8, provider_code: 'moonshot', health_status: 'healthy' },
    ])
    testApi.clearActions()
  })

  it('★ 缓存命中时「缓存」格绝不显示为故障红', async () => {
    // 审计前的形态：命中数累加进 errors，而 GatewayCoreBox 用 errors>0 画红
    // ⇒ 缓存工作正常反而显示成「缓存故障」。
    testApi.setRequests([
      {
        ts: nowIso(), request_id: 'r-1', status: 'success',
        provider_code: 'deepseek', credential_id: 7, canonical_name: 'glm-4.5',
        agent_name: 'claude-code', cache_read_tokens: 2048, total_tokens: 3000,
      },
    ])
    const { wrapper } = await mountView()
    expect(cellClasses(wrapper, '缓存')).not.toContain('is-error')
    expect(cellClasses(wrapper, '缓存')).toContain('is-active')
    // 命中数照常显示（不是被「修」成 0）
    expect(cellText(wrapper, '缓存')).toBe('1')
  })

  it('★ 成功与在途流量不会让任何模块格变红', async () => {
    testApi.setRequests([
      { ts: nowIso(), request_id: 'a', status: 'success', provider_code: 'deepseek', credential_id: 7, agent_name: 'x', cache_read_tokens: 10 },
      { ts: nowIso(), request_id: 'b', status: 'in_progress', provider_code: 'deepseek', credential_id: 7, agent_name: 'x' },
    ])
    const { wrapper } = await mountView()
    for (const cell of wrapper.findAll('.gw__cell')) {
      expect(cell.classes(), `模块 ${cell.attributes('aria-label')} 不该变红`).not.toContain('is-error')
    }
  })

  it('★ 真失败仍然要把「错误记录」格标红（别把判据写得过宽）', async () => {
    testApi.setRequests([
      { ts: nowIso(), request_id: 'f1', status: 'failure', provider_code: 'deepseek', credential_id: 7, agent_name: 'x', error_kind: 'upstream_timeout' },
    ])
    const { wrapper } = await mountView()
    expect(cellClasses(wrapper, '错误记录')).toContain('is-error')
  })

  it('★ 窗口丢弃的请求必须说出来，不能让图静默变空', async () => {
    // droppedByWindow 之前只在 buildTopology 里累加、全仓无人消费：
    // 窗口调小后图变空，用户分不清「没流量」还是「被窗口丢了」。
    testApi.setRequests([
      { ts: nowIso(), request_id: 'fresh', status: 'success', provider_code: 'deepseek', credential_id: 7, agent_name: 'x' },
      { ts: oldIso(), request_id: 'stale-1', status: 'success', provider_code: 'deepseek', credential_id: 7, agent_name: 'x' },
      { ts: oldIso(), request_id: 'stale-2', status: 'success', provider_code: 'moonshot', credential_id: 8, agent_name: 'x' },
    ])
    const { wrapper } = await mountView()
    const banners = wrapper.findAll('.live-topology__banner').map((b) => b.text()).join(' ')
    expect(banners).toContain('2')
    expect(banners).toMatch(/窗口/)
  })

  it('★ 没有丢弃时不该凭空出现该提示', async () => {
    testApi.setRequests([
      { ts: nowIso(), request_id: 'fresh', status: 'success', provider_code: 'deepseek', credential_id: 7, agent_name: 'x' },
    ])
    const { wrapper } = await mountView()
    const banners = wrapper.findAll('.live-topology__banner').map((b) => b.text()).join(' ')
    expect(banners).not.toMatch(/落在当前时间窗口之外/)
  })

  it('★ 节点过滤收窄时「节点状态管理」格必须跟着收敛', async () => {
    // 审计前的形态：这格直接显示全局节点数（2），而旁边八格都是过滤后的量
    // ⇒ 筛到单一节点后，这格说「管 2 个节点」，图上却只有 1 个。
    testApi.setRequests([
      { ts: nowIso(), request_id: 'r-7', status: 'success', provider_code: 'deepseek', credential_id: 7, agent_name: 'x' },
      { ts: nowIso(), request_id: 'r-8', status: 'success', provider_code: 'moonshot', credential_id: 8, agent_name: 'x' },
    ])
    const { wrapper } = await mountView()
    expect(cellText(wrapper, '节点状态管理')).toBe('2')

    await wrapper.vm.$router.push('/live?node=cred%3A7')
    await flushPromises()
    expect(cellText(wrapper, '节点状态管理')).toBe('1')
  })

  it('★ 摘要浮层的定位基准必须是所在列，不能是视口', async () => {
    // ★ 2026-10-27 摘要浮层（RequestZoomPanel）按属主口径恢复，本条回到原始形态：
    //
    // RequestZoomPanel 是 position:absolute，.live-topology__right 若没有
    // position，containing block 会退到视口，右下角那块 460px 浮层会盖住
    // 拓扑区与左栏 —— 窄屏下几乎占满整屏。必须由本列当定位基准。
    //
    // 三列（left / right / stage）共享 `position: relative` 还在承重的另一半：
    // stage 列里的 TopologyCanvas 有 8 处 position:absolute 的覆盖层
    // （实测 TopologyCanvas.vue），基准丢了它们同样会退到视口。
    //
    // 为什么读源码而不是 getComputedStyle：vitest 默认 `css:false`，
    // SFC 的 <style scoped> 根本不进 jsdom，computed style 恒为空串 ——
    // 那样的判据是**恒假绿**，测不到任何东西（判据纪律第 3 条）。
    // 定位基准是写在样式契约里的事实，直接对它做文本断言。
    const { wrapper } = await mountView()
    expect(wrapper.findAll('.live-topology__right').length).toBeGreaterThan(0)

    const source = readSrc('src/views/LiveTopologyView.vue')
    // 三列共享的样式规则必须带定位基准
    const rule = source.match(
      /\.live-topology__left,\s*\.live-topology__right,\s*\.live-topology__stage\s*\{[^}]*\}/,
    )
    expect(rule, '三列共享的样式规则未找到').toBeTruthy()
    expect(rule![0]).toMatch(/position:\s*relative/)

    // 浮层本体是 absolute（恢复后是活的 absolute 宿主，靠上面的基准定位）
    const zoomSource = readSrc('src/components/live-topology/RequestZoomPanel.vue')
    expect(zoomSource).toMatch(/position:\s*absolute/)

    // 反向钉：TopologyCanvas 是 stage 列里另一组 position:absolute 覆盖层。
    const canvasSource = readSrc('src/components/live-topology/TopologyCanvas.vue')
    expect(canvasSource).toMatch(/position:\s*absolute/)
  })

  it('★ 栅格行下限与项的 min-height 必须同口径（★ 项硬钉会抵消行下限）', () => {
    // ★★★ **2026-10-31 12h 审计轮补的回归钉**：★★★
    //
    // ★ 缺陷形态：★ 栅格行下限写的是 `minmax(min(320px, 50vh), 1fr)`（★ 矮窗时
    // ★ 行下限 = 50vh），★ 而**同一个项**上又硬钉 `min-height: 320px` ⇒ ⇒ ★★
    // ★★ **项比行高** ⇒ ⇒ ⇒ 溢出 ⇒ ⇒ ⇒ 压到第 2 行（右栏）⇒ ⇒ ⇒ ★★ ⇒ 而那
    // ★★ 正是「右栏摘要浮层被压成 16px 一条缝」的**同一种形态** ⇒ ⇒ ⇒ ★★ ⇒
    // ★★ **行下限被项自己的硬钉抵消了** ⇒ ⇒ ⇒ ★★ ⇒ 上一轮的修复在矮窗下无效。
    //
    // ★ 为什么读源码而不是 computed style：★ vitest 默认 css:false ⇒ ★ SFC 的
    // ★ <style scoped> 不进 jsdom ⇒ ★ computed style 恒为空串 ⇒ ⇒ ★★ 那是恒假绿。
    // ★★ 定位基准是写在样式契约里的事实（★ 同本文件既有做法）。
    const source = readSrc('src/views/LiveTopologyView.vue')

    // ① 行下限必须带 vh 封顶（★ 否则矮窗下无上限可退）。
    expect(source, '栅格行下限必须含 min(320px, 50vh)').toMatch(/grid-template-rows:\s*minmax\(min\(320px,\s*50vh\)/)

    // ② 桌面段里 .live-topology__clients 的 min-height 必须是 min(...) 口径，
    //    ★ 不许再是写死的 px（★ 写死的 px 会大于 50vh 的行下限 ⇒ ⇒ 抵消它）。
    const desktopBlock = source.slice(source.indexOf('@media (min-width: 1024px)'))
    const clientsRule = desktopBlock.match(/\.live-topology__clients\s*\{[^}]*\}/)
    expect(clientsRule, '桌面段里找不到 .live-topology__clients 规则').toBeTruthy()
    // ★★★ **判据要扫【声明】不扫【散文】**：★★★ ★ 本仓的注释里会写「原值
    // ★★★ `min-height: 320px` 是硬钉」这类**历史说明** ⇒ ⇒ ★ 若直接对整段文本
    // ★★★ 做正则 ⇒ ⇒ ⇒ **注释里的字面量也会命中** ⇒ ⇒ ⇒ ★★ ⇒ 判据会恒红
    // ★★★ （★ 这就是「扫的是代码还是散文」那道题）⇒ ⇒ ⇒ ★ 所以先把 CSS 注释涂白。
    const ruleText = clientsRule![0].replace(/\/\*[\s\S]*?\*\//g, '')
    expect(
      ruleText,
      '★ .live-topology__clients 的 min-height 必须与行下限同口径（min(320px, 50vh)）⇒ ⇒ ' +
        '写死的 px 会在矮窗下大于行下限 ⇒ ⇒ 抵消行下限 ⇒ ⇒ 又把右栏压扁',
    ).toMatch(/min-height:\s*min\(320px,\s*50vh\)/)
    expect(
      ruleText,
      '★ .live-topology__clients 不许再硬钉 min-height: 320px（★ 它大于 50vh 的行下限）',
    ).not.toMatch(/min-height:\s*320px/)
  })

  it('★ 两步交互：点行弹摘要浮层且不开新窗；浮层里「查看全景」才开新窗', async () => {
    // owner 2026-10-27 口径：点行 → 内联摘要浮层（本页不跳转、不开新窗）；
    // 浮层里的「查看全景」→ 全页详情开新窗。2026-10-23 曾把点行直接开新窗，
    // 本条判据把两步流钉成事实 —— 点行若直接开窗，第一段断言立刻变红。
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(null)
    testApi.setRequests([
      { ts: nowIso(), request_id: 'r-zoom', status: 'success', provider_code: 'deepseek', credential_id: 7, agent_name: 'claude-code' },
    ])
    const { wrapper } = await mountView()

    // 第一步：点左侧面板行 → 摘要浮层出现，**没有**开新窗
    expect(wrapper.find('.zoom').exists()).toBe(false)
    await wrapper.get('.cp__item').trigger('click')
    expect(wrapper.find('.zoom').exists()).toBe(true)
    expect(openSpy).not.toHaveBeenCalled()

    // 第二步：浮层里点「查看全景」→ 全页详情开新窗
    await wrapper.get('[data-testid="zoom-open-page"]').trigger('click')
    expect(openSpy).toHaveBeenCalledTimes(1)

    // 浮层 ✕ 关闭后回到无浮层状态
    await wrapper.get('.zoom__close').trigger('click')
    expect(wrapper.find('.zoom').exists()).toBe(false)

    openSpy.mockRestore()
  })
})
