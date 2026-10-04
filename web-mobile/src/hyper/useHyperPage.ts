import { onBeforeUnmount, onMounted } from 'vue'
import { Hyper } from './singleton'
import { nextHyperId } from './runtime'

// useHyperPage — UI规范 06 §8 接入 API；返回值契约见 17 §4-R6。
// 页面在 setup 期登记标题真源与滚动宿主；卸载自动注销。

export interface UseHyperPageOptions {
  /** 响应式标题 getter（与页面 PageHeader 同一数据源）。 */
  title?: () => string | undefined
  titleKey?: string
  /** 滚动宿主（主内容 #main-content 等）；首个为主宿主。 */
  scrollRoots?: () => Array<HTMLElement | null | undefined>
}

export interface UseHyperPage {
  setTitle(title: string): void
  snapshotView(): void
  release(): void
}

export function useHyperPage(opts: UseHyperPageOptions): UseHyperPage {
  const id = nextHyperId('page')
  let overrideTitle: string | undefined
  const unsubscribers: Array<() => void> = []

  const registration = {
    id,
    title: () => overrideTitle ?? opts.title?.(),
    titleKey: opts.titleKey,
  }

  onMounted(() => {
    Hyper.registerPage(registration)
    const roots = opts.scrollRoots?.() ?? []
    roots.forEach((el, i) => {
      if (!el) return
      unsubscribers.push(
        Hyper.scroll.register({
          id: i === 0 ? 'main' : `aux-${i}`,
          axis: 'y',
          getEl: () => el,
        }),
      )
    })
    // 06 §6：挂载后恢复（2s 预算，用户滚动即取消）
    void Hyper.consumePendingRestore()
  })

  onBeforeUnmount(() => {
    Hyper.saveCurrentView()
    for (const fn of unsubscribers) fn()
    Hyper.unregisterPage(id)
  })

  return {
    setTitle(title: string) {
      overrideTitle = title
      Hyper.syncTitleToEntry()
    },
    snapshotView() {
      Hyper.saveCurrentView()
    },
    release() {
      for (const fn of unsubscribers) fn()
      Hyper.unregisterPage(id)
    },
  }
}
