import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import DataLifecycleView from './DataLifecycleView.vue'
import { fetchStorageTableSizes, fetchPartitionStatuses } from '@/api/dataLifecycle'
import { setLocale, locale } from '@/i18n'

/**
 * DataLifecycleView 的七条不变量（2026-10-06）。
 *
 * 1. ★★★★ `total_human` 必须说成「上榜 N 张合计」，**不是**「本库共 X」；
 * 2. ★★★★ `percent_of_db` 必须说成「榜内占比」，分母是榜内之和；
 * 3. ★★★ `rows` 必须标「估计行数」；
 * 4. ★★★ 分区父表必须标出（体积已含子分区，不可与分区行相加）；
 * 5. ★★★ `row_count = -1` ⇒ 显示「未知」，不是「-1 行」也不是「0 行」；
 * 6. ★★ 分区表「本次返回 N 张」，不假装是全集（查不到的表会整个消失）；
 * 7. ★★ `archived_count` 含 columnar，必须说明。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/dataLifecycle', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/dataLifecycle')>()
  return { ...actual, fetchStorageTableSizes: vi.fn(), fetchPartitionStatuses: vi.fn() }
})

const sizesMock = fetchStorageTableSizes as unknown as ReturnType<typeof vi.fn>
const partsMock = fetchPartitionStatuses as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(DataLifecycleView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function tbl(over: Record<string, unknown> = {}) {
  return {
    table: 'request_logs',
    schema: 'public',
    rows: 123456,
    total_bytes: 1024,
    total_human: '1024 bytes',
    index_bytes: 256,
    toast_bytes: 128,
    toast_human: '128 bytes',
    percent_of_db: 60,
    is_partitioned: false,
    ...over,
  }
}
function sizesResp(over: Record<string, unknown> = {}) {
  return { tables: [tbl()], total_bytes: 1024, total_human: '1024 bytes', collected_at: '2026-10-07T10:00:00Z', ...over }
}
function part(over: Record<string, unknown> = {}) {
  return {
    partition_name: 'request_logs_2026_09',
    parent_table: 'request_logs',
    start_date: '2026-09-01T00:00:00Z',
    end_date: '2026-10-01T00:00:00Z',
    row_count: 1000,
    size_bytes: 2048,
    size_human: '2048 bytes',
    is_archived: false,
    is_columnar: false,
    can_archive: false,
    ...over,
  }
}
function partTable(over: Record<string, unknown> = {}) {
  return {
    table_name: 'request_logs',
    description: '请求日志主表',
    total_partitions: 2,
    archived_count: 1,
    archivable_count: 0,
    total_rows: 2000,
    total_size_bytes: 4096,
    total_size_human: '4096 bytes',
    partitions: [part()],
    has_archive_func: true,
    archive_table_name: 'request_logs_archive',
    ...over,
  }
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  sizesMock.mockResolvedValue(sizesResp())
  partsMock.mockResolvedValue([partTable()])
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

describe('入口', () => {
  it('挂载即请求两个端点，limit=20', async () => {
    await mountView()
    expect(sizesMock).toHaveBeenCalledTimes(1)
    expect(partsMock).toHaveBeenCalledTimes(1)
    expect(sizesMock.mock.calls[0]![0]).toEqual({ limit: 20 })
  })

  it('★ 切 limit chip ⇒ 带新值重查', async () => {
    const w = await mountView()
    await w.findAll('button').find((b) => b.text() === '前 50')!.trigger('click')
    await flushPromises()
    expect(sizesMock.mock.calls[1]![0]).toEqual({ limit: 50 })
  })

  it('★★ 一个端点失败 ⇒ 另一个照样显示', async () => {
    sizesMock.mockRejectedValueOnce(new Error('sizes boom'))
    const w = await mountView()
    expect(w.text()).toContain('sizes boom')
    expect(w.text()).toContain('request_logs')
  })
})

describe('★★★★ 判据 1/2：合计与占比口径', () => {
  it('★★★ 合计说成「上榜 N 张表合计 S」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('上榜 1 张表合计 1024 bytes')
  })

  it('★★★ 页面**不**出现「本库/整库共 X」这类说法', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('本库共')
    expect(w.text()).not.toContain('整库共')
  })

  it('★★★ 明说「合计不是整库大小」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('不是整库大小')
  })

  it('★★★ 明说「榜内占比的分母是这些表之和」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('分母是上面这些表之和')
    expect(w.text()).toContain('与占全库百分比无关')
  })

  it('★ 占比渲染为「榜内 N%」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('榜内 60%')
  })
})

describe('★★★ 判据 3/4：估计行数与父子重叠', () => {
  it('★★ 行数标「估计行数」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('估计行数 123,456')
  })

  it('★★ 分区父表被标出', async () => {
    sizesMock.mockResolvedValue(sizesResp({ tables: [tbl({ table: 'request_logs', is_partitioned: true })] }))
    const w = await mountView()
    expect(w.text()).toContain('分区父表')
  })

  it('★ 普通表不标父表（证明不是恒真）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('分区父表')
  })
})

describe('★★★ 判据 5：row_count = -1 是「未知」', () => {
  it('★★ -1 ⇒ 显示「未知」', async () => {
    partsMock.mockResolvedValue([partTable({ partitions: [part({ row_count: -1 })] })])
    const w = await mountView()
    await w.find('.dl__ptable-head').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('未知')
  })

  it('★ 0 ⇒ 显示 0（不是未知）', async () => {
    partsMock.mockResolvedValue([partTable({ partitions: [part({ row_count: 0 })] })])
    const w = await mountView()
    await w.find('.dl__ptable-head').trigger('click')
    await flushPromises()
    const rowsLine = w.findAll('.dl__part-meta').map((e) => e.text()).find((s) => s.includes('行数')) ?? ''
    expect(rowsLine).toMatch(/行数\s*:?\s*0\s*$/)
    expect(rowsLine).not.toContain('未知')
  })

  it('★ 正常值 ⇒ 显示数值且无「未知」', async () => {
    const w = await mountView()
    await w.find('.dl__ptable-head').trigger('click')
    await flushPromises()
    const rowsLine = w.findAll('.dl__part-meta').map((e) => e.text()).find((s) => s.includes('行数')) ?? ''
    expect(rowsLine).toMatch(/行数\s*:?\s*1,000\s*$/)
    expect(rowsLine).not.toContain('未知')
  })
})

describe('★★ 判据 6/7：返回张数与 archived 口径', () => {
  it('★★ 明说「本次返回 N 张」且说明缺失不可分辨', async () => {
    const w = await mountView()
    expect(w.text()).toContain('本次返回 1 张分区表')
    expect(w.text()).toContain('无法判断少了的那张是查不到还是不存在')
  })

  it('★★ 明说 archived 含 columnar', async () => {
    const w = await mountView()
    expect(w.text()).toContain('把「列存（columnar）」的分区也算了进去')
  })

  it('★ 分区默认不展开', async () => {
    const w = await mountView()
    expect(w.find('.dl__part').exists()).toBe(false)
  })

  it('★ 点表头 ⇒ 展开分区明细', async () => {
    const w = await mountView()
    await w.find('.dl__ptable-head').trigger('click')
    await flushPromises()
    expect(w.findAll('.dl__part')).toHaveLength(1)
  })

  it('★ can_archive / columnar 徽标按需显示', async () => {
    partsMock.mockResolvedValue([partTable({ partitions: [part({ is_columnar: true, can_archive: true })] })])
    const w = await mountView()
    await w.find('.dl__ptable-head').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('列存')
    expect(w.text()).toContain('可归档')
  })
})

describe('截断与空态', () => {
  it('★ 拿满上限 ⇒ 提示「榜上可能漏掉后面的表」', async () => {
    sizesMock.mockResolvedValue(sizesResp({ tables: Array.from({ length: 20 }, () => tbl()) }))
    const w = await mountView()
    expect(w.text()).toContain('榜上可能漏掉后面的表')
  })

  it('★ 未拿满 ⇒ 不提示', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('榜上可能漏掉后面的表')
  })

  it('空表榜 ⇒ 空态', async () => {
    sizesMock.mockResolvedValue(sizesResp({ tables: [] }))
    const w = await mountView()
    expect(w.text()).toContain('没有查到表体积数据')
  })

  it('空分区 ⇒ 空态', async () => {
    partsMock.mockResolvedValue([])
    const w = await mountView()
    expect(w.text()).toContain('没有查到分区表状态')
  })
})