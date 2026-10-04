import { describe, expect, it } from 'vitest'
import {
  createQueryEpoch,
  currentSessionEpoch,
  isRenderStale,
  isStale,
  nextSessionEpoch,
} from './epochs'

describe('epochs（R2 三层代次）', () => {
  it('sessionEpoch 只增不减，登出后旧代次请求判 stale', () => {
    const s1 = currentSessionEpoch()
    nextSessionEpoch()
    const s2 = currentSessionEpoch()
    expect(s2).toBe(s1 + 1)
    // 提交时是 s1，回写时已是 s2 → 旧响应不得回写。
    expect(isStale(s1, 0, { session: s2, query: 0 })).toBe(true)
    expect(isStale(s2, 0, { session: s2, query: 0 })).toBe(false)
  })

  it('queryRevision 隔离同会话内的新旧响应', () => {
    const qe = createQueryEpoch()
    const claim = qe.next()
    expect(claim).toBe(1)
    // 下拉刷新又翻了一代 → 旧响应 stale。
    qe.next()
    expect(isStale(currentSessionEpoch(), claim, { session: currentSessionEpoch(), query: qe.current })).toBe(true)
  })

  it('renderEpoch 不匹配即丢弃异步标题回写', () => {
    const current = { entryId: 'e1', renderEpoch: 2 }
    expect(isRenderStale('e1', 1, current)).toBe(true)
    expect(isRenderStale('e2', 2, current)).toBe(true)
    expect(isRenderStale('e1', 2, current)).toBe(false)
  })
})
