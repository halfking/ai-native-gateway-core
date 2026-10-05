import type { OverlayHandle } from '../types'

// BackDispatcher — UI规范 06 §5 / 11 §3 单一返回仲裁入口。
// 顶栏按钮、Esc、系统 Back、popstate 全走 back()；每次动作最多消费一层：
//   1. 关最高层覆盖层（beforeClose 拒绝 → 消费返回、保持现状，绝不跳背景路由）
//   2. 应用内 pop 到已知前驱（transition 单飞，快速连按不双 pop）
//   3. fallback（声明的安全回退或首页；根页 Web 留首页）
// IME / 应用内选择器层由各平台外壳先消费（Web 容器内浏览器先行处理）。

export interface BackDispatcherDeps {
  getOverlays(): OverlayHandle[]
  dismissOverlay(id: string): void
  canPopInApp(): boolean
  popInApp(): void
  fallback(): void
}

export class BackDispatcher {
  private inFlight = false

  constructor(private readonly deps: BackDispatcherDeps) {}

  /** 返回是否被消费（总有兜底，正常恒 true；仅并发第二击返回 false）。 */
  async back(): Promise<boolean> {
    if (this.inFlight) return false
    this.inFlight = true
    try {
      const overlays = this.deps.getOverlays()
      const top = overlays.length > 0 ? overlays[overlays.length - 1] : undefined
      if (top) {
        let allowed = true
        if (top.beforeClose) {
          allowed = await top.beforeClose()
        }
        if (!allowed) {
          // 消费返回事件，保持现状（06 §5 优先级 4）
          return true
        }
        this.deps.dismissOverlay(top.id)
        return true
      }
      if (this.deps.canPopInApp()) {
        this.deps.popInApp()
        return true
      }
      this.deps.fallback()
      return true
    } finally {
      this.inFlight = false
    }
  }
}
