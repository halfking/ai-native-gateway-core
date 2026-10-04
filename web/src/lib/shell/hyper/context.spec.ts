/**
 * hyper/context.spec.ts — 导航上下文门禁（docs/UI规范/00 §5.4 · H1）。
 *
 * 重点不在「能不能 push/pop」，而在**去敏**与**隔离域**：
 * 导航历史会落进 sessionStorage，同源可被后续任意脚本读到，
 * 所以白名单与拒绝清单必须被用例钉死。
 */
import { beforeEach, describe, expect, it } from 'vitest'
import {
  MAX_ENTRIES,
  NAV_STORAGE_KEY,
  NavigationStore,
  PERSISTED_QUERY_DENYLIST,
  PERSISTED_QUERY_WHITELIST,
  clearNavigation,
  persistNavigation,
  restoreNavigation,
} from './context'
import type { NavigationScope } from './types'

const SCOPE: NavigationScope = { serverId: 'https://gw.example.com', accountId: 'u1' }
const OTHER: NavigationScope = { serverId: 'https://gw.example.com', accountId: 'u2' }

function store(): NavigationStore {
  return new NavigationStore()
}

function pushList(s: NavigationStore, path: string, extra: Partial<Parameters<NavigationStore['push']>[0]> = {}) {
  return s.push({ fullPath: path, presentation: 'page', openedBy: 'push', scope: SCOPE, ...extra })
}

describe('NavigationStore：entry 链语义', () => {
  let s: NavigationStore
  beforeEach(() => {
    s = store()
    s.setScope(SCOPE)
  })

  it('id 是访问身份而非 URL：同一路径两次访问是两个 entry', () => {
    const a = pushList(s, '/keys')
    const b = s.push({ fullPath: '/keys/1', presentation: 'page', openedBy: 'push', scope: SCOPE, parentId: a })
    const c = s.push({ fullPath: '/keys', presentation: 'page', openedBy: 'pop', scope: SCOPE, parentId: b })
    const ids = new Set([a, b, c])
    expect(ids.size).toBe(3)
    expect(pushList(s, '/keys')).not.toBe(c)
  })

  it('replace 保留 entry id 与父级，不增栈', () => {
    const a = pushList(s, '/logs')
    const before = s.all().length
    const r = s.push({
      fullPath: '/logs?tab=waterfall',
      presentation: 'page',
      openedBy: 'replace',
      scope: SCOPE,
      parentId: 'SHOULD_BE_IGNORED',
    })
    expect(s.all().length).toBe(before)
    expect(r).toBe(a)
    expect(s.current()?.parentId).toBeUndefined()
  })

  it('返回后再打开新页会截断前进分支，旧 forward 不可达', () => {
    pushList(s, '/a')
    pushList(s, '/b')
    pushList(s, '/c')
    expect(s.hasForward).toBe(false)
    s.pop()
    expect(s.hasForward).toBe(true)
    pushList(s, '/d')
    expect(s.hasForward).toBe(false)
    expect(s.all().map((e) => e.fullPath)).toEqual(['/a', '/b', '/d'])
  })

  it('atRoot 只认 entry 链，与 history.length 无关', () => {
    expect(s.atRoot).toBe(true)
    pushList(s, '/a')
    expect(s.atRoot).toBe(true) // cursor=0，再退没有已知父页
    pushList(s, '/b')
    expect(s.atRoot).toBe(false)
    s.pop()
    expect(s.atRoot).toBe(true)
  })

  it('超出 MAX_ENTRIES 时淘汰最旧且 cursor 同步左移', () => {
    for (let i = 0; i < MAX_ENTRIES + 10; i++) pushList(s, `/p${i}`)
    expect(s.all()).toHaveLength(MAX_ENTRIES)
    expect(s.current()?.fullPath).toBe(`/p${MAX_ENTRIES + 9}`)
    expect(s.current()?.id).toBeTruthy()
  })

  it('滚动锚点按宿主分别保存', () => {
    const a = pushList(s, '/logs')
    s.saveScroll(a, 'main', { x: 0, y: 120, anchorId: 'row-42' })
    s.saveScroll(a, 'table-x', { x: 300, y: 0 })
    expect(s.byId(a)?.view.scroll).toEqual({
      main: { x: 0, y: 120, anchorId: 'row-42' },
      'table-x': { x: 300, y: 0 },
    })
  })
})

describe('NavigationStore：隔离域', () => {
  it('换账号即清空，不让 A 的标题/筛选出现在 B', () => {
    const s = store()
    s.setScope(SCOPE)
    pushList(s, '/keys?tab=mine', { title: 'A 的页面' })
    expect(s.all()).toHaveLength(1)
    s.setScope(OTHER)
    expect(s.all()).toHaveLength(0)
  })

  it('重复声明同一 scope 不清空', () => {
    const s = store()
    s.setScope(SCOPE)
    pushList(s, '/keys')
    s.setScope(SCOPE)
    expect(s.all()).toHaveLength(1)
  })
})

