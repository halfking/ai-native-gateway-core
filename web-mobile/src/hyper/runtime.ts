import { computed, ref, watch } from 'vue'
import type { ComputedRef } from 'vue'
import type { Router } from 'vue-router'
import type { NavigationEntry, OverlayHandle, ScrollPosition } from './types'
import { NavigationStore } from './navigation/context'
import {
  TitleResolver,
  type OverlayTitleRegistration,
  type PageTitleRegistration,
  type ResolvedTitle,
  type TitleSnapshot,
} from './navigation/titleResolver'
import { BackDispatcher } from './navigation/backDispatcher'
import { ScrollHostRegistry } from './scroll/scrollHost'
import { resolveCapabilities } from './capabilities'

// HyperRuntime — Web Runtime 装配（UI规范 08 §1 分层第 2 层）。
// 职责：页面/覆盖层注册表、标题真源、导航条目提交、覆盖层 history 标记
// 协调（06 §5.1）、Esc/返回仲裁接线、滚动快照与恢复、feature flag（R9）。
// 明确不做：不拥有路由表（Vue Router 真源）、不执行原生调用（Bridge 层）、
// 不持有凭据。

export interface HyperRuntimeOptions {
  appName: string
  getAccountId(): string
  translate(key: string): string
  /** 路由声明的安全回退（06 §5 优先级 6）。 */
  getFallback(routeName: string | symbol | undefined): string
}

const FLAGS_KEY = 'hyper.flags'
const MARKER_FLAG = '__hyperOverlay'

interface OverlayRecord {
  handle: OverlayHandle
  snapshot?: TitleSnapshot
}

let uid = 0
export function nextHyperId(prefix: string): string {
  return `${prefix}-${++uid}`
}

export class HyperRuntime {
  readonly nav = new NavigationStore()
  readonly scroll = new ScrollHostRegistry()

  private overlayRecords: OverlayRecord[] = []
  private overlaysTick = ref(0)
  private pageReg: PageTitleRegistration | null = null
  private pageTick = ref(0)
  private routeTitle = ref<string | undefined>(undefined)
  private markerStack: string[] = []
  private options: HyperRuntimeOptions | null = null
  private router: Router | null = null
  private detach: Array<() => void> = []
  private lastHistoryPosition: number | null = null
  private pendingRestore: NavigationEntry | null = null
  private flags: Record<string, boolean> = {}
  private restoredOnce = false

  private resolver = new TitleResolver({
    getPage: () => this.pageReg,
    getOverlays: (): OverlayTitleRegistration[] =>
      this.overlayRecords.map((r) => ({
        id: r.handle.id,
        title: r.handle.title,
        snapshot: r.snapshot,
      })),
    getRouteTitle: () => this.routeTitle.value,
    getAppName: () => this.options?.appName ?? 'Hyper',
  })

  readonly title: ComputedRef<ResolvedTitle> = computed(() => {
    // 依赖三个响应源：注册表版本 + 路由标题
    void this.overlaysTick.value
    void this.pageTick.value
    void this.routeTitle.value
    return this.resolver.resolve()
  })

  readonly topOverlay: ComputedRef<OverlayHandle | null> = computed(() => {
    void this.overlaysTick.value
    return this.overlayRecords.length > 0 ? (this.overlayRecords[this.overlayRecords.length - 1]?.handle ?? null) : null
  })

  private dispatcher = new BackDispatcher({
    getOverlays: () => this.overlayRecords.map((r) => r.handle),
    dismissOverlay: (id) => this.dismissOverlay(id),
    canPopInApp: () => (readHistoryPosition() ?? 0) > 0 && this.nav.getCursor() > 0,
    popInApp: () => window.history.back(),
    fallback: () => {
      const routeName = this.router?.currentRoute.value.name
      const target = this.options ? this.options.getFallback(routeName) : '/'
      void this.router?.replace(target)
    },
  })

