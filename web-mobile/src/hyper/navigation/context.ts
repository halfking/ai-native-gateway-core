import type { NavigationEntry, NavigationKind, NavigationOperation, OperationType } from '../types'

// NavigationStore — UI规范 06 §3 NavigationContext v2。
// 三真源分工（06 §4）：Vue Router = 路由真源；浏览器 History = 页面时间轴；
// 本 store = 打开意图 + 视图快照。淘汰内存记录不动浏览器真实历史。

export const MAX_ENTRIES = 80
export const MAX_OPERATIONS = 100
/**
 * 滚动快照保留条数（UI规范 06 §6 / R5）。移植自 feat/web-mobile-hyper。
 * 只约束快照，不约束 entry 记录数（那个由 MAX_ENTRIES 管）。
 */
export const SNAPSHOT_LRU = 5
export const STORAGE_VERSION = 2

/** 17 §4-R2 校验关系：响应回写前检查代次；标题/快照回写前检查 renderEpoch。 */
export interface EntrySeed {
  fullPath: string
  routeName?: string
  presentation: NavigationEntry['presentation']
  openedBy: NavigationKind | 'present'
  title: string
  titleSource: NavigationEntry['titleSource']
  titleKey?: string
  scope: { accountId: string }
  historyPosition?: number
}

/** 持久化载荷——去敏（敏感 query 键拒绝清单 + 不落滚动/筛选对象）。 */
export interface PersistedContext {
  version: number
  entries: Array<Pick<NavigationEntry, 'id' | 'fullPath' | 'routeName' | 'presentation' | 'title' | 'titleKey' | 'createdAt'>>
  cursor: number
}

const SENSITIVE_QUERY_KEYS = new Set(['token', 'secret', 'password', 'api_key', 'apikey', 'key'])

export function sanitizeFullPath(fullPath: string): string {
  const qIdx = fullPath.indexOf('?')
  if (qIdx < 0) return fullPath
  const path = fullPath.slice(0, qIdx)
  const query = fullPath.slice(qIdx + 1)
  if (!query) return path
  const kept = query.split('&').filter((kv) => {
    const key = kv.split('=', 1)[0]?.toLowerCase() ?? ''
    return !SENSITIVE_QUERY_KEYS.has(key)
  })
  return kept.length > 0 ? `${path}?${kept.join('&')}` : path
}

function entryId(routeName: string | undefined, fullPath: string): string {
  return `${routeName ?? 'anon'}:${fullPath}`
}

export class NavigationStore {
  private entries: NavigationEntry[] = []
  private cursor = -1
  private operations: NavigationOperation[] = []
  private opSeq = 0
  private epochSeq = 0

  getEntries(): readonly NavigationEntry[] {
    return this.entries
  }

  getCursor(): number {
    return this.cursor
  }

  current(): NavigationEntry | null {
    return this.cursor >= 0 && this.cursor < this.entries.length ? (this.entries[this.cursor] ?? null) : null
  }

  findEntry(id: string): NavigationEntry | null {
    return this.entries.find((e) => e.id === id) ?? null
  }

  nextEpoch(): number {
    return ++this.epochSeq
  }

  /** push：截断 forward 分支后追加（返回后开新详情 = 截断，06 §4）。 */
  commitPush(seed: EntrySeed, parentId?: string): NavigationEntry {
    if (this.cursor < this.entries.length - 1) {
      this.entries = this.entries.slice(0, this.cursor + 1)
    }
    const entry: NavigationEntry = {
      id: entryId(seed.routeName, seed.fullPath),
      parentId,
      ...seed,
      view: { scroll: {} },
      createdAt: Date.now(),
      renderEpoch: this.nextEpoch(),
    }
    this.entries.push(entry)
    if (this.entries.length > MAX_ENTRIES) {
      this.entries.splice(0, this.entries.length - MAX_ENTRIES)
    }
    this.cursor = this.entries.length - 1
    this.recordOp(entry.id, seed.openedBy === 'restore' ? 'restore' : seed.openedBy === 'deepLink' ? 'deepLink' : 'push', 'committed')
    return entry
  }

  /** replace：保留页面 ID（同页换筛选/排序/内容 Tab，06 §4）。 */
  commitReplace(seed: EntrySeed): NavigationEntry {
    const current = this.current()
    const entry: NavigationEntry = {
      id: current?.id ?? entryId(seed.routeName, seed.fullPath),
      parentId: current?.parentId,
      ...seed,
      view: current?.view ?? { scroll: {} },
      createdAt: current?.createdAt ?? Date.now(),
      renderEpoch: current?.renderEpoch ?? this.nextEpoch(),
    }
    if (current) {
      this.entries[this.cursor] = entry
    } else {
      this.entries.push(entry)
      this.cursor = this.entries.length - 1
    }
    this.recordOp(entry.id, 'replace', 'committed')
    return entry
  }

  commitPop(): NavigationEntry | null {
    if (this.cursor <= 0) {
      return null
    }
    const popped = this.entries[this.cursor]
    this.cursor--
    if (popped) this.recordOp(popped.id, 'pop', 'committed')
    return this.current()
  }

