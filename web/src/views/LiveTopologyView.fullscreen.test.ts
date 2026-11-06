// LiveTopologyView.fullscreen.test.ts — 三栏整体全屏判据（2026-11-06 全屏轮）
//
// owner需求：客户端响应 /中间实时流程图 /右侧供应商作为一个整体的全屏显示。
//判据直接驱动真实组件（SSE 单例 mock，同 honesty.test.ts 的工厂形态）：
// F1常驻按钮存在（不受过滤态影响）
// F2 点按钮 → 对 .live-topology 元素调 requestFullscreen
// F3 fullscreenchange 后：按钮文案切「退出」+根元素带 is-fullscreen 类
// F4 再点 → exitFullscreen
// F5卸载时清理 fullscreenchange监听并退出全屏（不留悬挂监听）

import { describe, expect, it, vi, beforeAll, beforeEach, afterEach } from 'vitest'
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
  setLiveStreamWantProbe: vi.fn(),
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

vi.mock('../components/live-topology/StatsPanel.vue', () => ({
  default: {
  name: 'StatsPanelStub',
  props: ['activeClient', 'activeProvider', 'activeModel', 'inFlight', 'queueDepth'],
  template: '<div class="rail-stub"></div>',
},
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { liveTopology: zhCN as never } },
})

/** jsdom 没有 Fullscreen API：桩掉并在触发后模拟 UA状态翻转 +事件派发。 */
let fullscreenTarget: Element | null = null
const listeners: Array<() => void> = []

beforeAll(() => {
  HTMLCanvasElement.prototype.getContext = (() => null) as unknown as HTMLCanvasElement['getContext']
  Object.defineProperty(document, 'fullscreenElement', {
  configurable: true,
  get: () => fullscreenTarget,
})
  ;(document as unknown as { requestFullscreen?: unknown }).requestFullscreen = undefined
  Element.prototype.requestFullscreen = vi.fn(function (this: Element) {
  fullscreenTarget = this
  queueMicrotask(() => {
  for (const fn of listeners) fn()
})
  return Promise.resolve()
})
  document.exitFullscreen = vi.fn(() => {
  fullscreenTarget = null
  queueMicrotask(() => {
  for (const fn of listeners) fn()
})
  return Promise.resolve()
})
})

beforeEach(() => {
  listeners.length =0
  fullscreenTarget = null
  const origAdd = document.addEventListener.bind(document)
  vi.spyOn(document, 'addEventListener').mockImplementation(((type: string, cb: EventListenerOrEventListenerObject) => {
  if (type === 'fullscreenchange') listeners.push(() => {
  if (typeof cb === 'function') cb(new Event('fullscreenchange'))
})
  return origAdd(type, cb)
}) as typeof document.addEventListener)
})

const mounted: Array<{ unmount: () => void }> = []

afterEach(() => {
  while (mounted.length) {
  const w = mounted.pop()
  try {
  w?.unmount()
} catch {
  // 已卸载过 —— 清理是卫生问题不是判据问题
}
}
  vi.restoreAllMocks()
  fullscreenTarget = null
})

async function mountView() {
  const router = createRouter({
  history: createMemoryHistory(),
  routes: [{ path: '/live', component: LiveTopologyView }],
})
  await router.push('/live')
  await router.isReady()
  const wrapper = mount(LiveTopologyView, { global: { plugins: [i18n, router] } })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('三栏整体全屏（Fullscreen API）', () => {
  it('F1常驻全屏按钮存在（无过滤激活也渲染，i18n 文案 = 进入全屏）', async () => {
  const wrapper = await mountView()
  const btn = wrapper.find('[data-testid="fullscreen-enter"]')
  expect(btn.exists()).toBe(true)
  expect(btn.text()).toBe('进入全屏')
})

  it('F2 点按钮 → 对三栏整体根元素调 requestFullscreen', async () => {
  const wrapper = await mountView()
  const root = wrapper.find('.live-topology')
  await wrapper.find('[data-testid="fullscreen-enter"]').trigger('click')
  await flushPromises()
  expect(root.element.requestFullscreen).toHaveBeenCalled()
})

  it('F3 全屏后：根元素带 is-fullscreen 类、按钮文案切「退出全屏」', async () => {
  const wrapper = await mountView()
  await wrapper.find('[data-testid="fullscreen-enter"]').trigger('click')
  await flushPromises()
  expect(wrapper.find('.live-topology.is-fullscreen').exists()).toBe(true)
  const exit = wrapper.find('[data-testid="fullscreen-exit"]')
  expect(exit.exists()).toBe(true)
  expect(exit.text()).toBe('退出全屏')
})

  it('F4 全屏态再点按钮 → exitFullscreen 且状态复位', async () => {
  const wrapper = await mountView()
  await wrapper.find('[data-testid="fullscreen-enter"]').trigger('click')
  await flushPromises()
  await wrapper.find('[data-testid="fullscreen-exit"]').trigger('click')
  await flushPromises()
  expect(document.exitFullscreen).toHaveBeenCalled()
  expect(wrapper.find('.live-topology.is-fullscreen').exists()).toBe(false)
  expect(wrapper.find('[data-testid="fullscreen-enter"]').exists()).toBe(true)
})

  it('F5 全屏态卸载组件 →主动退出全屏（不带全屏态回壳布局）', async () => {
  const wrapper = await mountView()
  await wrapper.find('[data-testid="fullscreen-enter"]').trigger('click')
  await flushPromises()
  wrapper.unmount()
  expect(document.exitFullscreen).toHaveBeenCalled()
})
})
