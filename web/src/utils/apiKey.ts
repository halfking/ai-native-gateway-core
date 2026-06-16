import type { ApiKey } from '../api'

export function isActiveApiKey(k: ApiKey): boolean {
  if (k.status !== 'active' || !k.enabled) return false
  if (k.expires_at && new Date(k.expires_at).getTime() <= Date.now()) return false
  return true
}

export function formatApiKeyLabel(k: ApiKey): string {
  const alias = k.key_alias?.trim()
  const prefix = k.key_prefix ? `${k.key_prefix}****` : `#${k.id}`
  if (alias) return `${alias} (${prefix})`
  return `${k.application_code} · ${prefix}`
}

export function isNoCiphertextError(message: string): boolean {
  const m = message.toLowerCase()
  return m.includes('no ciphertext') || m.includes('no encrypted key')
}