  recordOp(entryId: string, type: OperationType, outcome: NavigationOperation['outcome']): void {
    this.operations.push({ seq: ++this.opSeq, entryId, type, timestamp: Date.now(), outcome })
    if (this.operations.length > MAX_OPERATIONS) {
      this.operations.splice(0, this.operations.length - MAX_OPERATIONS)
    }
  }

  getOperations(): readonly NavigationOperation[] {
    return this.operations
  }

  /** 离开页面前把滚动快照写进条目（entryId + renderEpoch 校验）。 */
  saveView(entryId: string, renderEpoch: number, scroll: NavigationEntry['view']['scroll']): void {
    const entry = this.findEntry(entryId)
    if (!entry || entry.renderEpoch !== renderEpoch) return
    entry.view.scroll = scroll
    this.evictStaleSnapshots()
  }

  /**
   * 快照 LRU：只保留最近 SNAPSHOT_LRU 个条目的滚动快照，更早的**只清 view，
   * entry 记录保留**（路径与标题仍可查、仍可回退）。
   *
   * 移植自 feat/web-mobile-hyper（其 navigationContext.spec.ts 有 R5 对应用例）。
   * 为什么只清快照不清记录：MAX_ENTRIES 已经给记录数封了顶，内存上不构成泄漏；
   * 真正的问题是**拿几十次导航之前的滚动位置去恢复一个数据早已变化的页面** ——
   * 那是一个静默的错误位置，比不恢复更糟（用户看到列表停在半截且无法解释）。
   * 被驱逐后调用方 restoreView 拿不到快照，按约定重走 initialLoading。
   * 恢复侧另有 2s 预算 + 用户滚动即取消（hyper/scroll/scrollHost.ts）作第二道保险。
   */
  private evictStaleSnapshots(): void {
    const withSnapshot = this.entries.filter((e) => Object.keys(e.view.scroll).length > 0)
    const excess = withSnapshot.length - SNAPSHOT_LRU
    if (excess <= 0) return
    for (const stale of withSnapshot.slice(0, excess)) {
      stale.view.scroll = {}
    }
  }

  persist(storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>, scopeKey: string): void {
    const payload: PersistedContext = {
      version: STORAGE_VERSION,
      entries: this.entries.map((e) => ({
        id: e.id,
        fullPath: sanitizeFullPath(e.fullPath),
        routeName: e.routeName,
        presentation: e.presentation,
        title: e.title,
        titleKey: e.titleKey,
        createdAt: e.createdAt,
      })),
      cursor: this.cursor,
    }
    try {
      storage.setItem(`hyper.nav.v2.${scopeKey}`, JSON.stringify(payload))
    } catch {
      /* 配额/隐私模式 — 导航记录只在内存 */
    }
  }

  /**
   * 冷启动恢复（06 §3）：schema 校验失败静默放弃（保留 v1 兼容由调用方
   * 自行处理——本应用无 v1 存量）。恢复的条目不恢复滚动位置。
   */
  restore(storage: Pick<Storage, 'getItem' | 'removeItem'>, scopeKey: string): boolean {
    let raw: string | null = null
    try {
      raw = storage.getItem(`hyper.nav.v2.${scopeKey}`)
    } catch {
      return false
    }
    if (!raw) return false
    let parsed: unknown
    try {
      parsed = JSON.parse(raw)
    } catch {
      storage.removeItem(`hyper.nav.v2.${scopeKey}`)
      return false
    }
    const p = parsed as PersistedContext
    if (
      !p ||
      typeof p !== 'object' ||
      p.version !== STORAGE_VERSION ||
      !Array.isArray(p.entries) ||
      p.entries.length === 0 ||
      p.entries.length > MAX_ENTRIES ||
      typeof p.cursor !== 'number' ||
      p.cursor < 0 ||
      p.cursor >= p.entries.length
    ) {
      storage.removeItem(`hyper.nav.v2.${scopeKey}`)
      return false
    }
    const entries: NavigationEntry[] = p.entries.map((e) => ({
      id: String(e.id),
      fullPath: sanitizeFullPath(String(e.fullPath)),
      routeName: e.routeName ? String(e.routeName) : undefined,
      presentation: e.presentation === 'modal' || e.presentation === 'sheet' || e.presentation === 'focus' ? e.presentation : 'page',
      openedBy: 'restore' as const,
      title: String(e.title ?? ''),
      titleSource: 'route' as const,
      titleKey: e.titleKey ? String(e.titleKey) : undefined,
      scope: { accountId: scopeKey },
      view: { scroll: {} },
      createdAt: Number(e.createdAt) || Date.now(),
      renderEpoch: this.nextEpoch(),
    }))
    this.entries = entries
    this.cursor = Math.min(p.cursor, entries.length - 1)
    // 恢复后当前条目仍需重新渲染：标记 pendingRestore 由适配器处理
    return true
  }

  reset(): void {
    this.entries = []
    this.cursor = -1
    this.operations = []
    this.opSeq = 0
    this.epochSeq = 0
  }
}
