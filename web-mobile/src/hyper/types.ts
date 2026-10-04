// Hyper 运行时类型 — UI规范 06 §2/§3 + 17 §4-R2/R3/R6 的落地契约。
// 分层定位（08 §1）：本层是 Web Runtime，不依赖 Vue 组件树之外的原生假设。

export type Presentation = 'page' | 'modal' | 'sheet' | 'focus'
export type NavigationKind = 'push' | 'replace' | 'pop' | 'deepLink' | 'restore'
/** 17 §4-R6：操作环类型枚举（补 06 §3 留白）。 */
export type OperationType =
  | 'push'
  | 'replace'
  | 'pop'
  | 'deepLink'
  | 'restore'
  | 'present'
  | 'dismiss'
  | 'refresh'

export type TitleSource = 'registered' | 'overlay' | 'inherited' | 'route' | 'document'

export interface ScrollPosition {
  x: number
  y: number
  anchorId?: string
  /** 锚行相对滚动容器的偏移：恢复时 scrollTop = anchor.offsetTop - anchorOffset。 */
  anchorOffset?: number
}

/** 06 §2：NavigationEntry v2（视图快照裁剪到滚动 + Tab）。 */
export interface NavigationEntry {
  id: string
  parentId?: string
  fullPath: string
  routeName?: string
  presentation: Presentation
  openedBy: NavigationKind | 'present'
  title: string
  titleSource: TitleSource
  titleKey?: string
  /** 继承标题的来源条目（弹层无标题场景）。 */
  inheritedFrom?: string
  scope: { accountId: string }
  view: {
    tabId?: string
    scroll: Record<string, ScrollPosition>
  }
  historyPosition?: number
  createdAt: number
  /** 17 §4-R2：渲染代次——异步标题/快照回写必须携带，不匹配即丢弃。 */
  renderEpoch: number
}

export interface NavigationOperation {
  seq: number
  entryId: string
  type: OperationType
  timestamp: number
  outcome: 'committed' | 'cancelled' | 'failed'
}

/** 覆盖层注册句柄（modal/sheet/focus 共用；06 §5.1 history 标记由 runtime 负责）。 */
export interface OverlayHandle {
  id: string
  presentation: Exclude<Presentation, 'page'>
  /** 显式标题（优先级 1）；无标题则继承打开时刻快照。 */
  title?: () => string | undefined
  /** 返回 false 拒绝关闭（消费返回事件，保持现状）。 */
  beforeClose?: () => boolean | Promise<boolean>
  /** 执行实际关闭（置 modelValue=false 等）。 */
  close: () => void
}

/** 17 §4-R3：capabilities v2 合并 schema（08 §3 × 14 §2）。 */
export interface HyperCapabilities {
  protocolVersion: 2
  platform: 'web' | 'ios' | 'android'
  navigation: boolean
  back: 'none' | 'commit-only' | 'interactive'
  focusWorkspace: boolean
  insets: 'css-only' | 'native-css-px'
  keyboard: 'viewport-only' | 'native'
  haptics: boolean
  appLifecycle: boolean
  tasks: {
    durableLocal: boolean
    cloudDetached: boolean
    continuation: 'foregroundOnly' | 'bestEffort' | 'osScheduled' | 'activeAudio' | 'serverDurable'
  }
  recording: { available: boolean; background: boolean }
  recognition: { pdfText: boolean; ocr: 'none' | 'fast' | 'accurate'; asr: 'none' | 'fast' | 'accurate' }
  agent: { available: boolean; skillFormat: string }
}
