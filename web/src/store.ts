import { reactive } from 'vue'

const KEY = 'llmgw_api_key'
// JWT_KEY removed (rule 20 §6.1): JWT now in HttpOnly cookie
const USER_KEY = 'llmgw_user_info'
const PREFERRED_CHAT_KEY_PREFIX = 'llmgw_preferred_key_id:'
const LOCALE_KEY = 'llmgw_locale'

export interface UserInfo {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
  must_change_password?: boolean
}

export const store = reactive({
  apiKey: localStorage.getItem(KEY) ?? '',
  jwtToken: '', // in-memory only, not persisted
  userInfo: JSON.parse(localStorage.getItem(USER_KEY) ?? 'null') as UserInfo | null,
  locale: localStorage.getItem(LOCALE_KEY) ?? 'zh-CN',
  // 2026-07-09: authHydrated tracks whether we've probed /api/auth/me.
  // App.vue 必须等 authHydrated=true 才能决定渲染 app-layout vs guest-layout，
  // 否则页面首次渲染会基于空 store 错判为未登录，紧接着被 router 弹回首页。
  // 详见 admin/feishu_handlers.go 同名 PR 描述。
  authHydrated: false as boolean,
})

export function setApiKey(k: string) {
  store.apiKey = k
  localStorage.setItem(KEY, k)
}

export function clearApiKey() {
  store.apiKey = ''
  localStorage.removeItem(KEY)
}

/** Per-user preferred API key id for /chat (sk-* resolved via reveal). */
export function preferredChatKeyStorageKey(): string {
  const uid = store.userInfo?.id ?? 'legacy'
  return `${PREFERRED_CHAT_KEY_PREFIX}${uid}`
}

export function getPreferredChatKeyId(): number | null {
  const raw = localStorage.getItem(preferredChatKeyStorageKey())
  if (!raw) return null
  const n = Number.parseInt(raw, 10)
  return Number.isFinite(n) && n > 0 ? n : null
}

export function setPreferredChatKeyId(id: number) {
  localStorage.setItem(preferredChatKeyStorageKey(), String(id))
}

export function clearPreferredChatKeyId() {
  localStorage.removeItem(preferredChatKeyStorageKey())
}

export function setJwtToken(token: string) {
  store.jwtToken = token || 'cookie' // transient in-memory flag
}

// Returns the token that should go into the `Authorization: Bearer` header.
// Prefers the JWT (username/password login); falls back to the legacy API key.
// Empty when logged out — callers then get a 401 and redirect to /login.
//
// All admin-API fetch wrappers MUST use this instead of reading store.apiKey
// directly: a JWT login leaves store.apiKey empty, so hardcoding store.apiKey
// sends an empty bearer and 401s every admin endpoint. See api-autoroute.ts,
// api-work-types.ts, PricingManagementView.vue.
export function authBearer(): string {
  if (store.jwtToken) return '' // JWT via cookie
  return store.apiKey || ''
}

export function setUserInfo(user: UserInfo | null) {
  store.userInfo = user
  if (user) {
    localStorage.setItem(USER_KEY, JSON.stringify(user))
  } else {
    localStorage.removeItem(USER_KEY)
  }
}

export function clearMustChangePasswordFlag() {
  if (!store.userInfo) return
  setUserInfo({
    ...store.userInfo,
    must_change_password: false,
  })
}

export function clearJwt() {
  store.jwtToken = ''
  store.userInfo = null
  localStorage.removeItem(USER_KEY)
}

// 2026-07-09: 标记 auth hydration 完成。App.vue 首次进入 onMounted 时调用，
// 防止页面在 auth probe 完成前误判为未登录。
export function markAuthHydrated() {
  store.authHydrated = true
}

// 登出 / 401 时重置 hydration 标志位，强制下一次进入 / 重渲染时重新探测。
export function resetAuthHydrated() {
  store.authHydrated = false
}

export function clearAll() {
  clearApiKey()
  clearJwt()
}

// Returns true if we have any valid auth credential (JWT or legacy API key).
export function isAuthenticated(): boolean {
  return !!(store.jwtToken || store.userInfo || store.apiKey)
}

// Returns true if current user is super_admin
// For JWT users: checks role === 'super_admin'
// For legacy API key users (no JWT, only apiKey): treated as super_admin
export function isSuperAdmin(): boolean {
  // Legacy API key auth: no JWT but has apiKey → super_admin
  if (!store.jwtToken && store.apiKey) return true
  // JWT auth: check role
  return store.userInfo?.role === 'super_admin'
}

// Returns true if current user is tenant_admin
export function isTenantAdmin(): boolean {
  if (!store.jwtToken && store.apiKey) return false // legacy API key is super_admin
  return store.userInfo?.role === 'tenant_admin'
}

// Returns true if current user is read-only (non-default tenant tenant_admin)
export function isReadOnlyMode(): boolean {
  return isTenantAdmin() && !isDefaultTenant()
}

// Returns true if current tenant is default (整站数据)
export function isDefaultTenant(): boolean {
  // If no user info (not logged in), treat as default tenant
  if (!store.userInfo) return true
  return store.userInfo.tenant_id === 'default'
}

// Returns current tenant ID or 'default'
export function getCurrentTenantId(): string {
  return store.userInfo?.tenant_id || 'default'
}

// Platform ops UI: super_admin on default tenant (整站运维视图)
export function isPlatformOpsView(): boolean {
  return isSuperAdmin() && isDefaultTenant()
}

// Locale management
export function setLocale(locale: string) {
  store.locale = locale
  localStorage.setItem(LOCALE_KEY, locale)
}

export function getLocale(): string {
  return store.locale
}
