/**
 * hyper/hyperPages.spec.ts — 连续加载门禁（docs/UI规范/00 §5.4 · H3）。
 *
 * 最重要的一条是 **revision 闸门**：切租户/筛选后，上一个查询的慢响应
 * 回来时**必须被丢弃**。没有它，A 租户的行会被追加进 B 租户的列表 ——
 * 跨租户数据泄露，而且用户界面上看不到任何异常。
 */
import { describe, expect, it, vi } from 'vitest'
import { createHyperPages } from './hyperPages'

interface Row {
  id: number
  tenant: string
  name: string
}

const pageSize = 3

function rowsFrom(tenant: string, from: number, count: number): Row[] {
  return Array.from({ length: count }, (_, i) => ({
    id: from + i,
    tenant,
    name: `${tenant}-${from + i}`,
  }))
}

/** 受控的 fetchPage：按**页号**索引的固定计划。用于同租户内的累积/淘汰。 */
function makeFetcher(
  plan: Array<{ rows?: Row[]; delay?: number; fail?: boolean }>,
): { fetchPage: (page: number) => Promise<{ rows: Row[] }>; calls: number[] } {
  const calls: number[] = []
  return {
    calls,
    fetchPage: (page: number) => {
      calls.push(page)
      const step = plan[page - 1]
      if (!step) return Promise.resolve({ rows: [] })
      if (step.fail) return Promise.reject(new Error(`page ${page} failed`))
      const rows = step.rows ?? []
      const d = step.delay ?? 0
      return d > 0
        ? new Promise((res) => setTimeout(() => res({ rows }), d))
        : Promise.resolve({ rows })
    },
  }
}

/**
 * 按**调用次序**驱动的受控源：第 n 次调用返回 `steps[n-1]`。
 *
 * `makeFetcher`（按页号索引）**表达不了**本模块的三个核心语义，因为它们都依赖
 * 「同一个页号、不同次调用、拿到不同结果」这个维度：
 *   - 单飞/刷新：第 1 页在途时再调一次，第二次调用拿到的是**新**查询的数据
 *   - retry：失败的那一页重试，第二次调用**成功**
 *   - 跨租户：切租户后重取第 1 页，第二次调用返回**另一个租户**的行
 * 用 makeFetcher 写这些场景时，期望值与实际值会「看起来对不上」，
 * 或更糟 —— 判据恒真，把有缺陷的实现也放过去。本 spec 里每处需要该维度的用例都用它。
 */
function seqFetcher(steps: Array<{ rows?: Row[]; delay?: number; fail?: boolean }>): {
  fetchPage: (page: number) => Promise<{ rows: Row[] }>
  calls: number[]
} {
  const calls: number[] = []
  return {
    calls,
    fetchPage(page: number) {
      calls.push(page)
      const step = steps[calls.length - 1]
      if (!step) return Promise.resolve({ rows: [] })
      if (step.fail) return Promise.reject(new Error(`call ${calls.length} failed`))
      const rows = step.rows ?? []
      const d = step.delay ?? 0
      return d > 0
        ? new Promise((res) => setTimeout(() => res({ rows }), d))
        : Promise.resolve({ rows })
    },
  }
}

/**
 * 租户感知的受控源。
 *
 * 关键在于**按发出时刻打快照**：`tenant` 是闭包变量，切租户只影响之后**新发**的请求；
 * 切换前发出、之后才返回的请求仍然带着旧租户的行。revision 闸门要挡的正是这个。
 * 同一页在切租户前后返回不同租户的数据，这是 `makeFetcher` 表达不了的维度。
 */
