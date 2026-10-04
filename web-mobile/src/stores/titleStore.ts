// titleStore.ts — 顶栏标题的响应式来源（registered/overlay/inherited 三席）。
// AppShell 消费；useHyperPage 写 registered；Sheet/确认框写 overlay/inherited。

import { reactive } from 'vue'

interface TitleStore {
  registered: string | null
  overlay: string | null
  inherited: string | null
  moreOpen: boolean
  openMore(): void
  closeMore(): void
  setOverlay(title: string | null, inherited?: string | null): void
  setRegistered(title: string | null): void
}

export function useTitleStore(): TitleStore {
  return titleStoreInstance
}

const titleStoreInstance: TitleStore = reactive({
  registered: null,
  overlay: null,
  inherited: null,
  moreOpen: false,
  openMore() {
    titleStoreInstance.moreOpen = true
  },
  closeMore() {
    titleStoreInstance.moreOpen = false
  },
  setOverlay(title: string | null, inherited: string | null = null) {
    titleStoreInstance.overlay = title
    titleStoreInstance.inherited = inherited
  },
  setRegistered(title: string | null) {
    titleStoreInstance.registered = title
  },
}) as TitleStore
