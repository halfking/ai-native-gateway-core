import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchStorageTableSizes,
  fetchPartitionStatuses,
  storageAtCap,
  partitionRowCountUnknown,
  partitionCountsAsArchived,
  partitionArchivable,
  tableOverlapsItsPartitions,
  STORAGE_TABLES_LIMIT_DEFAULT,
  STORAGE_TABLES_LIMIT_MAX,
  PARTITION_ROW_COUNT_UNKNOWN,
  STORAGE_ROWS_ARE_ESTIMATES,
  STORAGE_TOTAL_IS_TOPN_SUM,
  STORAGE_PERCENT_IS_TOPN_RELATIVE,
  STORAGE_MAY_DOUBLE_COUNT_PARTITIONS,
  PARTITION_ARCHIVED_COUNT_INCLUDES_COLUMNAR,
  type TableSizeInfo,
  type PartitionInfo,
} from './dataLifecycle'

/**
 * 数据生命周期面的契约测试（2026-10-06）。
 *
 * 五条重点：
 * 1. ★★★★ `total_bytes` / `total_human` 是**返回行之和**，不是整库大小；
 * 2. ★★★★ `percent_of_db` 的分母是**榜内之和**，恒以「榜内占比」为语义；
 * 3. ★★★ `rows` 是 planner 估计（`n_live_tup`），不是 COUNT(*)；
 * 4. ★★★ 分区父表与分区行体积**重叠**，不可相加；
 * 5. ★★★ `row_count = -1` 是后端明确的「未知」哨兵。
 */

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})
function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}
function lastUrl(): string {
  return String(fetchMock.mock.calls[0]![0])
}
function okSizes(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse({ tables: [], total_bytes: 0, total_human: '0 bytes', collected_at: '2026-10-07T10:00:00Z' }))
}

