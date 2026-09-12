/**
 * useFocusTrap — 弹层焦点圈闭（方案 §4.5.7/§4.5.8）。
 *
 * activate()：记录当前焦点并把焦点移入容器内第一个可聚焦元素；
 * trapTab(e)：Tab/Shift+Tab 在容器内循环，焦点不逃逸到遮罩后方；
 * deactivate()：关闭时把焦点返还给打开前的元素。
 */
import type { Ref } from 'vue'

const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'textarea:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

export function useFocusTrap(container: Ref<HTMLElement | null>) {
  let previous: HTMLElement | null = null

  function focusableElements(): HTMLElement[] {
    const root = container.value
    if (!root) return []
    return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
  }

  function activate(): void {
    if (typeof document === 'undefined') return
    previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    // 组件以 v-if 渲染，调用方需在 DOM 更新后（nextTick/watch flush 'post'）调用
    const first = focusableElements()[0]
    if (first) first.focus()
  }

  function deactivate(): void {
    if (typeof document === 'undefined') return
    if (previous && previous.isConnected) previous.focus()
    previous = null
  }

  function trapTab(e: KeyboardEvent): void {
    if (e.key !== 'Tab') return
    const items = focusableElements()
    if (items.length === 0) {
      e.preventDefault()
      return
    }
    const first = items[0]
    const last = items[items.length - 1]
    const active = document.activeElement
    const inside = container.value?.contains(active) ?? false
    if (e.shiftKey) {
      if (active === first || !inside) {
        e.preventDefault()
        last.focus()
      }
    } else if (active === last || !inside) {
      e.preventDefault()
      first.focus()
    }
  }

  return { activate, deactivate, trapTab }
}
