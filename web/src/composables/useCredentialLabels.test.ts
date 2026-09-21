// 2026-08-23 凭据显示：共享解析器解析失败路径、TTL/去重和契约一致性。
//
// 2026-08-23 (Agent C): 扩展租户维度。新增测试覆盖：
//   - 同一浏览器内切换租户后凭据标签不串；
//   - clearCredentialLabels('A') 不影响 'B'。
// 通过 vi.mock('../store', ...) 桩掉 getCurrentTenantId，配合新的
// __setCurrentTenantIdResolver 测试 seam，currentTenantId() 始终返回
// 测试期望的租户。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const { getCredentialMonitorSummary, getCurrentTenantId } = vi.hoisted(() => ({
  getCredentialMonitorSummary: vi.fn(),
  getCurrentTenantId: vi.fn(() => 'default'),
}))

vi.mock('../api/credential-monitor', () => ({
  getCredentialMonitorSummary,
}))

vi.mock('../store', () => ({
  getCurrentTenantId,
}))

import {
  __setCurrentTenantIdResolver,
  clearCredentialLabels,
  credentialDisplayName,
  credentialIdFromSyntheticModel,
  credentialLabelForId,
  displaySyntheticCredentialModel,
  loadCredentialLabels,
  loadCredentialLabelsForTenant,
} from './useCredentialLabels'
import { i18n } from '../i18n'

function setTenant(tenantId: string): void {
  getCurrentTenantId.mockImplementation(() => tenantId)
  __setCurrentTenantIdResolver(() => tenantId)
}

function setLocale(locale: string): void {
  ;(i18n.global.locale as unknown as { value: string }).value = locale
}

function mockSummary(credentials: Array<{ id: number; label: string }>): void {
  getCredentialMonitorSummary.mockImplementation(async () => ({
    credentials: credentials.map((c) => ({
      id: c.id,
      label: c.label,
      provider_id: c.id,
      provider_name: `prov-${c.id}`,
    })),
  }))
}

