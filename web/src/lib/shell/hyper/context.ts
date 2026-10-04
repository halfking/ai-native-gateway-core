/**
 * hyper/context.ts — 导航上下文与去敏持久化（docs/UI规范/00 §5.2 · H1，参考规范 06 §3–4）。
 *
 * ## 三个不可省的设计决定
 *
 * **1. id 是访问身份，不是 URL。**
 * 同一个详情页打开两次是两个 entry —— 两次的滚动位置与筛选可能不同。
 * 用 `fullPath` 当 key 会互相覆盖，返回时恢复到错误的位置。
 *
 * **2. 持久化必须去敏，而且要「白名单 + 值形状」双保险。**
 * 导航历史会写进 `sessionStorage`，同源可被后续任意脚本读到。里面可能有
 * 租户名、会话 ID、搜索原文。本模块的策略是：
 *   - query 只留**结构化视图状态**白名单（Tab / 视图模式 / 期间 / 页码 / 指标）；
 *   - 白名单的值还必须匹配结构化 token 形状（`^[A-Za-z0-9_.:-]{1,64}$`），
 *     任何像自然语言的搜索原文都进不来；
 *   - 自由文本类参数（`q` `filter` `match` `search` …）与身份/会话类参数
 *     （`redirect` `login` `session` `token` …）显式列进 DENY 并写进 spec；
 *   - **标题只持久化 i18n key**。标题可能含姓名（实体名），所以没有 key 就不存。
 *   - 去敏后仍超长 → 整段丢弃 query，退回纯路径。宁可少恢复，不可多落盘。
 *
 * **3. 按隔离域分区。**
 * 换账号 / 换服务端 / 登出即清空。不允许 A 账号的标题与筛选出现在 B 账号。
 *
 * ## 存储不可用时怎么办
 *
 * 隐私模式 / 配额满 / 结构化克隆失败：内存上下文**照常工作**，只是不落盘。
 * 导航能力不能因为存不下就退化。
 */
import { sanitizeTitle } from './title'
import type {
  NavigationContext,
  NavigationEntry,
  NavigationKind,
  NavigationOperation,
  NavigationScope,
  Presentation,
  ScrollAnchor,
  ViewSnapshot,
} from './types'

export const NAV_STORAGE_KEY = 'llmgw:hyper.navigation.v2'

/** 页面历史上限。沿用参考实现 v1 的上限，不在本轮扩大。 */
export const MAX_ENTRIES = 80
/** 操作环上限。 */
export const MAX_OPERATIONS = 100
/** 单条 entry 持久化后允许的最大长度。超出则丢弃 query。 */
const MAX_PERSISTED_PATH = 512

/**
 * 允许持久化的 query 键：只包含「当前在看哪个视图」的结构化状态。
 * 依据是对 `route.query` 的实测使用（`tab` 28 处、`view` / `mode` /
 * `start` / `end` / `metric` / `page` 等），**不是**照抄参考仓。
 */
export const PERSISTED_QUERY_WHITELIST: readonly string[] = Object.freeze([
  'tab',
  'view',
  'mode',
  'start',
  'end',
  'page',
  'metric',
  'app',
])

/**
 * 显式拒绝的 query 键。写进这里是为了让「为什么拒绝」可审阅，
 * 并让 spec 能钉住这份清单不被静默放宽。
 *
 * 分三类：
 * - 自由文本 / 复杂筛选：`q` `filter` `match` `slist` `set` `search` `keyword`
 * - 身份与凭据：`redirect` `login` `token` `key` `api_key` `secret` `session`
 *   `gw_session_id` `person` `owner`
 * - 实体 id：`tenant` `tenant_id` `credential_id` `provider_id`
 *   —— 这些不是「不安全」，而是**不该在 query 里持久化**：它们属于
 *   `scope`（本实现暂未启用租户 scope）或应由页面按自身状态重新解析。
 *   丢弃它们的后果是恢复到列表页而不是详情页 —— 这正是想要的安全兜底。
 */
export const PERSISTED_QUERY_DENYLIST: readonly string[] = Object.freeze([
  'q',
  'filter',
  'match',
  'slist',
  'set',
  'search',
  'keyword',
  'redirect',
  'login',
  'token',
  'key',
  'api_key',
  'secret',
  'session',
  'gw_session_id',
  'person',
  'owner',
  'tenant',
  'tenant_id',
  'credential_id',
  'provider_id',
])

