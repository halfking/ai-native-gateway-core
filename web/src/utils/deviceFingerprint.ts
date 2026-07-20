/** Browser-side device fingerprint helpers for local bootstrap activation. */

const STORAGE_HASH = 'llmgw_hardware_hash'
const STORAGE_INSTANCE = 'llmgw_instance_id'

function bytesToHex(buf: ArrayBuffer): string {
  return Array.from(new Uint8Array(buf))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

/** Stable-ish client fingerprint (hash only — never upload raw hardware ids). */
export async function collectClientFingerprint(): Promise<{ hardware_hash: string; network_summary: string }> {
  const nav = typeof navigator !== 'undefined' ? navigator : ({} as Navigator)
  const screenInfo = typeof screen !== 'undefined' ? `${screen.width}x${screen.height}x${screen.colorDepth}` : ''
  const parts = [
    nav.userAgent || '',
    nav.language || '',
    String(nav.hardwareConcurrency || ''),
    String((nav as Navigator & { deviceMemory?: number }).deviceMemory || ''),
    screenInfo,
    Intl.DateTimeFormat().resolvedOptions().timeZone || '',
    localStorage.getItem(STORAGE_INSTANCE) || '',
  ]
  const raw = parts.join('|')
  let hardware_hash = ''
  if (typeof crypto !== 'undefined' && crypto.subtle) {
    const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(raw))
    hardware_hash = bytesToHex(digest).slice(0, 32)
  } else {
    // Fallback: crude hash
    let h = 0
    for (let i = 0; i < raw.length; i++) h = (Math.imul(31, h) + raw.charCodeAt(i)) | 0
    hardware_hash = `web_${Math.abs(h).toString(16).padStart(8, '0')}`
  }
  try {
    localStorage.setItem(STORAGE_HASH, hardware_hash)
  } catch {
    /* ignore */
  }
  const network_summary = [
    (nav as Navigator & { connection?: { effectiveType?: string } }).connection?.effectiveType || 'unknown',
    typeof location !== 'undefined' ? location.hostname : '',
  ].join(' / ')
  return { hardware_hash, network_summary }
}

export function readStoredHardwareHash(): string {
  try {
    return localStorage.getItem(STORAGE_HASH) || ''
  } catch {
    return ''
  }
}

export function ensureInstanceId(): string {
  try {
    let id = localStorage.getItem(STORAGE_INSTANCE) || localStorage.getItem('maintain_instance_id') || ''
    if (!id) {
      id = `gw-${Math.random().toString(36).slice(2, 10)}`
      localStorage.setItem(STORAGE_INSTANCE, id)
      localStorage.setItem('maintain_instance_id', id)
    }
    return id
  } catch {
    return `gw-${Date.now().toString(36)}`
  }
}

/** Prefer server fingerprint when available; fall back to browser hash. */
export async function resolveHardwareHash(serverHash?: string | null): Promise<string> {
  if (serverHash && serverHash.trim()) {
    try {
      localStorage.setItem(STORAGE_HASH, serverHash.trim())
    } catch {
      /* ignore */
    }
    return serverHash.trim()
  }
  const stored = readStoredHardwareHash()
  if (stored) return stored
  const { hardware_hash } = await collectClientFingerprint()
  return hardware_hash
}
