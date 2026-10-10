/**
 * AuthSession —— 原生注销通道（2026-10-10 设计）。
 *
 * 业务背景：移动壳内 web-mobile 的 auth store 登出**擦不掉远程 origin 的
 * HttpOnly cookie**（CookieJar 由 origin 通过 Set-Cookie 注入、JS 不可达），
 * 只能清自己那一侧的 localStorage / sessionStorage。重启 App 后 WebView
 * 会用同一份 cookie 自动续上，「登出」就成了「换个 tab 进去」——
 * 与用户预期（彻底退出）不符。
 *
 * 原生层做正确的事：调用 WKWebsiteDataStore / WebStorage 擦掉**整个 WebView
 * 的 cookie 与本地存储**，下次启动时 CookieJar 是空的，hydrate() 自然回
 * 到登录页。
 *
 * ## 为什么**不静态 import** `@capacitor/core`
 *
 * 与 `nativeTransport.ts` 同源（详见该文件头）：web-mobile 的 package.json
 * 故意不依赖 `@capacitor/core`，避免 Capacitor 拖进网页形态的构建产物。
 * 这里只按**结构类型**描述用到的 `Plugins.AuthSession.signOut`，运行时
 * 从 `window.Capacitor.Plugins.AuthSession` 取 —— 网页形态该路径为
 * undefined，自动落入 no-op 兜底。
 *
 * 协议形态（与 iOS `AuthSessionPlugin.swift`、Android `AuthSessionPlugin.java` 对齐）：
 *   - `signOut()` → `{ cleared: true, ts: string }`
 */
export interface AuthSessionSignOutResult {
  cleared: boolean
  ts: string
}

interface AuthSessionPluginShape {
  signOut: () => Promise<AuthSessionSignOutResult>
}

interface CapacitorGlobal {
  isNativePlatform?: () => boolean
  Plugins?: Record<string, unknown>
}

declare global {
  interface Window {
    Capacitor?: CapacitorGlobal
  }
}

/**
 * 原生层登出。**失败吞掉** —— 这是兜底动作；如果插件调用失败，
 * 至少上层 auth store 已经清了 localStorage/sessionStorage，
 * cookie 可能还在但下次启动 hydrate() 会发现并退到登录页。
 */
export async function nativeSignOut(): Promise<AuthSessionSignOutResult> {
  const cap = typeof window !== 'undefined' ? window.Capacitor : undefined
  const plugin = cap?.Plugins?.AuthSession as AuthSessionPluginShape | undefined
  if (!plugin || typeof plugin.signOut !== 'function') {
    return { cleared: false, ts: new Date().toISOString() }
  }
  try {
    return await plugin.signOut()
  } catch {
    return { cleared: false, ts: new Date().toISOString() }
  }
}
