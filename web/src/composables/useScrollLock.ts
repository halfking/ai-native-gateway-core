/**
 * useScrollLock — 引用计数式 body 滚动锁定（方案 §4.5.7）。
 *
 * 多个弹层（modal/drawer）嵌套打开时共享同一计数，最后一个关闭才真正恢复
 * body 滚动，避免嵌套弹层关闭其一就把锁解掉。同时以 data-scroll-locked
 * 属性暴露当前锁定态，供测试与样式钩子使用。
 */

let lockCount = 0
let prevOverflow = ''

export function lockBodyScroll(): void {
  if (typeof document === 'undefined') return
  if (lockCount === 0) {
    prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    document.body.setAttribute('data-scroll-locked', 'true')
  }
  lockCount++
}

export function unlockBodyScroll(): void {
  if (typeof document === 'undefined') return
  lockCount = Math.max(0, lockCount - 1)
  if (lockCount === 0) {
    document.body.style.overflow = prevOverflow
    document.body.removeAttribute('data-scroll-locked')
  }
}

/** 仅供测试：强制复位计数与 body 状态，隔离用例间的单例状态。 */
export function _resetScrollLockForTests(): void {
  lockCount = 0
  if (typeof document !== 'undefined') {
    document.body.style.overflow = ''
    document.body.removeAttribute('data-scroll-locked')
  }
}
