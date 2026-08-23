import { computed, ref, type Ref } from 'vue'
import { getCredentialMonitorSummary } from '../api/credential-monitor'
import { getCurrentTenantId } from '../store'

const LABEL_TTL_MS = 60_000

// 2026-08-23 (Agent C): credential label cache is now per-tenant. The
// earlier module-level `Map<number, string>` leaked across user sessions
// in two ways:
//
//   1. Two logged-in users in the same browser session shared one map, so
//      the previous tenant's labels survived past logout until the next
//      load() finished.
//   2. Cross-tenant operators (e.g. support / on-call engineer hopping
//      between customers) saw the previous tenant's labels until the
//      monitor-summary query for the new tenant resolved.
//
// Per-tenant sub-maps solve both: a tenant switch immediately swaps
// labels to the new tenant's map; an explicit clearCredentialLabels(A)
// only nukes A's slot. `loadedAt` / `pending` are tracked per tenant so
// concurrent loads on different tenants dedupe independently.

interface TenantLabelState {
  // labels is a ref<Map<number, string>> rather than a plain Map so that
  // swapping the map on a successful load triggers Vue reactivity. A
  // bare Map's internal mutation (.set / .clear) is not tracked by Vue,
  // so without this ref the template would never refresh after
  // loadCredentialLabels() resolved.
  labels: Ref<Map<number, string>>
  loadedAt: number
  pending: Promise<void> | null
}

const tenantCaches = new Map<string, TenantLabelState>()
const revision = ref(0)
const EMPTY_LABELS: ReadonlyMap<number, string> = new Map()

// 2026-08-23 (Agent C): test seam — vitest overrides
// currentTenantIdResolver so it does not have to mutate the global auth
// store (the store reads from localStorage and persists across tests).
let currentTenantIdResolver: () => string = () => {
  try {
    return getCurrentTenantId()
  } catch {
    return 'default'
  }
}

/** @internal — exported for the unit test only. */
export function __setCurrentTenantIdResolver(fn: () => string): void {
  currentTenantIdResolver = fn
}

export function currentTenantId(): string {
  return currentTenantIdResolver()
}

