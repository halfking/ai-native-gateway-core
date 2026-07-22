/**
 * 公开导航（未登录用户的顶部导航链接）。
 *
 * 2026-07-22: 修正路径，指向本地公开路由而非不存在的 /maintain/*
 * - 下载安装/安装激活/在线激活 → /customer/update-activate（更新与激活）
 * - 许可状态 → /customer/update-activate（更新与激活）
 * - 协议/支持 → /customer/update-activate（更新与激活）
 */

export type PublicNavLink = {
  path: string
  labelKey: string
}

export const PUBLIC_NAV_LINKS: PublicNavLink[] = [
  { path: '/customer/update-activate', labelKey: 'nav.publicDownload' },
  { path: '/customer/update-activate', labelKey: 'nav.publicSetup' },
  { path: '/customer/update-activate', labelKey: 'nav.publicActivate' },
  { path: '/customer/update-activate', labelKey: 'nav.publicLicense' },
  { path: '/customer/update-activate', labelKey: 'nav.publicAgreement' },
  { path: '/customer/update-activate', labelKey: 'nav.publicSupport' },
]
