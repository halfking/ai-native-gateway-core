import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ModelTaskIndexView from './ModelTaskIndexView.vue'
import { fetchModelTaskIndex } from '@/api/modelTaskIndex'
import { setLocale, locale } from '@/i18n'

/**
 * ModelTaskIndexView 的六条不变量（2026-10-07）。
 *
 * 1. ★★★ 口径必须常驻：只统计**自动路由**、只取**最新一个 5 分钟桶**；
 * 2. ★★ `bucket === null` = 「后台尚未首刷」，**不是**「没有数据」；
 * 3. ★★★ `success_rate` 是 **0..1** 比率 ⇒ 0.93 显示 93.0%（不是 0.93% / 9300%）；
 * 4. ★★★ `avg=0` / `p95=1000` / `cost=0` 是**生产者 COALESCE 兜底值** ⇒ 弱化标记 + 常驻图例；
 * 5. ★★ `items` 是**稀疏对象**：`canonical_name` 键可能整个不存在 ⇒ 回落成 `#id`；
 * 6. ★ `top` 是客户端自己发的 ⇒ `items.length === top` 是**精确**截断信号。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/modelTaskIndex', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/modelTaskIndex')>()
  return { ...actual, fetchModelTaskIndex: vi.fn() }
})

const tiMock = fetchModelTaskIndex as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(ModelTaskIndexView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function item(over: Record<string, unknown> = {}) {
  return {
    canonical_id: 7,
    canonical_name: 'gpt-4o',
    task_type: 'chat',
    sample_count: 42,
    success_rate: 0.93,
    avg_latency_ms: 820,
    p95_latency_ms: 1900,
    avg_cost_per_1k_usd: 0.0042,
    primary_credential_id: 12,
    updated_at: '2026-10-07T10:05:00Z',
    ...over
  }
}

function resp(over: Record<string, unknown> = {}) {
  return { bucket: '2026-10-07T10:05:00Z', items: [item()], ...over }
}

/** 铺满 n 行（条数用于截断判定）。 */
function respN(n: number) {
  return resp({
    items: Array.from({ length: n }, (_, i) => item({ canonical_id: i + 1, task_type: 'chat' }))
  })
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  tiMock.mockResolvedValue(resp())
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE as 'zh-CN' | 'en-US')
})

describe('★★★ 判据 1：口径必须常驻', () => {
  it('★ 无条件显示「只统计自动路由」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只统计「自动路由」的请求')
  })

  it('★ 无条件显示「只取最新一个 5 分钟桶」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只取「最新一个 5 分钟桶」')
  })

  it('★ 有 bucket ⇒ 显示「数据截至」与滞后提示', async () => {
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('数据截至')
    expect(text).toContain('可能滞后 5~10 分钟')
  })

  it('★ 没有 bucket ⇒ 不显示「数据截至」（没有可声称的截至时间）', async () => {
    tiMock.mockResolvedValue({ bucket: null, items: [], warning: 'x' })
    const w = await mountView()
    expect(w.text()).not.toContain('数据截至')
  })
})

describe('★★★ 判据 2：尚未首刷 ≠ 没有数据', () => {
  it('★ bucket=null ⇒ 显示「尚未首刷新」，**不**显示空态', async () => {
    tiMock.mockResolvedValue({ bucket: null, items: [], warning: 'model_task_index is empty; awaiting first bg worker refresh' })
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('后台刷新器尚未首刷新这张表')
    expect(text).not.toContain('这个 5 分钟桶里没有自动路由记录')
  })

  it('★★ 并明说「不是没有自动路由流量」', async () => {
    tiMock.mockResolvedValue({ bucket: null, items: [], warning: 'x' })
    const w = await mountView()
    expect(w.text()).toContain('不是「这段时间没有自动路由流量」')
  })

  it('★ 有 bucket 但 items 为空 ⇒ 走普通空态（不是「尚未首刷」）', async () => {
    tiMock.mockResolvedValue(resp({ items: [] }))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('这个 5 分钟桶里没有自动路由记录')
    expect(text).not.toContain('后台刷新器尚未首刷新这张表')
  })
})

