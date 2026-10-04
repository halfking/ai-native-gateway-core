/**
 * drag-dismiss.ts — 弹层拖拽关闭的**纯状态机**（规范 12 §1 / §2）。
 *
 * 存在理由：规范 12 §2 把「可取消的 Sheet 拖拽关闭」列为**先做这个，收益最高**，
 * 而 §1 是它的前置（手势争用与方向锁不先定，compact 上任何滑动交互都是赌博）。
 *
 * ## 为什么是纯函数而不是一个 Vue composable
 *
 * 规范 12 §2 的每一条契约都**只跟几何与时间有关**，跟 DOM 无关：
 * 方向锁、跟手、松手判定、可取消、中断复位、无固定延迟。
 * ⇒ 全部可以在没有浏览器、没有 jsdom 的环境里逐条断言。
 * 一旦把这些写成 `useDragDismiss(el)`，方向锁与速度判定就只能在真机上手感验证，
 * 而「手感」恰好是**最难回归**的那一类。
 *
 * ## 三条最容易被实现错的契约（都来自规范原文，不是我的补充）
 *
 * 1. **方向锁**：手势开始后锁定主轴，**不中途改判**。
 *    实现方式不是「看后续移动」，而是**起始斜率门槛**——超过 `axisRatio`
 *    才认定为主轴方向，否则判为「不是拖拽」，交还给消费者（内容滚动）。
 *    斜着划时既不滚动也不关闭，是最容易招致差评的手感缺陷。
 * 2. **松手前不锁死**：过了阈值就播动画的话，用户反悔也停不下来。
 *    所以 `onSettle` 只在 `pointerup` 调，中途一律只报 `onProgress`。
 * 3. **系统手势优先**：Android 屏幕边缘返回区不可被网页吞掉。
 *    ⇒ `edgeInset` 内的起手**直接拒绝**，不是「优先级降低」。
 *
 * ## 「中断即复位」
 *
 * `pointercancel`（来电、系统手势抢走、页面被切走）必须**立即回中性态**，
 * 既不关闭也不停在半路。见 `cancel()`。
 */

/** 拖拽轴向。`null` 表示「尚未判定主轴」。 */
export type DragAxis = 'x' | 'y' | null

export type DragPhase = 'idle' | 'undecided' | 'dragging'

/** 一次手势的完整可观测状态。 */
export interface DragSnapshot {
  phase: DragPhase
  /** 判定出的主轴；`undecided` 期间为 null。 */
  axis: DragAxis
  /** 距起手的位移（px），沿主轴；跟手时逐帧变化。 */
  offset: number
  /** 当前瞬时速度（px/s，正负随方向）。`idle` 时为 0。 */
  velocity: number
  /** 是否已越过位置阈值（只用于 UI 提示，**不用于提前决定**）。 */
  pastThreshold: boolean
  /** 拒绝原因，用于诊断与测试断言。 */
  rejected: DragReject | null
}

export type DragReject =
  /** 起手落在系统手势让出带内（规范 12 §1「网页侧滑必须让出」）。 */
  | 'system-gesture-zone'
  /** 起始斜率不在主轴上，判为内容滚动等其它意图。 */
  | 'axis-mismatch'
  /** 调用方显式禁用。 */
  | 'disabled'
  /** 起手不在可拖区域（例如只有把手/头部可拖）。 */
  | 'outside-grab-area'

export interface DragAxisConfig {
  /** 允许的主轴。 */
  axis: 'x' | 'y'
  /**
   * 关闭方向：`1` 表示沿正轴拖动即关闭
   * （bottom sheet 向下 = +y；右侧面板向右 = +x），`-1` 反之。
   */
  closeDirection: 1 | -1
  /** 面板沿主轴的尺寸（px），用于把位置阈值换算成比例。 */
  panelSize: number
  /** 位置阈值占面板尺寸的比例，默认 0.3。 */
  thresholdRatio?: number
  /** 速度阈值（px/s），超过即视为「甩出去」，默认 800。 */
  velocityThreshold?: number
  /**
   * 系统手势让出带（px），默认 18（规范给的是 16–20pt 区间）。
   * 起手 x 落在 `[0, inset)` 或 `[width - inset, width)` 即拒绝。
   * 仅对 `axis: 'x'` 的**边缘侧滑返回**语义有意义；轴向为 y 时不适用。
   */
  edgeInset?: number
  /**
   * 起始斜率门槛：主轴位移 / 副轴位移 需 ≥ 此值才认定为主轴。
   * 默认 1.2。设为 `Infinity` 表示必须**完全**沿主轴。
   */
  axisRatio?: number
  disabled?: boolean
}

export interface DragCallbacks {
  /** 跟手回调：手指移动多少就报多少，**不缓动**。 */
  onProgress?: (offset: number) => void
  /** 松手判定结果。只在 `end()` 里调一次。 */
  onSettle?: (outcome: 'close' | 'cancel', snapshot: DragSnapshot) => void
  /** 起手被拒时调一次（诊断用，不参与关闭决策）。 */
  onReject?: (reason: DragReject) => void
}

/** 速度采样窗口内的样本数。3 点足够压掉单帧抖动又不至于迟钝。 */
const VELOCITY_SAMPLES = 3
/** 小于该位移视为静止，避免除零与噪声放大。 */
const MIN_TRAVEL = 0.5

export class DragDismiss {
  private readonly cfg: Required<Omit<DragAxisConfig, 'disabled'>> & { disabled: boolean }
  private readonly cb: DragCallbacks

  private phase: DragPhase = 'idle'
  private axis: DragAxis = null
  private offset = 0
  private rejected: DragReject | null = null

