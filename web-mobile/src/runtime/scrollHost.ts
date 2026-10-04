// scrollHost.ts — 统一滚动宿主注册表（UI 规范 06 §6 + 07 §1）。
// 每页一个滚动宿主（.m-content）；离开快照、挂载恢复（锚行优先、clamp、
// 2s 预算——R8：恢复等待期内维持骨架态）。快照存进 NavigationContext.view。

import type { NavigationContext, ViewSnapshot } from './navigationContext'

export interface ScrollRestoreResult {
  restored: boolean
  scrollTop: number
  /** true=命中锚行（元素仍在 DOM）精确恢复；false=只有数值恢复。 */
  anchorHit: boolean
}

export class ScrollHost {
  private el: HTMLElement | null = null
  private anchor: string | null = null

  attach(el: HTMLElement): void {
    this.el = el
  }

  detach(): void {
    this.el = null
  }

  /** 设置锚行（如列表点开详情前的行 id），恢复时优先滚到锚元素。 */
  setAnchor(rowId: string | null): void {
    this.anchor = rowId
  }

  snapshot(nav: NavigationContext, entryId: string, activeTab?: string, filter?: string): ViewSnapshot | null {
    if (!this.el) return null
    const snap: ViewSnapshot = {
      scrollTop: this.el.scrollTop,
      activeTab,
      filter,
    }
    nav.snapshotView(entryId, snap)
    return snap
  }

  /**
   * 恢复滚动：锚行优先（clamp 到视口顶），否则恢复 scrollTop 数值。
   * 返回结果供视图决定骨架让位时机。
   */
  restore(nav: NavigationContext, entryId: string): ScrollRestoreResult {
    const snap = nav.restoreView(entryId)
    if (!this.el) {
      return { restored: false, scrollTop: 0, anchorHit: false }
    }
    if (!snap) {
      // R5：快照被 LRU 驱逐 → 调用方重新走 initialLoading，不静默复用过期位置。
      return { restored: false, scrollTop: 0, anchorHit: false }
    }
    if (this.anchor) {
      const anchorEl = this.el.querySelector(`[data-row-id="${cssEscape(this.anchor)}"]`)
      if (anchorEl) {
        anchorEl.scrollIntoView({ block: 'start' })
        return { restored: true, scrollTop: this.el.scrollTop, anchorHit: true }
      }
    }
    this.el.scrollTop = clamp(snap.scrollTop, 0, this.el.scrollHeight - this.el.clientHeight)
    return { restored: true, scrollTop: this.el.scrollTop, anchorHit: false }
  }
}

function clamp(v: number, min: number, max: number): number {
  return Math.min(Math.max(v, min), Math.max(min, max))
}

function cssEscape(s: string): string {
  if (typeof CSS !== 'undefined' && CSS.escape) return CSS.escape(s)
  return s.replace(/[^a-zA-Z0-9_-]/g, '\\$&')
}

let singleton: ScrollHost | null = null

export function scrollHost(): ScrollHost {
  if (!singleton) singleton = new ScrollHost()
  return singleton
}
