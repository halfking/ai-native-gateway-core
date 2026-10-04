/**
 * hyper/back.ts — 单一返回仲裁入口（docs/UI规范/00 §5.2 · H1，参考规范 06 §5 / 11 §3）。
 *
 * ## 为什么要「单一」
 *
 * 返回可能来自四个地方：顶栏返回钮、Android 系统 Back、iOS 边缘手势提交、
 * 键盘 Esc/快捷键。如果每处各判一次，就会出现「弹窗已经关了但页面也退了」
 * 这种**双 pop**。所以它们全部只发意图，由本模块决定「这次返回消费掉几层」。
 *
 * ## 仲裁次序（每次动作最多消费一层）
 *
 * 1. 子选择器/菜单声明消费返回 → 关它（输入法是否由系统先关，交给原生，不在 JS 提交表单）
 * 2. 关闭最高层覆盖层（modal / sheet / focus）
 * 3. 覆盖层不可关闭或脏表单 → 消费返回，**保持现状**，绝不穿透到背景路由
 * 4. 无覆盖层 → 按当前分支**已知的** entry 前驱返回
 * 5. 无已知前驱 → 路由登记的安全 fallback（用 replace，避免「首页→详情→首页」循环）
 * 6. 根页无层 → Web 留在原地；原生把意图交还系统，**不默认杀进程**
 *
 * ## 三条硬约束
 *
 * - **不用 `window.history.length > 1` 判断业务父页**。那里可能有登录页、
 *   外域、引导页。本模块只认 NavigationStore 里的 entry 链。
 * - **单飞**。进行中的返回不接受第二次；不靠固定 500ms 解锁（那是猜的），
 *   而是 await 到 close/pop 真正 settle。
 * - **能力不足就报 unsupported**，不把 no-op 冒充 success。
 */
import { overlays } from './overlay'
import { navigation, type NavigationStore } from './context'
import type { BackOutcome } from './types'

/** 返回意图来源。只影响日志与埋点，不改变仲裁结果。 */
export type BackSource = 'button' | 'system' | 'escape' | 'gesture'

/**
 * 导航端口。运行时不知道 Vue Router 的存在，由适配器注入 ——
 * 这样 BackDispatcher 可以脱离路由单测，路由也可以独立演进。
 */
export interface NavigationPort {
  /** 退到父页。返回是否真正提交（被路由守卫拒绝时为 false）。 */
  pop(): Promise<boolean>
  /** 安全兜底跳转，必须是 replace 语义（不留下返回环）。 */
  replace(path: string): Promise<boolean>
  /** 当前是否已无任何可退的页面前驱。 */
  isAtRoot(): boolean
  /**
   * 把返回意图交还系统（原生返回桌面）。
   * 浏览器环境应返回 false 表示「系统无接收者」。
   */
  deliverToSystem(): boolean
}

/** 声明「我要消费返回」的子层（选择器、下拉、搜索建议）。 */
interface BackConsumer {
  id: string
  close: () => void | Promise<void>
  priority: number
}

export class BackDispatcher {
  private inFlight = false
  private consumers: BackConsumer[] = []
  private port: NavigationPort | null = null

  /** 注入导航端口。由 installHyper 在应用入口调用一次。 */
  setPort(port: NavigationPort | null): void {
    this.port = port
  }

  hasPort(): boolean {
    return this.port !== null
  }

  /**
   * 登记一个子层消费返回。返回注销函数（幂等）。
   *
   * 与 overlay registry 的区别：consumer 说的是「我正在交互，但我不是一层
   * 弹窗」（例如下拉菜单、日期选择面板、搜索建议列表）。它们不进覆盖层栈，
   * 但 Back 必须先关它们。
   */
  registerConsumer(c: Omit<BackConsumer, 'priority'> & { priority?: number }): () => void {
    const full: BackConsumer = { ...c, priority: c.priority ?? Date.now() }
    this.consumers.push(full)
    let released = false
    return () => {
      if (released) return
      released = true
      this.consumers = this.consumers.filter((x) => x.id !== full.id)
    }
  }

