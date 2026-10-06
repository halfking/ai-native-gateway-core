import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import RequestDetailView from './RequestDetailView.vue'
import { fetchRequestDetail } from '@/api/requestDetail'
import { setLocale, locale } from '@/i18n'

/**
 * RequestDetailView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ **404 身兼两职**：不存在 **与** 跨租户被拒都返同一个 404
 *    ⇒ 404 时**必须**说「也可能是跨租户」，不能只说「这个请求不存在」；
 * 2. ★★★★★ **413 不是 500**、**503 是部署没接线** —— 三者各有文案，不许合并成「查不到」；
 * 3. ★★★★ 先渲染**来源/新鲜度**，再渲染正文；
 * 4. ★★★★ `body_status` 是**三态**，键缺失显示「未知」而不是「无」；
 * 5. ★★★★ 全部 meta 指针字段 omitempty ⇒ 缺键显示「—」而不是 0/空；
 * 6. ★★★ 本地先按后端同一规则校验 ID，非法时**不发请求**；
 * 7. ★★ `omit_body` 只在「要 / 不要」两态切换，绝不发 `omit_body=0`。
 */

const ID = 'req-abcdef12'

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

/**
 * ★ 路由参数可被测试改写：带 id 进来时 `watch(..., {immediate:true})` 会**直接调 load()**，
 * 绕过「查询按钮被禁用」那道 UI 防线。
 * ⇒ 这是 load() 内那第二道 ID 闸门的**唯一可达路径**，必须单独验。
 */
let routeParams: Record<string, string> = {}
vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return { ...actual, useRoute: () => ({ params: routeParams }) }
})

vi.mock('@/api/requestDetail', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/requestDetail')>()
  return { ...actual, fetchRequestDetail: vi.fn() }
})

