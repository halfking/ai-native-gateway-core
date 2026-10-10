import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { UserInfo } from '@/api/client'
import { bumpSessionEpoch } from '@/api/client'
import * as authApi from '@/api/auth'
import type { LoginOptions } from '@/api/auth'
import { Hyper } from '@/hyper'

// 鉴权 store — 网关 web/src/store.ts 的 2026-08-26 P1-7 语义照搬：
// JWT/sk-key 只存内存（HttpOnly llmgw_session cookie 承载跨请求鉴权），
// localStorage 只存非敏感展示数据 llmgw_mobile_user。
//
// ★ 「保持登录」（2026-10-10）把存储位置也变成了二选一：
//
//	勾选   → localStorage  —— 关掉 App / 浏览器后仍在（配 24h 持久 cookie）
//	不勾选 → sessionStorage —— 关掉即失效（配会话级 cookie）
//
// 为什么用两个 storage 而不是给 localStorage 加时间戳：后端 cookie 的
// 存活期已经由 remember_me 决定了，前端再实现一套过期逻辑就成了第二份
// 事实源，两份迟早漂。sessionStorage 的原生语义「随会话消亡」正好就是
// 「不保持登录」，不需要任何自研过期逻辑。
//
// ⚠️ 两条路径共用同一个 USER_KEY 是**故意的**：任何时刻只可能有一份落盘，
// 切换模式时另一侧会被清掉（见 persistUser），否则会出现「localStorage 里
// 残留着上一次勾选的账号」这种幽灵登录态。

const USER_KEY = 'llmgw_mobile_user'
// Origin-scoped tombstone survives an offline logout without erasing node profiles.
const LOGOUT_KEY = 'llmgw_mobile_logged_out'
function wasLoggedOut(): boolean {
  try { if (localStorage.getItem(LOGOUT_KEY) === '1') return true } catch { /* unavailable */ }
  try { return sessionStorage.getItem(LOGOUT_KEY) === '1' } catch { return false }
}
function markLoggedOut(value: boolean): void {
  for (const area of [localStorage, sessionStorage]) {
    try { if (value) area.setItem(LOGOUT_KEY, '1'); else area.removeItem(LOGOUT_KEY) } catch { /* unavailable */ }
  }
}

function readUser(raw: string | null): UserInfo | null {
  if (!raw) return null
  try {
    const parsed = JSON.parse(raw) as UserInfo & { user?: UserInfo }
    if (!parsed || typeof parsed !== 'object') return null
    // ⚠️ 2026-10-06 修正：原先在这里判 `typeof parsed.username !== 'string'` 就 return null。
    // 但落盘的可能是**包裹态**（{access_token, expires_at, user:{...}}）——
    // 那时 username 在 `parsed.user.username`，顶层没有 username，
    // 于是守卫**误杀**包裹态，刷新后 userInfo 变 null，
    // 账户面板退化成「—」+「普通用户」（super_admin 被显示成普通用户）。
    //
    // 正确顺序：先按 api/auth.ts 的 unwrapMe 取出真正的 UserInfo，再校验它。
    // 顺带修掉第 22 行的逻辑洞：`wrapped.role ? parsed : wrapped.user` 在
    // 包裹态下 role 也在内层，恒走 else 分支；裸态下才看得到 role。
    const user = parsed.user && typeof parsed.user === 'object' ? parsed.user : parsed
    if (typeof user.username !== 'string') return null
    return user
  } catch {
    return null
  }
}

/**
 * 冷启动恢复。
 *
 * 返回值里的 `remember` 不是「用户勾没勾」，而是**这份落盘是从哪儿读到的**：
 *   - 从 localStorage 读到  → true  （当初勾了「保持登录」）
 *   - 从 sessionStorage 读到 → false （当初没勾）
 *   - 两边都没有            → null  （未登录）
 *
 * ⚠️ 必须把 remember 一起恢复出来。store 在每次页面刷新时重建，
 * `rememberMe` ref 会复位成 null；而 `hydrate()` 是在 store 建好之后才跑的。
 * 若不恢复，刷新一次就会走 `persistUser` 的 `?? true` 分支，
 * 把一个明确选了「不保持登录」的用户**悄悄升级**成 localStorage 持久用户
 * ——正是这个特性要避免的事。（2026-10-10 实测修正）
 */
