/**
 * hyper/types.ts — Hyper 运行时共享类型（docs/UI规范/00 §5.2 · H1）。
 *
 * 这些类型是**导航上下文、覆盖层、返回仲裁、能力协商**四者的共同词汇。
 * 单独成文件的原因：它们会被 `hyper/*.ts` 互相引用，也会被
 * `useHyperPage` / `useHyperOverlay` 两个 composable 与组件 spec 引用；
 * 放在任一实现文件里都会造成循环依赖或让 spec 依赖实现细节。
 */

/** 一层界面在导航体系里的呈现形态。 */
export type Presentation = 'page' | 'modal' | 'sheet' | 'focus'

/** 打开这一层的意图。与 Presentation 正交：page 也可能是 replace 打开的。 */
export type NavigationKind = 'push' | 'replace' | 'pop' | 'deepLink' | 'restore' | 'present'

/**
 * 标题来源。用于**可解释性**：出问题时能立刻知道「这个标题是从哪来的」，
 * 而不是去猜。数值越大优先级越低，`registered` 最高。
 */
export type TitleSource =
  | 'registered' // 页面/弹层显式登记（最可靠）
  | 'aria' // 弹层自身 aria-labelledby 指向的节点
  | 'dom' // 旧页兼容：当前页面根节点内的 data-shell-title / h1
  | 'inherited' // 无标题弹层继承打开前的有效标题
  | 'route' // 本路由 meta.titleKey 的翻译值
  | 'document' // 最后兜底：document.title / 应用名

/**
 * 隔离域。**同一份导航历史不得跨账号/跨服务端/跨项目复用**——
 * 否则会出现「切了账号还看到上一个账号的页面标题与筛选」。
 */
export interface NavigationScope {
  serverId: string
  accountId: string
  projectId?: string
}

/** 一次页面访问的视图快照。只存**引用与几何**，不存业务行数据。 */
export interface ViewSnapshot {
  /** 内容 Tab 的稳定 ID。 */
  tabId?: string
  /** 白名单筛选缓存引用，不存敏感正文。 */
  filterRef?: string
  /** 列表缓存引用，不把业务行塞进历史。 */
  listRef?: string
  /** 各滚动宿主（主内容 / 当前 Tab / 弹层 body / 表格横轴）的位置。 */
  scroll?: Record<string, ScrollAnchor>
  focusTarget?: string
}

export interface ScrollAnchor {
  x: number
  y: number
  /** 首个可见行的稳定 rowId，用于数据异步就绪后的精确定位。 */
  anchorId?: string
}

/**
 * 一条导航条目。
 *
 * `id` 是一次**访问**的身份，不是 URL。同一个 URL 打开两次有两个 id，
 * 因为两次的滚动位置与筛选可能不同 —— 用 fullPath 当 key 会把它们互相覆盖。
 */
export interface NavigationEntry {
  id: string
  /** 打开来源。不靠路径层级猜。 */
  parentId?: string
  /** 运行时完整路径；**持久化时只保留白名单 query**。 */
  fullPath: string
  routeName?: string
  presentation: Presentation
  openedBy: NavigationKind
  title: string
  titleSource: TitleSource
  /** 有 i18n key 时存 key，持久化只存 key，恢复时重译。 */
  titleKey?: string
  /** 无标题弹层继承自哪一条。 */
  inheritedFrom?: string
  scope: NavigationScope
  view: ViewSnapshot
  historyPosition?: number
  createdAt: number
}

/** 导航操作日志。用于解释「刚才发生了什么」，不用于重放动作。 */
export interface NavigationOperation {
  seq: number
  entryId: string
  type: string
  timestamp: number
  outcome: 'committed' | 'cancelled' | 'failed'
}

export interface NavigationContext {
  version: 2
  entries: NavigationEntry[]
  /** 指向当前条目在 entries 中的下标。 */
  cursor: number
  /** 当前打开的覆盖层 id 栈（最上层在末尾）。 */
  overlayIds: string[]
  operations: NavigationOperation[]
}

/**
 * 覆盖层登记项。
 *
 * `dismissible=false` 或 `closeGuard` 返回 reject 时，BackDispatcher 必须
 * **消费这次返回并保持现状**，绝不能穿透到背景路由。
 */
