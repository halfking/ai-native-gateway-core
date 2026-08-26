import { reactive } from 'vue'

// 2026-08-26 (P1-28 fix): api-autoroute.ts holds a module-level sk-*
// relay cache. store.ts cannot import from api-autoroute.ts at module
// load time (would create a cycle: api-autoroute.ts already imports
// `store` + `authBearer` from here). Instead, api-autoroute.ts
// registers a synchronous invalidator on globalThis at its own module
// init, which we call here on every auth-state mutation so the cache
// cannot survive a logout / tenant swap / apiKey rotation.
declare global {
  // eslint-disable-next-line no-var
  var __llmgwRelayCacheInvalidator: (() => void) | undefined
}
function relayCacheInvalidator(): void {
  try {
    globalThis.__llmgwRelayCacheInvalidator?.()
  } catch {
    /* api-autoroute.ts not yet loaded — nothing to clear. */
  }
}

const KEY = 'llmgw_api_key'
const JWT_KEY = 'llmgw_jwt' // Real JWT persisted to localStorage for Bearer header
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

function normalizeUserInfo(raw: UserInfo | null): UserInfo | null {
  if (!raw) return null
  const wrapped = raw as UserInfo & { user?: UserInfo }
  if (!wrapped.role && wrapped.user?.role) {
    return wrapped.user
  }
  return raw
}

function loadStoredUserInfo(): UserInfo | null {
  try {
    return normalizeUserInfo(JSON.parse(localStorage.getItem(USER_KEY) ?? 'null'))
  } catch {
    return null
  }
}

export const store = reactive({
  apiKey: localStorage.getItem(KEY) ?? '',
  jwtToken: localStorage.getItem(JWT_KEY) ?? '', // Real JWT, persisted for Bearer header
  userInfo: loadStoredUserInfo(),
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
  // 2026-08-26 (P1-28 fix): the admin SPA ships a module-level sk-*
  // relay cache (web/src/api-autoroute.ts). Anything that mutates the
  // bearer must drop it, otherwise logging out as tenant A and logging
  // back in as tenant B reuses tenant A's revealed key. The import is
  // cycle-free: api-autoroute.ts imports from this file (the `store`
  // reactive singleton) but never the other way around, so a direct
  // top-level import would not work — we use a sync lazy require via
  // a globalThis-registered hook set up by api-autoroute.ts at module
  // load time.
  relayCacheInvalidator?.()
}

export function clearApiKey() {
  store.apiKey = ''
  localStorage.removeItem(KEY)
  relayCacheInvalidator()
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
  store.jwtToken = token
  if (token) {
    localStorage.setItem(JWT_KEY, token)
  } else {
    localStorage.removeItem(JWT_KEY)
  }
  // 2026-08-26 (P1-28 fix): drop the per-tenant sk-* relay cache when
  // the bearer changes. See setApiKey() above for the rationale.
  relayCacheInvalidator()
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
  return store.jwtToken || store.apiKey || ''
}

export function setUserInfo(user: UserInfo | null) {
  const normalized = normalizeUserInfo(user)
  // 2026-08-26 (P1-28 fix): a tenant or user-id swap must drop the sk-*
  // relay cache, otherwise the new user inherits the previous user's
  // revealed key in the same SPA mount.
  const prevTenant = store.userInfo?.tenant_id
  const prevUserId = store.userInfo?.id
  store.userInfo = normalized
  if (normalized) {
    localStorage.setItem(USER_KEY, JSON.stringify(normalized))
  } else {
    localStorage.removeItem(USER_KEY)
  }
  const tenantChanged = prevTenant !== normalized?.tenant_id
  const userChanged = prevUserId !== normalized?.id
  if (tenantChanged || userChanged) {
    relayCacheInvalidator()
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
  localStorage.removeItem(JWT_KEY)
  localStorage.removeItem(USER_KEY)
  // 2026-08-26 (P1-28 fix): drop the sk-* relay cache on logout so the
  // next user (possibly on a different tenant) never inherits the
  // previous user's revealed key.
  relayCacheInvalidator()
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

// Can access maintain service: only default tenant can see maintain/ops menu
// Maintain service is for platform operators only (not for other tenants)
export function canAccessMaintain(): boolean {
  return isDefaultTenant()
}

// Locale management
export function setLocale(locale: string) {
  store.locale = locale
  localStorage.setItem(LOCALE_KEY, locale)
}

export function getLocale(): string {
  return store.locale
}
