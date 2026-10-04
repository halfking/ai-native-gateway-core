/**
 * hyper/overlay.ts — 覆盖层登记与栈顶仲裁（docs/UI规范/00 §5.2 · H1）。
 *
 * ## 为什么需要它
 *
 * 本仓已有三套并行的弹层机制：`useConfirmDialog`（26 视图采用）、
 * `AppModal`、`AppDrawer`，加上 32 处历史手写 `modal-overlay`。
 * 它们各自处理 Esc、各自管 z-index、各自决定「能不能关」。
 * 后果是：Android 系统返回键回来时，**没有任何一个地方知道当前最上层是什么**。
 *
 * 本模块不接管渲染、不接管滚动锁（那些仍由 AppModal/AppDrawer 负责，
 * 避免与既有 `confirm-dialog.css` 的 z-index 约定打架），
 * 只做一件事：**把「当前有哪些层、哪层在最上、能不能关」变成可查询的状态**。
 *
 * ## 明确不做的事
 *
 * - 不关 z-index。层级由各组件的 CSS 决定；本模块只提供 `top()` 供它们查询。
 * - 不做滚动锁。既有实现已在本仓被 26 个视图验证过，重写只会引入回归。
 * - 不恢复焦点。`restoreFocus` 只登记，实际恢复由触发组件做
 *   （因为触发点 DOM 在组件里，运行时无法可靠重放）。
 */
import type { OverlayRegistration } from './types'

type Listener = (stack: readonly OverlayRegistration[]) => void

export class OverlayRegistry {
  /** 按登记顺序存储；`top()` 取 priority 最大者，同 priority 取后登记者。 */
  private stack: OverlayRegistration[] = []
  private listeners = new Set<Listener>()
  private seq = 0

  /**
   * 登记一个覆盖层。返回注销函数。
   *
   * 幂等：同 id 重复登记会替换旧项而不是堆叠，避免某个组件在
   * KeepAlive 激活/停用时反复 register 造成栈里出现两层同一个弹窗。
   */
  register(reg: Omit<OverlayRegistration, 'priority' | 'createdAt'> & { priority?: number }): () => void {
    const id = reg.id
    const existing = this.stack.findIndex((r) => r.id === id)

    // 同 id 已存在：保留原有 createdAt 与 priority 槽位，只更新内容。
    // 这样 KeepAlive 重新激活不会把弹窗「变新」导致它被误判为最上层。
    const createdAt = existing >= 0 ? this.stack[existing].createdAt : ++this.seq
    const priority = reg.priority ?? (existing >= 0 ? this.stack[existing].priority : createdAt)
    const full: OverlayRegistration = { ...reg, priority, createdAt }

    if (existing >= 0) this.stack[existing] = full
    else this.stack.push(full)

    this.emit()
    let released = false
    return () => {
      if (released) return // 注销必须幂等：组件卸载可能与守卫拒绝路径叠加
      released = true
      this.unregister(id)
    }
  }

  unregister(id: string): void {
    const before = this.stack.length
    this.stack = this.stack.filter((r) => r.id !== id)
    if (this.stack.length !== before) this.emit()
  }

  /**
   * 局部更新已登记的层（补 close / title / guard / dirty）。
   *
   * 存在理由：组件常常分两步接入 —— 先在 `onBeforeUnmount` 拿到稳定 id，
   * 打开时登记占位条目，之后才拿到自己的真实关闭动作（要 emit）。
   * 没有这个方法就只能整体重登记，那会丢掉 createdAt/priority 槽位。
   *
   * 对不存在的 id **静默忽略**：这通常意味着层已被关闭（组件卸载竞态），
   * 此时再补登记会凭空造出一个已死的层，比不做事更糟。
   */
  upgrade(id: string, patch: Partial<Omit<OverlayRegistration, 'id' | 'createdAt'>>): boolean {
    const idx = this.stack.findIndex((r) => r.id === id)
    if (idx < 0) return false
    this.stack[idx] = { ...this.stack[idx], ...patch }
    this.emit()
    return true
  }

  /** 最上层：priority 最大；同 priority 取后登记者（更「近」用户）。 */
  top(): OverlayRegistration | null {
    if (this.stack.length === 0) return null
    let best = this.stack[0]
    for (const r of this.stack) {
      if (r.priority >= best.priority) best = r
    }
    return best
  }

  get(id: string): OverlayRegistration | null {
    return this.stack.find((r) => r.id === id) ?? null
  }

  /** 自底向上的完整栈（用于调试与 spec 断言）。 */
  all(): readonly OverlayRegistration[] {
    return [...this.stack]
  }

  get depth(): number {
    return this.stack.length
  }

  /**
   * 尝试关闭最上层。
   *
   * 返回 `false` 表示**被消费**：层仍在栈顶，调用方（BackDispatcher）
   * 必须保持现状，不得穿透到背景路由。
   *
   * 三种拒绝路径，都不是 bug：
   * 1. `dismissible === false` —— 系统级层，不允许用户手势关闭；
   * 2. `closeGuard` 返回 false —— 脏表单，用户选择「继续编辑」；
   * 3. `close()` 抛错 —— 组件关闭失败，层仍在。
   */
  async closeTop(): Promise<boolean> {
    const top = this.top()
    if (!top) return false

    if (!top.dismissible) return false

    if (top.closeGuard) {
      const allowed = await top.closeGuard()
      if (!allowed) return false
    }

    try {
      await top.close()
    } catch {
      return false
    }
    return true
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn)
    return () => {
      this.listeners.delete(fn)
    }
  }

  private emit(): void {
    const snapshot = this.all()
    for (const fn of this.listeners) {
      try {
        fn(snapshot)
      } catch {
        // 订阅者异常不得影响其他订阅者，也不得让 registry 崩掉
      }
    }
  }

  /** 仅供测试与登出时清空。 */
  _reset(): void {
    this.stack = []
    this.seq = 0
    this.emit()
  }
}

/** 全局单例。壳与业务页面共享同一份栈。 */
export const overlays = new OverlayRegistry()
