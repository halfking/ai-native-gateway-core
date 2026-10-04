/**
 * dragDismiss.test.ts — 规范 12 §1 / §2 的逐条契约断言。
 *
 * 写法纪律：每条用例的 `it` 标题里**引用规范原文的措辞**，
 * 让人能一眼看出「这条在测哪半句话」，而不是「测一个函数」。
 * 时钟是注入的固定值，不用 `Date.now()` ⇒ 结果完全确定，无 flaky。
 */
import { describe, expect, it, vi } from 'vitest'
import { DragDismiss, type DragReject, type DragSnapshot } from './dragDismiss'

/** 面板 400px 高、400px 宽的典型 bottom sheet / 右侧面板。 */
const PANEL = 400

function makeBottomSheet(over: Partial<ConstructorParameters<typeof DragDismiss>[0]> = {}) {
  const onProgress = vi.fn<(o: number) => void>()
  const onSettle = vi.fn<(o: 'close' | 'cancel', s: DragSnapshot) => void>()
  const onReject = vi.fn<(r: DragReject) => void>()
  const d = new DragDismiss(
    { axis: 'y', closeDirection: 1, panelSize: PANEL, ...over },
    { onProgress, onSettle, onReject },
  )
  return { d, onProgress, onSettle, onReject }
}

/** 拖到 `dy`，每帧 16ms（60fps）。返回松手时刻。 */
function drag(d: DragDismiss, dy: number, { from = 0, dt = 16, t0 = 1000 } = {}) {
  d.start(200, from, t0, 400)
  let t = t0
  const step = dy >= 0 ? 1 : -1
  for (let moved = step; step > 0 ? moved <= dy : moved >= dy; moved += step * 20) {
    t += dt
    d.move(200, from + moved, t)
  }
  return t + dt
}

describe('规范 12 §1：方向锁与系统手势让出', () => {
  it('主轴斜率不足时判为「不是拖拽」，交还给内容滚动（斜着划不该既滚动又关闭）', () => {
    const { d, onSettle, onReject } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    // 水平远多于竖直：这是滚动意图，不是关闭意图
    const consumed = d.move(280, 4, 16)
    expect(consumed, '横移为主的起手不得被拖拽消费').toBe(false)
    expect(onReject).toHaveBeenCalledWith('axis-mismatch')
    expect(d.end()).toBe('cancel')
    expect(onSettle).not.toHaveBeenCalled()
  })

  it('主轴斜率达标后锁定方向，后续反向移动不改变轴向（不中途改判）', () => {
    const { d, onProgress } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    d.move(202, 40, 16) // 竖直为主 ⇒ 锁定 y
    expect(d.snapshot.axis).toBe('y')
    d.move(260, 60, 32) // 之后大量横移
    expect(d.snapshot.axis, '轴向一旦锁定不得改判').toBe('y')
    // offset 只跟主轴，横向移动不污染它
    expect(d.snapshot.offset).toBe(60)
    expect(onProgress).toHaveBeenLastCalledWith(60)
  })

  it('系统手势让出带内的起手直接被拒（Android 边缘返回不可被网页吞掉）', () => {
    const { d, onReject } = makeBottomSheet({ axis: 'x', closeDirection: 1 })
    expect(d.start(5, 100, 0, 400)).toBe(false)
    expect(onReject).toHaveBeenCalledWith('system-gesture-zone')
    // 右边缘同理
    d.start(5, 100, 0, 400)
    expect(d.snapshot.rejected).toBe('system-gesture-zone')
  })

  it('让出带外的中部起手正常接管（18px 之外的区域仍要能拖）', () => {
    const { d } = makeBottomSheet({ axis: 'x', closeDirection: 1 })
    expect(d.start(200, 100, 0, 400)).toBe(true)
  })

  it('轴向为 y 时不做边缘让出（bottom sheet 不该因为贴到屏幕侧边就失灵）', () => {
    const { d } = makeBottomSheet()
    expect(d.start(2, 0, 0, 400)).toBe(true)
  })

  it('调用方禁用时起手即被拒，且不得有任何位移被消费', () => {
    const { d, onProgress } = makeBottomSheet({ disabled: true })
    expect(d.start(200, 0, 0, 400)).toBe(false)
    expect(d.move(200, 100, 16)).toBe(false)
    expect(onProgress).not.toHaveBeenCalled()
  })
})

