import { onBeforeUnmount } from 'vue'
import type { Presentation } from './types'
import { Hyper } from './singleton'
import { nextHyperId } from './runtime'

// useHyperOverlay — UI规范 06 §8 接入 API；返回值契约见 17 §4-R6。
// present() 登记 + 写同 URL history 标记（06 §5.1）；close() 走 beforeClose
// 的受控关闭；release() 仅清理注册（组件即将卸载）。

export interface UseHyperOverlayOptions {
  presentation: Exclude<Presentation, 'page'>
  title?: () => string | undefined
  beforeClose?: () => boolean | Promise<boolean>
  /** 执行实际关闭（置 modelValue=false 等）。 */
  onClose: () => void
}

export interface UseHyperOverlay {
  present(): void
  close(): void
  setTitle(title: string): void
  release(): void
}

export function useHyperOverlay(opts: UseHyperOverlayOptions): UseHyperOverlay {
  const id = nextHyperId('overlay')
  let presented = false
  let overrideTitle: string | undefined

  const handle = {
    id,
    presentation: opts.presentation,
    title: () => overrideTitle ?? opts.title?.(),
    beforeClose: opts.beforeClose,
    close: opts.onClose,
  }

  onBeforeUnmount(() => {
    // 路由离开/卸载清理（07 §9）：静默释放，不再走 beforeClose
    if (presented) {
      Hyper.dismissOverlay(id)
      presented = false
    }
  })

  return {
    present() {
      if (presented) return
      presented = true
      Hyper.presentOverlay(handle)
    },
    close() {
      if (!presented) {
        opts.onClose()
        return
      }
      presented = false
      Hyper.dismissOverlay(id)
    },
    setTitle(title: string) {
      overrideTitle = title
      Hyper.syncTitleToEntry()
    },
    release() {
      if (presented) {
        presented = false
        Hyper.dismissOverlay(id)
      }
    },
  }
}
