// liveStreamPreferences.ts — dashboard /live-stream 偏好持久化
//
// 2026-08-24 LP7: 复用 LP6 useChatSessions debounce pattern —
// 300ms coalesce + lifecycle flush + snapshot short-circuit + cleanup。
// LP9 (2026-08-24): 持久化原语（debounce timer / snapshot short-circuit /
// lifecycle listeners / scope cleanup）抽到 persistenceShared.ts，
// useChatSessions / usePersistedValue 共享同一份实现。本文件保留
// user + tenant-scoped key generation + legacy migration + dual-write
// side-effect + module-level cache 这些 usePersistedValue 容纳不下的
// 业务逻辑。

import type { GroupByDimension, SwimLaneMode } from '../types/swimlane'
import { getCurrentTenantId, store } from '../store'
import {
  createDebouncedFlush,
  installLifecycleFlush,
  installScopeCleanup,
  PERSIST_DEBOUNCE_MS,
} from './persistenceShared'

const LIVE_STREAM_PREFERENCES_STORAGE_KEY_PREFIX = 'llmgw_live_stream_preferences_v1'
const LEGACY_LIVE_STREAM_PREFERENCES_STORAGE_KEY = 'llmgw_live_stream_preferences_v1'
const LEGACY_SWIM_LANE_MODE_STORAGE_KEY = 'llmgw_swimlane_mode'

/** Current user + tenant-scoped key. Browser users never inherit each other's filters. */
export function liveStreamPreferencesStorageKey(): string {
  const userID = store.userInfo?.id ?? 'legacy'
  const tenantID = getCurrentTenantId().replace(/[^a-zA-Z0-9._-]/g, '_') || 'default'
  return `${LIVE_STREAM_PREFERENCES_STORAGE_KEY_PREFIX}:${userID}:${tenantID}`
}

/** Builds a user + tenant scoped key for other overview preferences. */
export function dashboardPreferenceStorageKey(suffix: string): string {
  const userID = store.userInfo?.id ?? 'legacy'
  const tenantID = getCurrentTenantId().replace(/[^a-zA-Z0-9._-]/g, '_') || 'default'
  return `llmgw_dashboard:${suffix}:${userID}:${tenantID}`
}

export type LiveStreamRequestType = 'business' | 'probe'
export type QueueStatusBucket = 'active' | 'degraded' | 'manualDisabled' | 'exhausted'

export interface LiveStreamFilterPreferences {
  requestTypes: LiveStreamRequestType[]
  statuses: string[]
  models: string[]
  providers: string[]
  vendors: string[]
  agents: string[]
}

export interface QueuePerspectivePreferences {
  /** Undefined means the user has never chosen a depth panel state. */
  depthOpen?: boolean
  expandedModels: string[]
  statusFilter: Record<QueueStatusBucket, boolean>
}

export interface LiveStreamPreferences {
  version: 1
  groupBy: GroupByDimension
  mode: SwimLaneMode
  selectedLegends: string[]
  filters: LiveStreamFilterPreferences
  queue: QueuePerspectivePreferences
}

export type LiveStreamPreferencesPatch = Partial<Pick<LiveStreamPreferences, 'groupBy' | 'mode' | 'selectedLegends'>> & {
  filters?: Partial<LiveStreamFilterPreferences>
  queue?: { depthOpen?: boolean; expandedModels?: string[]; statusFilter?: Partial<Record<QueueStatusBucket, boolean>> }
}

const GROUP_BY_VALUES: GroupByDimension[] = ['queue', 'vendor', 'provider', 'model']
const MODE_VALUES: SwimLaneMode[] = ['small', 'large']
const REQUEST_TYPE_VALUES: LiveStreamRequestType[] = ['business', 'probe']
const QUEUE_STATUS_BUCKET_VALUES: QueueStatusBucket[] = ['active', 'degraded', 'manualDisabled', 'exhausted']

function defaultFilters(): LiveStreamFilterPreferences {
  return {
    requestTypes: ['business', 'probe'],
    statuses: [],
    models: [],
    providers: [],
    vendors: [],
    agents: [],
  }
}

function defaultQueuePreferences(): QueuePerspectivePreferences {
  return {
    expandedModels: [],
    // 2026-08-21: default to "in use" only; user can opt into the other three.
    statusFilter: {
      active: true,
      degraded: false,
      manualDisabled: false,
      exhausted: false,
    },
  }
}