  private topConsumer(): BackConsumer | null {
    if (this.consumers.length === 0) return null
    return [...this.consumers].sort((a, b) => b.priority - a.priority)[0]
  }

  /**
   * 处理一次返回请求。
   *
   * 幂等性由 `inFlight` 保证：进行中时直接返回 `'in-flight'`，
   * 调用方不得再动路由或关层。
   */
  async request(
    source: BackSource = 'button',
    opts: { store?: NavigationStore; port?: NavigationPort } = {},
  ): Promise<BackOutcome> {
    const store = opts.store ?? navigation
    const port = opts.port ?? this.port

    if (this.inFlight) return 'in-flight'

    this.inFlight = true
    try {
      // 1) 子层消费
      const consumer = this.topConsumer()
      if (consumer) {
        try {
          await consumer.close()
        } catch {
          // 消费失败也算消费掉本次返回，避免穿透
        }
        return 'dismissed-overlay'
      }

      // 2/3) 覆盖层。closeTop 内部处理 dismissible 与 closeGuard，
      //      返回 false 时**保持现状**，绝不穿透。
      if (overlays.depth > 0) {
        const closed = await overlays.closeTop()
        if (closed) return 'dismissed-overlay'
        store.log(overlays.top()?.id ?? 'unknown', `back:${source}`, 'cancelled')
        return 'consumed-by-guard'
      }

      // 4) 页面前驱。只认 entry 链，不看 history.length。
      //
      //    **顺序很重要**：这里只「问」路由，不动 store。store 的 cursor
      //    由 RouterHistoryAdapter 在导航**成功后**统一改写（单一写入者）。
      //    如果先 pop store 再调路由，一旦路由守卫拒绝，两者就脱节 ——
      //    页面没退但上下文以为退了，下一次返回会跳过一层。
      if (!store.atRoot) {
        const cur = store.current()
        if (!cur) {
          // entry 链与 cursor 不一致：如实报不支持，不要猜着退
          return 'unsupported'
        }
        if (!port) return 'unsupported'
        markPendingPop(cur.id)
        try {
          const ok = await port.pop()
          if (!ok) {
            // 守卫拒绝：撤回 pending 标记，store 完全不动
            clearPendingPop()
            store.log(store.current()?.id ?? 'unknown', `back:${source}`, 'cancelled')
            return 'consumed-by-guard'
          }
          return 'navigated'
        } catch (err) {
          clearPendingPop()
          throw err
        }
      }

      // 5/6) 根页
      if (!port) return 'unsupported'
      if (!port.isAtRoot()) {
        await port.replace('/')
        return 'navigated'
      }
      port.deliverToSystem()
      return 'delivered-to-system'
    } finally {
      // 无论成功、拒绝还是抛错，都要解锁，否则一次异常就永久卡死返回。
      this.inFlight = false
    }
  }

  /** 供测试：复位单飞标记与 consumer 列表。 */
  _reset(): void {
    this.inFlight = false
    this.consumers = []
    clearPendingPop()
  }

  get isBusy(): boolean {
    return this.inFlight
  }
}

/**
 * 待确认的 pop 目标（entry id）。
 *
 * 存在模块级而不是 BackDispatcher 实例上，是为了让 RouterHistoryAdapter
 * （在 `index.ts` 里装配）能在 afterEach 中读到，而不必把适配器依赖注入
 * 到每次 `request()` 调用里。
 */
let pendingPopEntryId: string | null = null

function markPendingPop(entryId: string): void {
  pendingPopEntryId = entryId
}

/** 撤回待确认的 pop（导航失败 / 守卫拒绝 / 复位时）。 */
export function clearPendingPop(): void {
  pendingPopEntryId = null
}

/** 供 RouterHistoryAdapter 消费：取走并清空待确认的 pop 目标。 */
export function consumePendingPop(): string | null {
  const id = pendingPopEntryId
  pendingPopEntryId = null
  return id
}

/** 只读窥视，供诊断与 spec 使用。 */
export function peekPendingPop(): string | null {
  return pendingPopEntryId
}

export const back = new BackDispatcher()