const dMock = fetchRequestDetail as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/** 夹具逐字抄自 `domains/requestdetail/types.go:12-80`。 */
function detailBody(over: Record<string, unknown> = {}) {
  return {
    source: 'request_logs',
    persistence: 'persisted',
    meta: {
      request_id: ID,
      tenant_id: 't-1',
      gw_session_id: 'sess-1',
      gw_task_id: 'task-1',
      client_model: 'gpt-4o',
      request_status: 'success',
      success: true,
      latency_ms: 1200,
      turn_number: 3,
      body_status: 'available',
    },
    bodies: { request_body: { a: 1 }, response_body: { b: 2 }, outbound_body: { c: 3 } },
    ...over,
  }
}

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(RequestDetailView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

/** 在输入框里填 id 并点「查询」。 */
async function lookup(w: ReturnType<typeof mount>, id: string): Promise<void> {
  await w.find('#rd-id').setValue(id)
  const btn = w.findAll('button').find((b) => b.text() === '查询')!
  await btn.trigger('click')
  await flushPromises()
  await flushPromises()
}

beforeEach(() => {
  setLocale('zh-CN')
  routeParams = {}
  vi.clearAllMocks()
  dMock.mockResolvedValue(detailBody())
  document.body.innerHTML = ''
})

afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ ID 闸门：非法不发请求', () => {
  it('★★★★★★ 合法 ID ⇒ 请求被发出', async () => {
    const w = await mountView()
    await lookup(w, ID)
    expect(dMock).toHaveBeenCalledTimes(1)
    expect(dMock).toHaveBeenCalledWith(ID, { omitBody: false })
  })

  it('★★★★★★ 非法 ID ⇒ **不发请求**，并给出说明', async () => {
    const w = await mountView()
    await w.find('#rd-id').setValue('abc..defgh')
    await flushPromises()
    expect(dMock).not.toHaveBeenCalled()
    expect(w.text()).toContain('不合法')
  })

  it('★★★★★ 「查询」按钮在 ID 非法时**禁用**（结构判据，不靠文案）', async () => {
    const w = await mountView()
    await w.find('#rd-id').setValue('short')
    await flushPromises()
    const btn = w.findAll('button').find((b) => b.text() === '查询')!
    expect(btn.attributes('disabled')).toBeDefined()
  })

  it('★★★★★★ 路由参数带**非法** ID 进来 ⇒ load() 内部那第二道闸门拦住，不发请求', async () => {
    // ★ 这是 V7 变异逼出来的：UI 上按钮被禁用，load() 的 if 永远走不到；
    //   只有路由参数这条路能到 —— 之前没有任何用例走过它。
    routeParams = { id: 'abc..defgh' }
    const w = await mountView()
    expect(dMock).not.toHaveBeenCalled()
    expect(w.text()).toContain('不合法')
  })

  it('★★★★ 路由参数带**合法** ID 进来 ⇒ 自动加载', async () => {
    routeParams = { id: ID }
    const w = await mountView()
    expect(dMock).toHaveBeenCalledWith(ID, { omitBody: false })
    expect(w.text()).toContain(ID)
  })

  it('★ 空 ID 时按钮也禁用，且不发请求', async () => {
    const w = await mountView()
    const btn = w.findAll('button').find((b) => b.text() === '查询')!
    expect(btn.attributes('disabled')).toBeDefined()
    expect(dMock).not.toHaveBeenCalled()
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 404 身兼两职：不存在 / 跨租户被拒', () => {
  it('★★★★★★ 404 ⇒ 显示「也可能是跨租户被拒」的说明', async () => {
    dMock.mockRejectedValue(new Error('request detail not found'))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('request detail not found')
    expect(w.text()).toContain('别的租户')
  })

  it('★★★★★★ 404 时**不**显示「数据来源」面板（真的没拿到东西）', async () => {
    dMock.mockRejectedValue(new Error('request detail not found'))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).not.toContain('request_logs')
  })

  it('★ 真正的空态文案**不**包含「不存在」这种断言（后端分不出来）', async () => {
    dMock.mockRejectedValue(new Error('request detail not found'))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('分不出来')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ 413 / 503 各有文案，不合并成「查不到」', () => {
  it('★★★★★ 413（body > 10MB）⇒ 说明「是请求级限制，不是服务端故障」', async () => {
    dMock.mockRejectedValue(new Error('request body exceeds 10MB limit'))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('exceeds 10MB')
    expect(w.text()).toContain('不是服务端故障')
  })

  it('★★★★★ 503（未接线）⇒ 说明「是部署没接线」', async () => {
    dMock.mockRejectedValue(new Error('request detail store not configured'))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('store not configured')
    expect(w.text()).toContain('部署没接线')
  })

  it('★★★ 503 那条**不**被误标成跨租户（两种 5xx 语义不同）', async () => {
    dMock.mockRejectedValue(new Error('request detail store not configured'))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).not.toContain('别的租户')
  })

  it('★ 其它错误 ⇒ 不套用上面三条中任何一条', async () => {
    dMock.mockRejectedValue(new Error('failed to load request detail'))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('failed to load request detail')
    expect(w.text()).not.toContain('别的租户')
    expect(w.text()).not.toContain('部署没接线')
    expect(w.text()).not.toContain('不是服务端故障')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 来源与新鲜度先于正文', () => {
  it('★★★★★★ 显示 source 与 persistence', async () => {
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('request_logs')
    expect(w.text()).toContain('persisted')
  })

  it('★★★★★★ 在途源（memory）⇒ 明确警告「进程重启后就查不到了」', async () => {
    dMock.mockResolvedValue(detailBody({ source: 'memory', persistence: 'in_flight' }))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('进程重启后就查不到了')
  })

  it('★★★★★ 落库源 ⇒ 显示「可复查的记录」那句，且**不**显示在途警告', async () => {
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('可复查的记录')
    expect(w.text()).not.toContain('进程重启后就查不到了')
  })

  it('★ `warning` 字段有值时原样透出', async () => {
    dMock.mockResolvedValue(detailBody({ warning: '部分字段来自实时流' }))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('部分字段来自实时流')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ body_status 三态', () => {
  it('★★★★★ `available` ⇒ 「有载荷」', async () => {
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('有载荷')
  })

  it('★★★★★ `unavailable` ⇒ 「无载荷」', async () => {
    dMock.mockResolvedValue(detailBody({ meta: { request_id: ID, body_status: 'unavailable' } }))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('无载荷')
  })

  it('★★★★★ **键缺失 ⇒ 「未知」**，且**不**显示成「无载荷」', async () => {
    dMock.mockResolvedValue(detailBody({ meta: { request_id: ID } }))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('未知')
    // ★ 关键：不得把「未知」渲染成「无」
    const cell = w.findAll('.rd__status')[0]
    if (!cell) throw new Error('没找到 body_status 那一格')
    expect(cell.text()).not.toContain('无载荷')
  })

  it('★★★★★ 明说「别把它当成正文被清理了」（dropped 有意不是契约值）', async () => {
    dMock.mockResolvedValue(detailBody({ meta: { request_id: ID } }))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('别当成')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ omit_body 只有两态', () => {
  it('★★★★★ 默认是「含正文」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('当前：含正文')
  })

  it('★★★★★ 切到「省略正文」⇒ 重查时带 omitBody: true', async () => {
    const w = await mountView()
    await lookup(w, ID)
    await w.findAll('button').find((b) => b.text().includes('当前：'))!.trigger('click')
    await flushPromises()
    expect(dMock).toHaveBeenLastCalledWith(ID, { omitBody: true })
  })

  it('★★★★★ 省略模式下说明「没有去取正文内容」，且不渲染正文块', async () => {
    const w = await mountView()
    await lookup(w, ID)
    await w.findAll('button').find((b) => b.text().includes('当前：'))!.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('没有去取正文内容')
    expect(w.findAll('.rd__pre')).toHaveLength(0)
  })

  it('★ 永远不会出现 `omit_body=0` 这类第三态（结构判据：只有两个按钮状态文案）', async () => {
    const w = await mountView()
    const labels = w.findAll('button').map((b) => b.text())
    expect(labels.filter((l) => l.includes('当前：')).length).toBe(1)
    expect(labels.some((l) => l.includes('=0'))).toBe(false)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ meta 的 omitempty 字段缺键显示「—」', () => {
  it('★★★★ 全部指针字段缺失 ⇒ 那些格子显示「—」而不是 0/空白', async () => {
    dMock.mockResolvedValue(detailBody({ meta: { request_id: ID }, bodies: {} }))
    const w = await mountView()
    await lookup(w, ID)
    const cells = w.findAll('.rd__cell')
    for (const label of ['耗时', '轮次', '客户端模型', '任务']) {
      const c = cells.find((x) => x.text().includes(label))
      expect(c, `找不到「${label}」这一格`).toBeTruthy()
      expect(c!.text().endsWith('—'), `「${label}」应显示「—」，实际 ${c!.text()}`).toBe(true)
    }
  })

  it('★★★ 存在时显示真实值（证明上条不是恒真）', async () => {
    const w = await mountView()
    await lookup(w, ID)
    // ★ fmtInt 加千分位（1200 → "1,200"）⇒ 判「该格有数字且不是破折号」
    const lat = w.findAll('.rd__cell').find((x) => x.text().includes('耗时'))!
    expect(lat.text()).not.toMatch(/—/)
    expect(lat.text()).toMatch(/\d/)
    expect(w.text()).toContain('gpt-4o')
  })

  it('★★ `success` 是布尔指针：缺失 ⇒ 「—」，false ⇒ 「失败」而不是「—」', async () => {
    dMock.mockResolvedValue(detailBody({ meta: { request_id: ID, success: false } }))
    const w = await mountView()
    await lookup(w, ID)
    const c = w.findAll('.rd__cell').find((x) => x.text().includes('是否成功'))!
    expect(c.text()).toContain('失败')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 正文块', () => {
  it('★★ 三块都返回时各渲染一段 JSON', async () => {
    const w = await mountView()
    await lookup(w, ID)
    expect(w.findAll('.rd__pre')).toHaveLength(3)
    expect(w.text()).toContain('"a": 1')
  })

  it('★★ 缺的那一块显示「没有返回」，**不**显示空内容', async () => {
    dMock.mockResolvedValue(
      detailBody({ bodies: { request_body: { a: 1 } } }),
    )
    const w = await mountView()
    await lookup(w, ID)
    expect(w.findAll('.rd__pre')).toHaveLength(1)
    expect(w.text()).toContain('没有返回')
  })

  it('★ `bodies` 整块缺失 ⇒ 「响应里没有正文块」', async () => {
    dMock.mockResolvedValue(detailBody({ bodies: undefined }))
    const w = await mountView()
    await lookup(w, ID)
    expect(w.text()).toContain('没有正文块')
  })
})
