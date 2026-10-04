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
    const parsed = JSON.parse(raw) as UserInfo
    if (!parsed || typeof parsed.username !== 'string') return null
    // 兼容旧包裹形态 {user: {...}}
    const wrapped = parsed as UserInfo & { user?: UserInfo }
    return wrapped.role ? parsed : (wrapped.user ?? null)
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
