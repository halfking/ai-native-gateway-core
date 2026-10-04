import type { TitleSource } from '../types'

// TitleResolver — UI规范 06 §2 优先级链的纯函数实现（命中即停）。
// 1. 最上层弹层的显式登记标题
// 2. 弹层无标题 → 继承「打开时刻」的有效标题快照（含前一层弹窗标题）
// 3. 当前激活页面登记标题（与 PageHeader 同一响应式源）
// 4. 路由 meta.titleKey 翻译值
// 5. 应用名兜底
// 禁止显示"弹窗"/空白；标题纯文本、trim、≤200 字符、禁止 HTML 注入。

export const TITLE_MAX_LENGTH = 200

export interface ResolvedTitle {
  title: string
  source: TitleSource
  /** 提供该标题的实体 ID（页面/弹层注册 ID，或 'route' / 'document'）。 */
  fromId: string
  inheritedFrom?: string
}

/** 弹层打开时刻的标题快照（06 §2：含前一层弹窗标题）。 */
export interface TitleSnapshot {
  title: string
  /** 提供该标题的条目（页面或弹层）ID——继承者记录 inheritedFrom。 */
  fromId: string
}

export interface PageTitleRegistration {
  id: string
  title: () => string | undefined
  titleKey?: string
}

export interface OverlayTitleRegistration {
  id: string
  title?: () => string | undefined
  snapshot?: TitleSnapshot
}

export interface TitleResolverDeps {
  getPage(): PageTitleRegistration | null
  /** 自底向上的弹层栈。 */
  getOverlays(): OverlayTitleRegistration[]
  getRouteTitle(): string | undefined
  getAppName(): string
}

export function clipTitle(raw: string): string {
  const text = raw.replace(/\s+/g, ' ').trim()
  return text.length > TITLE_MAX_LENGTH ? text.slice(0, TITLE_MAX_LENGTH - 1) + '…' : text
}

export class TitleResolver {
  constructor(private readonly deps: TitleResolverDeps) {}

  resolve(): ResolvedTitle {
    const overlays = this.deps.getOverlays()
    const top = overlays.length > 0 ? overlays[overlays.length - 1] : undefined
    if (top) {
      const explicit = top.title?.()
      if (explicit && explicit.trim()) {
        return { title: clipTitle(explicit), source: 'overlay', fromId: top.id }
      }
      if (top.snapshot) {
        return {
          title: clipTitle(top.snapshot.title),
          source: 'inherited',
          fromId: top.snapshot.fromId,
          inheritedFrom: top.snapshot.fromId,
        }
      }
    }

    const page = this.deps.getPage()
    if (page) {
      const registered = page.title()
      if (registered && registered.trim()) {
        return { title: clipTitle(registered), source: 'registered', fromId: page.id }
      }
    }

    const routeTitle = this.deps.getRouteTitle()
    if (routeTitle && routeTitle.trim()) {
      return { title: clipTitle(routeTitle), source: 'route', fromId: 'route' }
    }

    return { title: clipTitle(this.deps.getAppName()), source: 'document', fromId: 'document' }
  }
}
