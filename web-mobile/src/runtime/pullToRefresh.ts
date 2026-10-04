// pullToRefresh.ts — 下拉刷新状态机（UI 规范 07 §2 + 17 §3）。
// idle→pulling→armed→refreshing→settling；64px 触发 / 12px 方向锁 /
// 96px 最大位移 / single-flight / 与追加互斥（queryRevision，R2）。
// reduced-motion 下位移取消、文案保留（12 §5）。

export type PtrState = 'idle' | 'pulling' | 'armed' | 'refreshing' | 'settling'

export const PTR_TRIGGER_PX = 64
export const PTR_DIRECTION_LOCK_PX = 12
export const PTR_MAX_PULL_PX = 96

export interface PtrConfig {
  onRefresh: () => Promise<void>
  /** 追加加载器存活时互斥（下拉刷新与追加不得并发）。 */
  isLoadingMore?: () => boolean
  reducedMotion?: () => boolean
}

export interface PtrEmit {
  state: PtrState
  /** 指示器位移（px）。reduced-motion 恒 0。 */
  pull: number
  armed: boolean
}

export class PullToRefresh {
  private state: PtrState = 'idle'
  private pull = 0
  private startScrollTop = 0
  private locked = false
  private refreshing = false

  constructor(private cfg: PtrConfig) {}

  current(): PtrEmit {
    return {
      state: this.state,
      pull: this.reducedMotion() ? 0 : this.pull,
      armed: this.state === 'armed',
    }
  }

  private reducedMotion(): boolean {
    return this.cfg.reducedMotion?.() ?? false
  }

  touchStart(scrollTop: number): void {
    if (this.refreshing) return
    this.startScrollTop = scrollTop
    this.state = scrollTop <= 0 && !this.cfg.isLoadingMore?.() ? 'pulling' : 'idle'
    this.pull = 0
    this.locked = false
  }

  touchMove(dy: number, _scrollTop: number): void {
    if (this.state === 'refreshing' || this.state === 'settling') return
    if (this.cfg.isLoadingMore?.()) return // 互斥：追加中不接受下拉
    // 方向锁：先移动 12px 判方向——上滑（dy<0）立即放弃，防误触。
    if (!this.locked) {
      if (Math.abs(dy) < PTR_DIRECTION_LOCK_PX) return
      this.locked = true
      if (dy < 0 || this.startScrollTop > 0) {
        this.state = 'idle'
        return
      }
    }
    if (this.state !== 'pulling') return
    if (dy <= 0) {
      this.pull = 0
      this.state = 'pulling'
      return
    }
    // 阻尼：触发点内 1:1 跟手；超触发点后增量减半，逼近 96px 上限。
    const base = Math.min(dy, PTR_TRIGGER_PX)
    const over = Math.max(0, dy - PTR_TRIGGER_PX)
    this.pull = Math.min(base + over / 2, PTR_MAX_PULL_PX)
    this.state = this.pull >= PTR_TRIGGER_PX ? 'armed' : 'pulling'
  }

  async touchEnd(): Promise<void> {
    if (this.state === 'armed') {
      this.state = 'refreshing'
      this.pull = PTR_TRIGGER_PX
      this.refreshing = true
      try {
        await this.cfg.onRefresh()
      } finally {
        this.refreshing = false
        this.state = 'settling'
        this.pull = 0
        this.state = 'idle'
      }
      return
    }
    if (this.state === 'pulling') {
      this.state = 'idle'
      this.pull = 0
    }
  }

  /** 程序化中止（组件卸载）。 */
  cancel(): void {
    this.state = 'idle'
    this.pull = 0
    this.refreshing = false
  }
}
