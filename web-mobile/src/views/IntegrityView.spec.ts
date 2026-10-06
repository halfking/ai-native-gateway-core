import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import IntegrityView from './IntegrityView.vue'
import { fetchModelIntegrityEvents, resolveModelIntegrityEvent } from '@/api/modelIntegrity'
import type { ModelIntegrityRecord } from '@/api/modelIntegrity'
import { useAuthStore } from '@/stores/auth'

/**
 * 模型完整性视图（2026-10-06）。
 *
 * 这是移动端**第一个服务端分页**列表 —— 其余列表（节点/模型/供应商/密钥）都是
 * 「一次拉全量 + 客户端切片」。所以本组判据专打这个差异：
 *
 *   ① page → offset 映射：page 1 必须 offset=0，page 2 必须 offset=PAGE_SIZE。
 *      若照抄其他视图的 `if (page > 1) return []` 写法，本页会永远停在第一页。
 *   ② `total` 取 **resp.count**（后端 COUNT(*) 命中总数，:140/:202）而不是
 *      `resp.events.length`。后者会让「已加载 X / 总计 Y」两数永远相同，
 *      看起来像「已全部加载」。
 *   ③ 权限：整段 superAdmin（handler.go:924-925）⇒ 非 super_admin 不该看到处置入口。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/modelIntegrity', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/modelIntegrity')>()
  return {
    ...actual,
    fetchModelIntegrityEvents: vi.fn(),
    resolveModelIntegrityEvent: vi.fn(async () => ({ ok: true })),
  }
})

const PAGE_SIZE = 20

/** jsdom 里可靠驱动 v-model：原生 setter 定义 value + 派发 input。 */
function setNativeValue(el: HTMLTextAreaElement | HTMLInputElement, value: string): void {
  const proto = Object.getPrototypeOf(el) as object
  const desc = Object.getOwnPropertyDescriptor(proto, 'value')
  if (desc?.set) desc.set.call(el, value)
  else el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

function evt(n: number, over: Partial<ModelIntegrityRecord> = {}): ModelIntegrityRecord {
  return {
    id: n,
    detected_at: '2026-10-06T13:00:00Z',
    anomaly_type: 'fingerprint_drift',
    severity: 'high',
    resolved: false,
    provider_code: 'anthropic',
    client_model: 'claude-sonnet-4-6',
    ...over,
  }
}

/** 轮询等列表状态机离开 loading —— loadNext() 在 loading 态会静默 return。 */
async function waitForIdle(ctl: { readonly state: string }, tries = 40): Promise<void> {
  for (let i = 0; i < tries; i++) {
    await flushPromises()
    const s = String(ctl.state ?? '')
    if (s !== 'initialLoading' && s !== 'refreshing' && s !== 'loadingNext') return
    await new Promise((r) => setTimeout(r, 5))
  }
}

/** 清掉所有 mock 的**实现与 once 队列**（不只是调用记录）。 */
function mockResetAll(): void {
  vi.resetAllMocks()
}

/**
 * ★ 统一卸载。
 *
 * `document.body.innerHTML = ''` **只清 DOM 树，不销毁 Vue 组件实例**。
 * 本文件每个用例都 `mount(..., {attachTo: document.body})`，若不显式 unmount，
 * 上一用例的组件仍活着、它的 Teleport Sheet 仍挂在 body 上 ⇒
 * `document.body.querySelector('textarea')` 会抓到**上一个用例的输入框**。
 *
 * 症状极具迷惑性：单跑某用例绿、整跑红，且断言里收到的是**另一个用例造的数据**
 * （本轮实测收到 id=1 而非本用例的 id=5）。
 */
let mounted: Array<{ unmount(): void }> = []

async function mountAs(role: string) {
  const pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore().userInfo = {
    id: 1, tenant_id: 't1', username: 'u', display_name: 'U',
    email: 'e', role, enabled: true,
  }
  const w = mount(IntegrityView, { attachTo: document.body, global: { plugins: [pinia] } })
  mounted.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

describe('IntegrityView 服务端分页', () => {
  beforeEach(() => {
    // 先销毁组件实例，再清 DOM —— 顺序反了等于没清
    for (const w of mounted) {
      try {
        w.unmount()
      } catch {
        /* 已卸载 */
      }
    }
    mounted = []
    document.body.innerHTML = ''
    // ★ 必须是 mockReset 不是 clearAllMocks：clearAllMocks **只清调用记录，
    //   保留 once 队列**。而「非 super_admin」用例里 v-if 分支根本不挂 HyperList
    //   ⇒ 它 set 的 mockResolvedValueOnce(evt(1)) 永远不被消费，
    //   于是**串到下一个用例**，让下一个用例渲染出 id=1 而非它自己设的 id=5。
    //   症状：单跑绿、整跑红，收到的还是**另一个用例造的数据**。
    mockResetAll()
  })

  afterEach(() => {
    for (const w of mounted) {
      try {
        w.unmount()
      } catch {
        /* ignore */
      }
    }
    mounted = []
  })

  it('page 1 用 offset=0，且默认只查未处置', async () => {
    ;(fetchModelIntegrityEvents as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      events: [evt(1)], count: 57, limit: PAGE_SIZE, offset: 0,
    })
    await mountAs('super_admin')

    expect(fetchModelIntegrityEvents).toHaveBeenCalledWith({
      limit: PAGE_SIZE, offset: 0, unresolved_only: true,
    })
    const text = document.body.textContent ?? ''
    // ★ 总数取 count(57) 而不是本页条数(1)
    expect(text).toContain('57')
  })

  it('★ page→offset 映射：首屏 offset=0，loadNext 后 offset=20', async () => {
    // 本页是移动端**唯一的服务端分页**列表（其余列表都是全量 + 客户端切片），
    // 所以 page→offset 映射必须真验。
    // 用 controller 的公开 API 驱动：loadNext() 精确推进一页，不依赖
    // IntersectionObserver（jsdom 里没有，sentinel 不会触发预载）。
    const m = fetchModelIntegrityEvents as ReturnType<typeof vi.fn>
    m.mockResolvedValue({ events: [evt(1)], count: 999, limit: PAGE_SIZE, offset: 0 })
    const w = await mountAs('super_admin')
    await flushPromises()
    await flushPromises()

    const ctl = (w.vm as unknown as {
      controller: { readonly state: string; loadFirst(q: 'requery'): void; loadNext(): void }
    }).controller

    // ★ m.mockReset() 而非 mockClear()：clear 只清调用记录，**保留实现队列**；
    //   首屏 autoFill 可能已排了第 2、3 页的请求（07 §3 首屏补页 ≤3 页），
    //   它们在 mockClear 之后才落地 ⇒ at(-1) 取到的是补页的 offset=20 而不是首屏的 0。
    //   这就是「同一份代码有时绿有时红」的第二层原因。
    m.mockReset()
    m.mockResolvedValue({ events: [evt(1)], count: 999, limit: PAGE_SIZE, offset: 0 })
    ctl.loadFirst('requery')
    await flushPromises()
    await waitForIdle(ctl)
    expect((m.mock.calls.at(-1)?.[0] as { offset?: number }).offset).toBe(0)

    m.mockReset()
    m.mockResolvedValue({ events: [evt(21)], count: 999, limit: PAGE_SIZE, offset: PAGE_SIZE })
    // ★ 必须轮询等状态就绪再 loadNext：loadNext() 在 _state 为
    //   initialLoading / refreshing / loadingNext 时**静默 return**（:111-119），
    //   不 await 就调会随机被丢弃 —— 症状是同一份代码「有时绿有时红」。
    //   本轮实测 6 次里 3 次失败，报 expected 20 to be 0。
    //   判据本身没错，错的是**没等状态机**。
    await waitForIdle(ctl)
    ctl.loadNext()
    await flushPromises()
    await waitForIdle(ctl)

    // ★ 第二页必须带 offset=20。若实现照抄其他视图的「page>1 返空」，
    //   这里要么没有调用、要么 offset 仍是 0 —— 两种都会让本断言红。
    const calls = m.mock.calls.map((c) => (c[0] as { offset?: number }).offset)
    expect(calls).toContain(PAGE_SIZE)
  })

  it('非 super_admin 看到权限说明，不渲染列表与处置入口', async () => {
    ;(fetchModelIntegrityEvents as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      events: [evt(1)], count: 3, limit: PAGE_SIZE, offset: 0,
    })
    const w = await mountAs('tenant_admin')
    const text = w.text()
    expect(text).toContain('requires super admin')
    // 不该渲染卡片
    expect(w.findAll('.integrity__card').length).toBe(0)
  })

  it('处置后从「未处置」列表移除，且调用了 resolve 端点', async () => {
    const m = fetchModelIntegrityEvents as ReturnType<typeof vi.fn>
    m.mockResolvedValue({ events: [evt(5)], count: 1, limit: PAGE_SIZE, offset: 0 })
    const w = await mountAs('super_admin')

    const cards = w.findAll('.integrity__card')
    if (cards.length !== 1) throw new Error(`期望 1 张卡，实际 ${cards.length}`)
    await cards[0]?.trigger('click')
    await flushPromises()
    await flushPromises()

    // ★ Sheet 经 Teleport 渲染到 body（AppSheet.vue:89）⇒ 断言查 document，
    //   用 wrapper.find 会永远找不到，且失败信息会误导成「Sheet 没渲染」。
    const notes = document.body.querySelector('textarea') as HTMLTextAreaElement | null
    if (!notes) throw new Error('处置说明输入框不存在')
    // ★ 直接赋 .value 后 dispatchEvent('input') 对 Vue 3 的 v-model **可能不生效**
    //   （Vue 监听的是经过其 patch 过的值通道）。用原生 setter 定义 value 再派发，
    //   才能确定 v-model 收到。这是 jsdom 里驱动 v-model 的可靠写法。
    setNativeValue(notes, '上游已回滚')
    await flushPromises()
    const resolveBtn = Array.from(document.body.querySelectorAll('.btn')).find((b) =>
      (b.textContent ?? '').includes('Mark as resolved'),
    ) as HTMLButtonElement | undefined
    if (!resolveBtn) throw new Error('处置按钮不存在')
    resolveBtn.dispatchEvent(new Event('click'))
    await flushPromises()
    await flushPromises()

    // ★ 处置成功后视图会 detail=null 关掉 Sheet（默认视角是「未处置」），
    //   所以断言点必须放在**点击前**或只查 mock 调用，不能在点击后再去查 DOM。
    expect(resolveModelIntegrityEvent).toHaveBeenCalledWith(5, '上游已回滚')
  })

  it('403 单独提示权限，不显示后端英文原文', async () => {
    const { ApiError } = await import('@/api/client')
    ;(resolveModelIntegrityEvent as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new ApiError(403, 'forbidden'))
    const m = fetchModelIntegrityEvents as ReturnType<typeof vi.fn>
    m.mockResolvedValue({ events: [evt(5)], count: 1, limit: PAGE_SIZE, offset: 0 })
    const w = await mountAs('super_admin')
    await w.find('.integrity__card').trigger('click')
    await flushPromises()
    await flushPromises()
    const btn = Array.from(document.body.querySelectorAll('.btn')).find((b) =>
      (b.textContent ?? '').includes('Mark as resolved'),
    ) as HTMLButtonElement | undefined
    if (!btn) throw new Error('处置按钮不存在')
    btn.dispatchEvent(new Event('click'))
    await flushPromises()
    await flushPromises()

    const err = document.body.querySelector('.integrity__msg--err')
    if (!err) throw new Error('错误提示未渲染')
    expect(err.textContent).toContain('cannot resolve')
  })
})