  private startX = 0
  private startY = 0
  private samples: { t: number; v: number }[] = []

  constructor(cfg: DragAxisConfig, cb: DragCallbacks = {}) {
    this.cfg = {
      axis: cfg.axis,
      closeDirection: cfg.closeDirection,
      panelSize: cfg.panelSize,
      thresholdRatio: cfg.thresholdRatio ?? 0.3,
      velocityThreshold: cfg.velocityThreshold ?? 800,
      edgeInset: cfg.edgeInset ?? 18,
      axisRatio: cfg.axisRatio ?? 1.2,
      disabled: cfg.disabled ?? false,
    }
    this.cb = cb
  }

  get snapshot(): DragSnapshot {
    return {
      phase: this.phase,
      axis: this.axis,
      offset: this.offset,
      velocity: this.velocity(),
      pastThreshold: this.pastThreshold(),
      rejected: this.rejected,
    }
  }

  /** 面板尺寸可能在断点切换后变化，需能就地更新而不重建状态机。 */
  setPanelSize(px: number): void {
    this.cfg.panelSize = px
  }

  setDisabled(disabled: boolean): void {
    this.cfg.disabled = disabled
    if (disabled) this.cancel()
  }

  private reject(reason: DragReject): void {
    this.phase = 'idle'
    this.axis = null
    this.offset = 0
    this.rejected = reason
    this.samples = []
    this.cb.onReject?.(reason)
  }

  /**
   * 起手。`viewportWidth` 只在 `axis: 'x'` 时用于判断系统手势带。
   * 返回 `true` 表示手势被接受（进入 `undecided`，等第一次 move 定轴）。
   */
  start(x: number, y: number, t: number, viewportWidth = Number.POSITIVE_INFINITY): boolean {
    this.rejected = null
    if (this.cfg.disabled) {
      this.reject('disabled')
      return false
    }
    if (this.cfg.axis === 'x' && Number.isFinite(viewportWidth)) {
      const inset = this.cfg.edgeInset
      // 系统返回带必须让出：贴边的起手一律不接管。
      if (x < inset || x > viewportWidth - inset) {
        this.reject('system-gesture-zone')
        return false
      }
    }
    this.phase = 'undecided'
    this.axis = null
    this.offset = 0
    this.startX = x
    this.startY = y
    this.samples = [{ t, v: 0 }]
    return true
  }

  /**
   * 移动。首次进入时做**方向锁**判定。
   * @returns `true` 表示本次移动被手势消费（调用方应 `preventDefault`）。
   */
  move(x: number, y: number, t: number): boolean {
    if (this.phase === 'idle') return false

    const dx = x - this.startX
    const dy = y - this.startY
    const main = this.cfg.axis === 'x' ? dx : dy
    const cross = this.cfg.axis === 'x' ? dy : dx

    if (this.phase === 'undecided') {
      if (Math.abs(main) < MIN_TRAVEL && Math.abs(cross) < MIN_TRAVEL) return true
      // 方向锁：只看**这一帧**的斜率，判完就不再改。
      const ratio = Math.abs(cross) === 0 ? Infinity : Math.abs(main) / Math.abs(cross)
      if (!(ratio >= this.cfg.axisRatio)) {
        this.reject('axis-mismatch')
        return false
      }
      this.axis = this.cfg.axis
      this.phase = 'dragging'
    }

    this.offset = main
    this.samples.push({ t, v: main })
    if (this.samples.length > VELOCITY_SAMPLES) this.samples.shift()
    this.cb.onProgress?.(main)
    return true
  }

  /** 瞬时速度（px/s）。用窗口内多点而非相邻两点，压掉单帧抖动。 */
  private velocity(): number {
    if (this.phase !== 'dragging' || this.samples.length < 2) return 0
    const a = this.samples[0]
    const b = this.samples[this.samples.length - 1]
    const dt = (b.t - a.t) / 1000
    if (dt <= 0) return 0
    return (b.v - a.v) / dt
  }

  private pastThreshold(): boolean {
    if (this.phase !== 'dragging') return false
    const threshold = this.cfg.panelSize * this.cfg.thresholdRatio
    return this.offset * this.cfg.closeDirection >= threshold
  }

  /**
   * 松手判定。**位置 + 速度**双条件，任一满足即关闭；都不满足则回弹。
   * 两者都只看主轴，**不做固定等待**（规范 12 §2「无 300ms 延迟」）。
   */
  end(): 'close' | 'cancel' {
    // `undecided`（起了手但没动够定轴）与 `idle` 都视为取消：
    // 用户点了一下就松手，不该被当成一次成功的拖拽关闭。
    if (this.phase !== 'dragging') {
      this.phase = 'idle'
      this.axis = null
      this.offset = 0
      this.samples = []
      return 'cancel'
    }
    const v = this.velocity()
    const byPosition = this.pastThreshold()
    const byVelocity = v * this.cfg.closeDirection >= this.cfg.velocityThreshold
    const outcome: 'close' | 'cancel' = byPosition || byVelocity ? 'close' : 'cancel'
    const snap = this.snapshot
    this.phase = 'idle'
    this.axis = null
    this.offset = 0
    this.samples = []
    this.cb.onSettle?.(outcome, snap)
    return outcome
  }

  /**
   * 中断即复位：来电、系统手势抢走、页面切走。
   * **既不关闭也不停在半路**——`onProgress(0)` 让 UI 回到中性态。
   */
  cancel(): void {
    const wasDragging = this.phase === 'dragging'
    this.phase = 'idle'
    this.axis = null
    const had = this.offset
    this.offset = 0
    this.samples = []
    if (wasDragging && had !== 0) this.cb.onProgress?.(0)
  }
}