function tenantFetcher(
  pages: Record<string, Row[][]>,
  delay?: (call: number) => number,
): {
  fetchPage: (page: number) => Promise<{ rows: Row[] }>
  calls: Array<{ page: number; tenant: string }>
  switchTo: (tenant: string) => void
} {
  let tenant = Object.keys(pages)[0]
  const calls: Array<{ page: number; tenant: string }> = []
  return {
    calls,
    switchTo(next: string) {
      tenant = next
    },
    fetchPage(page: number) {
      const snapshot = tenant // 快照：请求发出时的租户
      const call = calls.length
      calls.push({ page, tenant: snapshot })
      const rows = pages[snapshot]?.[page - 1] ?? []
      const d = delay?.(call) ?? 0
      return d > 0
        ? new Promise((res) => setTimeout(() => res({ rows }), d))
        : Promise.resolve({ rows })
    },
  }
}

describe('createHyperPages：基本累积', () => {
  it('首页 + 追加按顺序累积', async () => {
    const f = makeFetcher([
      { rows: rowsFrom('a', 1, 3) },
      { rows: rowsFrom('a', 4, 3) },
      { rows: rowsFrom('a', 7, 3) },
    ])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })

    await hp.loadFirst()
    expect(hp.rows.value.map((r) => r.id)).toEqual([1, 2, 3])
    expect(hp.loadedPages.value).toBe(1)
    expect(hp.state.value).toBe('idle')

    await hp.loadNext()
    expect(hp.rows.value.map((r) => r.id)).toEqual([1, 2, 3, 4, 5, 6])
    expect(hp.loadedPages.value).toBe(2)
    expect(hp.loadedCount.value).toBe(6)
  })

  it('短页 → exhausted，不再请求', async () => {
    const f = makeFetcher([
      { rows: rowsFrom('a', 1, 3) },
      { rows: rowsFrom('a', 4, 2) }, // 不足 pageSize
      { rows: rowsFrom('a', 7, 3) },
    ])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    await hp.loadFirst()
    await hp.loadNext()
    expect(hp.hasMore.value).toBe(false)
    expect(hp.state.value).toBe('exhausted')
    // 再点加载更多不应发起请求
    expect(await hp.loadNext()).toBe(false)
    expect(f.calls).toEqual([1, 2])
  })

  it('按 rowId 去重：重复页不产生重复行', async () => {
    const f = makeFetcher([
      { rows: rowsFrom('a', 1, 3) },
      { rows: rowsFrom('a', 3, 3) }, // 与首页重叠 3,4
    ])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    await hp.loadFirst()
    await hp.loadNext()
    const ids = hp.rows.value.map((r) => r.id)
    expect(new Set(ids).size).toBe(ids.length)
    expect(ids).toEqual([1, 2, 3, 4, 5])
  })

  it('单飞：同一页并发只发一次请求', async () => {
    const f = makeFetcher([{ rows: rowsFrom('a', 1, 3), delay: 20 }])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    const [a, b, c] = await Promise.all([hp.loadFirst(), hp.loadFirst(), hp.loadFirst()])
    expect(f.calls).toEqual([1])
    expect([a, b, c].filter(Boolean).length).toBeGreaterThanOrEqual(1)
    expect(hp.rows.value).toHaveLength(3)
  })

  it('busy 时 loadNext 不发请求', async () => {
    const f = makeFetcher([
      { rows: rowsFrom('a', 1, 3), delay: 20 },
      { rows: rowsFrom('a', 4, 3), delay: 20 },
    ])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    const p1 = hp.loadFirst()
    const p2 = hp.loadNext()
    await Promise.all([p1, p2])
    // loadFirst 在途时 loadNext 应当被拒（state 不是 idle）
    expect(f.calls).toEqual([1])
  })
})

