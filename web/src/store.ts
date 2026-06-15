import { reactive } from 'vue'

const KEY = 'llmgw_api_key'
const JWT_KEY = 'llmgw_jwt_token'
const USER_KEY = 'llmgw_user_info'

export interface UserInfo {
  id: number
  tenant_id: string
  username: string
  display_name: string
  email: string
  role: string
  enabled: boolean
}

export const store = reactive({
  apiKey: localStorage.getItem(KEY) ?? '',
  jwtToken: localStorage.getItem(JWT_KEY) ?? '',
  userInfo: JSON.parse(localStorage.getItem(USER_KEY) ?? 'null') as UserInfo | null,
})

export function setApiKey(k: string) {
  store.apiKey = k
  localStorage.setItem(KEY, k)
}

export function clearApiKey() {
  store.apiKey = ''
  localStorage.removeItem(KEY)
}

export function setJwtToken(token: string) {
  store.jwtToken = token
  localStorage.setItem(JWT_KEY, token)
}

export function setUserInfo(user: UserInfo | null) {
  store.userInfo = user
  if (user) {
    localStorage.setItem(USER_KEY, JSON.stringify(user))
  } else {
    localStorage.removeItem(USER_KEY)
  }
}

export function clearJwt() {
  store.jwtToken = ''
  store.userInfo = null
  localStorage.removeItem(JWT_KEY)
  localStorage.removeItem(USER_KEY)
}

export function clearAll() {
  clearApiKey()
  clearJwt()
}

// Returns true if we have any valid auth credential (JWT or legacy API key).
export function isAuthenticated(): boolean {
  return !!(store.jwtToken || store.apiKey)
}

// Returns true if current user is super_admin or legacy admin key
export function isSuperAdmin(): boolean {
  // Only check JWT role; legacy admin_key auth is no longer supported
  return store.userInfo?.role === 'super_admin'
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
