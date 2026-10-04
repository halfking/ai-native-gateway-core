/**
 * useOverlayStack — 覆盖层栈的**唯一入口**（原 2026-09-13 audit R20 的数字栈，
 * 2026-10-04 改为 Hyper OverlayRegistry 的门面）。
 *
 * ## 为什么要改
 *
 * 原实现是模块级 `number[]`，只为「ESC 只关最上层」服务，没有标题、脏标记、
 * 关闭守卫。Hyper 引入后另有一套带完整元数据的 `OverlayRegistry`（给
 * `BackDispatcher` 仲裁 Android 系统返回用）。**两个独立栈会打架**：
 * ESC 依据 A 栈认定顶层是抽屉、Back 依据 B 栈认定顶层是弹窗，
 * 于是同一次操作关掉不同的层，或者一个都关不掉。
 *
 * 所以本文件不再自持状态，全部委托 `lib/shell/hyper/overlay.ts` 的单例
 * `overlays`。对外 API 保持不变，`AppModal` / `AppDrawer` 无需改动即可继续工作。
 *
 * ## 组件怎么把自己接进返回仲裁
 *
 * `pushOverlayLayer(id)` 登记的条目默认 `dismissible: true` 但 `close` 是空动作。
 * 组件必须调用 {@link setOverlayLayerClose} 登记**真实的关闭动作**，
 * 否则 Android 系统返回会「关了个寂寞」（Back 消费了但层还在）。
 * AppModal / AppDrawer 已在 `pushOverlayLayer` 处调用。
 *
 * ## 脏表单
 *
 * `dirty` 默认 false。本仓现有弹窗没有统一的脏状态追踪，强行推断会误判。
 * 需要守卫的组件用 `setOverlayLayerGuard(id, guard)` 显式提供；
 * 守卫返回 false 时 Back 消费返回并保持现状（不穿透到背景路由）。
 */
import { overlays } from '../lib/shell/hyper/overlay'
import type { OverlayRegistration } from '../lib/shell/hyper/types'

let seq = 0

/** 分配一个稳定的层 id。 */
export function nextOverlayLayerId(): string {
  return `legacy-layer-${++seq}`
}

/** 登记真实关闭动作。必须在 pushOverlayLayer 之后、组件就绪时调用。 */
export function setOverlayLayerClose(id: string, close: () => void): void {
  overlays.upgrade(id, { close })
}

/** 登记脏表单守卫。返回 false 表示拒绝关闭。 */
export function setOverlayLayerGuard(
  id: string,
  guard: () => boolean | Promise<boolean>,
  dirty = true,
): void {
  overlays.upgrade(id, { closeGuard: guard, dirty })
}

/** 登记标题，供无标题弹层的继承链使用。 */
export function setOverlayLayerTitle(id: string, title: string): void {
  overlays.upgrade(id, { title })
}

function baseRegistration(id: string, presentation: OverlayRegistration['presentation']) {
  return {
    id,
    presentation,
    // 默认可关闭。组件若不支持 ESC 关闭（如门控确认框），应显式传
    // escClose=false 并调用 setOverlayLayerGuard 表达「必须显式选择」。
    dismissible: true,
    dirty: false,
    // 占位关闭动作。真实动作由 setOverlayLayerClose 覆盖。
    // 这里刻意**不**复用 popOverlayLayer —— 那只会把层从栈里摘掉而
    // 组件状态不变，出现「返回说关好了、界面还在」的假成功。
    close: () => {},
  }
}

/** 打开一层。 */
export function pushOverlayLayer(id: string, presentation: OverlayRegistration['presentation'] = 'modal'): void {
  overlays.register(baseRegistration(id, presentation))
}

/** 关闭一层。幂等。 */
export function popOverlayLayer(id: string): void {
  overlays.unregister(id)
}

/** 是否为最上层。只有最上层响应 ESC / 返回。 */
export function isTopmostOverlayLayer(id: string): boolean {
  return overlays.top()?.id === id
}

/** 观测栈深度（测试用）。 */
export function overlayStackDepth(): number {
  return overlays.depth
}

/** 测试复位。 */
export function _resetOverlayStackForTests(): void {
  overlays._reset()
  seq = 0
}
