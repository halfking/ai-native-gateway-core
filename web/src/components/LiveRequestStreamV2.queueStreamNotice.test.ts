// LiveRequestStreamV2.queueStreamNotice.test.ts — 2026-10-15 首开连接反馈 + closed 自动重连判据
//
// 用户报告：第一次打开总览页「按处理队列」（groupBy 默认值就是 'queue'，
// liveStreamPreferences defaults）看不到任何连接动作，需切维度再切回才恢复。
// 判据与被测逻辑同层：真实挂载组件 + 真实 liveStreamStore 状态机，EventSource
// 用可控假件（jsdom 无 EventSource，直接裸跑会落 'unsupported' 分支，
// 连不上要测的 connecting/closed 路径——那是假绿的形态）。
//
// 三条断言链：
//   ① 首开 connecting → 队列区出现「正在连接实时流」状态条；open → 消失。
//   ② EventSource 致命错误（readyState=2 → closed）→ 状态条转「正在自动重连」，
//      5s 后组件自动调 reconnectStream（新 EventSource 实例产生）。
//      变异防护：删掉 watch 的 closed 分支 → ②红。
//   ③ open 后退避复位：不再有新实例；closed→open→closed 的第二次退避仍从 5s 起。
import { mount, enableAutoUnmount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import LiveRequestStreamV2 from './LiveRequestStreamV2.vue'
import { __testing, liveStreamState } from '../composables/liveStreamStore'
import { _resetPersistState } from '../composables/liveStreamPreferences'
import zhCNDashboard from '../locales/zh-CN/dashboard'

enableAutoUnmount(afterEach)

class FakeEventSource {
  static instances: FakeEventSource[] = []
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 2
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  url: string
  constructor(url: string, _opts?: { withCredentials?: boolean }) {
    this.url = url
    FakeEventSource.instances.push(this)
  }
  close() {
    this.readyState = 2
  }
}

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { dashboard: zhCNDashboard } },
})

function mountStream() {
  return mount(LiveRequestStreamV2, {
    shallow: true,
    global: { plugins: [i18n] },
  })
}

function noticeStrip(wrapper: ReturnType<typeof mountStream>) {
  return wrapper.find('.live-queue-connect')
}