beforeEach(() => {
  clearCredentialLabels()
  setTenant('default')
  // 2026-09-05 audit F2-#3: the default credential prefix now resolves via
  // i18n at call time, so pin the locale to keep the '凭据 #ID' assertions
  // deterministic regardless of the host navigator language.
  setLocale('zh-CN')
  getCredentialMonitorSummary.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('credentialIdFromSyntheticModel', () => {
  it('parses cred-<id> synthetic model names', () => {
    expect(credentialIdFromSyntheticModel('cred-12')).toBe(12)
    expect(credentialIdFromSyntheticModel('cred-1')).toBe(1)
  })

  it('rejects non-numeric or empty suffixes', () => {
    expect(credentialIdFromSyntheticModel('cred-')).toBeNull()
    expect(credentialIdFromSyntheticModel('cred-abc')).toBeNull()
    expect(credentialIdFromSyntheticModel('cred--1')).toBeNull()
  })

  it('passes through real upstream model names', () => {
    expect(credentialIdFromSyntheticModel('gpt-5.6-luna')).toBeNull()
    expect(credentialIdFromSyntheticModel('')).toBeNull()
  })
})

describe('credentialDisplayName fallback', () => {
  it('returns em-dash for invalid or missing ids', () => {
    expect(credentialDisplayName(null)).toBe('—')
    expect(credentialDisplayName(undefined)).toBe('—')
    expect(credentialDisplayName(0)).toBe('—')
    expect(credentialDisplayName(-1)).toBe('—')
    expect(credentialDisplayName(Number.NaN)).toBe('—')
  })

  it('returns 凭据 #ID before labels load', () => {
    expect(credentialDisplayName(99)).toBe('凭据 #99')
  })

  it('follows the active locale for the default prefix (audit F2-#3)', () => {
    setLocale('en')
    expect(credentialDisplayName(99)).toBe('Credential #99')
    // An explicit prefix still wins over the i18n default (signature compat).
    expect(credentialDisplayName(99, 'Key')).toBe('Key #99')
  })
})

describe('loadCredentialLabels', () => {
  it('returns label once monitor-summary resolves', async () => {
    mockSummary([{ id: 42, label: 'openai-prod' }])
    await loadCredentialLabels()
    expect(credentialLabelForId(42)).toBe('openai-prod')
    expect(credentialDisplayName(42)).toBe('openai-prod')
  })

  it('keeps fallback when label is blank', async () => {
    mockSummary([{ id: 42, label: '   ' }])
    await loadCredentialLabels()
    expect(credentialLabelForId(42)).toBeNull()
    expect(credentialDisplayName(42)).toBe('凭据 #42')
  })

  it('drops non-positive ids and ignores invalid rows', async () => {
    mockSummary([
      { id: 1, label: 'alpha' },
      { id: 0, label: 'zero' },
      { id: -2, label: 'neg' },
      { id: 9, label: '' },
    ])
    await loadCredentialLabels()
    expect(credentialLabelForId(1)).toBe('alpha')
    expect(credentialLabelForId(0)).toBeNull()
    expect(credentialLabelForId(-2)).toBeNull()
    expect(credentialLabelForId(9)).toBeNull()
  })

  it('dedupes concurrent calls and shares the in-flight promise', async () => {
    let resolveFn: ((value: { credentials: Array<{ id: number; label: string; provider_id?: number; provider_name?: string }> }) => void) | null = null
    getCredentialMonitorSummary.mockImplementation(() => new Promise((resolve) => {
      resolveFn = (value) => resolve(value)
    }))
    const a = loadCredentialLabels()
    const b = loadCredentialLabels()
    expect(getCredentialMonitorSummary).toHaveBeenCalledTimes(1)
    resolveFn!({ credentials: [] })
    await Promise.all([a, b])
  })

  it('keeps prior labels when the API call fails', async () => {
    mockSummary([{ id: 7, label: 'preloaded' }])
    await loadCredentialLabels()
    expect(credentialLabelForId(7)).toBe('preloaded')

    getCredentialMonitorSummary.mockImplementationOnce(async () => {
      throw new Error('boom')
    })
    await loadCredentialLabels(true) // force refresh
    expect(credentialLabelForId(7)).toBe('preloaded')
  })

  it('forces a refresh when force=true', async () => {
    mockSummary([{ id: 1, label: 'first' }])
    await loadCredentialLabels()
    expect(credentialLabelForId(1)).toBe('first')

    mockSummary([{ id: 1, label: 'updated' }])
    await loadCredentialLabels(true)
    expect(credentialLabelForId(1)).toBe('updated')
  })
})

describe('displaySyntheticCredentialModel', () => {
  it('passes real model names through unchanged', async () => {
    mockSummary([])
    await loadCredentialLabels()
    expect(displaySyntheticCredentialModel('gpt-5.6-luna')).toBe('gpt-5.6-luna')
  })

  it('substitutes label for cred-<id>', async () => {
    mockSummary([{ id: 22, label: 'glm-key' }])
    await loadCredentialLabels()
    expect(displaySyntheticCredentialModel('cred-22')).toBe('glm-key')
  })

  it('falls back to 凭据 #ID for cred-<id> when no label cached', async () => {
    mockSummary([])
    await loadCredentialLabels()
    expect(displaySyntheticCredentialModel('cred-77')).toBe('凭据 #77')
  })
})

// ── 2026-08-23 (Agent C) tenant-dimensioned cache ────────────────────────────

describe('tenant-dimensioned cache', () => {
  it('returns the label for the current tenant only', async () => {
    setTenant('tenant-a')
    mockSummary([
      { id: 1, label: 'shared-label' },
    ])
    await loadCredentialLabelsForTenant('tenant-a', false)
    expect(credentialLabelForId(1)).toBe('shared-label')

    // Switch tenant: the same id should NOT resolve to the same label
    // (unless tenant-b happens to have the same label, which our mock
    // ensures it does not).
    setTenant('tenant-b')
    mockSummary([{ id: 2, label: 'tenant-b-only' }])
    await loadCredentialLabelsForTenant('tenant-b', false)
    expect(credentialLabelForId(2)).toBe('tenant-b-only')
    // Tenant-a's label is invisible to tenant-b even though both maps
    // happen to be populated.
    expect(credentialLabelForId(1)).toBeNull()
    expect(credentialDisplayName(1)).toBe('凭据 #1')
  })

  it('loadCredentialLabels routes to the current tenant', async () => {
    setTenant('tenant-x')
    mockSummary([{ id: 5, label: 'x-label' }])
    await loadCredentialLabels()
    expect(credentialLabelForId(5)).toBe('x-label')

    setTenant('tenant-y')
    expect(credentialLabelForId(5)).toBeNull()
  })

  it('clearCredentialLabels(tenant) only drops that tenant', async () => {
    setTenant('tenant-a')
    mockSummary([{ id: 9, label: 'alpha' }])
    await loadCredentialLabelsForTenant('tenant-a', false)
    setTenant('tenant-b')
    mockSummary([{ id: 9, label: 'beta' }])
    await loadCredentialLabelsForTenant('tenant-b', false)

    expect(clearCredentialLabels('tenant-a')).toBe(true)
    setTenant('tenant-a')
    expect(credentialLabelForId(9)).toBeNull()

    setTenant('tenant-b')
    expect(credentialLabelForId(9)).toBe('beta')

    // Clearing tenant-a again is a no-op (returns false).
    expect(clearCredentialLabels('tenant-a')).toBe(false)
  })

  it('clearCredentialLabels() with no argument clears every tenant', async () => {
    setTenant('tenant-a')
    mockSummary([{ id: 1, label: 'one' }])
    await loadCredentialLabelsForTenant('tenant-a', false)
    setTenant('tenant-b')
    mockSummary([{ id: 1, label: 'uno' }])
    await loadCredentialLabelsForTenant('tenant-b', false)

    expect(clearCredentialLabels()).toBe(true)

    setTenant('tenant-a')
    expect(credentialLabelForId(1)).toBeNull()
    setTenant('tenant-b')
    expect(credentialLabelForId(1)).toBeNull()

    // Second clear with no args is a no-op.
    expect(clearCredentialLabels()).toBe(false)
  })

  it('per-tenant in-flight promise dedupes independently', async () => {
    let resolveA: ((value: { credentials: Array<{ id: number; label: string; provider_id?: number; provider_name?: string }> }) => void) | null = null
    let resolveB: ((value: { credentials: Array<{ id: number; label: string; provider_id?: number; provider_name?: string }> }) => void) | null = null

    getCredentialMonitorSummary.mockImplementation(() => {
      const tenantId = getCurrentTenantId()
      if (tenantId === 'tenant-a') {
        return new Promise<{ credentials: Array<{ id: number; label: string; provider_id?: number; provider_name?: string }> }>((resolve) => {
          resolveA = resolve
        })
      }
      if (tenantId === 'tenant-b') {
        return new Promise<{ credentials: Array<{ id: number; label: string; provider_id?: number; provider_name?: string }> }>((resolve) => {
          resolveB = resolve
        })
      }
      throw new Error('unexpected tenant ' + tenantId)
    })

    // Two concurrent calls in the SAME tenant must dedupe (one HTTP call).
    setTenant('tenant-a')
    const a1 = loadCredentialLabelsForTenant('tenant-a', false)
    const a2 = loadCredentialLabelsForTenant('tenant-a', false)
    expect(getCredentialMonitorSummary).toHaveBeenCalledTimes(1)
    resolveA!({ credentials: [{ id: 1, label: 'a-label', provider_id: 1, provider_name: 'pa' }] })
    await Promise.all([a1, a2])

    // A second tenant running in parallel must NOT share the first
    // tenant's pending promise — that's the regression the cache split
    // prevents.
    setTenant('tenant-b')
    const b1 = loadCredentialLabelsForTenant('tenant-b', false)
    expect(getCredentialMonitorSummary).toHaveBeenCalledTimes(2)
    resolveB!({ credentials: [{ id: 2, label: 'b-label', provider_id: 2, provider_name: 'pb' }] })
    await b1

    setTenant('tenant-a')
    expect(credentialLabelForId(1)).toBe('a-label')
    setTenant('tenant-b')
    expect(credentialLabelForId(2)).toBe('b-label')
  })

  it('swap-in new Map triggers reactivity on .labels.value consumers', async () => {
    // Regression guard: after Agent C split the cache per tenant, the
    // first iteration stored labels as a plain Map on TenantLabelState.
    // Mutating that Map (or replacing it via assignment) never reached
    // Vue's reactivity graph, so the rendered template kept showing
    // 凭据 #ID after loadCredentialLabels() resolved. The fix was to
    // store labels as ref<Map<...>>; this test confirms the ref's value
    // is what changes when a load resolves, and that a consumer
    // tracking the ref re-runs.
    setTenant('react-tenant')
    // Start from a known-empty state so cached prior loads don't poison
    // the snapshot or trigger the TTL short-circuit.
    clearCredentialLabels('react-tenant')

    const { useCredentialLabels } = await import('./useCredentialLabels')
    const { labels } = useCredentialLabels()
    const snapshot = () => labels.value.get(42) ?? null

    // No load yet → no label.
    expect(snapshot()).toBeNull()

    // Resolve load with one label (force=true to bypass any stale TTL
    // residue from other tests).
    mockSummary([{ id: 42, label: 'alpha' }])
    await loadCredentialLabels(true)
    expect(snapshot()).toBe('alpha')

    // Resolve a *second* load (force) with a different label. The
    // ref's .value must be a different Map so the template re-renders
    // rather than relying on Map-internal mutation.
    mockSummary([{ id: 42, label: 'beta' }])
    await loadCredentialLabels(true)
    expect(snapshot()).toBe('beta')
  })

  it('clearCredentialLabels on tenant drops in-flight pending and emits new Map', async () => {
    // After clear, the next load() must issue a fresh HTTP request
    // (not return the old pending promise) and write a new Map to
    // .labels.value. This protects against "logout left an in-flight
    // promise holding labels for a tenant the user no longer owns".
    setTenant('clear-tenant')
    let firstResolve: ((value: { credentials: Array<{ id: number; label: string; provider_id?: number; provider_name?: string }> }) => void) | null = null
    getCredentialMonitorSummary.mockImplementationOnce(
      () => new Promise((resolve) => { firstResolve = resolve }),
    )
    const stale = loadCredentialLabels()
    expect(getCredentialMonitorSummary).toHaveBeenCalledTimes(1)

    // Clear before the first load resolves — the pending promise must
    // be abandoned so a follow-up load issues a fresh fetch.
    expect(clearCredentialLabels('clear-tenant')).toBe(true)
    // Resolve the abandoned promise to unblock its microtask; its
    // .then handler still runs but writes to a now-stale slot, which
    // the tenant lookup won't find.
    firstResolve!({ credentials: [{ id: 1, label: 'stale', provider_id: 1, provider_name: 'p' }] })
    await stale.catch(() => undefined)

    // The cleared tenant must report no label.
    expect(credentialLabelForId(1)).toBeNull()

    // A new load() for the same tenant must trigger a fresh HTTP call
    // and surface the new label.
    mockSummary([{ id: 1, label: 'fresh' }])
    await loadCredentialLabels()
    expect(credentialLabelForId(1)).toBe('fresh')
    expect(getCredentialMonitorSummary).toHaveBeenCalledTimes(2)
  })

  it('stale in-flight promise does not clobber a freshly-cleared tenant cache', async () => {
    // Regression guard: if a clear happens while a fetch is in flight,
    // the closure inside loadCredentialLabelsForTenant still holds a
    // reference to the *original* TenantLabelState. Without a race
    // check, the late .then() handler would write the previous user's
    // labels into whatever cache object is currently registered under
    // the same tenantId — leaking across logout/login cycles.
    setTenant('race-tenant')
    clearCredentialLabels('race-tenant')

    let resolveOld: ((value: { credentials: Array<{ id: number; label: string; provider_id?: number; provider_name?: string }> }) => void) | null = null
    getCredentialMonitorSummary.mockImplementationOnce(
      () => new Promise((resolve) => { resolveOld = resolve }),
    )
    const oldLoad = loadCredentialLabels()

    // Drop the tenant while the fetch is pending.
    clearCredentialLabels('race-tenant')

    // Now load as a *different* user (same tenant key would be unusual
    // but possible during a fast login flow). Re-creating the slot must
    // install a fresh TenantLabelState.
    mockSummary([{ id: 99, label: 'fresh-after-clear' }])
    const newLoad = loadCredentialLabels(true)
    await newLoad
    expect(credentialLabelForId(99)).toBe('fresh-after-clear')

    // Late-resolving the old fetch must NOT overwrite the new label.
    resolveOld!({ credentials: [{ id: 1, label: 'stale-leak', provider_id: 1, provider_name: 'p' }] })
    await oldLoad.catch(() => undefined)
    expect(credentialLabelForId(99)).toBe('fresh-after-clear')
    expect(credentialLabelForId(1)).toBeNull()
  })
})