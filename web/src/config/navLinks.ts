/**
 * 公开导航（未登录用户的顶部导航链接）。
 *
 * 2026-07-21: 与 ai-native-maintain 风格一致 — 一级链接全部指向 ai-native-maintain
 * SPA（`/maintain/*`），i18n key 在 locales/<lang>/nav.ts 公共区块定义。
 *
 * 与 maintain 的不同点：
 *  - llm-gateway-go 这边导航 root 是 `/maintain/*`（同源子 SPA）
 *  - gateway 自身仍是登录后的「控制台」，公开导航只承担「产品介绍 + 登录入口」作用
 */

export type PublicNavLink = {
  path: string
  labelKey: string
}

export const PUBLIC_NAV_LINKS: PublicNavLink[] = [
  { path: '/maintain/download', labelKey: 'nav.publicDownload' },
  { path: '/maintain/setup', labelKey: 'nav.publicSetup' },
  { path: '/maintain/activate', labelKey: 'nav.publicActivate' },
  { path: '/maintain/license', labelKey: 'nav.publicLicense' },
  { path: '/maintain/agreement', labelKey: 'nav.publicAgreement' },
  { path: '/maintain/support', labelKey: 'nav.publicSupport' },
]