describe('LiveRequestStreamV2 queue-view stream notice + auto-reconnect', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    FakeEventSource.instances = []
    vi.stubGlobal('EventSource', FakeEventSource)
    __testing.resetStream()
    __testing.resetForTest()
    _resetPersistState()
    localStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('shows 正在连接实时流 in the queue view while connecting, and keeps a marker until the first frame arrives', async () => {
    const wrapper = mountStream()
    await vi.advanceTimersByTimeAsync(0)

    // 挂载即 acquire → openConnection → connecting（groupBy 默认 queue，
    // 状态条必须在队列数据区里可见，而不是只藏在上工具栏药丸里）。
    expect(liveStreamState.connection).toBe('connecting')
    const strip = noticeStrip(wrapper)
    expect(strip.exists()).toBe(true)
    expect(strip.classes()).toContain('live-queue-connect--connecting')
    expect(strip.text()).toContain('正在连接实时流')

    // ★ r1032c：连接建立 ≠ 数据到达。open 后首帧未到（lastEventAt 仍为 0），
    // 加载标记必须继续挂在数据区——本地实测这段空窗 ~1.5s，此前无任何标记，
    // 队列面板还会抢跑出「队列数据未接入」的误导空态。
    FakeEventSource.instances[0].onopen?.()
    await vi.advanceTimersByTimeAsync(0)
    expect(liveStreamState.connection).toBe('open')
    expect(liveStreamState.lastEventAt).toBe(0)
    const awaiting = noticeStrip(wrapper)
    expect(awaiting.exists()).toBe(true)
    expect(awaiting.classes()).toContain('live-queue-connect--connecting')
    expect(awaiting.text()).toContain('正在接收首批数据')

    // 首帧到达 → 标记消失（数据区不该常驻提示）。
    FakeEventSource.instances[0].onmessage?.({ data: JSON.stringify({ type: 'health_update', ts: new Date().toISOString() }) })
    await vi.advanceTimersByTimeAsync(0)
    expect(liveStreamState.lastEventAt).toBeGreaterThan(0)
    expect(noticeStrip(wrapper).exists()).toBe(false)
  })

  it('auto-reconnects with backoff after a fatal (closed) EventSource error', async () => {
    const wrapper = mountStream()
    const first = FakeEventSource.instances[0]
    first.onopen?.()
    // 送入首帧（本用例测的是断连重连，不是首帧等待——先把流置为「已交付」）。
    first.onmessage?.({ data: JSON.stringify({ type: 'health_update', ts: new Date().toISOString() }) })
    await vi.advanceTimersByTimeAsync(0)
    expect(liveStreamState.connection).toBe('open')
    expect(liveStreamState.lastEventAt).toBeGreaterThan(0)

    // 致命错误：readyState=2（HTTP 层 401/404 等），浏览器不会再自动重连。
    first.readyState = 2
    first.onerror?.()
    await vi.advanceTimersByTimeAsync(0)
    expect(liveStreamState.connection).toBe('closed')
    const strip = noticeStrip(wrapper)
    expect(strip.classes()).toContain('live-queue-connect--closed')
    expect(strip.text()).toContain('正在自动重连')

    // 5s 退避到期 → 组件自动重连：产生新 EventSource 实例。
    const before = __testing.openConnectionTotal()
    await vi.advanceTimersByTimeAsync(5_000)
    expect(__testing.openConnectionTotal()).toBe(before + 1)
    expect(FakeEventSource.instances.length).toBe(2)
    expect(liveStreamState.connection).toBe('connecting')

    // 新连接成功并交付首帧 → 状态条消失，退避计数复位。
    FakeEventSource.instances[1].onopen?.()
    FakeEventSource.instances[1].onmessage?.({ data: JSON.stringify({ type: 'health_update', ts: new Date().toISOString() }) })
    await vi.advanceTimersByTimeAsync(0)
    expect(liveStreamState.connection).toBe('open')
    expect(noticeStrip(wrapper).exists()).toBe(false)

    // open 之后没有任何排定的重连残留。
    const count = FakeEventSource.instances.length
    await vi.advanceTimersByTimeAsync(60_000)
    expect(FakeEventSource.instances.length).toBe(count)
  })

  it('does not auto-reconnect while the browser is handling a transient reconnect', async () => {
    // readyState=1（网络瞬断）是浏览器自身的重连窗口，store 记 'reconnecting'；
    // 组件此时不得叠加自己的重连（会把浏览器正在进行的重连掐断重来）。
    const wrapper = mountStream()
    const first = FakeEventSource.instances[0]
    first.onopen?.()
    first.onmessage?.({ data: JSON.stringify({ type: 'health_update', ts: new Date().toISOString() }) })
    await vi.advanceTimersByTimeAsync(0)
    first.readyState = 0
    first.onerror?.()
    await vi.advanceTimersByTimeAsync(0)
    expect(liveStreamState.connection).toBe('reconnecting')
    expect(noticeStrip(wrapper).classes()).toContain('live-queue-connect--connecting')

    const before = __testing.openConnectionTotal()
    await vi.advanceTimersByTimeAsync(30_000)
    expect(__testing.openConnectionTotal()).toBe(before)
    expect(FakeEventSource.instances.length).toBe(1)
  })

  it('★ swim-lane cold start says 正在连接 while the stream has not delivered (non-queue dimension)', async () => {
    // r1032c：非队列维度（按凭据/模型等）的原显示区域是泳道区，冷启动空态
    // 此前不分连接状态一律「等待实时请求流数据…」——建连窗口会被读成
    // 「没有数据」。连接未交付前必须说「正在连接」。
    localStorage.setItem('llmgw_live_stream_preferences_v1:legacy:default', JSON.stringify({ groupBy: 'credential' }))
    const wrapper = mountStream()
    await vi.advanceTimersByTimeAsync(0)
    expect(liveStreamState.connection).toBe('connecting')

    const empty = wrapper.find('.swim-lanes__empty')
    expect(empty.exists()).toBe(true)
    expect(empty.text()).toContain('正在连接实时流')
    expect(empty.text()).not.toContain('等待实时请求流数据')

    // 首帧交付后（连接 open + lastEventAt>0）才回到「等待请求」的口径。
    FakeEventSource.instances[0].onopen?.()
    FakeEventSource.instances[0].onmessage?.({ data: JSON.stringify({ type: 'health_update', ts: new Date().toISOString() }) })
    await vi.advanceTimersByTimeAsync(0)
    expect(wrapper.find('.swim-lanes__empty').text()).toContain('等待实时请求流数据')
    localStorage.removeItem('llmgw_live_stream_preferences_v1:legacy:default')
  })
})