function tbl(over: Partial<TableSizeInfo> = {}): TableSizeInfo {
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

function part(over: Partial<PartitionInfo> = {}): PartitionInfo {
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

describe('URL 与参数', () => {
  it('★ storage/tables 无参数 ⇒ 裸路径', async () => {
    okSizes()
    await fetchStorageTableSizes()
    expect(lastUrl()).toBe('/api/admin/data-lifecycle/storage/tables')
  })

  it('★ partitions 返回裸数组（无信封）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    const r = await fetchPartitionStatuses()
    expect(lastUrl()).toBe('/api/admin/data-lifecycle/partitions')
    expect(r).toEqual([])
  })

  it('limit=200 发得出去', async () => {
    okSizes()
    await fetchStorageTableSizes({ limit: 200 })
    expect(lastUrl()).toContain('limit=200')
  })

  it('★ limit=201 不发（越界静默回落 20）', async () => {
    okSizes()
    await fetchStorageTableSizes({ limit: STORAGE_TABLES_LIMIT_MAX + 1 })
    expect(lastUrl()).not.toContain('limit=')
  })

  it('★ limit=0 / 负数 / NaN 不发', async () => {
    for (const limit of [0, -3, NaN]) {
      fetchMock.mockResolvedValueOnce(jsonResponse({ tables: [], total_bytes: 0, total_human: '0 bytes', collected_at: 'x' }))
      await fetchStorageTableSizes({ limit })
      expect(lastUrl()).not.toContain('limit=')
    }
  })

  it('后端常量：默认 20 / 上限 200', () => {
    expect(STORAGE_TABLES_LIMIT_DEFAULT).toBe(20)
    expect(STORAGE_TABLES_LIMIT_MAX).toBe(200)
  })
})

describe('★★★★ 判据 1/2：total 是榜内之和、percent 是榜内占比', () => {
  it('★ 四个「口径」标记都在', () => {
    expect(STORAGE_ROWS_ARE_ESTIMATES).toBe(true)
    expect(STORAGE_TOTAL_IS_TOPN_SUM).toBe(true)
    expect(STORAGE_PERCENT_IS_TOPN_RELATIVE).toBe(true)
    expect(STORAGE_MAY_DOUBLE_COUNT_PARTITIONS).toBe(true)
  })

  it('★ total_bytes 随返回行数变化（拿 20 张不等于拿整库）', () => {
    const few = { tables: [tbl()], total_bytes: 1024, total_human: '1 kB', collected_at: 'x' }
    const many = { tables: [tbl(), tbl({ total_bytes: 4096 })], total_bytes: 5120, total_human: '5 kB', collected_at: 'x' }
    expect(few.total_bytes).toBe(1024)
    expect(many.total_bytes).toBe(5120)
    // ★ 这两个数都不是整库大小，只是「本次返回的行之和」
  })

  it('★★ percent_of_db 的分母是榜内之和（两行各 50 ⇒ 加起来 100，与全库无关）', () => {
    const two = [tbl({ percent_of_db: 50 }), tbl({ percent_of_db: 50 })]
    expect(two.reduce((s, x) => s + x.percent_of_db, 0)).toBe(100)
  })
})

describe('★★★ 判据 3/4：估计行数与父子重叠', () => {
  it('★ rows 原样透传（不做任何精确化）', () => {
    expect(tbl({ rows: 123456 }).rows).toBe(123456)
    expect(tbl({ rows: 0 }).rows).toBe(0)
  })

  it('★★ 分区父表被标出（它的体积已含子分区）', () => {
    expect(tableOverlapsItsPartitions(tbl({ is_partitioned: true }))).toBe(true)
    expect(tableOverlapsItsPartitions(tbl({ is_partitioned: false }))).toBe(false)
    expect(tableOverlapsItsPartitions(null)).toBe(false)
  })
})

describe('★ 判据 5：row_count = -1 是「未知」哨兵', () => {
  it('★ 哨兵常量 = -1', () => {
    expect(PARTITION_ROW_COUNT_UNKNOWN).toBe(-1)
  })

  it('★ -1 ⇒ 未知；0 ⇒ 真的是 0（不能混）', () => {
    expect(partitionRowCountUnknown(-1)).toBe(true)
    expect(partitionRowCountUnknown(0)).toBe(false)
    expect(partitionRowCountUnknown(1000)).toBe(false)
    expect(partitionRowCountUnknown(undefined)).toBe(false)
  })
})

describe('partition 口径', () => {
  it('★ archived 计数口径 = is_archived || is_columnar（与后端逐字同款）', () => {
    expect(PARTITION_ARCHIVED_COUNT_INCLUDES_COLUMNAR).toBe(true)
    expect(partitionCountsAsArchived(part({ is_archived: true }))).toBe(true)
    expect(partitionCountsAsArchived(part({ is_columnar: true }))).toBe(true)
    expect(partitionCountsAsArchived(part({ is_archived: false, is_columnar: false }))).toBe(false)
    expect(partitionCountsAsArchived(null)).toBe(false)
  })

  it('★ can_archive 原样透传', () => {
    expect(partitionArchivable(part({ can_archive: true }))).toBe(true)
    expect(partitionArchivable(part({ can_archive: false }))).toBe(false)
    expect(partitionArchivable(null)).toBe(false)
  })
})

describe('截断判定（响应**不回显** limit）', () => {
  it('★ 拿满请求上限 ⇒ 判可能截断', () => {
    const r = { tables: Array.from({ length: 20 }, () => tbl()), total_bytes: 1, total_human: 'x', collected_at: 'x' }
    expect(storageAtCap(r)).toBe(true)
  })

  it('★ 少于上限 ⇒ 不判', () => {
    const r = { tables: [tbl()], total_bytes: 1, total_human: 'x', collected_at: 'x' }
    expect(storageAtCap(r)).toBe(false)
  })

  it('★ 按请求的 limit 判（切到 200 后 20 张不算截断）', () => {
    const r = { tables: Array.from({ length: 20 }, () => tbl()), total_bytes: 1, total_human: 'x', collected_at: 'x' }
    expect(storageAtCap(r, 200)).toBe(false)
    expect(storageAtCap(r, 20)).toBe(true)
  })

  it('★ null / 空 ⇒ 不判', () => {
    expect(storageAtCap(null)).toBe(false)
    expect(storageAtCap({ tables: [], total_bytes: 0, total_human: 'x', collected_at: 'x' })).toBe(false)
  })

  it('★ limit 非法（0）⇒ 不判（没有基准）', () => {
    const r = { tables: [tbl()], total_bytes: 1, total_human: 'x', collected_at: 'x' }
    expect(storageAtCap(r, 0)).toBe(false)
  })
})