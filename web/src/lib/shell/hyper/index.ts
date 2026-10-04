/**
 * hyper/index.ts — Hyper 运行时门面与应用入口装配（docs/UI规范/00 §5.2 · H1）。
 *
 * 装配顺序很重要：
 *   capabilities → scope → 恢复历史 → 路由适配器 → 返回端口 → 全局键盘
 * 任一步失败都**降级**而不是抛错：Hyper 是增强层，它挂掉不能让控制台打不开。
 */
import type { Router } from 'vue-router'
import { overlays } from './overlay'
import { navigation, persistNavigation, restoreNavigation, clearNavigation } from './context'
import { titleResolver } from './title'
import { back, clearPendingPop, consumePendingPop, type NavigationPort } from './back'
import { capabilities, detectCapabilities, setCapabilities, type DetectOptions } from './capabilities'
import { isDesktopOnlyAllowed, type WindowClass } from '../../../config/window-class'
import type { HyperCapabilities, NavigationScope, Presentation } from './types'

export * from './types'
export { overlays, OverlayRegistry } from './overlay'
export {
  TitleResolver,
  titleResolver,
  sanitizeTitle,
  resolveDomTitle,
  TITLE_MAX_LENGTH,
  type ResolvedTitle,
} from './title'
export {
  NavigationStore,
  navigation,
  persistNavigation,
  restoreNavigation,
  clearNavigation,
  NAV_STORAGE_KEY,
  MAX_ENTRIES,
  MAX_OPERATIONS,
  PERSISTED_QUERY_WHITELIST,
  PERSISTED_QUERY_DENYLIST,
} from './context'
export {
  back,
  BackDispatcher,
  consumePendingPop,
  peekPendingPop,
  type NavigationPort,
  type BackSource,
} from './back'
export {
  capabilities,
  detectCapabilities,
  setCapabilities,
  onCapabilitiesChange,
  hyperMode,
  can,
  hasInteractiveBack,
  isTrustedOrigin,
  PROTOCOL_VERSION,
  WEB_CAPABILITIES,
  type DetectOptions,
} from './capabilities'

/** 恢复历史里**允许保留**的 query 由 context.ts 白名单控制；此处只做兜底上限。 */
const MAX_PERSISTED_ROUTE = 512

export interface InstallHyperOptions {
  /** 隔离域。换账号/换服务端会清空历史。 */
  scope: NavigationScope
  /**
   * 是否启用 Hyper 交互形态（壳内或 compact）。
   *
   * **桌面（large/expanded 且不在壳内）必须传 false。** 桌面走浏览器原生
   * 历史与现状顶栏，Hyper 的 entry 链与 Esc 仲裁都不该介入 —— 这是桌面
   * 零回归红线。开启时本模块才会挂路由适配器与持久化。
   */
  hyperActive: boolean
  /** 当前窗口档。 */
  getWindowClass: () => WindowClass
  /** 路由 meta.titleKey → 翻译文本。 */
  translate: (key: string) => string
  /** 能力探测注入点。 */
  capabilitiesOptions?: DetectOptions
  /** 是否绑定 Esc 键。默认 true。 */
  bindEscape?: boolean
}

let installed = false
let routerHookInstalled = false
let unbindEscape: (() => void) | null = null

/**
 * 装 Hyper 到 Vue Router 上。
 *
 * 幂等：重复调用只更新配置，不重复注册监听。应用 HMR 与二次挂载都靠这个。
 */
