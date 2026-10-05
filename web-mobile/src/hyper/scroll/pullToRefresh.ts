// PullToRefreshMachine — UI规范 07 §2 下拉刷新状态机的纯实现。
// 状态：idle → pulling → armed → refreshing → settling → idle（失败 error → settling）
// 参数（首版，待真机调校）：到顶容差 2px、方向锁定 12px、触发 64px、
// 最大视觉位移 96px、回弹 180–240ms。组件负责 pointer 归属与方向锁
// （07 §7 一个 pointer 序列一个 owner），本机只认 dy。

export type PullToRefreshState = 'idle' | 'pulling' | 'armed' | 'refreshing' | 'settling' | 'error'

export const PTR_CONFIG = {
  startTolerancePx: 2,
  lockAxisPx: 12,
  thresholdPx: 64,
  maxVisualPx: 96,
  settleMs: 200,
  errorSettleMs: 380,
  /** 超阈值后的阻尼系数。 */
  resistance: 0.4,
} as const

/** 07 §7 起始判据：主轴 ≥ 1.3× 次轴且超过 12px 才认领纵向。 */
export function isVerticalPull(dx: number, dy: number): boolean {
  if (Math.abs(dy) < PTR_CONFIG.lockAxisPx && Math.abs(dx) < PTR_CONFIG.lockAxisPx) return true // 未锁定前不抢
  return Math.abs(dx) <= Math.abs(dy) / 1.3
}

export interface PullToRefreshHooks {
  onRefresh: () => Promise<void>
  onSettled?: (failed: boolean) => void
}

export class PullToRefreshMachine {
  private _state: PullToRefreshState = 'idle'
  private singleFlight: Promise<void> | null = null

  constructor(private readonly hooks: PullToRefreshHooks) {}

  get state(): PullToRefreshState {
    return this._state
  }

  /** 只有 idle（含 settling 完成后）可开始新一次下拉；刷新中拒绝。 */
  begin(): boolean {
    if (this._state !== 'idle' || this.singleFlight) return false
    this._state = 'pulling'
    return true
  }

  /** 传入原始纵向位移（px，向下为正），返回视觉位移。 */
  move(dy: number): number {
    if (this._state !== 'pulling' && this._state !== 'armed') return 0
    if (dy <= 0) {
      this._state = 'pulling'
      return 0
    }
    const { thresholdPx, maxVisualPx, resistance } = PTR_CONFIG
    const visual = dy <= thresholdPx ? dy : thresholdPx + (dy - thresholdPx) * resistance
    this._state = dy >= thresholdPx ? 'armed' : 'pulling'
    return Math.min(visual, maxVisualPx)
  }

  /** 松手：达阈值单飞一次刷新；未达只回弹。返回是否触发刷新。 */
  release(): boolean {
    if (this._state === 'pulling') {
      this.settle(false)
      return false
    }
    if (this._state !== 'armed') return false
    this._state = 'refreshing'
    if (!this.singleFlight) {
      this.singleFlight = this.hooks
        .onRefresh()
        .then(() => {
          this.settle(false)
        })
        .catch(() => {
          this.settle(true)
        })
        .finally(() => {
          this.singleFlight = null
        })
    }
    return true
  }

  /** 手势被仲裁转移（如转向横滚）时静默归位。 */
  abandon(): void {
    if (this._state === 'pulling' || this._state === 'armed') {
      this.settle(false)
    }
  }

  get busy(): boolean {
    return this.singleFlight != null
  }

  private settle(failed: boolean): void {
    this._state = failed ? 'error' : 'settling'
    const ms = failed ? PTR_CONFIG.errorSettleMs : PTR_CONFIG.settleMs
    setTimeout(() => {
      this._state = 'idle'
      this.hooks.onSettled?.(failed)
    }, ms)
  }
}
