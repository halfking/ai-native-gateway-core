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

/**
 * Whether the ops-center nav group should be visible right now.
 *
 * audit 2026-07-22 (A): 探测到 maintain 可达时，运维中心菜单优先用 maintain
 * 远程 `/maintain-api/menu/ops` 返回的超集；否则降级到本地 6 项兜底，
 * vibecoding 始终由本地菜单挂载（不在 maintain 描述里）。
 */
export function showOpsPlatform(): boolean {
  const forced = envOpsOverride()
  if (forced !== null) return forced
  return maintainState === 'available'
}

/**
 * Core node = ai-native-maintain co-deployed (same-origin healthz OK).
 * Super-admin + core node → full「运维中心」; otherwise customer「更新与激活」.
 */
export function isCoreNode(): boolean {
  return showOpsPlatform()
}

/** Cached remote ops menu (maintain). `null` while loading or when absent. */
let remoteOpsMenu: OpsMenuGroup[] | null = null
let remoteOpsMenuAt = 0
let remoteOpsMenuPromise: Promise<OpsMenuGroup[] | null> | null = null
const REMOTE_OPS_MENU_TTL_MS = 5 * 60_000

export type OpsMenuItem = {
  path: string
  label: string
  labelKey?: string
  icon?: string
  super?: boolean
  hide_for_tenant?: boolean
  external?: boolean
  sort?: number
}

export type OpsMenuGroup = {
  id: string
  label: string
  items: OpsMenuItem[]
}

export type OpsMenuDocument = { version: number; groups: OpsMenuGroup[] }

async function loadRemoteOpsMenu(force = false): Promise<OpsMenuGroup[] | null> {
  if (!showOpsPlatform()) return null
  const now = Date.now()
  if (!force && remoteOpsMenu && now - remoteOpsMenuAt < REMOTE_OPS_MENU_TTL_MS) {
    return remoteOpsMenu
  }
  if (!force && remoteOpsMenuPromise) return remoteOpsMenuPromise
  remoteOpsMenuPromise = (async () => {
    try {
      const res = await fetch(`${MAINTAIN_API_BASE}/menu/ops?tenant_id=default`, {
        credentials: 'omit',
        cache: 'no-store',
      })
      if (!res.ok) return null
      const doc = (await res.json()) as OpsMenuDocument
      remoteOpsMenu = doc.groups || []
      remoteOpsMenuAt = Date.now()
      return remoteOpsMenu
    } catch {
      return null
    } finally {
      remoteOpsMenuPromise = null
    }
  })()
  return remoteOpsMenuPromise
}

/** Local fallback menu (when maintain is unreachable or remote menu not yet fetched). */
export const LOCAL_OPS_MENU: OpsMenuGroup[] = [
  {
    id: 'opsplatform',
    label: '运维中心',
    items: [
      { path: '/maintain/ops/overview', label: '运维总览', icon: '🧭', super: true, hide_for_tenant: true, external: true },
      { path: '/maintain/ops/center', label: '中心运维', icon: '🖥️', super: true, hide_for_tenant: true, external: true },
      { path: '/maintain/ops/downloads', label: '发布与下载', icon: '📦', super: true, hide_for_tenant: true, external: true },
      { path: '/maintain/ops/licenses', label: 'License 管理', icon: '🔑', super: true, hide_for_tenant: true, external: true },
      { path: '/maintain/ops/faults', label: '故障管理', icon: '⚠️', super: true, hide_for_tenant: true, external: true },
      { path: '/maintain/ops/autoupdate', label: '自动更新', icon: '🛰️', super: true, hide_for_tenant: true, external: true },
    ],
  },
]

/** Resolve the ops-center menu (remote superset if available, else local fallback). */
export async function resolveOpsMenu(): Promise<OpsMenuGroup[]> {
  const remote = await loadRemoteOpsMenu()
  if (remote && remote.length) return remote
  return LOCAL_OPS_MENU
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
