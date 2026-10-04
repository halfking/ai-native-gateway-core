// focusWorkspace.ts — 专注工作区（UI 规范 07 §8–§9 + 17 §3）。
// 单实例 Teleport portal、背景 inert（降级焦点门）、滚动锁引用计数、
// 进入/退出状态机 normal→entering→focused→exiting→normal、history 标记。
// 「重要表格点击全页查看与编辑」的运行时载体。

import { backDispatcher, type OverlayHandle } from './backDispatcher'

export type FocusState = 'normal' | 'entering' | 'focused' | 'exiting'

export interface FocusOpenOptions {
  title: string
  /** 退出时恢复触发按钮焦点。 */
  triggerEl?: HTMLElement | null
}

export interface FocusHandle {
  id: string
  title: string
  state: FocusState
}

type Listener = () => void

class FocusWorkspaceRuntime {
  private handle: FocusHandle | null = null
  private listeners = new Set<Listener>()
  private triggerEl: HTMLElement | null = null
  private unregisterBack: (() => void) | null = null
  private scrollLockCount = 0
  private savedBgStyle: string | null = null

  current(): FocusHandle | null {
    return this.handle
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn)
    return () => this.listeners.delete(fn)
  }

  private emit(): void {
    for (const fn of this.listeners) fn()
  }

  open(opts: FocusOpenOptions): FocusHandle {
    if (this.handle) this.closeInternal(true)
    const handle: FocusHandle = {
      id: `focus-${Date.now().toString(36)}`,
      title: opts.title,
      state: 'entering',
    }
    this.handle = handle
    this.triggerEl = opts.triggerEl ?? null
    this.lockBackground()
    const bd = backDispatcher()
    const overlay: OverlayHandle = {
      id: handle.id,
      beforeClose: () => true,
      onClose: () => this.close(),
      title: opts.title,
    }
    this.unregisterBack = bd.register(overlay)
    handle.state = 'focused'
    this.emit()
    return handle
  }

  close(): void {
    if (!this.handle) return
    this.closeInternal(false)
  }

  private closeInternal(silent: boolean): void {
    const h = this.handle
    if (!h) return
    h.state = 'exiting'
    if (!silent) this.emit()
    if (this.unregisterBack) {
      this.unregisterBack()
      this.unregisterBack = null
      // 层自己消化 history 标记位（非返回触发关闭时）。
      backDispatcher().consumeHistoryMark()
    }
    this.unlockBackground()
    const trigger = this.triggerEl
    this.handle = null
    this.triggerEl = null
    this.emit()
    // 退出恢复触发按钮焦点（07 §8）。
    if (trigger && trigger.isConnected) trigger.focus({ preventScroll: true })
  }

  /** 滚动锁引用计数：子确认框共享同一把锁，准确恢复原 inline style。 */
  retainScrollLock(el: HTMLElement): void {
    if (this.scrollLockCount === 0) {
      this.savedBgStyle = el.style.overflow
      el.style.overflow = 'hidden'
    }
    this.scrollLockCount += 1
  }

  releaseScrollLock(el: HTMLElement): void {
    this.scrollLockCount = Math.max(0, this.scrollLockCount - 1)
    if (this.scrollLockCount === 0) {
      el.style.overflow = this.savedBgStyle ?? ''
      this.savedBgStyle = null
    }
  }

  private lockBackground(): void {
    const app = document.getElementById('app')
    if (!app) return
    // inert 优先；旧 WebView 缺 inert 时 DOM 属性设置静默失败，交互遮罩
    // （FocusWorkspace.vue 的 pointer-events:none 背景层）承担降级。
    (app as HTMLElement & { inert?: boolean }).inert = true
  }

  private unlockBackground(): void {
    const app = document.getElementById('app')
    if (!app) return
    ;(app as HTMLElement & { inert?: boolean }).inert = false
  }
}

let singleton: FocusWorkspaceRuntime | null = null

export function focusWorkspace(): FocusWorkspaceRuntime {
  if (!singleton) singleton = new FocusWorkspaceRuntime()
  return singleton
}