describe('createHyperPages：revision 闸门（防跨租户回写）', () => {
  it('切租户后，A 的慢响应被丢弃，B 的数据不被污染', async () => {
    // 第 0 次调用（A 的第 1 页）慢 60ms，第 1 次调用（B 的第 1 页）快 —— 制造乱序
    const f = tenantFetcher({ A: [rowsFrom('A', 1, 3)], B: [rowsFrom('B', 101, 3)] }, (call) =>
      call === 0 ? 60 : 5,
    )
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })

    const slowA = hp.loadFirst()
    f.switchTo('B')
    hp.invalidate()
    const fastB = hp.loadFirst()
    await Promise.all([slowA, fastB])
    // 等 A 的响应也 settle
    await new Promise((r) => setTimeout(r, 80))

    // 阳性对照：确实有两个租户的响应都回来了，且它们的租户不同
    expect(f.calls.map((c) => c.tenant)).toEqual(['A', 'B'])
    // 关键断言：列表里只有 B 的行
    expect(new Set(hp.rows.value.map((r) => r.tenant))).toEqual(new Set(['B']))
    expect(hp.rows.value.map((r) => r.id)).toEqual([101, 102, 103])
  })

  it('invalidate 后旧页数据不再被后续追加混进新查询', async () => {
    const f = tenantFetcher(
      { A: [rowsFrom('A', 1, 3), rowsFrom('A', 4, 3)], B: [rowsFrom('B', 201, 3)] },
      (call) => (call === 1 ? 40 : 0),
    )
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    await hp.loadFirst()

    const slowAppend = hp.loadNext() // A 的第 2 页，慢
    f.switchTo('B')
    hp.invalidate()
    await hp.loadFirst() // B 的第 1 页
    await slowAppend
    await new Promise((r) => setTimeout(r, 60))

    expect(new Set(hp.rows.value.map((r) => r.tenant))).toEqual(new Set(['B']))
    expect(hp.rows.value.map((r) => r.id)).toEqual([201, 202, 203])
  })

  it('refresh 提升 revision，旧的 loadNext 结果被丢弃', async () => {
    const f = makeFetcher([
      { rows: rowsFrom('A', 1, 3) },
      { rows: rowsFrom('A', 4, 3), delay: 40 },
      { rows: rowsFrom('A', 1, 3) },
    ])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    await hp.loadFirst()
    expect(hp.rows.value).toHaveLength(3)

    const slowNext = hp.loadNext()
    await hp.refresh()
    await slowNext
    await new Promise((r) => setTimeout(r, 60))

    // refresh 后应只有首页的 3 行，没有被慢响应追加到 6 行
    expect(hp.rows.value).toHaveLength(3)
    expect(hp.loadedPages.value).toBe(1)
  })

  it('失效的在途请求落地时不会删掉同页新请求的单飞记录', async () => {
    // A 的第 1 页（20ms，已失效）先落地，B 的第 1 页（80ms）还在途。
    // 若失效请求在 finally 里按 key 无条件 delete，会把 B 的单飞记录删掉，
    // 此后对第 1 页的重复调用就会重复发请求 —— 这里用「不得出现第 3 次调用」钉住。
    const f = tenantFetcher(
      { A: [rowsFrom('A', 1, 3)], B: [rowsFrom('B', 101, 3)] },
      (call) => (call === 0 ? 20 : 80),
    )
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })

    const staleA = hp.loadFirst()
    f.switchTo('B')
    hp.invalidate()
    const inFlightB = hp.loadFirst()

    await new Promise((r) => setTimeout(r, 40)) // A 已落地，B 仍在途
    const joined = hp.loadFirst() // 必须加入 B，不得再发一次
    await Promise.all([staleA, inFlightB, joined])

    expect(f.calls.length).toBe(2)
    expect(hp.rows.value.map((r) => r.id)).toEqual([101, 102, 103])
  })

  it('已失效的请求即使失败，也不得把状态改成 failed', async () => {
    // 失效请求的失败对用户无意义：它属于上一个查询，落地时应当**完全静默**。
    // 若把 state 置成 failed，当前查询明明成功却显示成失败态。
    const calls: Array<{ tenant: string }> = []
    let tenant = 'A'
    const fetchPage = (page: number) => {
      calls.push({ tenant })
      const snapshot = tenant
      if (snapshot === 'A') {
        return new Promise<{ rows: Row[] }>((_res, rej) => setTimeout(() => rej(new Error('A boom')), 40))
      }
      return Promise.resolve({ rows: rowsFrom('B', 101, 3) })
    }
    const hp = createHyperPages<Row>({ fetchPage, pageSize, rowKey: (r) => r.id })

    const staleA = hp.loadFirst()
    tenant = 'B'
    hp.invalidate()
    await hp.loadFirst()
    expect(hp.state.value).toBe('idle') // B 的查询是成功的

    await staleA
    await new Promise((r) => setTimeout(r, 60)) // A 的失败响应此时才落地

    // 阳性对照：两个租户的请求确实都发出且都失败了/成功了
    expect(calls.map((c) => c.tenant)).toEqual(['A', 'B'])
    // 关键：A 的失败不得覆盖 B 的成功态
    expect(hp.state.value).toBe('idle')
    expect(hp.rows.value.map((r) => r.id)).toEqual([101, 102, 103])
  })

  it('refresh 在第 1 页仍在途时发起新请求，而不是加入旧的', async () => {
    const f = seqFetcher([
      { rows: rowsFrom('A', 1, 3), delay: 60 }, // 首次，慢
      { rows: rowsFrom('A', 7, 3) }, // refresh，快
    ])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })

    const slow = hp.loadFirst()
    await hp.refresh() // 第 1 页还在途 —— 必须发新请求
    await slow
    await new Promise((r) => setTimeout(r, 80))

    expect(f.calls).toEqual([1, 1]) // 两次都是第 1 页，但确实是两次请求
    expect(hp.rows.value.map((r) => r.id)).toEqual([7, 8, 9]) // 新数据，不是慢响应的
  })
})

