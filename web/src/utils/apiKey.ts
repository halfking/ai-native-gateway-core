import type { ApiKey } from '../api'
import {
  getCurrentTenantId,
  isDefaultTenant,
  isSuperAdmin,
  store,
} from '../store'

export function isActiveApiKey(k: ApiKey): boolean {
  if (k.status !== 'active' || !k.enabled) return false
  if (k.expires_at && new Date(k.expires_at).getTime() <= Date.now()) return false
  return true
}

/** Active keys scoped to the logged-in user (tenant + owner). */
export function filterKeysForCurrentUser(keys: ApiKey[]): ApiKey[] {
  const active = keys.filter(isActiveApiKey)
  if (!store.jwtToken) return active

  const tenantId = getCurrentTenantId()
  const username = store.userInfo?.username?.trim() ?? ''

  return active.filter((k) => {
    if (k.tenant_id !== tenantId) return false
    const owner = (k.owner_user || '').trim()
    if (!username) return true
    if (isSuperAdmin() && isDefaultTenant()) {
      if (!owner || owner === 'admin' || owner === username) return true
      return false
    }
    return !owner || owner === username
  })
}

export function formatApiKeyLabel(k: ApiKey): string {
  const alias = k.key_alias?.trim()
  const prefix = k.key_prefix ? `${k.key_prefix}****` : `#${k.id}`
  if (alias) return `${alias} (${prefix})`
  return `${k.application_code} · ${prefix}`
}

export function isNoCiphertextError(message: string): boolean {
  const m = message.toLowerCase()
  return (
    m.includes('no ciphertext') ||
    m.includes('no encrypted key') ||
    m.includes('decryption failed') ||
    m.includes('cannot decrypt stored key')
  )
}

/** Reveal endpoint returned a non-fatal failure (skip key, try next). */
export function isRevealFailureError(message: string): boolean {
  const m = message.toLowerCase()
  return (
    isNoCiphertextError(m) ||
    m.includes('decryption failed') ||
    m.includes('cannot decrypt')
  )
}
