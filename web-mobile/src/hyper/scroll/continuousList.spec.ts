import { describe, expect, it, vi } from 'vitest'
import { ContinuousListController, type ListPageResult } from './continuousList'

interface Row {
  id: number
}

function row(id: number): Row {
  return { id }
}

function makeController(
  fetchPage: (page: number, signal: AbortSignal) => Promise<ListPageResult<Row>>,
  opts: { autoFillMaxPages?: number } = {},
) {
  return new ContinuousListController<Row>({
    fetchPage,
    stableKey: (r) => `row-${r.id}`,
    scopeKey: 'test',
    autoFillMaxPages: opts.autoFillMaxPages,
  })
}

function waitFor(predicate: () => boolean, timeoutMs = 2000): Promise<void> {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + timeoutMs
    const tick = () => {
      if (predicate()) return resolve()
      if (Date.now() > deadline) return reject(new Error('waitFor timeout'))
      setTimeout(tick, 10)
    }
    tick()
  })
}

describe('ContinuousListController（07 §3）', () => {
  it('首载 + 翻页追加 + rowId 去重 + total 满即 exhausted', async () => {
    const c = makeController(async (page) => ({
      items: page === 1 ? [row(1), row(2)] : [row(2), row(3)],
      total: 3,
    }))
    c.loadFirst('initial')
    await waitFor(() => c.state === 'idle')
    expect(c.items.map((r) => r.id)).toEqual([1, 2])
    c.loadNext()
    await waitFor(() => c.state === 'exhausted')
    expect(c.items.map((r) => r.id)).toEqual([1, 2, 3]) // row-2 去重
    // exhausted 不再加载
    c.loadNext()
    expect(c.state).toBe('exhausted')
  })

  it('追加失败保留已加载行 → loadMoreFailed → retry 续载', async () => {
    let failNext = true
    const c = makeController(async (page) => {
      if (page > 1 && failNext) throw new Error('network down')
      return { items: page === 1 ? [row(1)] : [row(2)], total: undefined }
    })
    c.loadFirst('initial')
    await waitFor(() => c.state === 'idle')
    c.loadNext()
    await waitFor(() => c.state === 'loadMoreFailed')
    expect(c.items.map((r) => r.id)).toEqual([1])
    failNext = false
    c.retry()
    await waitFor(() => c.state === 'idle')
    expect(c.items.map((r) => r.id)).toEqual([1, 2])
  })

  it('空页 → exhausted（无更多）', async () => {
    const c = makeController(async () => ({ items: [], total: 0 }))
    c.loadFirst('initial')
    await waitFor(() => c.state === 'exhausted')
    expect(c.items.length).toBe(0)
  })

  it('保旧刷新：refresh 不清行，成功后整组替换', async () => {
    let version = 1
    const c = makeController(async () => ({ items: version === 1 ? [row(1)] : [row(9)], total: 1 }))
    c.loadFirst('initial')
    await waitFor(() => c.state === 'exhausted')
    version = 2
    c.refresh()
    expect(c.state).toBe('refreshing')
    expect(c.items.map((r) => r.id)).toEqual([1]) // 保旧（13 §3）
    await waitFor(() => c.state === 'exhausted')
    expect(c.items.map((r) => r.id)).toEqual([9])
  })

  it('旧响应不得回写：requery 后迟到的旧页被丢弃（17 §4-R2）', async () => {
    let releaseStale: () => void = () => {}
    let firstCall = true
    const c = makeController((page) => {
      if (page > 1) return Promise.resolve({ items: [], total: 0 })
      if (firstCall) {
        firstCall = false
        return new Promise<ListPageResult<Row>>((resolve) => {
          releaseStale = () => resolve({ items: [row(1)], total: 1 })
        })
      }
      return Promise.resolve({ items: [], total: 0 })
    })
    c.loadFirst('initial')
    // 首载未完成即 requery（筛选变更）
    c.loadFirst('requery')
    await waitFor(() => c.state === 'exhausted') // 新查询以空页结束
    releaseStale() // 旧响应此刻才到
    await new Promise((r) => setTimeout(r, 20))
    expect(c.items.length).toBe(0) // 旧响应未回写
  })

  it('订阅回调在每次状态迁移触发', async () => {
    const c = makeController(async () => ({ items: [row(1)], total: 1 }))
    const listener = vi.fn()
    c.subscribe(listener)
    c.loadFirst('initial')
    await waitFor(() => c.state === 'exhausted')
    expect(listener.mock.calls.length).toBeGreaterThanOrEqual(2)
  })
})