/** 白名单值的形状闸门：结构化 token，不接受自然语言。 */
const STRUCTURED_VALUE = /^[A-Za-z0-9_.:-]{1,64}$/

function sameScope(a: NavigationScope, b: NavigationScope): boolean {
  return a.serverId === b.serverId && a.accountId === b.accountId && (a.projectId ?? '') === (b.projectId ?? '')
}

export interface NewEntryInput {
  parentId?: string
  fullPath: string
  routeName?: string
  presentation: Presentation
  openedBy: NavigationKind
  title?: string | null
  titleKey?: string
  scope: NavigationScope
  view?: ViewSnapshot
  historyPosition?: number
  id?: string
}

export class NavigationStore {
  private entries: NavigationEntry[] = []
  private cursor = -1
  private operations: NavigationOperation[] = []
  private overlayIds: string[] = []
  private scope: NavigationScope | null = null
  private seq = 0
  private idSeq = 0

  /**
   * 声明当前隔离域。**首次声明或域变化时清空历史** ——
   * 换账号/换服务端绝不能看到上一个人的标题与筛选。
   */
  setScope(scope: NavigationScope): void {
    if (this.scope && sameScope(this.scope, scope)) return
    this.scope = scope
    this.clear()
  }

  getScope(): NavigationScope | null {
    return this.scope
  }

  private nextId(): string {
    return `ne${++this.idSeq}`
  }

  /**
   * 登记一次页面访问。
   *
   * `replace` 复用当前 entry 的 id（不增栈）；`push` / `deepLink` 截断前进分支后追加。
   * 返回新 entry id。
   */
  push(input: NewEntryInput): string {
    const at = this.cursor
    if (input.openedBy === 'replace' && at >= 0 && this.entries[at]) {
      // replace：保留 id 与 position，只换内容，父级不变
      const prev = this.entries[at]
      this.entries[at] = {
        ...this.build(input, prev.id),
        parentId: prev.parentId,
        historyPosition: input.historyPosition ?? prev.historyPosition,
      }
      this.log(prev.id, 'replace', 'committed')
      return prev.id
    }

    // 截断前进分支：返回后再打开新页，旧 forward 不可达
    if (at < this.entries.length - 1) this.entries = this.entries.slice(0, at + 1)

    const entry = this.build(input, this.nextId())
    this.entries.push(entry)
    this.cursor = this.entries.length - 1
    this.log(entry.id, input.openedBy, 'committed')
    this.enforceLimits()
    return entry.id
  }

  private build(input: NewEntryInput, id: string): NavigationEntry {
    return {
      id,
      parentId: input.parentId,
      fullPath: input.fullPath,
      routeName: input.routeName,
      presentation: input.presentation,
      openedBy: input.openedBy,
      title: sanitizeTitle(input.title) ?? '',
      titleSource: input.title ? 'registered' : 'route',
      titleKey: input.titleKey,
      scope: { ...input.scope },
      view: input.view ?? {},
      historyPosition: input.historyPosition,
      createdAt: Date.now(),
    }
  }

  /**
   * 应用一次**外部**出栈（RouterHistoryAdapter 在导航成功后调用）。
   *
   * 与 `pop()` 的区别：`pop()` 是「我要退」的前置动作，调用方随后还要驱动路由；
   * 本方法是「路由已经退成功了」的事后对齐，是 cursor 的**唯一写入入口**。
   *
   * 拆成两个动作的原因：先退 store 再调路由，一旦路由守卫拒绝，两者就脱节 ——
   * 页面没退但上下文以为退了，下一次返回会跳过一层。
   *
   * @param fromId 发起本次 pop 的 entry id。传入时只有在「当前 cursor 正好
   *               指向它」的情况下才左移一级，避免连续两次 pop 重复左移。
   * @returns 是否真的发生了位移。
   */
  applyExternalPop(fromId?: string): boolean {
    if (this.cursor <= 0) return false
    if (fromId && this.entries[this.cursor]?.id !== fromId) {
      // 期间已经发生过别的导航，不要拿旧的 pop 意图去改新位置的 cursor
      return false
    }
    const popped = this.entries[this.cursor]
    this.cursor -= 1
    this.log(popped.id, 'pop', 'committed')
    return true
  }