function loadStoredUser(): { user: UserInfo | null; remember: boolean | null } {
  // localStorage（保持登录）优先，其次 sessionStorage（本次会话）。
  try {
    const persisted = readUser(localStorage.getItem(USER_KEY))
    if (persisted) return { user: persisted, remember: true }
  } catch {
    /* Safari 隐私模式等：localStorage 不可用，退到 sessionStorage */
  }
  try {
    const session = readUser(sessionStorage.getItem(USER_KEY))
    if (session) return { user: session, remember: false }
  } catch {
    /* ignore */
  }
  return { user: null, remember: null }
}

function safeSet(area: Storage, value: string): void {
  try {
    area.setItem(USER_KEY, value)
  } catch {
    /* 配额满 / 隐私模式：登录本身不依赖落盘，忽略 */
  }
}

function safeClear(area: Storage): void {
  try {
    area.removeItem(USER_KEY)
  } catch {
    /* ignore */
  }
}

export const useAuthStore = defineStore('auth', () => {
  const restored = wasLoggedOut() ? { user: null, remember: null } : loadStoredUser()
  let generation = 0
  let authAborter: AbortController | null = null
  let pendingLogout: Promise<void> | null = null
  const userInfo = ref<UserInfo | null>(restored.user)
  const token = ref('') // 内存 only
  const authHydrated = ref(false)
  /**
   * 当前登录是否勾选了「保持登录」。null = 本次会话尚未登录（未决）。
   *
   * 冷启动时从「落盘在哪个 storage」反推（见 loadStoredUser）；
   * hydrate() 复用它，避免刷新页面时把「会话级」用户悄悄升级成持久用户。
   */
  const rememberMe = ref<boolean | null>(restored.remember)

  const isAuthenticated = computed(() => userInfo.value != null)
  const role = computed(() => userInfo.value?.role ?? '')
  const accountId = computed(() => String(userInfo.value?.id ?? 'anon'))

  async function hydrate(): Promise<void> {
    authAborter?.abort()
    authAborter = new AbortController()
    const signal = authAborter.signal
    const current = ++generation
    if (wasLoggedOut() || pendingLogout) {
      userInfo.value = null
      authHydrated.value = true
      return
    }
    try {
      const me = await authApi.fetchMe({ signal })
      if (current !== generation) return
      userInfo.value = me
      persistUser(me)
    } catch {
      if (current !== generation) return
      userInfo.value = null
      token.value = ''
      safeClear(localStorage)
      safeClear(sessionStorage)
    } finally {
      if (current === generation) authHydrated.value = true
    }
  }

  async function login(username: string, password: string, options?: LoginOptions): Promise<UserInfo> {
    // A late logout response must not clear a newly issued session cookie.
    if (pendingLogout) await pendingLogout
    authAborter?.abort()
    authAborter = new AbortController()
    const current = ++generation
    const resp = await authApi.login(username, password, { ...options, signal: authAborter.signal })
    if (current !== generation) throw new Error('Login superseded')
    bumpSessionEpoch()
    markLoggedOut(false)
    token.value = resp.access_token
    userInfo.value = resp.user
    rememberMe.value = options?.rememberMe ?? false
    authHydrated.value = true
    persistUser(resp.user)
    return resp.user
  }

  /**
   * 落盘策略的唯一入口。
   *
   * remember=true  → localStorage，并清掉 sessionStorage 里的旧值
   * remember=false → sessionStorage，并清掉 localStorage 里的旧值
   * remember=null  → hydrate() 路径：沿用当前模式；会话刚启动还判不出来时，
   *                  按「保持登录」处理（旧的 app 升级过来就是这个状态）。
   */
  function persistUser(user: UserInfo): void {
    const persist = rememberMe.value ?? true
    const target: Storage = persist ? localStorage : sessionStorage
    const other: Storage = persist ? sessionStorage : localStorage
    safeSet(target, JSON.stringify(user))
    safeClear(other)
  }

  async function logout(): Promise<void> {
    if (pendingLogout) return pendingLogout
    ++generation
    authAborter?.abort()
    bumpSessionEpoch()
    markLoggedOut(true)
    token.value = ''
    userInfo.value = null
    rememberMe.value = null
    authHydrated.value = true
    safeClear(localStorage)
    safeClear(sessionStorage)
    Hyper.clearPersistence()
    const aborter = new AbortController()
    const timeout = setTimeout(() => aborter.abort(), 5000)
    pendingLogout = authApi.logout({ signal: aborter.signal }).catch(() => {
      // Offline logout stays logged out on this origin until an explicit login.
    }).finally(() => { clearTimeout(timeout); pendingLogout = null })
    return pendingLogout
  }

  function bearer(): string {
    return token.value
  }

  return {
    userInfo,
    authHydrated,
    isAuthenticated,
    role,
    accountId,
    rememberMe,
    hydrate,
    login,
    logout,
    bearer,
  }
})
