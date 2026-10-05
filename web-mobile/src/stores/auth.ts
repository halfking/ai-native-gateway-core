import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { UserInfo } from '@/api/client'
import { bumpSessionEpoch } from '@/api/client'
import * as authApi from '@/api/auth'
import { Hyper } from '@/hyper'

// 鉴权 store — 网关 web/src/store.ts 的 2026-08-26 P1-7 语义照搬：
// JWT/sk-key 只存内存（HttpOnly llmgw_session cookie 承载跨请求鉴权），
// localStorage 只存非敏感展示数据 llmgw_mobile_user。

const USER_KEY = 'llmgw_mobile_user'

function loadStoredUser(): UserInfo | null {
  try {
    const raw = localStorage.getItem(USER_KEY)
    if (!raw) return null
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

export const useAuthStore = defineStore('auth', () => {
  const userInfo = ref<UserInfo | null>(loadStoredUser())
  const token = ref('') // 内存 only
  const authHydrated = ref(false)

  const isAuthenticated = computed(() => userInfo.value != null)
  const role = computed(() => userInfo.value?.role ?? '')
  const accountId = computed(() => String(userInfo.value?.id ?? 'anon'))

  async function hydrate(): Promise<void> {
    try {
      const me = await authApi.fetchMe()
      userInfo.value = me
      persistUser(me)
    } catch {
      userInfo.value = null
      token.value = ''
      try {
        localStorage.removeItem(USER_KEY)
      } catch {
        /* ignore */
      }
    } finally {
      authHydrated.value = true
    }
  }

  async function login(username: string, password: string): Promise<UserInfo> {
    const resp = await authApi.login(username, password)
    bumpSessionEpoch()
    token.value = resp.access_token
    userInfo.value = resp.user
    persistUser(resp.user)
    return resp.user
  }

  function persistUser(user: UserInfo): void {
    try {
      localStorage.setItem(USER_KEY, JSON.stringify(user))
    } catch {
      /* ignore */
    }
  }

  async function logout(): Promise<void> {
    try {
      await authApi.logout()
    } catch {
      /* 后端不可达也照样清本地 */
    }
    bumpSessionEpoch()
    token.value = ''
    userInfo.value = null
    try {
      localStorage.removeItem(USER_KEY)
    } catch {
      /* ignore */
    }
    // 06 §3：登出/换号清空导航持久化（scope 隔离）
    Hyper.clearPersistence()
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
    hydrate,
    login,
    logout,
    bearer,
  }
})