  /** 记录一次操作（用于解释，不用于重放）。 */
  log(entryId: string, type: string, outcome: NavigationOperation['outcome']): void {
    this.operations.push({ seq: ++this.seq, entryId, type, timestamp: Date.now(), outcome })
    if (this.operations.length > MAX_OPERATIONS) {
      this.operations = this.operations.slice(this.operations.length - MAX_OPERATIONS)
    }
  }

  setOverlayIds(ids: readonly string[]): void {
    this.overlayIds = [...ids]
  }

  getOverlayIds(): readonly string[] {
    return this.overlayIds
  }

  current(): NavigationEntry | null {
    return this.cursor >= 0 ? (this.entries[this.cursor] ?? null) : null
  }

  byId(id: string): NavigationEntry | null {
    return this.entries.find((e) => e.id === id) ?? null
  }

  all(): readonly NavigationEntry[] {
    return [...this.entries]
  }

  get hasForward(): boolean {
    return this.cursor < this.entries.length - 1
  }

  /**
   * 是否已无「已知」页面前驱。
   *
   * 刻意**不**用 `window.history.length > 1` 或 Capacitor `canGoBack`：
   * 那里面可能有登录页、外域、引导页，据此判断会把用户退到不该去的地方。
   * 只认本 store 的 entry 链。
   */
  get atRoot(): boolean {
    return this.cursor <= 0
  }

  /**
   * 出栈到父 entry。返回被弹出的 entry；已在根则返回 null。
   * 只改内存 cursor —— **不调用** `history.back()`，真实浏览器历史由
   * RouterHistoryAdapter 负责，避免两边各动一次。
   */
  pop(): NavigationEntry | null {
    if (this.cursor <= 0) return null
    const popped = this.entries[this.cursor]
    this.cursor -= 1
    this.log(popped.id, 'pop', 'committed')
    return popped
  }

  /** 前进到已存在的目的地。不重放任何动作。 */
  forward(): NavigationEntry | null {
    if (!this.hasForward) return null
    this.cursor += 1
    const e = this.entries[this.cursor]
    this.log(e.id, 'forward', 'committed')
    return e
  }

  updateView(entryId: string, patch: Partial<ViewSnapshot>): void {
    const e = this.byId(entryId)
    if (!e) return
    e.view = { ...e.view, ...patch }
  }

  saveScroll(entryId: string, hostId: string, anchor: ScrollAnchor): void {
    const e = this.byId(entryId)
    if (!e) return
    e.view = { ...e.view, scroll: { ...(e.view.scroll ?? {}), [hostId]: anchor } }
  }

  private enforceLimits(): void {
    if (this.entries.length <= MAX_ENTRIES) return
    // 丢最旧的，且 cursor 同步左移。淘汰内存记录**不动**浏览器真实历史。
    const drop = this.entries.length - MAX_ENTRIES
    this.entries = this.entries.slice(drop)
    this.cursor -= drop
    if (this.cursor < 0) this.cursor = this.entries.length > 0 ? 0 : -1
  }

  clear(): void {
    this.entries = []
    this.cursor = -1
    this.operations = []
    this.overlayIds = []
    this.idSeq = 0
    this.seq = 0
  }

  // ——— 去敏与持久化 ———

