import { describe, expect, it } from 'vitest'
import { MAX_ENTRIES, NavigationStore, SNAPSHOT_LRU, sanitizeFullPath } from './context'

describe('NavigationStore（06 §3）', () => {
  function seed(): NavigationStore {
    const s = new NavigationStore()
    s.commitPush({ fullPath: '/', routeName: 'home', presentation: 'page', openedBy: 'deepLink', title: '总览', titleSource: 'route', scope: { accountId: 'u1' } })
    s.commitPush({ fullPath: '/nodes', routeName: 'nodes', presentation: 'page', openedBy: 'push', title: '节点', titleSource: 'route', scope: { accountId: 'u1' } }, s.getEntries()[0]?.id)
    return s
  }

  it('push 截断 forward 分支；replace 保留页面 ID', () => {
    const s = seed()
    expect(s.getCursor()).toBe(1)
    s.commitPop()
    expect(s.getCursor()).toBe(0)
    s.commitReplace({ fullPath: '/?days=30', routeName: 'home', presentation: 'page', openedBy: 'replace', title: '总览', titleSource: 'route', scope: { accountId: 'u1' } })
    expect(s.current()?.id).toBe(s.getEntries()[0]?.id) // ID 不变（06 §4 同页换筛选）
    s.commitPush({ fullPath: '/alerts', routeName: 'alerts', presentation: 'page', openedBy: 'push', title: '告警', titleSource: 'route', scope: { accountId: 'u1' } })
    expect(s.getEntries().map((e) => e.routeName)).toEqual(['home', 'alerts']) // forward 已截断
  })

  it('entries ≤ 80 上限', () => {
    const s = new NavigationStore()
    for (let i = 0; i < MAX_ENTRIES + 20; i++) {
      s.commitPush({ fullPath: `/p${i}`, presentation: 'page', openedBy: 'push', title: `p${i}`, titleSource: 'route', scope: { accountId: 'u1' } })
    }
    expect(s.getEntries().length).toBe(MAX_ENTRIES)
  })

  it('saveView 受 renderEpoch 校验（17 §4-R2）', () => {
    const s = seed()
    const entry = s.current()
    expect(entry).toBeTruthy()
    const rightEpoch = entry!.renderEpoch
    s.saveView(entry!.id, rightEpoch, { main: { x: 0, y: 120 } })
    expect(s.current()?.view.scroll.main?.y).toBe(120)
    s.saveView(entry!.id, rightEpoch + 999, { main: { x: 0, y: 999 } })
    expect(s.current()?.view.scroll.main?.y).toBe(120) // 旧纪元不回写
  })

  // ↓ 移植自 feat/web-mobile-hyper 的 navigationContext.spec.ts（R5 用例）
  it(`快照 LRU(${SNAPSHOT_LRU})：只清 view，entry 记录保留`, () => {
    const s = new NavigationStore()
    const total = SNAPSHOT_LRU + 2
    for (let i = 0; i < total; i++) {
      s.commitPush({ fullPath: `/p${i}`, presentation: 'page', openedBy: 'push', title: `p${i}`, titleSource: 'route', scope: { accountId: 'u1' } })
      const e = s.current()!
      s.saveView(e.id, e.renderEpoch, { main: { x: 0, y: (i + 1) * 100 } })
    }
    const entries = s.getEntries()
    expect(entries.length).toBe(total) // 记录全在：路径/标题仍可查、可回退
    expect(entries[0]!.view.scroll.main).toBeUndefined() // 最老两个快照被驱逐
    expect(entries[1]!.view.scroll.main).toBeUndefined()
    // 最近 SNAPSHOT_LRU 个保留。
    expect(entries[total - 1]!.view.scroll.main?.y).toBe(total * 100)
    expect(entries[2]!.view.scroll.main?.y).toBe(300)
  })

  it('持久化去敏：敏感 query 剥离；恢复校验坏数据拒绝', () => {
    const storage = new Map<string, string>()
    const fake: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> = {
      getItem: (k: string) => storage.get(k) ?? null,
      setItem: (k: string, v: string) => void storage.set(k, v),
      removeItem: (k: string) => void storage.delete(k),
    }
    const s = seed()
    s.commitPush({ fullPath: '/keys?token=abc&tab=2', presentation: 'page', openedBy: 'push', title: '密钥', titleSource: 'route', scope: { accountId: 'u1' } })
    s.persist(fake, 'u1')
    expect(JSON.parse(storage.get('hyper.nav.v2.u1') ?? '{}').entries[2].fullPath).toBe('/keys?tab=2')

    const restored = new NavigationStore()
    expect(restored.restore(fake, 'u1')).toBe(true)
    expect(restored.getEntries().length).toBe(3)

    storage.set('hyper.nav.v2.u1', '{broken json')
    const bad = new NavigationStore()
    expect(bad.restore(fake, 'u1')).toBe(false)
    expect(bad.getEntries().length).toBe(0)
  })

  it('sanitizeFullPath 剥离敏感键', () => {
    expect(sanitizeFullPath('/a?token=x&ok=1')).toBe('/a?ok=1')
    expect(sanitizeFullPath('/a?api_key=x&ok=1')).toBe('/a?ok=1')
    expect(sanitizeFullPath('/a?ok=1')).toBe('/a?ok=1')
    expect(sanitizeFullPath('/a')).toBe('/a')
  })
})
