// 网关站点目录 —— 登录页与「我的」页共用的多站点选择。
//
// ★ 为什么放在 web app 而不是只用壳侧那份 node-directory：
//   壳侧（webDist/node-directory.ts）管的是「**原生导航授权**」——它决定
//   WebView 允许跳到哪些 origin。web app 管的是「**当前登录的是哪台机器**」——
//   两者是不同的关注点，共用一份会导致「壳里能选、App 里不能选」。
//
// ★ 为什么零凭据（rule 19）：
//   这里**只有地址**，没有账号/令牌/密钥。账号密码每次切换后重新输入，
//   这是刻意的：跨站点共用一份凭据落盘会扩大泄漏面。
//
// ★ 为什么切换 = 整页跳转而不是改 base 再发请求：
//   业务 SPA 由网关同源提供（`https://<host>/m/`），页面里的相对路径请求
//   天然打到当前 origin。改 base 发请求会让「相对路径资源」仍留在旧站点，
//   出现 SPA 来自 A、数据来自 B 的混态。整页跳到目标站点的 `/m/` 是唯一
//   自洽的做法。

export interface GatewaySite {
  /** 稳定 id，用于「当前站点」的判定与埋点。 */
  id: string
  /** 展示名，短、人类可读。 */
  name: string
  /** 站点 origin（无尾斜杠、无路径）。 */
  origin: string
  /** 环境标签，给「生产」加醒目标记，避免误操作。 */
  env: 'production' | 'staging' | 'internal'
}

/**
 * 预置站点。与壳侧 `webDist/node-directory.ts` 的 PRESETS 保持一致。
 * ⚠️ 两份是**有意重复**的：壳侧那份决定原生放行哪些 origin（早于任何
 *   JS 执行），web 侧这份决定登录页显示哪些选项。合并会让壳侧那份
 *   反向依赖 web 构建产物。
 */
export const GATEWAY_SITES: GatewaySite[] = [
  { id: 'production', name: '生产', origin: 'https://llm.kxpms.cn', env: 'production' },
  { id: 'staging', name: '测试', origin: 'https://llmgo.kxpms.cn', env: 'staging' },
  { id: 'internal', name: '内部', origin: 'https://llm.itestu.cn', env: 'internal' },
]

/** 移动端 SPA 在网关上的挂载路径（router base '/m/'，见 utils/base.ts）。 */
const APP_PATH = '/m/'

/** 当前页面所属站点。origin 不在目录里时返回 null（自建/测试站点属正常情况）。 */
export function currentSite(): GatewaySite | null {
  if (typeof window === 'undefined') return null
  const here = window.location.origin
  return GATEWAY_SITES.find(site => site.origin === here) ?? null
}

/**
 * 站点详情页 URL：切到目标站点后应落在它的登录页。
 *
 * ⚠️ 必须带 origin 全量而不是相对路径：相对路径会留在**当前**站点，
 *   那正是「切换了站点却还登着旧站点」的来源。
 */
export function siteLoginUrl(site: GatewaySite): string {
  return `${site.origin}${APP_PATH}login`
}

/**
 * 判断站点是否就是当前所在站点。
 * ⚠️ 只比 origin，不比完整 URL —— 带上 path/query 会让 `/m/` 与 `/m/login`
 *   被判成两个站点，当前站点永远不高亮。
 */
export function isCurrentSite(site: GatewaySite): boolean {
  return typeof window !== 'undefined' && window.location.origin === site.origin
}