describe('NavigationStore.sanitizePath：去敏', () => {
  it('只保留白名单结构化 query', () => {
    const out = NavigationStore.sanitizePath('/logs?tab=waterfall&view=compact&page=2&metric=cost')!
    expect(out).toBe('/logs?tab=waterfall&view=compact&page=2&metric=cost')
  })

  it('剔除自由文本搜索原文', () => {
    const out = NavigationStore.sanitizePath('/logs?q=alice%20secret&q=token&tab=waterfall')!
    expect(out).toBe('/logs?tab=waterfall')
    expect(out).not.toContain('alice')
  })

  it('剔除身份与凭据类参数', () => {
    const out = NavigationStore.sanitizePath(
      '/x?redirect=%2Fadmin%3Ftoken%3Dabc&login=1&session=s1&api_key=k&tab=t',
    )!
    expect(out).toBe('/x?tab=t')
  })

  it('剔除实体 id（丢弃后恢复到列表页是想要的安全兜底）', () => {
    const out = NavigationStore.sanitizePath('/x?tenant_id=t1&credential_id=c1&provider_id=p1&tab=t')!
    expect(out).toBe('/x?tab=t')
  })

  it('白名单键的值必须是结构化 token，自然语言进不来', () => {
    const out = NavigationStore.sanitizePath('/x?tab=waterfall%20%E8%BF%99%E9%87%8C%E6%9C%89%E5%95%86%E5%93%81')!
    expect(out).toBe('/x')
  })

  it('超长值被形状闸门拒绝', () => {
    const out = NavigationStore.sanitizePath(`/x?tab=${'a'.repeat(200)}`)!
    expect(out).toBe('/x')
  })

  it('无 query 时只保留路径', () => {
    expect(NavigationStore.sanitizePath('/models')).toBe('/models')
  })

  it('非站内路径（外域/协议相对）返回 null', () => {
    expect(NavigationStore.sanitizePath('https://evil.example.com/x')).toBeNull()
    expect(NavigationStore.sanitizePath('//evil.com')).toBeNull()
    expect(NavigationStore.sanitizePath('')).toBeNull()
  })

  it('路径本身也过形状闸门（防注入）', () => {
    expect(NavigationStore.sanitizePath('/x"><script>')).toBeNull()
  })

  it('白名单与拒绝清单不重叠', () => {
    for (const k of PERSISTED_QUERY_WHITELIST) {
      expect(PERSISTED_QUERY_DENYLIST).not.toContain(k)
    }
  })
})

describe('导航持久化：sessionStorage', () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  it('落盘内容不含标题文本、只含 titleKey（标题可能含姓名）', () => {
    const s = store()
    s.setScope(SCOPE)
    pushList(s, '/tenants', { title: '张三的租户', titleKey: 'nav.tenants' })
    persistNavigation(s)
    const raw = sessionStorage.getItem(NAV_STORAGE_KEY)!
    expect(raw).not.toContain('张三')
    expect(raw).toContain('nav.tenants')
    const parsed = JSON.parse(raw)
    expect(parsed.context.entries[0].title).toBe('')
  })

  it('落盘后不残留敏感 query', () => {
    const s = store()
    s.setScope(SCOPE)
    pushList(s, '/logs?q=alice&tab=waterfall&redirect=%2Fadmin')
    persistNavigation(s)
    const raw = sessionStorage.getItem(NAV_STORAGE_KEY)!
    expect(raw).not.toContain('alice')
    expect(raw).not.toContain('redirect')
    expect(raw).toContain('tab=waterfall')
  })

  it('往返恢复保结构，query 已去敏', () => {
    const s = store()
    s.setScope(SCOPE)
    pushList(s, '/logs?tab=waterfall&q=secret')
    pushList(s, '/keys', { titleKey: 'nav.keys' })
    persistNavigation(s)

    const s2 = store()
    expect(restoreNavigation(SCOPE, s2)).toBe(true)
    expect(s2.all()).toHaveLength(2)
    expect(s2.current()?.fullPath).toBe('/keys')
    expect(s2.all()[0].fullPath).toBe('/logs?tab=waterfall')
    // 覆盖层标记不恢复（不重开脏表单）
    expect(s2.getOverlayIds()).toEqual([])
  })

  it('换账号恢复被拒（scope 不匹配）', () => {
    const s = store()
    s.setScope(SCOPE)
    pushList(s, '/logs')
    persistNavigation(s)
    const s2 = store()
    expect(restoreNavigation(OTHER, s2)).toBe(false)
    expect(s2.all()).toHaveLength(0)
  })

  it('结构损坏 / 版本不符 / 类型不对 → 整体丢弃，不半信半疑', () => {
    sessionStorage.setItem(NAV_STORAGE_KEY, 'not json')
    const s1 = store()
    expect(restoreNavigation(SCOPE, s1)).toBe(false)

    sessionStorage.setItem(NAV_STORAGE_KEY, JSON.stringify({ scope: SCOPE, context: { version: 1, entries: [], cursor: 0 } }))
    expect(restoreNavigation(SCOPE, store())).toBe(false)

    sessionStorage.setItem(
      NAV_STORAGE_KEY,
      JSON.stringify({ scope: SCOPE, context: { version: 2, entries: [{ nope: 1 }], cursor: 0 } }),
    )
    expect(restoreNavigation(SCOPE, store())).toBe(false)
  })

  it('clearNavigation 同时清内存与落盘', () => {
    const s = store()
    s.setScope(SCOPE)
    pushList(s, '/logs')
    persistNavigation(s)
    expect(sessionStorage.getItem(NAV_STORAGE_KEY)).toBeTruthy()
    clearNavigation(s)
    expect(s.all()).toHaveLength(0)
    expect(sessionStorage.getItem(NAV_STORAGE_KEY)).toBeNull()
  })

  it('存储不可用（setItem 抛错）时内存导航照常工作', () => {
    const s = store()
    s.setScope(SCOPE)
    pushList(s, '/a')
    const original = Storage.prototype.setItem
    Storage.prototype.setItem = () => {
      throw new Error('QuotaExceeded')
    }
    try {
      expect(() => persistNavigation(s)).not.toThrow()
      expect(s.all()).toHaveLength(1) // 内存仍是真源
    } finally {
      Storage.prototype.setItem = original
    }
  })
})
