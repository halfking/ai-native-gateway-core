/**
 * hyper/title.ts — 标题真源解析（docs/UI规范/00 §5.2 · H1，参考规范 06 §2）。
 *
 * ## 要解决的问题
 *
 * 「导航栏显示什么」在多数实现里是各页面各写一个字符串，于是出现：
 * 弹窗把标题清空、异步加载的项目名晚到后盖掉新页面标题、隐藏的 KeepAlive
 * 页面被当成当前页标题、多语言切换后标题不重译。
 *
 * ## 解析次序（严格按此顺序，数值越小越优先）
 *
 * 1. `registered`  最上层覆盖层的显式标题
 * 2. `aria`        最上层覆盖层自身 `aria-labelledby` 指向的节点
 * 3. `dom`         最上层覆盖层内的 `data-shell-title` / 标题节点
 * 4. `inherited`   覆盖层无标题 → 沿用**打开时**的有效标题快照
 * 5. `registered`  当前页面显式登记的标题
 * 6. `dom`         当前页面根节点内的 `data-shell-title` / 主 h1
 * 7. `route`       本路由 `meta.titleKey` 的翻译值
 * 8. `document`    `document.title` / 应用名
 *
 * 两个易错点，代码里都显式处理了：
 *
 * - **覆盖层的 DOM 回退只查它自己**。查背景页面会得到「你在弹窗里但标题
 *   显示的是背后列表页」。
 * - **异步结果带 epoch**。旧页面晚到的项目名不能覆盖新页面标题
 *   （`registerPageTitle` 对过期 epoch 返回 false，不写入）。
 */
import type { TitleSource } from './types'

/** 标题长度上限。超长截断，避免异常数据把导航栏撑爆。 */
export const TITLE_MAX_LENGTH = 200

/**
 * 清洗标题：去标签、去首尾空白、折叠内部空白、限长。
 *
 * **禁止 HTML 注入**：实体名可能来自服务端（项目名、租户名），
 * 直接插进 `textContent` 之外的任何位置都是 XSS 面。所以这里只产出纯文本，
 * 调用方必须用 `textContent` 渲染。
 */
export function sanitizeTitle(raw: unknown): string | null {
  if (raw == null) return null
  const text = String(raw)
    .replace(/<[^>]*>/g, ' ') // 去标签（不是 DOMParser：标题不值得造一个文档）
    .replace(/\s+/g, ' ')
    .trim()
  if (!text) return null
  return text.length > TITLE_MAX_LENGTH ? `${text.slice(0, TITLE_MAX_LENGTH - 1)}…` : text
}

const TITLE_ATTR = 'data-shell-title'

/**
 * 在**给定根节点内**解析标题。绝不向 `document` 兜底。
 *
 * @param root 作用域根元素。传 null 直接返回 null —— 「查不到」与
 *             「查到空」都必须如实返回 null，让上层去走继承或路由回落，
 *             而不是编一个标题出来。
 */
export function resolveDomTitle(root: Element | null | undefined): string | null {
  if (!root) return null
  // 1. 显式标记优先
  const marked = root.querySelector(`[${TITLE_ATTR}]`)
  if (marked) {
    const v = sanitizeTitle(marked.getAttribute(TITLE_ATTR))
    if (v) return v
  }
  // 2. 主标题节点
  const heading = root.querySelector('h1, h2')
  if (heading) {
    const v = sanitizeTitle(heading.textContent)
    if (v) return v
  }
  return null
}

export interface ResolvedTitle {
  title: string
  source: TitleSource
  /**
   * 继承链：非空表示当前标题来自上层。
   * 指向**真正产生该标题的那一层**（覆盖层 id 优先，否则页面 entry id），
   * 而不是笼统地记成「当前页面」—— 否则弹窗套弹窗时无法解释标题从哪来。
   */
  inheritedFrom?: string
  /** 命中的页面条目 id。 */
  entryId?: string
  /** 命中的覆盖层 id（仅当标题来自覆盖层时存在）。 */
  overlayId?: string
}

interface PageTitleRecord {
  title?: string
  titleKey?: string
  /** 该条目当前的 renderEpoch。异步结果必须携带自己那份 epoch。 */
  epoch: number
}

