// useHyperOverlay.ts — 弹层接入 Hyper 运行时（UI 规范 17 §4-R6）。
// useHyperOverlay(opts): { setTitle, close, release }

import { onUnmounted, getCurrentInstance } from 'vue'
import { backDispatcher, type OverlayHandle } from '../runtime/backDispatcher'
import { focusWorkspace } from '../runtime/focusWorkspace'

export interface UseHyperOverlayOptions {
  /** 显式标题；null=无标题（继承下层快照，06 §2）。 */
  title: string | null
  /** 返回前确认（脏表单守卫）：true=允许关闭。 */
  beforeClose?: () => boolean
  onClose: () => void
  /** 专注层会接管背景滚动锁，普通 Sheet 不需要。 */
  lockScrollEl?: HTMLElement | null
}

export interface HyperOverlayHandle {
  setTitle(title: string): void
  close(result?: unknown): void
  release(): void
}

let overlaySeq = 0

export function useHyperOverlay(opts: UseHyperOverlayOptions): HyperOverlayHandle {
  overlaySeq += 1
  const id = `ovl-${overlaySeq}`
  const bd = backDispatcher()
  let currentTitle = opts.title
  let unregister: (() => void) | null = null

  const handle: OverlayHandle = {
    id,
    get title() {
      return currentTitle ?? undefined
    },
    beforeClose: () => (opts.beforeClose ? opts.beforeClose() : true),
    onClose: () => opts.onClose(),
  }

  unregister = bd.register(handle)
  if (opts.lockScrollEl) {
    focusWorkspace().retainScrollLock(opts.lockScrollEl)
  }

  function release(): void {
    if (unregister) {
      unregister()
      unregister = null
    }
    if (opts.lockScrollEl) {
      focusWorkspace().releaseScrollLock(opts.lockScrollEl)
    }
  }

  if (getCurrentInstance()) {
    onUnmounted(() => release())
  }

  return {
    setTitle(next: string) {
      currentTitle = next
    },
    close() {
      // 走 beforeClose 的受控关闭。
      if (handle.beforeClose()) {
        release()
        bd.consumeHistoryMark()
        opts.onClose()
      }
    },
    release,
  }
}