describe('★★★ 判据 3：success_rate 是 0..1', () => {
  it('★ 0.93 显示 93.0%（不是 0.93%）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('93.0%')
    expect(w.text()).not.toContain('0.93%')
  })

  it('★ 不出现 9300（证明没多乘一次）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('9300')
  })

  it('★ 0 显示 0.0%（真的是全失败）', async () => {
    tiMock.mockResolvedValue(resp({ items: [item({ success_rate: 0 })] }))
    const w = await mountView()
    expect(w.text()).toContain('0.0%')
  })

  it('★ 键不存在 ⇒ —，不是 0.0%', async () => {
    tiMock.mockResolvedValue(resp({ items: [item({ success_rate: undefined })] }))
    const w = await mountView()
    expect(w.text()).not.toContain('0.0%')
  })
})

describe('★★★ 判据 4：三个兜底值要弱化 + 有图例', () => {
  it('★★ avg_latency_ms=0 ⇒ 打弱化标记', async () => {
    tiMock.mockResolvedValue(resp({ items: [item({ avg_latency_ms: 0 })] }))
    const w = await mountView()
    expect(w.findAll('.ti__kv-item--maybe').length).toBeGreaterThan(0)
  })

  it('★★ avg_latency_ms=820 ⇒ 不打标记（证明不是恒真）', async () => {
    const w = await mountView()
    expect(w.findAll('.ti__kv-item--maybe')).toHaveLength(0)
  })

  it('★ p95=1000 ⇒ 打标记；p95=999 ⇒ 不打（证明不是「小于等于 1000 都算」）', async () => {
    tiMock.mockResolvedValue(resp({ items: [item({ p95_latency_ms: 1000 })] }))
    const a = await mountView()
    expect(a.findAll('.ti__kv-item--maybe').length).toBeGreaterThan(0)

    tiMock.mockResolvedValue(resp({ items: [item({ p95_latency_ms: 999 })] }))
    const b = await mountView()
    expect(b.findAll('.ti__kv-item--maybe')).toHaveLength(0)
  })

  it('★ cost=0 ⇒ 打标记', async () => {
    tiMock.mockResolvedValue(resp({ items: [item({ avg_cost_per_1k_usd: 0 })] }))
    const w = await mountView()
    expect(w.findAll('.ti__kv-item--maybe').length).toBeGreaterThan(0)
  })

  it('★★ 弱化的是**样式**，数值仍然照显（后端给不出判据，不能改数）', async () => {
    tiMock.mockResolvedValue(resp({ items: [item({ avg_latency_ms: 0, p95_latency_ms: 1000, avg_cost_per_1k_usd: 0 })] }))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('0ms')
    expect(text).toContain('1000ms')
    expect(text).toContain('$0.0000/1k')
  })

  it('★★ 图例常驻，且逐条说明三个兜底值', async () => {
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('这三个数字的「0 / 1000」是兜底值')
    expect(text).toContain('生产者用 0 兜底')
    expect(text).toContain('生产者用 1000 兜底')
    expect(text).toContain('不是「免费」')
  })

  it('无数据时不显示图例（不占空页面）', async () => {
    tiMock.mockResolvedValue(resp({ items: [] }))
    const w = await mountView()
    expect(w.find('.ti__legend').exists()).toBe(false)
  })
})

describe('★ 判据 5：稀疏对象', () => {
  it('★ canonical_name 键不存在 ⇒ 回落成 #id（不是空白）', async () => {
    tiMock.mockResolvedValue(resp({ items: [item({ canonical_name: undefined })] }))
    const w = await mountView()
    expect(w.find('.ti__item-model').text()).toBe('#7')
  })

  it('★ primary_credential_id 缺失 ⇒ 不渲染那一格', async () => {
    tiMock.mockResolvedValue(resp({ items: [item({ primary_credential_id: undefined })] }))
    const w = await mountView()
    expect(w.text()).not.toContain('主用凭据')
  })

  it('★ primary_credential_id 存在 ⇒ 渲染 #id', async () => {
    const w = await mountView()
    expect(w.text()).toContain('主用凭据')
    expect(w.text()).toContain('#12')
  })

  it('★ 稀疏 item（只有 NOT NULL 列）不崩', async () => {
    tiMock.mockResolvedValue(
      resp({ items: [{ canonical_id: 3, task_type: 'chat', sample_count: 1, updated_at: '2026-10-07T10:05:00Z' }] }),
    )
    const w = await mountView()
    expect(w.find('.ti__item').exists()).toBe(true)
  })
})

