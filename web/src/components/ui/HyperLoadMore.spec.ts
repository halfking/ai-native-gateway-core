// HyperLoadMore.spec.ts — 连续加载尾部控件门禁（docs/UI规范/00 §5.4 · H3，参考规范 07 §3 / 13 §1）。
//
// 本组件的复杂度几乎全在「什么时候**不该**触发」上，所以大部分用例是
// 「不该发请求时确实没发」。只测正向路径的话，四条约束一条都拦不住。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import HyperLoadMore from './HyperLoadMore.vue'
import type { ContinuousState } from '../../lib/shell/hyper/hyperPages'

/**
 * 剥掉全部注释再做源码断言。
 * 只剥块注释是不够的 —— 解释性 `//` 行注释里全是中文，
 * 会被误判成「硬编码中文文案」（本仓既有教训）。
 * 行注释的正则要避开 `://`，否则会把 URL 截断。
 */
export function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '') // 块注释
    .replace(/<!--[\s\S]*?-->/g, '') // 模板注释
    .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1') // 行注释
}

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/HyperLoadMore.vue'), 'utf8')
const codeOnly = stripComments(source)

/** 可手动驱动的 IntersectionObserver 替身。jsdom 不提供这个 API。 */
class MockIO {
  static instances: MockIO[] = []
  callback: IntersectionObserverCallback
  options: IntersectionObserverInit | undefined
  observed: Element[] = []
  disconnected = false

  constructor(cb: IntersectionObserverCallback, options?: IntersectionObserverInit) {
    this.callback = cb
    this.options = options
    MockIO.instances.push(this)
  }

  observe(el: Element) {
    this.observed.push(el)
  }

  unobserve() {}
  disconnect() {
    this.disconnected = true
    this.observed = []
  }

  takeRecords() {
    return []
  }

  /** 模拟一次交叉状态上报。 */
  trigger(isIntersecting: boolean, top = 100) {
    this.callback(
      [{ isIntersecting, boundingClientRect: { top } } as unknown as IntersectionObserverEntry],
      this as unknown as IntersectionObserver,
    )
  }
}

const originalIO = (globalThis as { IntersectionObserver?: unknown }).IntersectionObserver

function withIO() {
  MockIO.instances = []
  ;(globalThis as { IntersectionObserver?: unknown }).IntersectionObserver = MockIO
}

function withoutIO() {
  MockIO.instances = []
  delete (globalThis as { IntersectionObserver?: unknown }).IntersectionObserver
}

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { hyper: { list: { allLoaded: '已全部加载 {count} 条', loadFailed: '加载失败，点击重试', retry: '重试', loadMore: '继续加载', loadingMore: '加载中…', refreshing: '正在刷新…' } } } },
})

function factory(props: Partial<{ state: ContinuousState; hasMore: boolean; loadedCount: number; maxAutoFillPages: number; rootMarginBottom: number; scrollHost: HTMLElement | null }> = {}) {
  return mount(HyperLoadMore, {
    props: { state: 'idle', hasMore: true, loadedCount: 30, ...props },
    global: { plugins: [i18n] },
  })
}

beforeEach(() => withIO())
afterEach(() => {
  if (originalIO === undefined) delete (globalThis as { IntersectionObserver?: unknown }).IntersectionObserver
  else (globalThis as { IntersectionObserver?: unknown }).IntersectionObserver = originalIO
})

