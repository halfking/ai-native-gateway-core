// backDispatcher.ts — 返回单一仲裁（UI 规范 06 §5 + 11 §3 + 17 §3）。
// 顺序：覆盖层（beforeClose 拒绝即消费）→ pop 前驱 → fallback（Web 停留首页）。
// 顶栏返回钮 / Esc / popstate（系统返回）三入口同走 dispatchBack()。
// 覆盖层 history 标记：打开时同 URL pushState；popstate 拦截后若关闭被拒绝，
// 受控恢复（再 pushState 回标记位），保证浏览器历史与层栈一致。

export interface OverlayHandle {
  id: string
  /** 返回true=消费此次返回（层自行关闭）；false=拒绝（如脏表单确认）。 */
  beforeClose: () => boolean
  onClose: () => void
  title?: string
}

type BackOutcome =
  | { kind: 'overlay-consumed'; id: string }
  | { kind: 'overlay-rejected'; id: string }
  | { kind: 'pop' }
  | { kind: 'fallback' }

export class BackDispatcher {
  private overlays: OverlayHandle[] = []
  private pendingRestore = false

  /** 注册覆盖层（Sheet/确认框/专注层）。返回注销函数。 */
  register(handle: OverlayHandle): () => void {
    this.overlays.push(handle)
    this.pushHistoryMark()
    return () => {
      this.overlays = this.overlays.filter((o) => o.id !== handle.id)
    }
  }

  get topOverlay(): OverlayHandle | null {
    return this.overlays[this.overlays.length - 1] ?? null
  }

  get overlayCount(): number {
    return this.overlays.length
  }

  /**
   * 统一返回入口。
   * @param hasHistory 前驱是否存在（Vue Router history.state.position > 0 或
   *                   window.history.length 判定，由调用方按路由所有权三真源提供）。
   * @param goBack 前驱回退动作（router.back() / history.back()）。
   */
  dispatchBack(hasHistory: () => boolean, goBack: () => void): BackOutcome {
    const top = this.topOverlay
    if (top) {
      if (top.beforeClose()) {
        top.onClose()
        return { kind: 'overlay-consumed', id: top.id }
      }
      return { kind: 'overlay-rejected', id: top.id }
    }
    if (hasHistory()) {
      goBack()
      return { kind: 'pop' }
    }
    // fallback：根页 Web 停留（不跳外域，17 §8-2）。
    return { kind: 'fallback' }
  }

  /** 覆盖层打开时压同 URL 标记，使浏览器返回先作用于层而不是路由。 */
  private pushHistoryMark(): void {
    try {
      window.history.pushState({ __hyperOverlay: true }, '', window.location.href)
      this.pendingRestore = false
    } catch {
      /* 某些嵌入容器禁 pushState：降级为不标记（返回直接走路由 pop） */
    }
  }

  /**
   * popstate 处理：若当前历史项是覆盖层标记位且有层存活 → 判定系统返回
   * 作用于层。层关闭被拒绝时受控恢复标记位（再 push 回去），防止历史与
   * 层栈错位后「返回直接跳页但层还挂着」。
   */
  handlePopState(onPopRoute: () => void): void {
    const top = this.topOverlay
    if (top) {
      if (top.beforeClose()) {
        top.onClose()
        this.pendingRestore = false
        return
      }
      // 拒绝：受控恢复标记位。
      try {
        window.history.pushState({ __hyperOverlay: true }, '', window.location.href)
      } catch {
        /* ignore */
      }
      this.pendingRestore = true
      return
    }
    onPopRoute()
  }

  /** 层正常关闭（非返回触发）后，同步消化掉自己的历史标记位。 */
  consumeHistoryMark(): void {
    if (this.pendingRestore) {
      this.pendingRestore = false
      return
    }
    try {
      const st = window.history.state as { __hyperOverlay?: boolean } | null
      if (st && st.__hyperOverlay) {
        window.history.back()
      }
    } catch {
      /* ignore */
    }
  }
}

let singleton: BackDispatcher | null = null

export function backDispatcher(): BackDispatcher {
  if (!singleton) singleton = new BackDispatcher()
  return singleton
}