export interface OverlayRegistration {
  id: string
  /** 打开它的页面条目 id。 */
  parentEntry?: string
  /** 数值越大越靠上层。只让栈顶响应遮罩/Esc/Back。 */
  priority: number
  presentation: Extract<Presentation, 'modal' | 'sheet' | 'focus'>
  /** 显式标题；缺省走 title.ts 的继承链。 */
  title?: string
  titleKey?: string
  dismissible: boolean
  /** 是否有未保存内容。 */
  dirty: boolean
  /**
   * 关闭守卫。返回 `true` 允许关闭；返回 `false` 表示拒绝（例如用户选择
   * 「继续编辑」）。异步亦可，BackDispatcher 会 await。
   */
  closeGuard?: () => boolean | Promise<boolean>
  /** 实际执行关闭。必须幂等：关闭钮与 Back 都可能调用它。 */
  close: () => void | Promise<void>
  /** 关闭后恢复焦点到的元素选择器。 */
  restoreFocus?: string
  createdAt: number
}

/**
 * 原生能力协商结果（拟议契约，非任何现有插件的 API）。
 *
 * **两个维度必须分开**：
 * - `hyperMode` 是交互形态开关（壳内或 compact），只是形态；
 * - `capabilities` 才证明原生能力真的可用。
 *
 * compact 浏览器里 `hyperMode` 为 true，但 `back` 可能只有 `'none'`、
 * `haptics` 为 false。不能凭 `inShell()` 推断有录音/OCR/可持续后台。
 */
export interface HyperCapabilities {
  protocolVersion: number
  platform: 'ios' | 'android' | 'web'
  /** BackDispatcher 可用（本专题已实现，见 back.ts）。 */
  navigation: boolean
  /** 返回能力：none=无入口；commit-only=只有完成事件；interactive=有可取消预览。 */
  back: 'none' | 'commit-only' | 'interactive'
  /** 专注工作区可用（H4 未实现 ⇒ 恒 false）。 */
  focusWorkspace: boolean
  /** 安全区来源：css-only=只用 env()；native-css-px=由原生上报 CSS px。 */
  insets: 'css-only' | 'native-css-px'
  /** 键盘：viewport-only=仅视口变化；native=有原生键盘事件。 */
  keyboard: 'viewport-only' | 'native'
  haptics: boolean
  /** 是否能收到 App 前后台生命周期事件。 */
  appLifecycle: boolean
  /**
   * 任务能力。R3（2026-10-04 吸收）要求报出，**不报就等于让消费方自己猜**。
   * 本专题未实现任何一项 ⇒ 恒取「不可用」，且**不采信壳的 claim**。
   */
  tasks: {
    durableLocal: boolean
    cloudDetached: boolean
    continuation: 'foregroundOnly' | 'bestEffort' | 'osScheduled' | 'activeAudio' | 'serverDurable'
  }
  /** 录音能力。本专题未实现 ⇒ 恒 false。 */
  recording: { available: boolean; background: boolean }
  /** 识别能力（PDF 文本 / OCR / ASR）。本专题未实现 ⇒ 恒 'none'。 */
  recognition: {
    pdfText: boolean
    ocr: 'none' | 'fast' | 'accurate'
    asr: 'none' | 'fast' | 'accurate'
  }
  /** Agent 能力。本专题未实现 ⇒ 恒 unavailable。 */
  agent: { available: boolean; skillFormat: string }
  /** 壳包版本；Web 降级时为空串。 */
  shellVersion?: string
  /**
   * 可信远端 origin。只有它与当前页面 origin 一致时，原生能力才应当授予。
   * 远端桥扩大了原生能力的信任面，导航到外站/iframe 不能自动继承。
   */
  trustedOrigin?: string
}

/** 返回仲裁的处置结果。用于把「为什么没退页面」讲清楚。 */
export type BackOutcome =
  | 'dismissed-overlay' // 关掉了最上层覆盖层
  | 'navigated' // 页面出栈
  | 'consumed-by-guard' // 脏表单/不可关闭层消费了返回，留在原地
  | 'delivered-to-system' // 交还系统（根页无层）
  | 'in-flight' // 已有一次返回在进行中，本次忽略
  | 'unsupported' // 当前环境没有返回能力