describe('createHyperPages：失败与保旧', () => {
  it('追加失败保留已加载行，并进入 failed', async () => {
    const f = makeFetcher([
      { rows: rowsFrom('a', 1, 3) },
      { fail: true },
    ])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    await hp.loadFirst()
    expect(await hp.loadNext()).toBe(false)
    expect(hp.state.value).toBe('failed')
    expect(hp.rows.value.map((r) => r.id)).toEqual([1, 2, 3]) // 旧行还在
    expect(hp.hasMore.value).toBe(true) // 仍可重试
  })

  it('refresh 失败保留旧数据（stale 态，不清空列表）', async () => {
    // 按调用次序失败：第 1 次（loadFirst）成功，第 2 次（refresh）失败。
    const f = seqFetcher([{ rows: rowsFrom('a', 1, 3) }, { fail: true }])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    await hp.loadFirst()
    expect(hp.rows.value.map((r) => r.id)).toEqual([1, 2, 3])

    expect(await hp.refresh()).toBe(false)
    // 保旧：刷新失败**不清空**用户正在看的内容
    expect(hp.rows.value.map((r) => r.id)).toEqual([1, 2, 3])
    expect(hp.state.value).toBe('failed')
    expect(f.calls).toEqual([1, 1])
  })

  it('retry 重取的是失败的那一页，而不是跳到下一页', async () => {
    // calls 的断言是本例的核心：必须是 [1, 2, 2]。写成 [1, 2, 3] 会放行
    // 「retry 跳到下一页」这种会静默丢一整页数据的错误行为。
    const f = seqFetcher([
      { rows: rowsFrom('a', 1, 3) }, // 第 1 页
      { fail: true }, // 第 2 页失败
      { rows: rowsFrom('a', 4, 3) }, // 重取第 2 页，这次成功
    ])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })

    await hp.loadFirst()
    await hp.loadNext() // 失败
    expect(hp.state.value).toBe('failed')
    expect(hp.rows.value.map((r) => r.id)).toEqual([1, 2, 3]) // 旧行还在

    expect(await hp.retry()).toBe(true)
    expect(f.calls).toEqual([1, 2, 2])
    expect(hp.rows.value.map((r) => r.id)).toEqual([1, 2, 3, 4, 5, 6])
    expect(hp.loadedPages.value).toBe(2)
    expect(hp.state.value).toBe('idle')
  })

  it('idle 态调用 retry 不发请求', async () => {
    const f = makeFetcher([{ rows: rowsFrom('a', 1, 3) }])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    await hp.loadFirst()
    expect(await hp.retry()).toBe(false)
    expect(f.calls).toEqual([1])
  })
})