describe('★ 判据 6：top 与截断', () => {
  it('挂载即请求，默认 top=20', async () => {
    await mountView()
    expect(tiMock).toHaveBeenCalledTimes(1)
    expect(tiMock.mock.calls[0]![0]).toEqual({ taskType: undefined, top: 20 })
  })

  it('★ items === top ⇒ 提示截断，且**不**显示正常计数', async () => {
    tiMock.mockResolvedValue(respN(20))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('结果可能被截断')
    expect(text).not.toContain('共 20 个模型 × 任务组合')
  })

  it('★ items < top ⇒ 显示正常计数（证明上一条不是恒真）', async () => {
    tiMock.mockResolvedValue(respN(19))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('共 19 个模型 × 任务组合')
    expect(text).not.toContain('结果可能被截断')
  })

  it('★ 点 chip 切 top ⇒ 重新请求并带上新值', async () => {
    const w = await mountView()
    const chip = w.findAll('button').find((b) => b.text() === '前 50')!
    await chip.trigger('click')
    await flushPromises()
    expect(tiMock).toHaveBeenCalledTimes(2)
    expect(tiMock.mock.calls[1]![0]).toEqual({ taskType: undefined, top: 50 })
  })

  it('★ 切到 50 后 items=50 ⇒ 按新 top 判截断', async () => {
    tiMock.mockResolvedValue(respN(50))
    const w = await mountView()
    const chip = w.findAll('button').find((b) => b.text() === '前 50')!
    await chip.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('结果可能被截断')
  })

  it('★ 切到 50 后 items=20 ⇒ **不**判截断（判据跟 top 走，不是写死 20）', async () => {
    tiMock.mockResolvedValue(respN(20))
    const w = await mountView()
    const chip = w.findAll('button').find((b) => b.text() === '前 50')!
    await chip.trigger('click')
    await flushPromises()
    expect(w.text()).not.toContain('结果可能被截断')
  })

  it('★ task_type 只 trim、**不改大小写**（后端 SQL 大小写敏感）', async () => {
    const w = await mountView()
    await w.find('input.ti__input').setValue('  Code  ')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(tiMock.mock.calls[1]![0]).toEqual({ taskType: 'Code', top: 20 })
  })

  it('★ 空 task_type ⇒ 发 undefined（不是空串）', async () => {
    await mountView()
    expect(tiMock.mock.calls[0]![0]).toEqual({ taskType: undefined, top: 20 })
  })
})

describe('杂项', () => {
  it('★ `__specified__` ⇒ 显示合成键说明', async () => {
    tiMock.mockResolvedValue(resp({ items: [item({ task_type: '__specified__' })] }))
    const w = await mountView()
    expect(w.text()).toContain('不是一种真实任务')
  })

  it('★ 普通 task_type ⇒ 不显示该说明', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('不是一种真实任务')
  })

  it('403 ⇒ 报「没有权限」', async () => {
    tiMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    expect(w.text()).toContain('当前账号没有查看模型任务索引的权限')
  })

  it('无筛选时不显示「清空筛选」', async () => {
    const w = await mountView()
    const labels = w.findAll('button').map((b) => b.text())
    expect(labels).not.toContain('清空筛选')
  })

  it('★ 有筛选才显示「清空筛选」，点了归零并重查全量', async () => {
    const w = await mountView()
    await w.find('input.ti__input').setValue('chat')
    const btn = w.findAll('button').find((b) => b.text() === '清空筛选')!
    await btn.trigger('click')
    await flushPromises()
    expect((w.find('input.ti__input').element as HTMLInputElement).value).toBe('')
    expect(tiMock.mock.calls[1]![0]).toEqual({ taskType: undefined, top: 20 })
  })

  it('非 403 错误 ⇒ 显示后端消息', async () => {
    tiMock.mockRejectedValue(new Error('boom'))
    const w = await mountView()
    expect(w.text()).toContain('boom')
  })
})