export async function installHyper(router: Router, opts: InstallHyperOptions): Promise<void> {
  // 1) 能力先探，决定后续降级口径
  const caps: HyperCapabilities = await detectCapabilities(opts.capabilitiesOptions)
  setCapabilities(caps)

  // 2) 隔离域。首次声明会清空历史 —— 这是刻意的。
  navigation.setScope(opts.scope)
  if (opts.hyperActive) restoreNavigation(opts.scope)

  // 3) 路由适配器：只在导航**成功后**提交条目。桌面不挂。
  if (opts.hyperActive && !routerHookInstalled) {
    routerHookInstalled = true
    router.afterEach((to, from, failure) => {
      // 守卫拒绝 / 导航失败：不提交、不动 cursor
      if (failure) {
        clearPendingPop()
        navigation.log(navigation.current()?.id ?? 'unknown', `navigate:${to.fullPath}`, 'cancelled')
        return
      }

      // 根路径首次加载不建条目（没有「从哪来」）
      if (!from.name && to.matched.length === 0) {
        clearPendingPop()
        return
      }

      // ★ 出栈路径：cursor 的**唯一**写入入口在这里。
      // BackDispatcher 发起 pop 时只登记意图，导航成功后才由本处左移。
      // 这样「路由守卫拒绝」时 store 完全不动，两者不会脱节。
      const pendingPop = consumePendingPop()
      if (pendingPop) {
        navigation.applyExternalPop(pendingPop)
        persistNavigation()
        return
      }

      if (to.fullPath === from.fullPath) {
        // 同路径 replace（换筛选/Tab）：保留 entry id，只更新内容
        navigation.push({
          parentId: navigation.current()?.parentId,
          fullPath: to.fullPath.slice(0, MAX_PERSISTED_ROUTE),
          routeName: String(to.name ?? ''),
          presentation: 'page',
          openedBy: 'replace',
          titleKey: typeof to.meta?.titleKey === 'string' ? to.meta.titleKey : undefined,
          scope: navigation.getScope() ?? opts.scope,
        })
        persistNavigation()
        return
      }
      const id = navigation.push({
        parentId: navigation.current()?.id,
        fullPath: to.fullPath.slice(0, MAX_PERSISTED_ROUTE),
        routeName: String(to.name ?? ''),
        presentation: 'page',
        openedBy: to.meta?.redirect ? 'deepLink' : 'push',
        titleKey: typeof to.meta?.titleKey === 'string' ? to.meta.titleKey : undefined,
        scope: navigation.getScope() ?? opts.scope,
      })
      titleResolver.bumpEpoch(id)
      persistNavigation()
    })
  }

  // 4) 返回端口
  const port: NavigationPort = {
    async pop() {
      try {
        await router.back()
        return true
      } catch {
        return false
      }
    },
    async replace(path: string) {
      try {
        await router.replace(path)
        return true
      } catch {
        return false
      }
    },
    isAtRoot() {
      return navigation.atRoot
    },
    deliverToSystem() {
      // 浏览器/PWA 无接收者；原生壳由壳侧 App 插件接管（capabilities.back != 'none' 时）
      return capabilities().platform !== 'web' && capabilities().back !== 'none'
    },
  }
  back.setPort(port)

  // 5) Esc。仅关可关闭的上层，**不**默认退出整个 App。桌面不挂（现状行为不变）。
  if (opts.hyperActive && (opts.bindEscape ?? true) && !unbindEscape) {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (overlays.depth === 0) return
      void back.request('escape')
    }
    window.addEventListener('keydown', onKey)
    unbindEscape = () => window.removeEventListener('keydown', onKey)
  }

  installed = true
}

/** 登出/换账号/换服务端：清导航历史、标题登记与覆盖层栈。 */
export function resetHyper(): void {
  clearNavigation()
  titleResolver._reset()
  overlays._reset()
  back._reset()
  installed = false
  routerHookInstalled = false
  unbindEscape?.()
  unbindEscape = null
}

/** 是否已装配（供 spec 与诊断使用）。 */
export function isHyperInstalled(): boolean {
  return installed
}

/** 便捷判定：当前档位是否允许进入「桌面专属」页。 */
export function canEnterDesktopOnly(windowClass: WindowClass): boolean {
  return isDesktopOnlyAllowed(windowClass)
}

/** 登记一个覆盖层并在返回时纳入仲裁（供 useHyperOverlay 使用）。 */
export function presentOverlay(
  reg: Parameters<typeof overlays.register>[0],
  options: { titleFallback?: string } = {},
): () => void {
  return overlays.register({
    ...reg,
    title: reg.title ?? options.titleFallback,
  })
}

export type { Presentation }
