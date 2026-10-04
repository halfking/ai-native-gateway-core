import { beforeEach, describe, expect, it } from 'vitest'
import { NavigationContext, MAX_ENTRIES, readPersistedNav } from './navigationContext'

function newCtx() {
  return new NavigationContext(() => {})
}

describe('navigationContext（06 §3 + R5）', () => {
  let ctx: NavigationContext
  beforeEach(() => {
    ctx = newCtx()
  })

  it('push 建立继承链：下层标题沿链上浮', () => {
    const root = ctx.push('/')
    ctx.setTitle(root.entryId, '总览', root.renderEpoch)
    const child = ctx.push('/nodes')
    expect(child.inheritedTitle).toBe('总览')
    expect(ctx.canPop()).toBe(true)
  })

  it('query 白名单外键被剥离（凭据/Key 不落盘）', () => {
    const e = ctx.push('/keys', { id: '3', key: 'sk-secret', q: 'searchterm' })
    expect(e.query).toEqual({ id: '3' })
  })

  it('entries ≤80：截断最旧', () => {
    for (let i = 0; i < MAX_ENTRIES + 10; i++) ctx.push(`/p${i}`)
    expect(ctx.list().length).toBe(MAX_ENTRIES)
    expect(ctx.list()[ctx.list().length - 1]?.path).toBe(`/p${MAX_ENTRIES + 9}`)
  })

  it('R5：快照 LRU(5) 驱逐只清 view，entry 记录保留', () => {
    const ids: string[] = []
    for (let i = 0; i < 7; i++) {
      const e = ctx.push(`/p${i}`)
      ids.push(e.entryId)
      ctx.snapshotView(e.entryId, { scrollTop: i * 100 })
    }
    // 前 2 个 entry 的 view 被驱逐。
    expect(ctx.restoreView(ids[0]!)).toBeNull()
    expect(ctx.restoreView(ids[1]!)).toBeNull()
    // 记录仍在（标题/路径可查）。
    expect(ctx.list().length).toBe(7)
    // 最近 5 个保留快照。
    expect(ctx.restoreView(ids[6]!)).toEqual({ scrollTop: 600 })
  })

  it('renderEpoch 不匹配的 setTitle 被丢弃', () => {
    const e = ctx.push('/')
    ctx.setTitle(e.entryId, '新标题', e.renderEpoch + 1)
    expect(ctx.top()?.title).toBeNull()
    ctx.setTitle(e.entryId, '新标题', e.renderEpoch)
    expect(ctx.top()?.title).toBe('新标题')
  })

  it('operations 记录且 ≤100', () => {
    for (let i = 0; i < 130; i++) ctx.push(`/p${i}`)
    expect(ctx.operationsLog().length).toBeLessThanOrEqual(100)
    expect(ctx.operationsLog()[ctx.operationsLog().length - 1]?.type).toBe('push')
  })

  it('sessionStorage 持久化 scope 隔离（/m 档）', () => {
    sessionStorage.clear()
    const persisting = new NavigationContext()
    persisting.push('/nodes')
    expect(readPersistedNav()).not.toBeNull()
    sessionStorage.clear()
  })
})
