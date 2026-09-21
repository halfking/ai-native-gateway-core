import { describe, expect, it } from 'vitest'
import { readdir, readFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'

// ProviderDetailView tab/menu parity guard (added 2026-08-31)
//
// Three contracts this test pins:
//
//   1. ProviderDetailView.vue declares exactly the canonical set of tab IDs
//      via `setTab('xxx')` and the `<button ... @click="setTab('xxx')">`
//      rows. Adding or removing a tab requires updating CANONICAL_TABS
//      below — this is the same model as the i18n parity gate.
//
//   2. Every tab ID has a matching `tabXxx` i18n key in
//      locales/zh-CN/providerDetailPage.ts (zh-CN is the SoT). This
//      catches the failure mode where a tab is added but the locale
//      file is forgotten.
//
//   3. Every tab ID has a corresponding component file under
//      views/provider-detail/<X>Tab.vue (or `<X>.vue` for special cases
//      like OverviewCards). Catches the failure mode where a tab button
//      exists but no body component is mounted.
//
// The "parity" in the handoff sense: the tab list, the i18n keys, and
// the component files form a triangle. Drift on any edge silently
// breaks the page in non-default locales (untranslated tab labels) or
// at runtime (no component mounted → blank panel). This test makes
// the triangle explicit.

const CANONICAL_TABS = [
  'creds',
  'models',
  'quality',
  'logs',
  'error-detail',
  'diag',
  'probe',
  'settings',
] as const

type TabId = (typeof CANONICAL_TABS)[number]

function readProviderDetailViewSource(): Promise<string> {
  const candidates = [
    resolve(process.cwd(), 'web/src/views/ProviderDetailView.vue'),
    resolve(process.cwd(), 'src/views/ProviderDetailView.vue'),
  ]
  return readFirstExisting(candidates)
}

function readZhCnProviderDetailPageSource(): Promise<string> {
  const candidates = [
    resolve(process.cwd(), 'web/src/locales/zh-CN/providerDetailPage.ts'),
    resolve(process.cwd(), 'src/locales/zh-CN/providerDetailPage.ts'),
  ]
  return readFirstExisting(candidates)
}

async function readFirstExisting(paths: string[]): Promise<string> {
  for (const path of paths) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      // try next candidate
    }
  }
  throw new Error(`None of the candidate paths exist: ${paths.join(', ')}`)
}

