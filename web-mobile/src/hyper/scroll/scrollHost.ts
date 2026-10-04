import type { ScrollPosition } from '../types'

// ScrollHost 注册表 — UI规范 07 §1 / 06 §6。
// 每个页面/弹层/专注区登记滚动宿主；一个轴只允许一个主宿主。
// 恢复顺序（06 §6）：挂载后定位锚行 → clamp → 应用；最长等 2s，
// 超期保留可取消意图；用户开始滚动即取消。
// 锚行约定：容器内带 data-hyper-anchor 属性的子元素（首行/行卡片）。

export const SCROLL_RESTORE_BUDGET_MS = 2000

export interface ScrollHostRegistration {
  /** 稳定 scroll ID：页面主体 / 当前 Tab / 弹层 body / 表格横轴。 */
  id: string
  axis: 'x' | 'y'
  getEl(): HTMLElement | null
}

interface SnapshotEntry extends ScrollPosition {}

export function snapshotElement(el: HTMLElement): SnapshotEntry {
  const rect = el.getBoundingClientRect()
  let anchorId: string | undefined
  let anchorOffset: number | undefined
  const anchors = el.querySelectorAll<HTMLElement>('[data-hyper-anchor]')
  for (const anchor of anchors) {
    // 首个进入视口顶缘下方的锚行 = 恢复目标
    const top = anchor.getBoundingClientRect().top - rect.top
    if (top >= -anchor.offsetHeight) {
      anchorId = anchor.dataset.hyperAnchor || undefined
      anchorOffset = el.scrollTop - top
      break
    }
  }
  return { x: el.scrollLeft, y: el.scrollTop, anchorId, anchorOffset }
}

export class ScrollHostRegistry {
  private hosts = new Map<string, ScrollHostRegistration>()

  register(host: ScrollHostRegistration): () => void {
    this.hosts.set(host.id, host)
    return () => {
      // 仅当仍是同一注册时移除（防新旧页面同 ID 竞态）
      if (this.hosts.get(host.id) === host) {
        this.hosts.delete(host.id)
      }
    }
  }

  snapshot(): Record<string, ScrollPosition> {
    const out: Record<string, ScrollPosition> = {}
    for (const [id, host] of this.hosts) {
      const el = host.getEl()
      if (el) out[id] = snapshotElement(el)
    }
    return out
  }

  /**
   * 挂载后恢复（06 §6）：内容高度不足时按 rAF 轮询等待（数据到达后列表
   * 变高），预算 2s；用户滚动即取消。锚行命中用 offset 校正，否则回落数值。
   */
  restore(snapshot: Record<string, ScrollPosition>, opts?: { timeoutMs?: number }): Promise<void> {
    const timeoutMs = opts?.timeoutMs ?? SCROLL_RESTORE_BUDGET_MS
    return new Promise<void>((resolve) => {
      let cancelled = false
      const cancellers: Array<() => void> = []

      const finish = () => {
        for (const cancel of cancellers) cancel()
        resolve()
      }

      const attempt = (): boolean => {
        let pending = false
        for (const [id, pos] of Object.entries(snapshot)) {
          const host = this.hosts.get(id)
          const el = host?.getEl()
          if (!el) continue
          if (pos.anchorId) {
            const anchor = el.querySelector<HTMLElement>(`[data-hyper-anchor="${cssEscape(pos.anchorId)}"]`)
            if (anchor) {
              const target = anchor.offsetTop - (pos.anchorOffset ?? 0)
              el.scrollTop = clamp(target, 0, el.scrollHeight - el.clientHeight)
              el.scrollLeft = clamp(pos.x, 0, el.scrollWidth - el.clientWidth)
              continue
            }
          }
          // 无锚或锚未渲染：数值可达则直接应用，否则等待内容
          const maxY = el.scrollHeight - el.clientHeight
          if (pos.y <= maxY) {
            el.scrollTop = clamp(pos.y, 0, maxY)
            el.scrollLeft = clamp(pos.x, 0, el.scrollWidth - el.clientWidth)
          } else {
            pending = true
          }
        }
        return pending
      }

      if (!attempt()) {
        finish()
        return
      }

      const deadline = Date.now() + timeoutMs
      const tick = () => {
        if (cancelled) return
        if (!attempt() || Date.now() > deadline) {
          finish()
          return
        }
        requestAnimationFrame(tick)
      }
      requestAnimationFrame(tick)

      // 用户开始滚动即取消（06 §6）
      const onUserScroll = () => {
        cancelled = true
        finish()
      }
      for (const host of this.hosts.values()) {
        const el = host.getEl()
        if (el) {
          el.addEventListener('scroll', onUserScroll, { once: true, passive: true })
          cancellers.push(() => el.removeEventListener('scroll', onUserScroll))
        }
      }
      const timer = setTimeout(() => {
        cancelled = true
        finish()
      }, timeoutMs)
      cancellers.push(() => clearTimeout(timer))
    })
  }
}

function clamp(v: number, min: number, max: number): number {
  return Math.min(Math.max(v, min), Math.max(min, max))
}

function cssEscape(value: string): string {
  if (typeof CSS !== 'undefined' && typeof CSS.escape === 'function') return CSS.escape(value)
  return value.replace(/["\\\]]/g, '\\$&')
}