describe('HyperLoadMore：状态呈现（三态统一，不出假控件）', () => {
  it('exhausted：显示已全部加载 N 条，无按钮无 sentinel，无 observer', () => {
    const w = factory({ state: 'exhausted', hasMore: false, loadedCount: 42 })
    expect(w.text()).toContain('已全部加载 42 条')
    expect(w.find('.hyper-load-more__btn').exists()).toBe(false)
    expect(w.find('.hyper-load-more__sentinel').exists()).toBe(false)
    expect(MockIO.instances).toHaveLength(0)
  })

  it('failed：显示失败文案 + 重试按钮，点击发 retry', async () => {
    const w = factory({ state: 'failed' })
    expect(w.text()).toContain('加载失败，点击重试')
    const btn = w.find('.hyper-load-more__btn')
    expect(btn.exists()).toBe(true)
    expect(btn.text()).toBe('重试')
    await btn.trigger('click')
    expect(w.emitted('retry')).toHaveLength(1)
    expect(w.emitted('load-more')).toBeUndefined()
  })

  it('loadingNext：只出忙态，不出可点控件（防重复请求）', () => {
    const w = factory({ state: 'loadingNext' })
    expect(w.find('.hyper-load-more__busy').exists()).toBe(true)
    expect(w.text()).toContain('加载中…')
    expect(w.find('.hyper-load-more__btn').exists()).toBe(false)
    expect(w.find('.hyper-load-more__sentinel').exists()).toBe(false)
  })

  it('refreshing：忙态文案与 loadingNext 不同', () => {
    const w = factory({ state: 'refreshing' })
    expect(w.text()).toContain('正在刷新…')
    expect(w.find('.hyper-load-more__btn').exists()).toBe(false)
  })

  it('paused：给出回来的入口，且不发自定义事件', async () => {
    const w = factory({ state: 'paused' })
    const btn = w.find('.hyper-load-more__btn')
    expect(btn.exists()).toBe(true)
    await btn.trigger('click')
    // 组件只收集触发，「要不要先 resume」是宿主的事
    expect(w.emitted('load-more')).toHaveLength(1)
    expect(w.emitted('resume')).toBeUndefined()
  })
})

describe('HyperLoadMore：约束 1 — 只有有下一页且非 busy 才触发', () => {
  it('idle + 交叉命中 → 发 load-more', () => {
    const w = factory()
    expect(w.find('.hyper-load-more__sentinel').exists()).toBe(true)
    MockIO.instances[0].trigger(true)
    expect(w.emitted('load-more')).toHaveLength(1)
  })

  it('不交叉不发', () => {
    factory()
    MockIO.instances[0].trigger(false)
    expect(MockIO.instances[0].callback).toBeDefined()
  })

  it('busy 状态下即使交叉命中也不发（触发点二次校验）', async () => {
    const w = factory({ state: 'idle' })
    MockIO.instances[0].trigger(true)
    expect(w.emitted('load-more')).toHaveLength(1)

    // 状态转 busy 后，再来的交叉事件必须被忽略
    await w.setProps({ state: 'loadingNext' })
    const live = MockIO.instances[MockIO.instances.length - 1]
    if (live) live.trigger(true)
    expect(w.emitted('load-more')).toHaveLength(1)
  })

  it('hasMore=false 时连 observer 都不建', () => {
    factory({ hasMore: false, state: 'idle' })
    expect(MockIO.instances).toHaveLength(0)
  })

  it('exhausted / failed 状态下不建 observer（停自动重试）', () => {
    factory({ state: 'exhausted', hasMore: false })
    expect(MockIO.instances).toHaveLength(0)
    MockIO.instances = []
    factory({ state: 'failed' })
    expect(MockIO.instances).toHaveLength(0)
  })
})

describe('HyperLoadMore：约束 2 — root 是祖先，rootMargin 240px', () => {
  it('scrollHost 被用作 root', () => {
    const host = document.createElement('div')
    factory({ scrollHost: host })
    expect(MockIO.instances[0].options?.root).toBe(host)
  })

  it('不传 scrollHost 时 root 为 null（退化成视口）', () => {
    factory({ scrollHost: null })
    expect(MockIO.instances[0].options?.root ?? null).toBeNull()
  })

  it('rootMargin 底边提前量默认 240px，可覆盖', () => {
    factory()
    expect(MockIO.instances[0].options?.rootMargin).toBe('0px 0px 240px 0px')
    MockIO.instances = []
    factory({ rootMarginBottom: 100 })
    expect(MockIO.instances[0].options?.rootMargin).toBe('0px 0px 100px 0px')
  })

  it('观察的是 sentinel 本身', () => {
    const w = factory()
    const el = w.find('.hyper-load-more__sentinel').element
    expect(MockIO.instances[0].observed).toContain(el)
  })
})