describe('createHyperPages：内存预算与暂停', () => {
  it('超过 evictAbove 后清退最早的页，保留游标', async () => {
    const plan = Array.from({ length: 8 }, (_, i) => ({ rows: rowsFrom('a', i * 3 + 1, 3) }))
    const f = makeFetcher(plan)
    const hp = createHyperPages<Row>({
      fetchPage: f.fetchPage,
      pageSize,
      rowKey: (r) => r.id,
      evictAbove: 6,
    })
    await hp.loadFirst()
    await hp.loadNext()
    expect(hp.rows.value).toHaveLength(6)
    await hp.loadNext() // 触发淘汰
    expect(hp.rows.value.length).toBeLessThanOrEqual(6)
    expect(hp.evictedFromPage.value).toBeGreaterThan(1)
    // 行数确实减少了（不是死循环）
    expect(hp.rows.value).toHaveLength(6)
  })

  it('pause 后 loadNext 不发请求，resume 后恢复', async () => {
    const f = makeFetcher([
      { rows: rowsFrom('a', 1, 3) },
      { rows: rowsFrom('a', 4, 3) },
    ])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    await hp.loadFirst()

    hp.pause()
    expect(await hp.loadNext()).toBe(false)
    expect(f.calls).toEqual([1])

    hp.resume()
    expect(await hp.loadNext()).toBe(true)
    expect(f.calls).toEqual([1, 2])
  })

  it('evictAbove 小于单页行数时保留最后一页，不把列表清空', async () => {
    // 阈值配得比单页还小时，淘汰循环仍必须**至少留一页**。
    // 去掉这条保护会把 pageRows 删空、rows 变成 [] —— 用户看到的是「加载完就没了」。
    const f = makeFetcher([{ rows: rowsFrom('a', 1, 3) }])
    const hp = createHyperPages<Row>({
      fetchPage: f.fetchPage,
      pageSize,
      rowKey: (r) => r.id,
      evictAbove: 2, // 故意小于单页的 3 行
    })
    await hp.loadFirst()
    expect(hp.rows.value).toHaveLength(3)
    expect(hp.evictedFromPage.value).toBe(1) // 没有可淘汰的页
  })

  it('_reset 回到初始态', async () => {
    const f = makeFetcher([{ rows: rowsFrom('a', 1, 3) }])
    const hp = createHyperPages<Row>({ fetchPage: f.fetchPage, pageSize, rowKey: (r) => r.id })
    await hp.loadFirst()
    hp._reset()
    expect(hp.rows.value).toEqual([])
    expect(hp.loadedPages.value).toBe(0)
    expect(hp.revision.value).toBe(0)
    expect(hp.state.value).toBe('idle')
  })

  it('total 存在时按 total 判定 hasMore', async () => {
    const fetchPage = vi.fn(async (page: number) => ({
      rows: rowsFrom('a', (page - 1) * 3 + 1, 3),
      total: 6,
    }))
    const hp = createHyperPages<Row>({ fetchPage, pageSize, rowKey: (r) => r.id })
    await hp.loadFirst()
    expect(hp.hasMore.value).toBe(true)
    await hp.loadNext()
    expect(hp.hasMore.value).toBe(false) // 2*3 >= 6
    expect(fetchPage).toHaveBeenCalledTimes(2)
  })
})
