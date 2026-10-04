// navigationContext.ts — 本地导航历史上下文（UI 规范 06 §3 + 17 §3 简化实现）。
// entries ≤80 / operations ≤100；sessionStorage 去敏持久化（scope 隔离、
// query 白名单、凭据/Key 值不落盘）。R5：快照 LRU(5) 驱逐时保留 entry 记录、
// 只清 view 快照——回到被驱逐页时重新走 initialLoading，不复用过期滚动位置。

export type NavigationOperationType =
  | 'push'
  | 'replace'
  | 'pop'
  | 'deepLink'
  | 'restore'
  | 'present'
  | 'dismiss'
  | 'refresh'

export interface NavigationOperation {
  type: NavigationOperationType
  at: number
  from?: string
  to?: string
}

export interface ViewSnapshot {
  scrollTop: number
  activeTab?: string
  filter?: string
}

export interface NavigationEntry {
  entryId: string
  path: string
  /** query 白名单内的键值（其余丢弃，敏感值不落盘）。 */
  query: Record<string, string>
  title: string | null
  /** 弹层继承快照（overlay 无标题时下层标题沿链上浮）。 */
  inheritedTitle: string | null
  renderEpoch: number
  /** 可选 view 快照（LRU 驱逐后为 null：entry 在、快照不在）。 */
  view: ViewSnapshot | null
}

export const MAX_ENTRIES = 80
export const MAX_OPERATIONS = 100
export const SNAPSHOT_LRU = 5

/** 允许持久化的 query 键白名单（凭据/Key/搜索词不落盘）。 */
const QUERY_WHITELIST = new Set(['days', 'tab', 'redirect', 'id'])

const STORE_KEY = 'llmgw_mobile_navctx'
const STORE_SCOPE = '/m'

let sessionSeq = 0

function newEntryId(): string {
  sessionSeq += 1
  return `e${Date.now().toString(36)}-${sessionSeq.toString(36)}`
}

export class NavigationContext {
  private entries: NavigationEntry[] = []
  private operations: NavigationOperation[] = []
  private listeners = new Set<() => void>()

  constructor(private persist = defaultPersist) {}

  /** push/replace/pop 之外先记操作再动栈。 */
  record(op: NavigationOperation): void {
    this.operations.push(op)
    if (this.operations.length > MAX_OPERATIONS) {
      this.operations.splice(0, this.operations.length - MAX_OPERATIONS)
    }
  }

  push(path: string, query: Record<string, string> = {}): NavigationEntry {
    const prev = this.top()
    const entry: NavigationEntry = {
      entryId: newEntryId(),
      path,
      query: sanitizeQuery(query),
      title: null,
      inheritedTitle: prev?.title ?? null,
      renderEpoch: 0,
      view: null,
    }
    this.entries.push(entry)
    this.trimEntries()
    this.record({ type: 'push', at: Date.now(), from: prev?.path, to: path })
    this.persist(this.serialize())
    this.emit()
    return entry
  }

  replace(path: string, query: Record<string, string> = {}): void {
    const entry: NavigationEntry = {
      entryId: newEntryId(),
      path,
      query: sanitizeQuery(query),
      title: null,
      inheritedTitle: this.top()?.inheritedTitle ?? null,
      renderEpoch: 0,
      view: null,
    }
    this.entries[this.entries.length - 1] = entry
    this.record({ type: 'replace', at: Date.now(), to: path })
    this.persist(this.serialize())
    this.emit()
  }

  pop(): NavigationEntry | null {
    const leaving = this.entries.pop() ?? null
    if (leaving) {
      this.record({ type: 'pop', at: Date.now(), from: leaving.path, to: this.top()?.path })
      this.persist(this.serialize())
      this.emit()
    }
    return leaving
  }

  top(): NavigationEntry | null {
    return this.entries[this.entries.length - 1] ?? null
  }

  canPop(): boolean {
    return this.entries.length > 1
  }

  setTitle(entryId: string, title: string, renderEpoch: number): void {
    const entry = this.entries.find((e) => e.entryId === entryId)
    if (!entry || entry.renderEpoch !== renderEpoch) return
    entry.title = title
    this.emit()
  }

  bumpRenderEpoch(entryId: string): number {
    const entry = this.entries.find((e) => e.entryId === entryId)
    if (!entry) return -1
    entry.renderEpoch += 1
    return entry.renderEpoch
  }

  /** R5：快照写当前 entry；LRU(5) 之外的 entry 只保留记录、清 view。 */
  snapshotView(entryId: string, snap: ViewSnapshot): void {
    const entry = this.entries.find((e) => e.entryId === entryId)
    if (!entry) return
    entry.view = snap
    const withView = this.entries.filter((e) => e.view !== null)
    for (const e of withView.slice(0, Math.max(0, withView.length - SNAPSHOT_LRU))) {
      e.view = null
    }
    this.persist(this.serialize())
  }

  /** 恢复：仅当 entry 仍持有 view 快照时返回，否则 null（调用方走 initialLoading）。 */
  restoreView(entryId: string): ViewSnapshot | null {
    return this.entries.find((e) => e.entryId === entryId)?.view ?? null
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn)
    return () => this.listeners.delete(fn)
  }

  list(): readonly NavigationEntry[] {
    return this.entries
  }

  operationsLog(): readonly NavigationOperation[] {
    return this.operations
  }

  private trimEntries(): void {
    if (this.entries.length > MAX_ENTRIES) {
      // 截断 forward 分支语义：保留最近 MAX_ENTRIES 条。
      this.entries.splice(0, this.entries.length - MAX_ENTRIES)
    }
  }

  private emit(): void {
    for (const fn of this.listeners) fn()
  }

  private serialize(): string {
    // 落盘只存 path/query/title/view；renderEpoch 等运行态随会话重建。
    return JSON.stringify({
      scope: STORE_SCOPE,
      entries: this.entries.map((e) => ({
        path: e.path,
        query: e.query,
        title: e.title,
        view: e.view,
      })),
    })
  }
}

function sanitizeQuery(query: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(query)) {
    if (QUERY_WHITELIST.has(k)) out[k] = v
  }
  return out
}

export function defaultPersist(payload: string): void {
  try {
    sessionStorage.setItem(STORE_KEY, payload)
  } catch {
    /* 隐私模式/满额：导航时间轴退化为内存态，不阻塞 */
  }
}

/** 供测试与冷启动恢复探测。 */
export function readPersistedNav(): unknown {
  try {
    const raw = sessionStorage.getItem(STORE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as { scope?: string }
    // scope 隔离：桌面端 / 与本 SPA 互不串档。
    if (parsed.scope !== STORE_SCOPE) return null
    return parsed
  } catch {
    return null
  }
}