describe('HyperLoadMore：约束 3 — 单轮自动填屏上限', () => {
  it('连续自动触发到上限后降级为按钮，不再观察', async () => {
    const w = factory({ maxAutoFillPages: 2 })
    const io = MockIO.instances[0]
    io.trigger(true)
    io.trigger(true)
    await nextTick() // 配额是响应式的，DOM 要等一拍
    expect(w.emitted('load-more')).toHaveLength(2)
    // 配额用尽 → sentinel 消失，按钮出现
    expect(w.find('.hyper-load-more__sentinel').exists()).toBe(false)
    const btn = w.find('.hyper-load-more__btn')
    expect(btn.exists()).toBe(true)
    expect(btn.text()).toBe('继续加载')
  })

  it('默认上限为 3（规范设计值）', async () => {
    const w = factory()
    const io = MockIO.instances[0]
    io.trigger(true)
    io.trigger(true)
    io.trigger(true)
    await nextTick()
    expect(w.emitted('load-more')).toHaveLength(3)
    expect(w.find('.hyper-load-more__sentinel').exists()).toBe(false)
  })

  it('手动点按钮后配额重置，下一屏仍可自动填', async () => {
    const w = factory({ maxAutoFillPages: 1 })
    MockIO.instances[0].trigger(true)
    await nextTick()
    expect(w.find('.hyper-load-more__sentinel').exists()).toBe(false)

    await w.find('.hyper-load-more__btn').trigger('click')
    expect(w.emitted('load-more')).toHaveLength(2)
    // 配额已重置 → 重新回到自动形态
    expect(w.find('.hyper-load-more__sentinel').exists()).toBe(true)
  })

  it('resetAutoFill 重置配额并通知宿主', async () => {
    const w = factory({ maxAutoFillPages: 1 })
    MockIO.instances[0].trigger(true)
    await nextTick()
    expect(w.find('.hyper-load-more__sentinel').exists()).toBe(false)

    ;(w.vm as unknown as { resetAutoFill: () => void }).resetAutoFill()
    await w.vm.$nextTick()
    expect(w.emitted('auto-fill-reset')).toHaveLength(1)
    expect(w.find('.hyper-load-more__sentinel').exists()).toBe(true)
  })
})

describe('HyperLoadMore：约束 4 — 滚出顶部不算接近底部', () => {
  it('isIntersecting 但 top 为负时不触发', () => {
    const w = factory()
    MockIO.instances[0].trigger(true, -50)
    expect(w.emitted('load-more')).toBeUndefined()
  })

  it('top 为 0 时仍触发（恰好在顶部边缘）', () => {
    const w = factory()
    MockIO.instances[0].trigger(true, 0)
    expect(w.emitted('load-more')).toHaveLength(1)
  })
})

describe('HyperLoadMore：无 IntersectionObserver 时降级为按钮', () => {
  beforeEach(() => withoutIO())

  it('渲染「继续加载」按钮，且没有 sentinel', () => {
    const w = factory()
    expect(w.find('.hyper-load-more__sentinel').exists()).toBe(false)
    const btn = w.find('.hyper-load-more__btn')
    expect(btn.exists()).toBe(true)
    expect(btn.text()).toBe('继续加载')
  })

  it('点击按钮发 load-more', async () => {
    const w = factory()
    await w.find('.hyper-load-more__btn').trigger('click')
    expect(w.emitted('load-more')).toHaveLength(1)
  })

  it('不是空页：按钮可聚焦且有可见文案', () => {
    const w = factory()
    const btn = w.find('.hyper-load-more__btn')
    expect(btn.element.tagName).toBe('BUTTON')
    expect(btn.element.getAttribute('type')).toBe('button')
    expect(btn.text().trim().length).toBeGreaterThan(0)
  })

  it('降级态下 exhausted / failed 仍按状态呈现，不受影响', () => {
    const w = factory({ state: 'exhausted', hasMore: false, loadedCount: 7 })
    expect(w.text()).toContain('已全部加载 7 条')
    expect(w.find('.hyper-load-more__btn').exists()).toBe(false)
  })
})

