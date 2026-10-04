// useHyperPage.ts — 页面接入 Hyper 运行时（UI 规范 17 §4-R6）。
// useHyperPage(opts): { setTitle, snapshotView, release }

import { onMounted, onUnmounted, getCurrentInstance, ref } from 'vue'
import { navigationContextSingleton } from './navSingleton'
import { scrollHost } from '../runtime/scrollHost'
import { nextSessionEpoch, currentSessionEpoch } from '../runtime/epochs'

export interface UseHyperPageOptions {
  /** 路由静态标题（titleKey 已解析）。 */
  routeTitle: string
  /** 页面级筛选/Tab 状态提供者（快照用）。 */
  activeTab?: () => string | undefined
  filter?: () => string | undefined
}

export interface HyperPageHandle {
  setTitle(title: string): void
  snapshotView(): void
  /** 显式提前释放（onUnmounted 自动调用）。 */
  release(): void
}

export function useHyperPage(opts: UseHyperPageOptions): HyperPageHandle {
  const nav = navigationContextSingleton()
  const title = ref(opts.routeTitle)
  let entryId = ''

  if (getCurrentInstance()) {
    onMounted(() => {
      const entry = nav.push(normalizePath(), queryOf())
      entryId = entry.entryId
      nav.setTitle(entryId, opts.routeTitle, entry.renderEpoch)
      title.value = opts.routeTitle
    })
    onUnmounted(() => {
      release()
    })
  }

  function normalizePath(): string {
    return window.location.pathname.replace(/^\/m/, '') || '/'
  }

  function queryOf(): Record<string, string> {
    const out: Record<string, string> = {}
    new URLSearchParams(window.location.search).forEach((v, k) => {
      out[k] = v
    })
    return out
  }

  function setTitle(next: string): void {
    title.value = next
    if (entryId) {
      const entry = nav.list().find((e) => e.entryId === entryId)
      nav.setTitle(entryId, next, entry?.renderEpoch ?? 0)
    }
  }

  function snapshotView(): void {
    if (!entryId) return
    scrollHost().snapshot(
      nav,
      entryId,
      opts.activeTab?.(),
      opts.filter?.(),
    )
  }

  function release(): void {
    if (entryId) {
      // 离开页面先落快照（滚动/Tab/筛选），返回时锚行优先恢复。
      snapshotView()
      entryId = ''
    }
  }

  return { setTitle, snapshotView, release }
}

/** 会话代次透出：登录/登出时视图调用。 */
export { nextSessionEpoch, currentSessionEpoch }