export interface ResolveOptions {
  /** 当前路由 meta.titleKey 的翻译值。 */
  routeTitle?: string | null
  /** document.title 或应用名，最后兜底。 */
  documentTitle?: string | null
  /**
   * 覆盖层的 DOM 探针。运行时不做 DOM 查询（可测性 + 避免每帧扫全树），
   * 由 Vue 侧注入：给定最上层覆盖层的根元素，返回其自身标题。
   */
  overlayDomTitle?: (rootId: string) => string | null
  /** 当前页面根节点的 DOM 探针。 */
  pageDomTitle?: (entryId: string) => string | null
  /** 最上层覆盖层信息。 */
  overlay?: { id: string; title?: string; titleKey?: string; rootId?: string } | null
  /** 当前页面条目 id。 */
  currentEntryId?: string
  /** appName 最终兜底。 */
  appName?: string
}

export class TitleResolver {
  private pages = new Map<string, PageTitleRecord>()

  /**
   * 登记/更新页面标题。
   *
   * @param epoch 由调用方维护的 renderEpoch。传入**小于**已记录值的 epoch
   *              视为过期异步结果，**拒绝写入并返回 false**。
   */
  registerPageTitle(
    entryId: string,
    patch: { title?: string | null; titleKey?: string; epoch: number },
  ): boolean {
    const prev = this.pages.get(entryId)
    if (prev && patch.epoch < prev.epoch) return false // 过期，不覆盖

    const clean = patch.title != null ? sanitizeTitle(patch.title) : null
    this.pages.set(entryId, {
      title: clean ?? prev?.title, // 本次没给标题时保留上一次，别把已有标题擦成空
      titleKey: patch.titleKey ?? prev?.titleKey,
      epoch: patch.epoch,
    })
    return true
  }

  /** 提升 renderEpoch。页面重新进入时调用，使更早的异步结果失效。 */
  bumpEpoch(entryId: string): number {
    const prev = this.pages.get(entryId)
    const next = (prev?.epoch ?? 0) + 1
    this.pages.set(entryId, { ...(prev ?? {}), epoch: next })
    return next
  }

  getEntryTitle(entryId: string): PageTitleRecord | null {
    return this.pages.get(entryId) ?? null
  }

  /**
   * 解析当前应当显示的标题。
   *
   * @param inheritedTitle 覆盖层打开时捕获的父层有效标题快照。
   *        必须在**打开那一刻**取，不能现算——弹层打开期间背景页可能换页。
   */
  resolve(opts: ResolveOptions, inheritedTitle?: ResolvedTitle | null): ResolvedTitle {
    const ov = opts.overlay
    if (ov) {
      // 1) 覆盖层显式标题
      const own = sanitizeTitle(ov.title)
      if (own) return { title: own, source: 'registered', overlayId: ov.id }

      // 2) 覆盖层 aria / dom —— 只查它自己
      const dom = ov.rootId && opts.overlayDomTitle ? opts.overlayDomTitle(ov.rootId) : null
      const cleanDom = sanitizeTitle(dom)
      if (cleanDom) return { title: cleanDom, source: 'aria', overlayId: ov.id }

      // 3) 无标题 → 继承打开前的快照。继承来源是「产生该标题的那一层」：
      //    上层也是弹层时记它的 overlayId，否则记页面 entryId。
      if (inheritedTitle?.title) {
        return {
          title: inheritedTitle.title,
          source: 'inherited',
          inheritedFrom: inheritedTitle.overlayId ?? inheritedTitle.entryId,
        }
      }
      // 继承快照都没有（冷启动直接开弹层）→ 继续往下走页面/路由回落，
      // 但**不**置空、**不**填「弹窗」二字。
    }

    // 4) 当前页面显式登记
    if (opts.currentEntryId) {
      const rec = this.pages.get(opts.currentEntryId)
      const own = rec?.title ? sanitizeTitle(rec.title) : null
      if (own) return { title: own, source: 'registered', entryId: opts.currentEntryId }
    }

    // 5) 当前页面 DOM 回退
    if (opts.currentEntryId && opts.pageDomTitle) {
      const dom = sanitizeTitle(opts.pageDomTitle(opts.currentEntryId))
      if (dom) return { title: dom, source: 'dom', entryId: opts.currentEntryId }
    }

    // 6) 路由 meta 翻译值
    const route = sanitizeTitle(opts.routeTitle)
    if (route) return { title: route, source: 'route' }

    // 7) document.title / 应用名
    const doc = sanitizeTitle(opts.documentTitle) ?? sanitizeTitle(opts.appName)
    if (doc) return { title: doc, source: 'document' }

    // 理论上不可达：调用方总会给 appName。返回空串而不是抛错，
    // 因为导航栏渲染失败不该连带整页崩。
    return { title: '', source: 'document' }
  }

  /** 登出/换账号/换服务端时清空。标题可能含实体名，不可跨隔离域复用。 */
  _reset(): void {
    this.pages.clear()
  }
}

export const titleResolver = new TitleResolver()
