/**
 * window-class.ts — 窗口四档语义层（docs/UI规范/00 §5.2 · H0）。
 *
 * ## 为什么需要这一层
 *
 * `config/breakpoints.ts` 回答的是「视口落在哪几个像素档」，是 CSS 白名单的治理源；
 * 但 Hyper 规范需要的是**语义档**（compact / medium / expanded / large），因为
 * 底栏可见性、compact 强制卡片、弹窗形态、账户 Sheet 形态这四件事都挂在
 * 「这一档是什么」上，而不是挂在某个具体 px 上。
 *
 * 早期方案（参考仓）直接用 compact<600 / medium<960 / expanded<1280 / large>=1280。
 * 本仓不照抄那组数值 —— 见 `docs/UI规范/00` 决策 D2：沿用本仓 SSOT，
 * 保住 `responsive-audit.mjs` 白名单与 43 文件存量，避免覆盖 95 视图的视觉回归。
 *
 * ## 档位边界
 *
 *   compact   <  768    手机竖屏、壳内 WebView
 *   medium    768–1023  折叠展开、平板竖横持、窄桌面
 *   expanded 1024–1439  平板横屏、常规桌面
 *   large    >= 1440    大屏作业
 *
 * 边界用 `.98` 分数而非 `-1` 整数：`matchMedia` 在分数宽度（如 767.5px，来自
 * 缩放/分屏）上会与整数边界产生缝隙，`.98` 是既有的「<768」传统写法，不引入
 * Media Queries Level 4 范围语法（`width <= 768px` 被仓库门禁禁止）。
 *
 * ## 已知 1px 差异（不要"顺手修"）
 *
 * `styles/responsive-base.css` 里存量写的是 `@media (max-width: 768px)`（**含** 768），
 * 而本层 compact 是 **< 768**。也就是说在恰好 768.0 CSS px 这一条线上，
 * CSS 走移动兜底、JS 走 medium。这是 P0 阶段留下的既有形态：
 * 改它要动 43 个存量文件并重跑视觉回归，收益为零、风险为正。
 * 新增 Hyper CSS 一律用 `.98`；存量不动，此差异在规范 `01-原则与断点.md` §2 登记。
 *
 * ## SSOT 约束
 *
 * 本文件**不得**出现独立于 `BREAKPOINTS` 的裸像素数：所有边界都从
 * `BREAKPOINTS` 派生。`window-class.spec.ts` 会钉住这条约束。
 */
import { BREAKPOINTS } from './breakpoints'

/** 语义四档。命名与参考规范一致，数值按本仓 SSOT 映射（决策 D2）。 */
export type WindowClass = 'compact' | 'medium' | 'expanded' | 'large'

/**
 * 各档的**下界**（含）。`compact` 下界为 0。
 * 全部由 BREAKPOINTS 派生，见文件头「SSOT 约束」。
 */
export const WINDOW_CLASS_PX: Readonly<Record<WindowClass, number>> = Object.freeze({
  compact: 0,
  medium: BREAKPOINTS.tablet, // 768
  expanded: BREAKPOINTS.desktop, // 1024
  large: BREAKPOINTS.wide, // 1440
})

/**
 * 各档的**上界**（不含）。`large` 为 `Infinity`。
 * 与 WINDOW_CLASS_PX 成对存在，避免上界被写成独立的魔法数。
 */
export const WINDOW_CLASS_MAX_PX: Readonly<Record<WindowClass, number>> = Object.freeze({
  compact: BREAKPOINTS.tablet, // 768
  medium: BREAKPOINTS.desktop, // 1024
  expanded: BREAKPOINTS.wide, // 1440
  large: Number.POSITIVE_INFINITY,
})

/**
 * 给 `matchMedia` 用的传统媒体查询串。键为档名，值为「该档及其以下」的查询，
 * 这样单例只需绑定 N-1 条 min-width 查询即可推导当前档。
 *
 * 只用 `min-width` / `max-width` 传统特性；**禁止** `width <= …` 范围语法
 * （见 scripts/responsive-audit.mjs 与 01 规范 §3）。
 */
export const WINDOW_CLASS_MEDIA: Readonly<Record<WindowClass, string>> = Object.freeze({
  compact: `(max-width: ${WINDOW_CLASS_MAX_PX.compact - 0.02}px)`,
  medium: `(min-width: ${WINDOW_CLASS_PX.medium}px)`,
  expanded: `(min-width: ${WINDOW_CLASS_PX.expanded}px)`,
  large: `(min-width: ${WINDOW_CLASS_PX.large}px)`,
})

/** 档位顺序（由窄到宽），用于「取最宽命中档」的推导与测试断言。 */
export const WINDOW_CLASS_ORDER: readonly WindowClass[] = Object.freeze([
  'compact',
  'medium',
  'expanded',
  'large',
] as const)

/**
 * 纯函数：由视口宽度推导窗口档。SSR / 无 window 环境可安全调用。
 *
 * 实现取「最宽命中档」而非「最窄命中档」：宽度 1440 同时满足四条查询，
 * 必须落 large。逐条 if 顺序固定为 compact→large，语义等价且可读。
 */
export function currentWindowClass(width: number): WindowClass {
  if (width < WINDOW_CLASS_MAX_PX.compact) return 'compact'
  if (width < WINDOW_CLASS_MAX_PX.medium) return 'medium'
  if (width < WINDOW_CLASS_MAX_PX.expanded) return 'expanded'
  return 'large'
}

/**
 * 路由/页面级「桌面专属」标记的判定辅助。
 *
 * 与参考规范一致：不隐藏入口，而是在 compact 档显示「建议桌面端」横幅，
 * 允许进入并可读，只有重编辑操作被禁用。
 */
export function isDesktopOnlyAllowed(windowClass: WindowClass): boolean {
  return windowClass !== 'compact'
}

/** 供 CSS 精确选择某一档的 data 属性值，与 `useWindowClass` 写在根节点上。 */
export const WINDOW_CLASS_ATTR = 'data-window-class'