export function defaultLiveStreamPreferences(): LiveStreamPreferences {
  return {
    version: 1,
    groupBy: 'queue',
    mode: 'small',
    selectedLegends: [],
    filters: defaultFilters(),
    queue: defaultQueuePreferences(),
  }
}

function isOneOf<T extends string>(value: unknown, values: readonly T[]): value is T {
  return typeof value === 'string' && values.includes(value as T)
}

function stringList(value: unknown, options?: { lowercase?: boolean; allowed?: readonly string[] }): string[] {
  if (!Array.isArray(value)) return []
  const seen = new Set<string>()
  for (const item of value) {
    if (typeof item !== 'string') continue
    const normalized = options?.lowercase ? item.trim().toLowerCase() : item.trim()
    if (!normalized || (options?.allowed && !options.allowed.includes(normalized))) continue
    seen.add(normalized)
  }
  return Array.from(seen)
}

function readStorage(key: string): unknown {
  try {
    const raw = localStorage.getItem(key)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

function hasValidMode(raw: unknown): boolean {
  return Boolean(raw && typeof raw === 'object' && !Array.isArray(raw)
    && isOneOf((raw as Record<string, unknown>).mode, MODE_VALUES))
}

function normalize(raw: unknown): LiveStreamPreferences {
  const defaults = defaultLiveStreamPreferences()
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return defaults

  const value = raw as Record<string, unknown>
  const filters = value.filters && typeof value.filters === 'object' && !Array.isArray(value.filters)
    ? value.filters as Record<string, unknown>
    : {}
  const queue = value.queue && typeof value.queue === 'object' && !Array.isArray(value.queue)
    ? value.queue as Record<string, unknown>
    : {}
  const rawStatusFilter = queue.statusFilter && typeof queue.statusFilter === 'object' && !Array.isArray(queue.statusFilter)
    ? queue.statusFilter as Record<string, unknown>
    : {}

  const requestTypes = stringList(filters.requestTypes, { allowed: REQUEST_TYPE_VALUES }) as LiveStreamRequestType[]
  return {
    version: 1,
    groupBy: isOneOf(value.groupBy, GROUP_BY_VALUES) ? value.groupBy : defaults.groupBy,
    mode: isOneOf(value.mode, MODE_VALUES) ? value.mode : defaults.mode,
    selectedLegends: stringList(value.selectedLegends),
    filters: {
      requestTypes: requestTypes.length > 0 ? requestTypes : defaults.filters.requestTypes,
      statuses: stringList(filters.statuses),
      models: stringList(filters.models),
      providers: stringList(filters.providers),
      vendors: stringList(filters.vendors),
      agents: stringList(filters.agents, { lowercase: true }),
    },
    queue: {
      depthOpen: typeof queue.depthOpen === 'boolean' ? queue.depthOpen : undefined,
      expandedModels: stringList(queue.expandedModels),
      statusFilter: Object.fromEntries(
        QUEUE_STATUS_BUCKET_VALUES.map(bucket => [
          bucket,
          typeof rawStatusFilter[bucket] === 'boolean'
            ? rawStatusFilter[bucket]
            : defaults.queue.statusFilter[bucket],
        ]),
      ) as Record<QueueStatusBucket, boolean>,
    },
  }
}

// ─── Debounced persistence (LP7, 2026-08-24) ──────────────────────────────
// 复用 LP6 useChatSessions pattern：300ms debounce + snapshot short-circuit +
// lifecycle flush + onScopeDispose 清理 + try/catch 错误降级。
// LP9 (2026-08-24): debounce / snapshot / lifecycle / cleanup 全部迁到
// persistenceShared.ts（与 useChatSessions / usePersistedValue 共享实现）。
// 本文件保留 user+tenant-scoped key generation + legacy migration + dual-write
// side-effect + module-level cache 这些 usePersistedValue 容纳不下的业务逻辑。

// In-memory cache 是真相源（对齐 LP6 pattern）。Key-tracked 防止 user 切换读到旧值。
let currentPreferences: LiveStreamPreferences | null = null
let currentPreferencesKey: string | null = null
let pendingKey: string | null = null
let pendingPreferences: LiveStreamPreferences | null = null

function persistLiveStreamPreferencesImmediate(key: string, preferences: LiveStreamPreferences): void {
  // Best-effort: localStorage 可能抛错（隐私模式/配额），下次 flush 会重试。
  try {
    localStorage.setItem(key, JSON.stringify(preferences))
    localStorage.setItem(LEGACY_SWIM_LANE_MODE_STORAGE_KEY, preferences.mode)
  } catch { /* preference persistence must never prevent the dashboard from working */ }
}

/** Test-only hook: reset module-level state. `_` prefix 标志非生产 API。*/
export function _resetPersistState(): void {
  persistHandle.cancel()
  pendingKey = null
  pendingPreferences = null
  currentPreferences = null
  currentPreferencesKey = null
}

export function flushPersist(): boolean {
  if (pendingKey == null || pendingPreferences == null) {
    return persistHandle.flush()
  }
  const key = pendingKey
  const preferences = pendingPreferences
  pendingKey = null
  pendingPreferences = null
  // Stage the pending snapshot before delegating so createDebouncedFlush's
  // snapshot-source closure sees the most recent write.
  currentPreferencesKey = key
  currentPreferences = preferences
  return persistHandle.flush()
}

// Debounce / snapshot / lifecycle / cleanup are now shared with LP6 + LP8
// via persistenceShared.ts. The write callback handles the dual-write
// (main key + legacy swimlane mode) and any localStorage failures.
const persistHandle = createDebouncedFlush<LiveStreamPreferences>({
  getSnapshot: () => pendingPreferences ?? currentPreferences,
  serialize: (prefs) => JSON.stringify(prefs),
  write: (prefs) => {
    const key = currentPreferencesKey
    if (!key) return true
    persistLiveStreamPreferencesImmediate(key, prefs)
    return true
  },
  debounceMs: PERSIST_DEBOUNCE_MS,
})

function schedulePersist(key: string, preferences: LiveStreamPreferences): void {
  pendingKey = key
  pendingPreferences = preferences
  currentPreferencesKey = key
  currentPreferences = preferences
  persistHandle.schedule()
}

// Lifecycle flush listeners + scope cleanup delegate to the shared helper.
// liveStreamPreferences is module-level so installScopeCleanup is a no-op
// when called outside an active effect scope — the listeners live until
// page unload, which matches the module's actual lifetime.
const removeLifecycleListeners = installLifecycleFlush(flushPersist)
installScopeCleanup(flushPersist, removeLifecycleListeners)

/**
 * Reads validated real-time dashboard preferences for the active user + tenant.
 * Malformed storage values degrade field-by-field to defaults and never block page load.
 */
export function readLiveStreamPreferences(): LiveStreamPreferences {
  const key = liveStreamPreferencesStorageKey()
  // In-memory cache 是真相源 — 写入失败（隐私模式/配额）时仍返回最新值。
  // Key mismatch（user/tenant 切换）自动失效并 flush 旧用户的 pending。
  if (currentPreferences && currentPreferencesKey === key) return currentPreferences
  if (pendingKey != null && pendingKey !== key) flushPersist()

  const scopedRaw = readStorage(key)
  if (scopedRaw) {
    const normalized = normalize(scopedRaw)
    currentPreferences = normalized
    currentPreferencesKey = key
    return normalized
  }

  // Migration from unscoped v1 key. Immediate path so write completes before read returns.
  const legacyV1 = readStorage(LEGACY_LIVE_STREAM_PREFERENCES_STORAGE_KEY)
  const legacyMode = (() => { try { return localStorage.getItem(LEGACY_SWIM_LANE_MODE_STORAGE_KEY) } catch { return null } })()
  const source = legacyV1 ?? {}
  const preferences = normalize(source)
  if (!hasValidMode(source) && isOneOf(legacyMode, MODE_VALUES)) preferences.mode = legacyMode
  if (legacyV1 || isOneOf(legacyMode, MODE_VALUES)) persistLiveStreamPreferencesImmediate(key, preferences)
  currentPreferences = preferences
  currentPreferencesKey = key
  return preferences
}

/** Writes a validated partial update without losing selections owned by other dashboard controls. */
export function writeLiveStreamPreferences(patch: LiveStreamPreferencesPatch): LiveStreamPreferences {
  const key = liveStreamPreferencesStorageKey()
  const current = readLiveStreamPreferences()
  const next = normalize({
    ...current,
    ...patch,
    filters: { ...current.filters, ...patch.filters },
    queue: {
      ...current.queue, ...patch.queue,
      expandedModels: patch.queue?.expandedModels ?? current.queue.expandedModels,
      statusFilter: { ...current.queue.statusFilter, ...patch.queue?.statusFilter },
    },
  })
  currentPreferences = next
  currentPreferencesKey = key
  schedulePersist(key, next)
  return next
}
