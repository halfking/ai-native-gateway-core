/**
 * Dual-mode product edition + runtime Maintain detection.
 *
 * - Customer / gateway console (default): llm-gateway-go SPA is the
 *   post-login admin UI. Ops center lives in Maintain SPA and is NOT
 *   present in Gateway menus unless Maintain is reachable.
 * - Runtime probe: GET /maintain-api/healthz — when ai-native-maintain
 *   is co-deployed (local or same-origin), inject the ops-center group.
 * - Override: VITE_SHOW_OPS_PLATFORM=true|false forces show/hide.
 */

export const MAINTAIN_API_BASE = String(import.meta.env.VITE_MAINTAIN_API_BASE || '/maintain-api')

type TriState = 'unknown' | 'available' | 'unavailable'

let maintainState: TriState = 'unknown'
let probePromise: Promise<boolean> | null = null
const listeners = new Set<() => void>()

function envOpsOverride(): boolean | null {
  const raw = String(import.meta.env.VITE_SHOW_OPS_PLATFORM || '').toLowerCase()
  if (raw === 'true' || raw === '1' || raw === 'yes') return true
  if (raw === 'false' || raw === '0' || raw === 'no') return false
  return null
}

/** Whether the ops-center nav group should be visible right now. */
export function showOpsPlatform(): boolean {
  const forced = envOpsOverride()
  if (forced !== null) return forced
  return maintainState === 'available'
}

export function isMaintainProbed(): boolean {
  return maintainState !== 'unknown' || envOpsOverride() !== null
}

export function onMaintainAvailabilityChange(cb: () => void): () => void {
  listeners.add(cb)
  return () => listeners.delete(cb)
}

function notify() {
  listeners.forEach((cb) => {
    try {
      cb()
    } catch {
      /* ignore listener errors */
    }
  })
}

/**
 * Probe same-origin Maintain health. Safe to call repeatedly; caches result.
 * Returns true when ai-native-maintain is reachable.
 */
export async function probeMaintainAvailable(force = false): Promise<boolean> {
  const forced = envOpsOverride()
  if (forced !== null) {
    maintainState = forced ? 'available' : 'unavailable'
    notify()
    return forced
  }

  if (!force && maintainState !== 'unknown') {
    return maintainState === 'available'
  }
  if (!force && probePromise) return probePromise

  probePromise = (async () => {
    const controller = typeof AbortController !== 'undefined' ? new AbortController() : null
    const timer = controller ? window.setTimeout(() => controller.abort(), 2500) : 0
    try {
      const res = await fetch(`${MAINTAIN_API_BASE}/healthz`, {
        method: 'GET',
        credentials: 'omit',
        cache: 'no-store',
        signal: controller?.signal,
      })
      maintainState = res.ok ? 'available' : 'unavailable'
    } catch {
      maintainState = 'unavailable'
    } finally {
      if (timer) window.clearTimeout(timer)
    }
    notify()
    return maintainState === 'available'
  })()

  return probePromise
}