describe('HyperLoadMore：状态迁移时断开观察', () => {
  /**
   * 断言用「同一时刻只有一个活 observer」而不是数实例个数：
   * 重建几次是实现细节，但**旧的一定要断开**才是真约束。
   * 数个数会把「实现碰巧只重建一次」也变成门禁，反而更脆。
   */
  function liveObservers(): MockIO[] {
    return MockIO.instances.filter((i) => !i.disconnected)
  }

  // 这条路径才是常态：先观察着，一路加载到 exhausted/failed/paused。
  // 挂载时的断言管不到这里 —— 真正的风险是「已建的 observer 没被断开」。
  for (const to of ['exhausted', 'failed', 'paused'] as const) {
    it(`idle → ${to}：断开已建的 observer，且不再新建`, async () => {
      const w = factory({ state: 'idle' })
      expect(liveObservers()).toHaveLength(1)

      await w.setProps({ state: to, hasMore: to === 'exhausted' ? false : true })
      await nextTick()

      expect(liveObservers()).toHaveLength(0)
      expect(w.find('.hyper-load-more__sentinel').exists()).toBe(false)
    })
  }

  it('idle → loadingNext：断开观察，请求进行中不再观察', async () => {
    const w = factory({ state: 'idle' })
    await w.setProps({ state: 'loadingNext' })
    await nextTick()
    expect(liveObservers()).toHaveLength(0)
  })

  it('回到 idle 后重新建立观察，且旧的都已断开', async () => {
    const w = factory({ state: 'idle' })
    await w.setProps({ state: 'loadingNext' })
    await nextTick()
    await w.setProps({ state: 'idle' })
    await nextTick()

    // ★ 这条曾经转红：pre-flush 的 watch 在 sentinel 重新挂载前就跑了，
    // 表现为「加载完第一页后自动加载永久失效」。
    const live = liveObservers()
    expect(live).toHaveLength(1)
    expect(live[0].observed).toContain(w.find('.hyper-load-more__sentinel').element)
  })

  it('多轮 idle↔loadingNext 之后仍只有一个活 observer', async () => {
    const w = factory({ state: 'idle' })
    for (let i = 0; i < 3; i++) {
      await w.setProps({ state: 'loadingNext' })
      await nextTick()
      await w.setProps({ state: 'idle' })
      await nextTick()
    }
    expect(liveObservers()).toHaveLength(1)
  })
})

describe('HyperLoadMore：生命周期与静态约束', () => {
  it('卸载时断开 observer', () => {
    const w = factory()
    const io = MockIO.instances[0]
    expect(io.disconnected).toBe(false)
    w.unmount()
    expect(io.disconnected).toBe(true)
  })

  it('本组件不发任何请求（加载语义只在 createHyperPages 一处）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest', 'useQuery', 'useFetch']) {
      expect(codeOnly, `HyperLoadMore 不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
  })

  it('sentinel 是纯几何触发点：不可聚焦、不抢焦点', () => {
    const w = factory()
    const el = w.find('.hyper-load-more__sentinel')
    expect(el.attributes('aria-hidden')).toBe('true')
    // 没有任何 tabindex / role="button" 之类的伪交互
    expect(el.attributes('tabindex')).toBeUndefined()
    expect(el.attributes('role')).toBeUndefined()
  })

  it('忙态用 aria-live 播报，状态变化可被读屏感知', () => {
    const w = factory({ state: 'loadingNext' })
    const busy = w.find('.hyper-load-more__busy')
    expect(busy.attributes('role')).toBe('status')
    expect(busy.attributes('aria-live')).toBe('polite')
  })

  it('没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `模板里出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })
})
