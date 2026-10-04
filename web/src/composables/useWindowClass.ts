/**
 * useWindowClass — 响应式四档的**唯一** JS 入口（docs/UI规范/00 §5.2 · H0）。
 *
 * ## 与 useBreakpoint 的关系（不要误当成两个体系）
 *
 * - `useBreakpoint`（2026-09-13，P0 阶段）是**布局决策口径**：`isMobile` 意为
 *   「<1024，需要抽屉导航/全屏弹窗」。它已被 `App.vue` / `AppTopbar` / 既有单测消费，
 *   本轮**不改它**，避免触碰桌面红线。
 * - `useWindowClass`（本文件）是**语义四档**：`compact / medium / expanded / large`，
 *   供 Hyper 运行时按「档位是什么」做分支（底栏可见性、compact 强制卡片、弹窗形态）。
 *
 * 二者不是替代关系。新增 Hyper 代码只准用 `useWindowClass`；
 * 既有代码继续用 `useBreakpoint`。两者数值同源于 `config/breakpoints.ts`。
 *
 * ## 约定
 *
 * - 模块级单例：N 条 matchMedia 只绑定一次，所有组件共享同一 readonly ref，
 *   与 `useBreakpoint` 同构，避免每组件一条 resize 监听。
 * - 默认值 `expanded`（桌面优先）：无 window / 无 matchMedia 时不抛错，且**不**
 *   让 compact 专属 UI（底栏、卡片模式）在测试与非浏览器环境里凭空出现。
 * - 根节点镜像：把当前档写到 `document.documentElement.dataset.windowClass`，
 *   供 CSS 精确选择某一档（`html[data-window-class='compact'] .foo { … }`），
 *   也让手工走查时在 DevTools 里一眼看到当前档。
 * - SSR / jsdom 安全：所有 window/document 访问都先做存在性判断。
 */
import { computed, readonly, ref, type ComputedRef, type Ref } from 'vue'
import {
  WINDOW_CLASS_ATTR,
  WINDOW_CLASS_MEDIA,
  currentWindowClass,
  type WindowClass,
} from '../config/window-class'

/** 默认档。桌面优先：与 useBreakpoint 全部默认 false（isDesktop=true）保持同一取向。 */
const DEFAULT_WINDOW_CLASS: WindowClass = 'expanded'

const windowClass = ref<WindowClass>(DEFAULT_WINDOW_CLASS)

/** 最近一次实际测量到的视口宽度；-1 表示尚未测量（非浏览器环境）。 */
const viewportWidth = ref(-1)

let initialized = false
let cleanup: (() => void) | null = null

/**
 * 把当前档写到根节点，供 CSS 与手工走查使用。
 * 写失败（无 document / 隐私模式）静默忽略：这是增强项，不是功能依赖。
 */
function mirrorToDocument(value: WindowClass): void {
  if (typeof document === 'undefined' || !document.documentElement) return
  try {
    document.documentElement.setAttribute(WINDOW_CLASS_ATTR, value)
  } catch {
    // 纯增强，失败不阻断
  }
}

/**
 * 由三条「最窄命中档」查询推导实际档位。
 *
 * 宽度 1440 会同时命中 medium/expanded/large 三条 min-width，因此必须**从宽到窄**
 * 取第一个命中的档；窄到宽会把 1440 判成 medium。
 */
function derive(queries: Array<{ cls: WindowClass; mql: MediaQueryList }>): WindowClass {
  if (queries.find((q) => q.cls === 'large')!.mql.matches) return 'large'
  if (queries.find((q) => q.cls === 'expanded')!.mql.matches) return 'expanded'
  if (queries.find((q) => q.cls === 'medium')!.mql.matches) return 'medium'
  return 'compact'
}

function bind(): void {
  if (initialized) return
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
  initialized = true

  // 只绑定三条 min-width；compact 是「都不命中」的兜底，不需要自己的监听。
  const classes: WindowClass[] = ['medium', 'expanded', 'large']
  const queries = classes.map((cls) => ({
    cls,
    mql: window.matchMedia(WINDOW_CLASS_MEDIA[cls]),
  }))

  const sync = () => {
    const next = derive(queries)
    windowClass.value = next
    viewportWidth.value = typeof window.innerWidth === 'number' ? window.innerWidth : -1
    mirrorToDocument(next)
  }
  sync()

  if (typeof queries[0].mql.addEventListener === 'function') {
    for (const q of queries) q.mql.addEventListener('change', sync)
    cleanup = () => {
      for (const q of queries) q.mql.removeEventListener('change', sync)
    }
  }
}

export interface UseWindowClass {
  /** 当前语义档。只读，多处调用共享同一实例。 */
  windowClass: Readonly<Ref<WindowClass>>
  isCompact: ComputedRef<boolean>
  isMedium: ComputedRef<boolean>
  isExpanded: ComputedRef<boolean>
  isLarge: ComputedRef<boolean>
  /**
   * 是否处于「移动壳层」——沿用 useBreakpoint 的 `<1024` 决策口径，
   * 供同时需要「语义档」与「旧口径」的地方做兼容过渡。
   * 新代码若只做 Hyper 分支，请直接判 windowClass，不要引入这个。
   */
  isMobileShell: ComputedRef<boolean>
  /** 最近一次实测宽度；非浏览器环境为 -1。 */
  viewportWidth: Readonly<Ref<number>>
}

export function useWindowClass(): UseWindowClass {
  bind()
  return {
    windowClass: readonly(windowClass),
    isCompact: computed(() => windowClass.value === 'compact'),
    isMedium: computed(() => windowClass.value === 'medium'),
    isExpanded: computed(() => windowClass.value === 'expanded'),
    isLarge: computed(() => windowClass.value === 'large'),
    isMobileShell: computed(() => windowClass.value === 'compact' || windowClass.value === 'medium'),
    viewportWidth: readonly(viewportWidth),
  }
}

/**
 * 非组件上下文（Capacitor 桥回调、window 事件、库代码）读取当前档的入口。
 * 不会触发绑定；调用方需确保 `useWindowClass()` 已在应用入口被调用过一次。
 */
export function getWindowClass(): WindowClass {
  return windowClass.value
}

/**
 * 仅供测试：解除单例绑定、复位懒初始化标记与状态，
 * 使下一个用例能针对当前 mock 的 matchMedia 重新绑定。
 * 与 `useBreakpoint._resetForTests` 同构。
 */
export function _resetForTests(): void {
  cleanup?.()
  cleanup = null
  initialized = false
  windowClass.value = DEFAULT_WINDOW_CLASS
  viewportWidth.value = -1
  if (typeof document !== 'undefined' && document.documentElement) {
    try {
      document.documentElement.removeAttribute(WINDOW_CLASS_ATTR)
    } catch {
      // 同上，纯增强
    }
  }
}

export type { WindowClass }
export { currentWindowClass }