function normalizeLabel(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function getOrCreateTenantCache(tenantId: string): TenantLabelState {
  let cache = tenantCaches.get(tenantId)
  if (!cache) {
    cache = {
      labels: ref<Map<number, string>>(new Map()),
      loadedAt: 0,
      pending: null,
    }
    tenantCaches.set(tenantId, cache)
  }
  return cache
}

function bumpRevision(): void {
  revision.value++
}

export function credentialIdFromSyntheticModel(modelName: string): number | null {
  if (!modelName.startsWith('cred-')) return null
  const raw = modelName.slice('cred-'.length)
  if (!/^\d+$/.test(raw)) return null
  const id = Number(raw)
  return Number.isSafeInteger(id) && id > 0 ? id : null
}

export function credentialLabelForId(id: number | null | undefined): string | null {
  if (id == null || !Number.isFinite(id) || id <= 0) return null
  const cache = tenantCaches.get(currentTenantId())
  if (!cache) return null
  // Track the ref's identity so a successful load (which swaps .value to
  // a new Map) invalidates any computed/template reading through it.
  void cache.labels.value
  return cache.labels.value.get(id) || null
}

export function credentialDisplayName(
  id: number | null | undefined,
  prefix = '凭据',
): string {
  if (id == null || !Number.isFinite(id) || id <= 0) return '—'
  const label = credentialLabelForId(id)
  if (label) return label
  return `${prefix} #${id}`
}

export function displaySyntheticCredentialModel(modelName: string): string {
  const id = credentialIdFromSyntheticModel(modelName)
  return id == null ? modelName : credentialDisplayName(id)
}

/**
 * Load (or refresh) the credential label cache for the given tenant.
 * Returns a shared in-flight promise so concurrent callers dedupe on a
 * per-tenant basis. Pass `force=true` to ignore the TTL and refetch.
 *
 * Pass `tenantId` explicitly when the caller has already resolved the
 * current tenant (so a fast tenant switch mid-call doesn't race against
 * the auth store). When omitted, the current tenant from the auth store
 * is used.
 */
export function loadCredentialLabelsForTenant(
  tenantId: string,
  force = false,
): Promise<void> {
  const cache = getOrCreateTenantCache(tenantId)
  const fresh = !force && cache.loadedAt > 0 && Date.now() - cache.loadedAt < LABEL_TTL_MS
  if (fresh) return Promise.resolve()
  if (cache.pending) return cache.pending

  cache.pending = getCredentialMonitorSummary({ mode: 'core' })
    .then((response) => {
      // Race guard: if `clearCredentialLabels(tenantId)` ran while this
      // fetch was in flight, the tenant slot may have been dropped (and
      // even re-created with a fresh TenantLabelState for the next
      // caller). The closure above still holds the *original* `cache`
      // object, so writing to it would clobber labels for whoever owns
      // the slot now. Skip the write in that case.
      if (tenantCaches.get(tenantId) !== cache) return

      const next = new Map<number, string>()
      for (const credential of response.credentials ?? []) {
        const id = Number(credential.id)
        const label = normalizeLabel(credential.label)
        if (Number.isSafeInteger(id) && id > 0 && label) next.set(id, label)
      }
      // Replace the ref's value rather than mutating the existing Map so
      // Vue's reactivity fires for every consumer that read .value.
      cache.labels.value = next
      cache.loadedAt = Date.now()
      bumpRevision()
    })
    .catch(() => {
      // A label lookup is best-effort; callers retain the numeric fallback.
    })
    .finally(() => {
      cache.pending = null
    })

  return cache.pending
}

/**
 * Backwards-compatible helper that loads labels for the *current* tenant
 * (per `getCurrentTenantId()` from the auth store). Pre-existing call
 * sites (`App.vue`, `useCredentialLabels()` consumers) keep working
 * without changes.
 */
export function loadCredentialLabels(force = false): Promise<void> {
  return loadCredentialLabelsForTenant(currentTenantId(), force)
}

/**
 * Clear the credential label cache.
 *
 *   - Without arguments: drop every tenant's cache (logout / 401).
 *   - With a tenantId:   drop only that tenant's slot (targeted refresh).
 *
 * Returns true when something was actually cleared (callers can no-op
 * on false without churning the reactivity system).
 */
export function clearCredentialLabels(tenantId?: string): boolean {
  if (tenantId === undefined) {
    if (tenantCaches.size === 0) return false
    tenantCaches.clear()
    bumpRevision()
    return true
  }
  const cache = tenantCaches.get(tenantId)
  if (!cache) return false
  // Drop any pending in-flight load so the next call after a clear
  // issues a fresh request instead of returning the stale pending promise.
  if (cache.pending) {
    // Swallow the rejection — the caller has already decided to clear.
    cache.pending.catch(() => {})
    cache.pending = null
  }
  cache.labels.value = new Map()
  cache.loadedAt = 0
  tenantCaches.delete(tenantId)
  bumpRevision()
  return true
}

export function useCredentialLabels() {
  const labelRevision = computed(() => revision.value)
  // `labels` is a getter (not a computed) on purpose: a computed would
  // memoize the resolved Map based on whichever ref it tracked on the
  // first read. After a per-tenant clear + reload cycle, the underlying
  // ref object gets replaced with a new one, and a memoized computed
  // would still hand back the old empty Map. The getter pays the
  // tenant-resolution + Map deref cost each read — negligible, and
  // exactly what callers expect when they iterate the current tenant's
  // labels.
  const labels = {
    get value(): ReadonlyMap<number, string> {
      const cache = tenantCaches.get(currentTenantId())
      // Touch the ref so consumers running inside an effect scope pick
      // up future .value swaps.
      return cache?.labels.value ?? EMPTY_LABELS
    },
  }
  return {
    labels,
    labelRevision,
    credentialLabelForId,
    credentialDisplayName,
    credentialIdFromSyntheticModel,
    displaySyntheticCredentialModel,
    loadCredentialLabels,
    loadCredentialLabelsForTenant,
    clearCredentialLabels,
    currentTenantId,
  }
}