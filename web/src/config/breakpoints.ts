/**
 * breakpoints.ts — 全项目唯一合法断点体系（方案 docs/03-design §4.2）。
 *
 * JS 侧（组件 / 组合式函数）一律从本文件取值，禁止手写魔法数字。
 *
 * CSS 侧说明：@media 条件无法使用 var()，且绝大多数组件为原生 CSS
 * （不切 SCSS），因此 CSS 不做断点 mixin，改由 scripts/responsive-audit.mjs
 * 按 MEDIA_QUERY_WHITELIST 白名单审计治理；新写 @media 只允许使用白名单值。
 */
export const BREAKPOINTS = {
  mobile: 0, // < 768：抽屉导航、单列、卡片化表格、弹窗全屏
  tablet: 768, // >= 768：2 列栅格、抽屉导航（或收纳式顶栏）
  desktop: 1024, // >= 1024：现状桌面顶栏布局（布局决策口径：>=1024 才算桌面）
  wide: 1440, // >= 1440：大屏内容最大宽度约束（可选档）
  small: 480, // < 480：小屏微调（字号/按钮尺寸），不改变布局结构
} as const

export type BreakpointKey = keyof typeof BREAKPOINTS

/**
 * 允许出现在 CSS @media 中的断点白名单（scripts/responsive-audit.mjs 消费）。
 * 640 为存量过渡保留值；随页面迁移逐步收缩，最终只留 480/768/1024/1440。
 */
export const MEDIA_QUERY_WHITELIST: readonly number[] = [480, 640, 768, 1024, 1440]
