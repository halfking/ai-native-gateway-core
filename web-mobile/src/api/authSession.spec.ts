import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { nativeSignOut } from './authSession'

/**
 * AuthSession native plugin integration（2026-10-10）。
 *
 * 业务背景：移动壳内 web-mobile 登出擦不掉远程 origin 的 HttpOnly cookie —
 * 那是 CookieJar 由 origin 通过 Set-Cookie 注入、JS 不可达。让原生层把整
 * WebView 的 cookie + Local/SessionStorage 一起清掉，下次启动 CookieJar
 * 是空的，hydrate() 自然退到登录页。
 *
 * ★ 判据：
 *  (1) 原生容器有 Capacitor.Plugins.AuthSession → 必须调用并 resolve
 *  (2) 原生容器无该插件 → no-op（resolved），不抛
 *  (3) 原生插件 reject → 必须吞掉（兜底由 localStorage/sessionStorage 那一步完成）
 *  (4) 浏览器形态（无 Capacitor）→ 同样 no-op，不抛
 */

declare global {
  // eslint-disable-next-line no-var
  var Capacitor: unknown | undefined
}

beforeEach(() => {
  vi.restoreAllMocks()
})

afterEach(() => {
  delete (globalThis as { Capacitor?: unknown }).Capacitor
})

describe('nativeSignOut', () => {
  it('浏览器形态（无 Capacitor）→ no-op + 返回 cleared=false + 当前 ISO 时间戳', async () => {
    delete (globalThis as { Capacitor?: unknown }).Capacitor
    const r = await nativeSignOut()
    expect(r.cleared).toBe(false)
    expect(new Date(r.ts).toISOString()).toBe(r.ts)
  })

  it('原生容器无 AuthSession 插件 → no-op + 返回 cleared=false', async () => {
    ;(globalThis as { Capacitor?: unknown }).Capacitor = {
      isNativePlatform: () => true,
      Plugins: {},
    }
    const r = await nativeSignOut()
    expect(r.cleared).toBe(false)
  })

  it('原生容器有 AuthSession 插件 → 调用 signOut 并 resolve 返回值', async () => {
    const signOut = vi.fn().mockResolvedValue({ cleared: true, ts: '2026-10-10T01:02:03Z' })
    ;(globalThis as { Capacitor?: unknown }).Capacitor = {
      isNativePlatform: () => true,
      Plugins: { AuthSession: { signOut } },
    }
    const r = await nativeSignOut()
    expect(signOut).toHaveBeenCalledTimes(1)
    expect(r.cleared).toBe(true)
    expect(r.ts).toBe('2026-10-10T01:02:03Z')
  })

  it('原生插件 reject → 必须吞掉，**不抛**，兜底仍返回 cleared=false', async () => {
    const signOut = vi.fn().mockRejectedValue(new Error('plugin not available'))
    ;(globalThis as { Capacitor?: unknown }).Capacitor = {
      isNativePlatform: () => true,
      Plugins: { AuthSession: { signOut } },
    }
    const r = await nativeSignOut()
    expect(signOut).toHaveBeenCalledTimes(1)
    expect(r.cleared).toBe(false)
    // ts 仍是当前 ISO 时间
    expect(typeof r.ts).toBe('string')
    expect(() => new Date(r.ts).toISOString()).not.toThrow()
  })

  it('插件对象存在但 signOut 不是函数 → 当作 no-plugin 处理，不抛', async () => {
    ;(globalThis as { Capacitor?: unknown }).Capacitor = {
      isNativePlatform: () => true,
      Plugins: { AuthSession: {} },
    }
    const r = await nativeSignOut()
    expect(r.cleared).toBe(false)
  })
})