describe('规范 12 §2：跟手、松手才决定、可取消、中断复位', () => {
  it('跟手：手指移动多少就报多少，不缓动不吸附', () => {
    const { d, onProgress } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    d.move(200, 13, 16)
    d.move(200, 27, 32)
    d.move(200, 28, 48)
    expect(onProgress.mock.calls.map((c) => c[0])).toEqual([13, 27, 28])
    expect(d.snapshot.offset).toBe(28)
  })

  it('松手前不锁死：越过阈值也只报 pastThreshold，不提前 settle', () => {
    const { d, onSettle } = makeBottomSheet()
    const t = drag(d, 200) // 200/400 = 50% > 30% 阈值
    expect(d.snapshot.pastThreshold).toBe(true)
    expect(onSettle, '松手前不得调用 onSettle —— 这是可取消的前提').not.toHaveBeenCalled()
    d.end()
    expect(onSettle).toHaveBeenCalledTimes(1)
    expect(t).toBeGreaterThan(0)
  })

  it('位置过阈值 ⇒ 关闭', () => {
    const { d } = makeBottomSheet()
    drag(d, 160) // 160/400 = 40% > 30%
    expect(d.end()).toBe('close')
  })

  it('位置不够但甩得快 ⇒ 仍然关闭（速度 + 位置双条件，规范明写「不要用固定等待」）', () => {
    const { d } = makeBottomSheet()
    // 90px 在 3 帧内走完 ⇒ 约 1875px/s，远超 800 阈值
    d.start(200, 0, 0, 400)
    d.move(200, 30, 16)
    d.move(200, 60, 32)
    d.move(200, 90, 48)
    expect(d.snapshot.pastThreshold, '位置未过阈值').toBe(false)
    expect(d.end()).toBe('close')
  })

  it('位置不够、速度也慢 ⇒ 回弹取消', () => {
    const { d, onSettle } = makeBottomSheet()
    // 每帧 1px / 16ms = 62.5px/s，慢速小位移
    d.start(200, 0, 0, 400)
    for (let i = 1; i <= 20; i++) d.move(200, i, i * 200)
    expect(d.end()).toBe('cancel')
    expect(onSettle).toHaveBeenCalledWith('cancel', expect.objectContaining({ offset: 20 }))
  })

  it('反向拖回原位 ⇒ 取消（可取消；反方向速度不得触发关闭）', () => {
    const { d } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    d.move(200, 200, 16) // 已过阈值
    d.move(200, 40, 800) // 快速往回拖
    expect(d.end()).toBe('cancel')
  })

  it('「反方向很快」不构成关闭条件：closeDirection 决定符号', () => {
    const { d } = makeBottomSheet({ closeDirection: 1 })
    d.start(200, 0, 0, 400)
    d.move(200, -30, 16)
    d.move(200, -90, 32) // 负向高速
    expect(d.end()).toBe('cancel')
  })

  it('中断即复位：cancel 把 offset 归零并回报中性态，不关闭也不停在半路', () => {
    const { d, onProgress, onSettle } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    d.move(200, 180, 16)
    expect(d.snapshot.offset).toBe(180)
    d.cancel()
    expect(d.snapshot.offset, '中断后不得停在半路').toBe(0)
    expect(d.snapshot.phase).toBe('idle')
    expect(onProgress).toHaveBeenLastCalledWith(0)
    expect(onSettle, '中断不是松手，不得触发关闭决策').not.toHaveBeenCalled()
  })

  it('中断后再松手是取消（陈旧手势不得继续生效）', () => {
    const { d } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    d.move(200, 300, 16)
    d.cancel()
    expect(d.end()).toBe('cancel')
  })

  it('未达到定轴门槛就松手 ⇒ 取消（点一下不该被当成拖拽关闭）', () => {
    const { d, onSettle } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    d.move(201, 0, 8) // 1px，远不到 MIN_TRAVEL 与斜率门槛
    expect(d.end()).toBe('cancel')
    expect(onSettle).not.toHaveBeenCalled()
  })

  it('速度取窗口平均而非末帧瞬时值：末帧 2px 抖动不得把「向前甩」读成「向后」', () => {
    // 这是 MUTATION M7 逼出来的一条。匀速下 2 点与 3 点**完全等价**，
    // 所以「三点窗口更稳」这句话在匀速样本上是**测不出来的**——必须造加减速。
    // 末帧回退 2px 是真实抖动（手指抬起时的坐标回弹）：
    //   3 点：(28 - 0) / 0.032 = 875 px/s，向前 ✔
    //   2 点：(28 - 30) / 0.016 = -125 px/s，**反向** ✘
    // 反向读数会让「快速前甩、末尾轻微回正」这种最常见的甩动**关不掉**。
    const { d } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    d.move(200, 30, 16)
    d.move(200, 28, 32)
    expect(d.snapshot.velocity).toBeCloseTo(875, 0)
    expect(d.snapshot.velocity, '净位移是向下的，窗口平均不得为负').toBeGreaterThan(0)
    expect(d.end(), '快速前甩（875px/s > 800）仍应关闭').toBe('close')
  })

  it('onSettle 的快照保留松手瞬间的 offset/velocity（供退场动画使用）', () => {
    const { d, onSettle } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    d.move(200, 30, 16)
    d.move(200, 60, 32)
    d.move(200, 90, 48)
    d.end()
    const [, snap] = onSettle.mock.calls[0]
    expect(snap.offset).toBe(90)
    expect(snap.velocity).toBeGreaterThan(800)
    expect(snap.axis).toBe('y')
  })

  it('面板尺寸可在断点切换后就地更新，阈值随之改变', () => {
    const { d } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    d.move(200, 100, 16) // 100/400 = 25% < 30%
    d.setPanelSize(200) // 换成矮面板：100/200 = 50% > 30%
    expect(d.snapshot.pastThreshold).toBe(true)
    expect(d.end()).toBe('close')
  })

  it('disabled 置真会把进行中的手势复位（切后台/加载态的通用收口）', () => {
    const { d, onProgress } = makeBottomSheet()
    d.start(200, 0, 0, 400)
    d.move(200, 100, 16)
    d.setDisabled(true)
    expect(d.snapshot.offset).toBe(0)
    expect(onProgress).toHaveBeenLastCalledWith(0)
  })
})