  // ---- 安装与拆卸 ----

  install(router: Router, options: HyperRuntimeOptions): void {
    if (this.router) return // 幂等
    this.router = router
    this.options = options
    this.loadFlags()

    if (!this.restoredOnce) {
      this.restoredOnce = this.nav.restore(window.sessionStorage, options.getAccountId())
    }

    const offAfterEach = router.afterEach((to, from, failure) => {
      if (failure) return // 导航失败不提交条目（06 §4）
      this.onRouteCommitted(to, from)
    })

    const onPopState = (ev: PopStateEvent) => this.onPopState(ev)
    window.addEventListener('popstate', onPopState)

    const onKeyDown = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') {
        ev.preventDefault()
        void this.dispatcher.back()
      }
    }
    window.addEventListener('keydown', onKeyDown)

    const onPageHide = () => this.persistNow()
    window.addEventListener('pagehide', onPageHide)

    this.detach = [offAfterEach, () => window.removeEventListener('popstate', onPopState), () => window.removeEventListener('keydown', onKeyDown), () => window.removeEventListener('pagehide', onPageHide)]

    // 标题变化 → 写回当前条目 + document.title（06 §2）
    const stopWatch = watch(
      this.title,
      () => {
        this.syncTitleToEntry()
      },
      { flush: 'sync' },
    )
    this.detach.push(() => stopWatch())
  }

  /** 标题变化写回当前条目（watchEffect 由 facade 挂接，这里提供纯方法）。 */
  syncTitleToEntry(): void {
    const entry = this.nav.current()
    if (!entry) return
    const t = this.title.value
    entry.title = t.title
    entry.titleSource = t.source
    if (typeof document !== 'undefined' && t.title !== lastDocumentTitle) {
      lastDocumentTitle = t.title
      document.title = t.title
    }
  }

  // ---- 路由提交（06 §4 语义表） ----

  private onRouteCommitted(to: { fullPath: string; name?: string | symbol; meta?: Record<string, unknown> }, _from: { fullPath: string }): void {
    if (!this.options) return
    // 路由离开必须清理覆盖层（07 §9）：静默关闭（guards 已在 beforeEach 由
    // 各 Sheet 自行处理弹层内确认）；残留的 history 标记由 popstate 消费为 no-op。
    this.closeAllOverlaysSilently()

    // 离开页滚动快照（entryId + renderEpoch 校验在 store.saveView 内）
    const prev = this.nav.current()
    if (prev) {
      this.nav.saveView(prev.id, prev.renderEpoch, this.scroll.snapshot())
    }

    const position = readHistoryPosition()
    let kind: 'push' | 'replace' | 'pop' | 'deepLink'
    if (this.lastHistoryPosition == null) {
      kind = 'deepLink'
    } else if (position == null) {
      kind = 'replace'
    } else if (position > this.lastHistoryPosition) {
      kind = 'push'
    } else if (position < this.lastHistoryPosition) {
      kind = 'pop'
    } else {
      kind = 'replace'
    }
    this.lastHistoryPosition = position

    const titleKey = typeof to.meta?.titleKey === 'string' ? to.meta.titleKey : undefined
    const routeTitle = titleKey ? this.options.translate(titleKey) : undefined
    this.routeTitle.value = routeTitle

    const seed = {
      fullPath: to.fullPath,
      routeName: typeof to.name === 'string' ? to.name : undefined,
      presentation: 'page' as const,
      openedBy: kind,
      title: routeTitle ?? to.fullPath,
      titleSource: (routeTitle ? 'route' : 'document') as NavigationEntry['titleSource'],
      titleKey,
      scope: { accountId: this.options.getAccountId() },
      historyPosition: position ?? undefined,
    }

    if (kind === 'push') {
      const parentId = prev?.id
      this.nav.commitPush(seed, parentId)
    } else if (kind === 'replace') {
      this.nav.commitReplace(seed)
    } else if (kind === 'pop') {
      // 浏览器一次可能跨多步；近似提交一次 pop（Web 时间轴真源仍是 History）
      this.nav.commitPop()
      const cur = this.nav.current()
      if (cur) {
        // 回到的条目与新路由不符（深度跳跃）→ 按 replace 对齐
        if (cur.fullPath !== to.fullPath) {
          this.nav.commitReplace(seed)
        }
      } else {
        this.nav.commitPush(seed)
      }
    } else {
      this.nav.commitPush(seed) // deepLink 新栈（06 §4）
    }

    this.syncTitleToEntry()
    const entry = this.nav.current()
    if (entry && Object.keys(entry.view.scroll).length > 0) {
      this.pendingRestore = entry
    }
    this.persistNow()
  }

  /** 页面挂载后消费恢复（06 §6：最长 2s，用户滚动即取消）。 */
  async consumePendingRestore(): Promise<void> {
    const entry = this.pendingRestore
    if (!entry) return
    const current = this.nav.current()
    if (!current || current.id !== entry.id) {
      this.pendingRestore = null
      return
    }
    const snap = entry.view.scroll as Record<string, ScrollPosition>
    this.pendingRestore = null
    await this.scroll.restore(snap)
  }

  // ---- 覆盖层（06 §5.1 history 标记协调） ----

  presentOverlay(handle: OverlayHandle): void {
    // 继承快照在登记前采集（06 §2：继承打开时刻的有效标题）
    const snapshot: TitleSnapshot = { title: this.title.value.title, fromId: this.title.value.fromId }
    this.overlayRecords.push({ handle, snapshot })
    try {
      window.history.pushState({ [MARKER_FLAG]: handle.id }, '', window.location.href)
      this.markerStack.push(handle.id)
    } catch {
      /* history 不可用（极旧 WebView）→ 降级为可见关闭钮（14 §2） */
    }
    const cur = this.nav.current()
    if (cur) this.nav.recordOp(cur.id, 'present', 'committed')
    this.overlaysTick.value++
    this.syncTitleToEntry()
  }

  /** 业务自关 / BackDispatcher 关闭：同步清标记（06 §5.1）。 */
  dismissOverlay(id: string): void {
    const idx = this.overlayRecords.findIndex((r) => r.handle.id === id)
    if (idx < 0) return
    const record = this.overlayRecords[idx]
    this.overlayRecords.splice(idx, 1)
    const markerIdx = this.markerStack.lastIndexOf(id)
    if (markerIdx >= 0) {
      this.markerStack.splice(markerIdx, 1)
      // 标记仍是历史栈顶 → 弹出它（popstate 到页面态，注册表已无该层 → no-op）
      if (markerIdx === this.markerStack.length && readMarkerFromState(window.history.state) === id) {
        window.history.back()
      }
    }
    record?.handle.close()
    const cur = this.nav.current()
    if (cur) this.nav.recordOp(cur.id, 'dismiss', 'committed')
    this.overlaysTick.value++
    this.syncTitleToEntry()
  }

  private closeAllOverlaysSilently(): void {
    if (this.overlayRecords.length === 0) return
    for (const record of [...this.overlayRecords].reverse()) {
      record.handle.close()
    }
    this.overlayRecords = []
    this.markerStack = []
    this.overlaysTick.value++
  }

  /** popstate：落点是标记态 → no-op；否则有未清标记 = 标记被弹出 → 关顶层。 */
  private onPopState(ev: PopStateEvent): void {
    const stateMarker = readMarkerFromState(ev.state)
    if (stateMarker) return // 落在标记上（补偿 forward / 并发），不动
    if (this.markerStack.length === 0) return
    const topMarker = this.markerStack.pop()
    const top = this.overlayRecords[this.overlayRecords.length - 1]
    if (!top || top.handle.id !== topMarker) {
      // 标记与注册表失配（刷新残留）→ 仅消费
      return
    }
    // beforeClose 拒绝 → 受控 history.go 恢复标记位置（06 §5.1）
    void (async () => {
      let allowed = true
      if (top.handle.beforeClose) {
        allowed = await top.handle.beforeClose()
      }
      if (!allowed) {
        this.markerStack.push(topMarker as string)
        window.history.go(1)
        return
      }
      const idx = this.overlayRecords.findIndex((r) => r.handle.id === top.handle.id)
      if (idx >= 0) {
        this.overlayRecords.splice(idx, 1)
        top.handle.close()
        const cur = this.nav.current()
        if (cur) this.nav.recordOp(cur.id, 'dismiss', 'committed')
        this.overlaysTick.value++
        this.syncTitleToEntry()
      }
    })()
  }

  // ---- 页面注册 ----

  registerPage(reg: PageTitleRegistration): void {
    this.pageReg = reg
    this.pageTick.value++
    this.syncTitleToEntry()
  }

  unregisterPage(id: string): void {
    if (this.pageReg?.id === id) {
      this.pageReg = null
      this.pageTick.value++
    }
  }

  saveCurrentView(): void {
    const cur = this.nav.current()
    if (!cur) return
    this.nav.saveView(cur.id, cur.renderEpoch, this.scroll.snapshot())
  }

  // ---- 返回 / 快照 / 恢复门面 ----

  back(): Promise<boolean> {
    return this.dispatcher.back()
  }

  snapshot(): NavigationEntry | null {
    this.saveCurrentView()
    return this.nav.current()
  }

  // ---- capabilities / flags（17 §4-R3 / R9） ----

  capabilities() {
    return resolveCapabilities()
  }

  flag(name: string, fallback: boolean): boolean {
    const v = this.flags[name]
    return v === undefined ? fallback : v
  }

  setFlag(name: string, value: boolean): void {
    this.flags[name] = value
    try {
      localStorage.setItem(FLAGS_KEY, JSON.stringify(this.flags))
    } catch {
      /* 持久化失败不阻断 */
    }
  }

  private loadFlags(): void {
    try {
      const raw = localStorage.getItem(FLAGS_KEY)
      if (raw) {
        const parsed: unknown = JSON.parse(raw)
        if (parsed && typeof parsed === 'object') {
          for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
            if (typeof v === 'boolean') this.flags[k] = v
          }
        }
      }
    } catch {
      /* 坏数据 → 空 flags */
    }
  }

  // ---- 持久化 ----

  private persistNow(): void {
    if (!this.options) return
    this.nav.persist(window.sessionStorage, this.options.getAccountId())
  }

  clearPersistence(): void {
    if (!this.options) return
    try {
      window.sessionStorage.removeItem(`hyper.nav.v2.${this.options.getAccountId()}`)
    } catch {
      /* ignore */
    }
    this.nav.reset()
    this.lastHistoryPosition = readHistoryPosition()
  }

  // ---- 测试 ----

  resetForTests(): void {
    for (const fn of this.detach) fn()
    this.detach = []
    this.router = null
    this.options = null
    this.nav.reset()
    this.overlayRecords = []
    this.markerStack = []
    this.pageReg = null
    this.pendingRestore = null
    this.lastHistoryPosition = null
    this.restoredOnce = false
    this.routeTitle.value = undefined
    this.overlaysTick.value++
    this.pageTick.value++
  }

  /** 测试注入：直接驱动路由标题（绕过 Router）。 */
  setRouteTitleForTests(title: string | undefined): void {
    this.routeTitle.value = title
  }
}

let lastDocumentTitle = ''

function readHistoryPosition(): number | null {
  const pos = (window.history.state as { position?: unknown } | null)?.position
  return typeof pos === 'number' ? pos : null
}

function readMarkerFromState(state: unknown): string | null {
  const marker = (state as Record<string, unknown> | null)?.[MARKER_FLAG]
  return typeof marker === 'string' ? marker : null
}
