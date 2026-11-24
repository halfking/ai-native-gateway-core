// latestRequestGuard.spec.ts —— 「只有最后一次发出的请求可以落地」的行为测试。
import { describe, expect, it } from 'vitest'
import { createLatestRequestGuard, type LatestRequestGuard } from './latestRequestGuard'

describe('createLatestRequestGuard', () => {
  it('最新一次发起的请求是 current', () => {
    const g = createLatestRequestGuard()
    const seq = g.next()
    expect(g.isCurrent(seq)).toBe(true)
  })

  it('发起新请求后，之前的序号全部过期', () => {
    const g = createLatestRequestGuard()
    const a = g.next()
    const b = g.next()
    const c = g.next()
    expect(g.isCurrent(a)).toBe(false)
    expect(g.isCurrent(b)).toBe(false)
    expect(g.isCurrent(c)).toBe(true)
  })

  it('序号单调递增且不重复（不会绕回到旧序号）', () => {
    const g = createLatestRequestGuard()
    const seen = new Set<number>()
    let prev = 0
    for (let i = 0; i < 1000; i++) {
      const seq = g.next()
      expect(seq).toBeGreaterThan(prev)
      expect(seen.has(seq)).toBe(false)
      seen.add(seq)
      prev = seq
    }
  })

  it('两个守卫互不相干（不同页面 / 不同列表各自计数）', () => {
    const g1 = createLatestRequestGuard()
    const g2 = createLatestRequestGuard()
    const a = g1.next()
    g2.next()
    g2.next()
    expect(g1.isCurrent(a)).toBe(true)
    expect(g2.isCurrent(a)).toBe(false)
  })
})

/**
 * 按 RequestLogsView.vue 的 load() **同款用法**（领号 → await → 对号才落地；
 * 失败与收尾同样对号）模拟一张列表。这测的是守卫的用法契约；真实页面上的表现
 * 由 Maestro 在 211 上验证（连续切换时间范围后，列表显示的是最后一次选择的数据）。
 */
function simulatedList(g: LatestRequestGuard) {
  const applied: string[] = []
  const errors: string[] = []
  let loading = false
  let finished: string[] = []

  /** 发起一次加载；返回让这次请求「返回 / 失败」的句柄。 */
  function load(label: string) {
    const seq = g.next()
    loading = true
    let resolve!: (value: string) => void
    let reject!: (reason: string) => void
    const response = new Promise<string>((res, rej) => {
      resolve = res
      reject = rej
    })
    const done = (async () => {
      try {
        const value = await response
        if (!g.isCurrent(seq)) return // 过期响应：丢弃
        applied.push(value)
      } catch (reason) {
        if (!g.isCurrent(seq)) return // 过期请求的失败：丢弃
        errors.push(String(reason))
      } finally {
        if (g.isCurrent(seq)) {
          loading = false
          finished.push(label)
        }
      }
    })()
    return {
      arrives: () => resolve(label),
      fails: () => reject(`${label} 超时`),
      done,
    }
  }
  return {
    applied,
    errors,
    load,
    state: () => ({ loading, finished: [...finished] }),
    resetJournal: () => {
      finished = []
    },
  }
}

describe('乱序返回时的落地规则（load() 用法契约）', () => {
  it('先发出的慢请求后到 ⇒ 不落地（界面上只留最后一次选择的数据）', async () => {
    const list = simulatedList(createLatestRequestGuard())
    const slow = list.load('近14天(慢)')
    const fast = list.load('今天(快)')
    fast.arrives()
    await fast.done
    expect(list.applied).toEqual(['今天(快)'])
    slow.arrives()
    await slow.done
    // 旧范围的响应后到：被丢弃，没有把「今天」的数据覆盖掉。
    expect(list.applied).toEqual(['今天(快)'])
  })

  it('先发出的慢请求后到、且它是**失败** ⇒ 不把旧范围的超时写进错误面', async () => {
    const list = simulatedList(createLatestRequestGuard())
    const slow = list.load('近14天(慢)')
    const fast = list.load('今天(快)')
    fast.arrives()
    await fast.done
    slow.fails()
    await slow.done
    expect(list.applied).toEqual(['今天(快)'])
    expect(list.errors).toEqual([])
  })

  it('先完成的旧请求不许提前收尾：新请求在途时 loading 仍为 true', async () => {
    const list = simulatedList(createLatestRequestGuard())
    const first = list.load('今天')
    const second = list.load('近7天')
    first.arrives()
    await first.done
    expect(list.state().loading).toBe(true)
    second.arrives()
    await second.done
    expect(list.state().loading).toBe(false)
    expect(list.state().finished).toEqual(['近7天'])
  })

  it('按发出顺序返回 ⇒ 每次都落地（正常情况不被误伤）', async () => {
    const list = simulatedList(createLatestRequestGuard())
    const a = list.load('今天')
    a.arrives()
    await a.done
    const b = list.load('近7天')
    b.arrives()
    await b.done
    expect(list.applied).toEqual(['今天', '近7天'])
    expect(list.state().loading).toBe(false)
  })

  it('三次连发、最后那发先到 ⇒ 只有它落地', async () => {
    const list = simulatedList(createLatestRequestGuard())
    const a = list.load('A')
    const b = list.load('B')
    const c = list.load('C')
    c.arrives()
    await c.done
    a.arrives()
    await a.done
    b.fails()
    await b.done
    expect(list.applied).toEqual(['C'])
    expect(list.errors).toEqual([])
  })

  it('最新那次请求自己失败 ⇒ 错误照常进错误面（守卫不是吞错）', async () => {
    const list = simulatedList(createLatestRequestGuard())
    const only = list.load('近30天')
    only.fails()
    await only.done
    expect(list.errors).toEqual(['近30天 超时'])
    expect(list.state().loading).toBe(false)
  })
})