function extractSetTabIds(source: string): string[] {
  // Match `setTab('xxx')` invocations (click handlers + programmatic
  // navigation). The regex is loose on purpose: tabs may be added with
  // double quotes, single quotes, or backticks; this catches all three.
  const ids = new Set<string>()
  const re = /setTab\(\s*['"`]([^'"`]+)['"`]/g
  let m: RegExpExecArray | null
  while ((m = re.exec(source))) {
    ids.add(m[1])
  }
  return [...ids]
}

function extractTabKeyIds(zhSource: string): string[] {
  // Match `tabXxx: '...'` lines inside the zh-CN providerDetailPage.ts
  // source. The shape is `tabCreds: '凭据 ({n})',` etc. We then
  // exclude ancillary keys that aren't tabs themselves — `tabProbeTitle`
  // is the tooltip for the probe tab, not a tab. The canonical tab
  // keyset is decided by tabKeyFromTabId() so any other keys that show
  // up here are either typos or future drift and are flagged.
  const ids = new Set<string>()
  const re = /^\s*(tab[A-Z][A-Za-z]+)\s*:/gm
  let m: RegExpExecArray | null
  while ((m = re.exec(zhSource))) {
    ids.add(m[1])
  }
  return [...ids]
}

// Ancillary tab-prefixed keys that are not tab labels. Today this is
// just `tabProbeTitle` (the tooltip shown on the probe tab button).
// If new ancillary keys are added, append them here so the snapshot
// test doesn't false-positive.
const ANCILLARY_TAB_KEYS = new Set(['tabProbeTitle'])

function tabKeyFromTabId(tabId: string): string {
  // 'creds' → 'tabCreds', 'error-detail' → 'tabErrorDetail',
  // 'diag' → 'tabDiag'. Title-cases after splitting on '-' and joins.
  const parts = tabId.split('-').map((p) => p.charAt(0).toUpperCase() + p.slice(1))
  return 'tab' + parts.join('')
}

async function listTabComponentFiles(tabDir: string): Promise<string[]> {
  try {
    const entries = await readdir(tabDir)
    return entries.filter((e) => e.endsWith('.vue'))
  } catch {
    return []
  }
}

describe('provider detail tab/menu parity', () => {
  it('ProviderDetailView declares exactly the canonical tab IDs (no drift, no extras)', async () => {
    const source = await readProviderDetailViewSource()
    const declared = extractSetTabIds(source).sort()
    const expected = [...CANONICAL_TABS].sort()
    expect(declared).toEqual(expected)
  })

  it('every canonical tab ID has a matching tabXxx i18n key in zh-CN/providerDetailPage.ts', async () => {
    const zhSource = await readZhCnProviderDetailPageSource()
    const keys = new Set(extractTabKeyIds(zhSource))
    for (const tabId of CANONICAL_TABS) {
      const key = tabKeyFromTabId(tabId)
      expect(keys.has(key), `missing zh-CN i18n key: providerDetailPage.${key} (tab: ${tabId})`).toBe(true)
    }
  })

  it('every canonical tab ID has a corresponding <X>Tab.vue component file (or OverviewCards for the implicit summary)', async () => {
    const tabDirCandidates = [
      resolve(process.cwd(), 'web/src/views/provider-detail'),
      resolve(process.cwd(), 'src/views/provider-detail'),
    ]
    let files: string[] = []
    for (const dir of tabDirCandidates) {
      const got = await listTabComponentFiles(dir)
      if (got.length > 0) {
        files = got
        break
      }
    }
    expect(files.length).toBeGreaterThan(0)
    // Map tab ID → expected basename. Defaults to `<TabId>Tab.vue` in
    // PascalCase. Use the alias map for IDs whose component follows a
    // different naming convention (today: 'probe' → 'ProbeHistoryTab',
    // 'creds' → 'CredsTab' which is already the default).
    const aliases: Record<TabId, string> = {
      creds: 'CredsTab',
      models: 'ModelsTab',
      quality: 'QualityTab',
      logs: 'LogsTab',
      'error-detail': 'ErrorDetailTab',
      diag: 'DiagTab',
      probe: 'ProbeHistoryTab',
      settings: 'SettingsTab',
    }
    for (const tabId of CANONICAL_TABS) {
      const expected = `${aliases[tabId]}.vue`
      expect(
        files,
        `missing tab component for '${tabId}' (looked for ${expected} in views/provider-detail)`,
      ).toContain(expected)
    }
  })

  it('tab defaults: route.query.tab falls back to "creds" and "error-detail" requires credential_id', async () => {
    // Pins the route-driven defaults so a future refactor that drops
    // the fallback (or removes credential_id plumbing) doesn't silently
    // leave error-detail open with no row selected.
    const source = await readProviderDetailViewSource()
    // The source uses `route.query.tab === 'string' ? ... : 'creds'`
    // (NOT typeof — the typeof check is a no-op since query.tab is
    // either string | string[] | undefined; using === 'string'
    // narrows correctly). Tolerate whitespace variations.
    expect(source).toMatch(/route\.query\.tab\s*===\s*'string'\s*\?\s*route\.query\.tab\s*:\s*'creds'/)
    expect(source).toMatch(/route\.query\.credential_id/)
    expect(source).toMatch(/errorCredentialId\.value\s*=\s*id/)
  })

  it('zh-CN tab keys are stable (no rename allowed without updating CANONICAL_TABS)', async () => {
    // Snapshot the current set of tab* keys (excluding ancillary keys
    // like tabProbeTitle) so an accidental rename is caught even if the
    // alphabetical-equality test above passes.
    const zhSource = await readZhCnProviderDetailPageSource()
    const all = extractTabKeyIds(zhSource)
    const keys = all.filter((k) => !ANCILLARY_TAB_KEYS.has(k)).sort()
    const expectedKeys = CANONICAL_TABS.map(tabKeyFromTabId).sort()
    expect(keys).toEqual(expectedKeys)
  })
})