  /**
   * 把 fullPath 裁剪为可安全落盘的形态。
   * 非法 / 超长 / 丢失必要信息时返回 null（调用方退化为不存 query）。
   */
  static sanitizePath(fullPath: string): string | null {
    if (typeof fullPath !== 'string' || !fullPath.startsWith('/')) return null
    // 协议相对 URL（`//evil.com`）会以 `//` 开头且能通过下面的字符闸门，
    // 一旦被当作 location 使用就是外域跳转。必须显式拒绝。
    if (fullPath.startsWith('//')) return null
    const [rawPath, rawQuery] = fullPath.split('?')
    // 路径本身也要过一遍形状闸门，防注入
    if (!/^\/[A-Za-z0-9/_.:-]*$/.test(rawPath)) return null
    if (!rawQuery) return rawPath

    const kept: string[] = []
    for (const pair of rawQuery.split('&')) {
      if (!pair) continue
      const eq = pair.indexOf('=')
      const k = decodeURIComponent(eq >= 0 ? pair.slice(0, eq) : pair)
      const v = eq >= 0 ? decodeURIComponent(pair.slice(eq + 1).replace(/\+/g, ' ')) : ''
      if (PERSISTED_QUERY_DENYLIST.includes(k)) continue
      if (!PERSISTED_QUERY_WHITELIST.includes(k)) continue
      if (!STRUCTURED_VALUE.test(v)) continue
      kept.push(`${k}=${v}`)
    }
    if (kept.length === 0) return rawPath
    const joined = `${rawPath}?${kept.join('&')}`
    return joined.length > MAX_PERSISTED_PATH ? rawPath : joined
  }

  /** 构造可落盘的快照。白名单 + titleKey-only + 隔离域校验。 */
  toPersisted(): { scope: NavigationScope; context: NavigationContext } | null {
    if (!this.scope) return null
    const entries: NavigationEntry[] = this.entries.map((e) => {
      const path = NavigationStore.sanitizePath(e.fullPath)
      return {
        ...e,
        fullPath: path ?? e.fullPath.split('?')[0],
        // 标题只存 key。含姓名的标题文本不落盘。
        title: '',
        titleKey: e.titleKey,
        view: {
          tabId: e.view.tabId,
          scroll: e.view.scroll,
          focusTarget: e.view.focusTarget,
        },
      }
    })
    return {
      scope: { ...this.scope },
      context: { version: 2, entries, cursor: this.cursor, overlayIds: [], operations: [] },
    }
  }

  /**
   * 从落盘快照恢复。结构校验不通过时**整体丢弃**并返回 false ——
   * 半可信的导航历史比没有更危险。
   */
  fromPersisted(raw: unknown, scope: NavigationScope): boolean {
    if (!raw || typeof raw !== 'object') return false
    const data = raw as { scope?: NavigationScope; context?: NavigationContext }
    if (!data.scope || !data.context) return false
    if (!sameScope(data.scope, scope)) return false
    if (data.context.version !== 2) return false
    if (!Array.isArray(data.context.entries)) return false
    if (data.context.entries.some((e) => typeof e?.fullPath !== 'string')) return false

    this.scope = { ...scope }
    this.entries = data.context.entries.map((e, i) => ({ ...e, title: sanitizeTitle(e.title) ?? '' }))
    this.cursor = typeof data.context.cursor === 'number' ? data.context.cursor : this.entries.length - 1
    this.cursor = Math.max(-1, Math.min(this.cursor, this.entries.length - 1))
    // 恢复的是**页面**时间轴，覆盖层标记一律不恢复（不重开脏表单）
    this.overlayIds = []
    this.operations = []
    this.idSeq = this.entries.length
    return true
  }
}

export const navigation = new NavigationStore()

/** 写 sessionStorage。任何失败都静默 —— 内存上下文才是真源。 */
export function persistNavigation(store: NavigationStore = navigation): void {
  if (typeof sessionStorage === 'undefined') return
  const payload = store.toPersisted()
  if (!payload) return
  try {
    sessionStorage.setItem(NAV_STORAGE_KEY, JSON.stringify(payload))
  } catch {
    // 配额满 / 隐私模式：放弃落盘，不影响内存导航
  }
}

/** 读 sessionStorage 并恢复。 */
export function restoreNavigation(scope: NavigationScope, store: NavigationStore = navigation): boolean {
  if (typeof sessionStorage === 'undefined') return false
  try {
    const raw = sessionStorage.getItem(NAV_STORAGE_KEY)
    if (!raw) return false
    return store.fromPersisted(JSON.parse(raw), scope)
  } catch {
    return false
  }
}

/** 登出/换账号/换服务端：内存与落盘一起清。 */
export function clearNavigation(store: NavigationStore = navigation): void {
  store.clear()
  if (typeof sessionStorage === 'undefined') return
  try {
    sessionStorage.removeItem(NAV_STORAGE_KEY)
  } catch {
    // 同上
  }
}
