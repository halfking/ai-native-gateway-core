// 引用计数配对滚动锁 — UI规范 06 §5 / 07 §9。
// 锁真实背景滚动宿主（不只锁 body）；同一元素多次加锁只置一次
// overflow:hidden，解锁次数配对后才还原，防止嵌套弹层互相踩。

const locks = new Map<HTMLElement, number>()
const PREV_OVERFLOW = '__hyperPrevOverflow'

export function lockScroll(el: HTMLElement): () => void {
  const n = locks.get(el) ?? 0
  if (n === 0) {
    el.dataset[PREV_OVERFLOW] = el.style.overflow
    el.style.overflow = 'hidden'
  }
  locks.set(el, n + 1)
  let released = false
  return () => {
    if (released) return
    released = true
    const cur = locks.get(el) ?? 1
    if (cur <= 1) {
      el.style.overflow = el.dataset[PREV_OVERFLOW] ?? ''
      delete el.dataset[PREV_OVERFLOW]
      locks.delete(el)
    } else {
      locks.set(el, cur - 1)
    }
  }
}
