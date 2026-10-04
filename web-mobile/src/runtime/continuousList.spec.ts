import { describe, expect, it, vi } from 'vitest'
import { ContinuousListController, FIRST_FILL_MAX_PAGES } from './continuousList'

interface Row {
  id: string
}

function makeFetcher(total: number, failAt?: number) {
  let calls = 0
  return {
    calls: () => calls,
    fetch: vi.fn(async (offset: number) => {
      calls += 1
      if (failAt !== undefined && calls > failAt) throw new Error('network down')
      const pageSize = 20
      const rows: Row[] = []
      for (let i = offset; i < Math.min(offset + pageSize, total); i++) rows.push({ id: `r${i}` })
      return { rows, total }
    }),
    rowId: (r: Row) => r.id,
  }
}

describe('continuousList（07 §3 + 13 §3）', () => {
  it('首屏补页 ≤3 页且塞满即停', async () => {
    const f = makeFetcher(500)
    const c = new ContinuousListController<Row>(f, 20)
    await c.firstFill()
    expect(c.current().items.length).toBeGreaterThanOrEqual(20)
    expect(c.current().items.length).toBeLessThanOrEqual(20 * FIRST_FILL_MAX_PAGES)
  })

  it('rowId 去重：服务端 offset 漂移重复回发同页时不产生重复行', async () => {
    // 模拟服务端忽略 offset：第二次调用仍回发 0..20 的行。
    let calls = 0
    const fetcher = {
      fetch: vi.fn(async (offset: number) => {
        calls += 1
        const rows: Row[] = []
        const start = calls === 2 ? 0 : offset
        for (let i = start; i < start + 20 && i < 100; i++) rows.push({ id: `r${i}` })
        return { rows, total: 100 }
      }),
      rowId: (r: Row) => r.id,
    }
    const c = new ContinuousListController<Row>(fetcher, 20)
    await c.firstFill()
    expect(c.current().items.length).toBe(20)
    await c.loadMore() // 第二次回发重复行 → 全部去重 → exhausted
    expect(c.current().items.length).toBe(20)
    const ids = c.current().items.map((r) => r.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('失败保旧：错误态 + 旧列表保留，重试续载', async () => {
    const failOn = new Set([2])
    let calls = 0
    const fetcher = {
      fetch: vi.fn(async (offset: number) => {
        calls += 1
        if (failOn.has(calls)) throw new Error('network down')
        const rows: Row[] = []
        // 每页 10 行：controller pageSize=20 → 首屏需 2 页，第 2 页失败。
        for (let i = offset; i < Math.min(offset + 10, 100); i++) rows.push({ id: `r${i}` })
        return { rows, total: 100 }
      }),
      rowId: (r: Row) => r.id,
    }
    const c = new ContinuousListController<Row>(fetcher, 20)
    await c.firstFill() // 第1页成功10行，第2页失败
    expect(c.current().error).not.toBeNull()
    expect(c.current().items.length).toBe(10)
    // 手动重试成功续载，从失败页续起、无重复。
    const ok = await c.loadMore()
    expect(ok).toBe(true)
    expect(c.current().error).toBeNull()
    expect(c.current().items.length).toBe(20)
  })

  it('空页判定到底（exhausted）', async () => {
    const f = makeFetcher(10)
    const c = new ContinuousListController<Row>(f, 20)
    await c.firstFill()
    expect(c.current().exhausted).toBe(true)
    expect(c.current().total).toBe(10)
  })

  it('距底 240px 预载触发 + single-flight 不并发', async () => {
    const f = makeFetcher(500)
    const c = new ContinuousListController<Row>(f, 20)
    await c.firstFill()
    const before = c.current().items.length
    // 距底 < 240px：scrollTop 1200 / viewport 600 / scrollHeight 1400。
    c.onScrollMetrics(1200, 600, 1400)
    c.onScrollMetrics(1200, 600, 1400) // 第二次同帧触发被 loading 守卫吞掉
    await new Promise((r) => setTimeout(r, 0))
    expect(c.current().items.length).toBe(before + 20)
    // firstFill 塞满一页即停（1 次）+ 预载 1 次；第二次同帧触发被 loading 吞。
    expect(f.fetch).toHaveBeenCalledTimes(2)
  })
})
