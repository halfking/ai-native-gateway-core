# Live Stream Frontend Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the realtime swim-lane model filter showing duplicate lanes for casing / whitespace variants, and stop SSE deltas resetting the multi-dimension filter dialog draft while it's open.

**Architecture:** Backend (`admin/live_stream_redis_store.go`) already normalises model keys and emits canonical `tile.model`; we just simplify the frontend `standardModelName` to a trim pass-through so the source of truth stays on the server. `LiveStreamFilterDialog` snapshots `props.options` at open time and renders from the snapshot until the dialog closes, so SSE delta updates never overwrite the user's in-progress draft. We persist the chosen dimension / mode / filters to localStorage under a single key so the dashboard reloads with the operator's last view.

**Tech Stack:** Vue 3 (script setup), Vitest, Vue I18n, Vue Router, localStorage.

**Spec source:** `docs/superpowers/specs/2026-07-25-realtime-routing-self-heal.md` §1 (model name canonicalisation) + §2 (filter dialog snapshot).

**Sibling plan:** §3 NodeProbe real-time recovery → `docs/superpowers/plans/2026-07-25-node-probe-realtime-recovery.md`.

**Files touched (overview):**

| File | Purpose |
|---|---|
| `web/src/components/LiveRequestStreamV2.vue` | simplify `standardModelName`, persist dashboard view choice to localStorage |
| `web/src/composables/liveStreamDisplay.ts` | new shared `standardModelName` export (passthrough trim) |
| `web/src/composables/liveStreamDisplay.test.ts` | pin `standardModelName` trim behavior |
| `web/src/components/LiveStreamFilterDialog.vue` | snapshot options at open time, re-snapshot on close |
| `web/src/components/LiveStreamFilterDialog.test.ts` | new vitest file |
| `web/src/locales/zh-CN/dashboard.ts` (and 7 other locales) | locale keys for the new localStorage notice (optional fallback) |

---

## Task 1: Centralise `standardModelName` as a passthrough trim helper

**Files:**
- Modify: `web/src/composables/liveStreamDisplay.ts` (add export, no breaking change)
- Modify: `web/src/components/LiveRequestStreamV2.vue` (drop in-house helper, import from display module)
- Modify: `web/src/composables/liveStreamDisplay.test.ts` (add test for new helper)

- [ ] **Step 1: Write the failing test**

Append to `web/src/composables/liveStreamDisplay.test.ts` (verify file exists first; if not, create with `pnpm vitest run liveStreamDisplay.test.ts` style — match neighbouring file's imports):

```ts
import { describe, expect, it } from 'vitest'
import { standardModelName } from './liveStreamDisplay'

describe('standardModelName', () => {
  it('returns trimmed canonical when provided', () => {
    expect(standardModelName('  minimax-M3  ')).toBe('minimax-M3')
  })

  it('returns empty string for nullish input', () => {
    expect(standardModelName(undefined)).toBe('')
    expect(standardModelName(null)).toBe('')
    expect(standardModelName('')).toBe('')
  })

  it('does not lowercase or fold — backend is authoritative', () => {
    expect(standardModelName('MiniMax-M3')).toBe('MiniMax-M3')
    expect(standardModelName('claude-3')).toBe('claude-3')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && pnpm vitest run src/composables/liveStreamDisplay.test.ts`
Expected: FAIL — `standardModelName is not a function` / missing export.

- [ ] **Step 3: Export the helper from `liveStreamDisplay.ts`**

In `web/src/composables/liveStreamDisplay.ts` add at the end of the file (preserving the existing comment block format):

```ts
/**
 * Trim-only pass-through for the canonical model name shown in the swim lane.
 * Backend (admin/live_stream_redis_store.go) is the source of truth — it builds
 * `tile.model` via `normalizeModelKey(emptyAs(CanonicalName, Model))` and lowercases
 * once at queue-key time. The frontend relies on that already-canonical string
 * and only strips whitespace to guard against regressions where the upstream
 * payload accidentally carries leading/trailing blanks.
 *
 * SPEC §1.1.3: do NOT add lowercase / unicode folding here. Doing so risks
 * diverging from `liveStreamDimensionKey('model', req)` and re-splitting lanes.
 */
export function standardModelName(model: string | undefined | null): string {
  return (model || '').trim()
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `pnpm vitest run src/composables/liveStreamDisplay.test.ts`
Expected: PASS (3 cases).

- [ ] **Step 5: Remove the in-house helper from `LiveRequestStreamV2.vue`**

In `web/src/components/LiveRequestStreamV2.vue`:

1. Delete the in-house function:

```ts
/** 模型维度一律用标准名（tile.model 后端已优先 canonical） */
function standardModelName(model: string | undefined | null): string {
  return (model || '').trim()
}
```

2. Add to the import block (after `import { ... liveStreamDisplay.ts }` is needed; for now keep the existing line `import { useSwimLane ... }` and add the new import right below):

```ts
import { standardModelName } from '../composables/liveStreamDisplay'
```

- [ ] **Step 6: Verify call sites still resolve**

Run: `pnpm vue-tsc --noEmit` (or `pnpm tsc --noEmit` if vue-tsc isn't configured). It must report zero "unused" / "cannot find name standardModelName" errors.

- [ ] **Step 7: Re-run tests to confirm green**

Run: `pnpm vitest run`
Expected: Full suite remains green (the snapshot tests in `liveStreamStore.test.ts` use just `liveStreamStore` — no behaviour change here).

- [ ] **Step 8: Commit**

```bash
git add web/src/composables/liveStreamDisplay.ts web/src/composables/liveStreamDisplay.test.ts web/src/components/LiveRequestStreamV2.vue
git commit -m "refactor(live-stream): centralize standardModelName as trim-only pass-through"
```

---

## Task 2: Filter dialog snapshot — freeze options list during open

**Files:**
- Modify: `web/src/components/LiveStreamFilterDialog.vue` (replace `filteredOptions` source: use `localOptions` snapshot)
- Create: `web/src/components/LiveStreamFilterDialog.test.ts` (vitest)

- [ ] **Step 1: Write the failing test**

Create `web/src/components/LiveStreamFilterDialog.test.ts`:

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import LiveStreamFilterDialog from './LiveStreamFilterDialog.vue'

describe('LiveStreamFilterDialog snapshot', () => {
  it('freezes options while open even if props.options changes', async () => {
    const wrapper = mount(LiveStreamFilterDialog, {
      props: { open: false, title: '模型', options: ['a', 'b'], selected: [] },
    })

    // Open with options=['a','b']
    await wrapper.setProps({ open: true })
    await nextTick()
    let labels = wrapper.findAll('.lsfd-option-label').map((n: any) => n.text())
    expect(labels.sort()).toEqual(['a', 'b'])

    // SSE delta: parent updates options to ['a','b','c','d']
    await wrapper.setProps({ options: ['a', 'b', 'c', 'd'] })
    await nextTick()
    labels = wrapper.findAll('.lsfd-option-label').map((n: any) => n.text())
    // Snapshot semantics — labels keep including only ['a','b'] while still open
    expect(labels.sort()).toEqual(['a', 'b'])

    // Close + reopen — now reflects fresh props.options
    await wrapper.setProps({ open: false })
    await nextTick()
    await wrapper.setProps({ open: true })
    await nextTick()
    labels = wrapper.findAll('.lsfd-option-label').map((n: any) => n.text())
    expect(labels.sort()).toEqual(['a', 'b', 'c', 'd'])
  })

  it('re-syncs draft from props.selected every open even if it changed', async () => {
    const wrapper = mount(LiveStreamFilterDialog, {
      props: { open: false, title: '模型', options: ['a', 'b'], selected: [] },
    })
    await wrapper.setProps({ open: true })
    await nextTick()
    // Operator checks 'a'
    const checkboxes = wrapper.findAll<HTMLInputElement>('input[type="checkbox"]')
    await checkboxes[0].setValue(true)
    expect(wrapper.vm.$data.draft.has('a')).toBe(true)

    // Close + reopen — draft must reset to props.selected (operator must apply to persist)
    await wrapper.setProps({ open: false })
    await nextTick()
    await wrapper.setProps({ open: true, selected: [] })
    await nextTick()
    expect(wrapper.vm.$data.draft.has('a')).toBe(false)
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `pnpm vitest run src/components/LiveStreamFilterDialog.test.ts`
Expected: FAIL — first test will see labels = ['a','b','c','d'] instead of frozen ['a','b'] because current source uses `props.options` directly.

- [ ] **Step 3: Add `localOptions` snapshot**

In `web/src/components/LiveStreamFilterDialog.vue`:

1. After the `const search = ref('')` declaration, add:

```ts
// 2026-07-25 SPEC §2: snapshot props.options at open time so SSE-driven
// updates don't churn the visible list mid-edit. Search, draft, and the
// footer button all keep referencing the snapshot.
const localOptions = ref<string[]>(props.options)
```

2. Replace the `filteredOptions` computed's source:

```ts
const filteredOptions = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return localOptions.value
  return localOptions.value.filter((opt) => {
    const label = (props.labelOf?.(opt) || opt).toLowerCase()
    return label.includes(q) || opt.toLowerCase().includes(q)
  })
})
```

3. Update the existing `watch(() => props.open, ...)` to also (re)snapshot `localOptions`:

```ts
watch(
  () => props.open,
  (v) => {
    if (v) {
      draft.value = new Set(props.selected)
      localOptions.value = [...props.options]
      search.value = ''
    }
  },
)
```

4. Important: do **not** add a `watch(() => props.options, ...)` while `open===true` — that would defeat the contract.

- [ ] **Step 4: Run the test to verify it passes**

Run: `pnpm vitest run src/components/LiveStreamFilterDialog.test.ts`
Expected: BOTH tests pass.

- [ ] **Step 5: Run the full web test suite**

Run: `pnpm vitest run`
Expected: All tests pass; if any snapshot test asserts on `filteredOptions` rendering different content, update the snapshot after verifying the new labels are correct.

- [ ] **Step 6: Run type check**

Run: `pnpm vue-tsc --noEmit`
Expected: no type errors.

- [ ] **Step 7: Commit**

```bash
git add web/src/components/LiveStreamFilterDialog.vue web/src/components/LiveStreamFilterDialog.test.ts
git commit -m "fix(live-stream): freeze multi-dim filter dialog options while open to avoid SSE delta churn"
```

---

## Task 3: Persist stream view + filters to localStorage and rehydrate on mount

**Files:**
- Modify: `web/src/components/LiveRequestStreamV2.vue` (`onMounted` + watchers for filter sets)
- (Optional) `web/src/locales/en-US/dashboard.ts` + 7 other locale files for the toast — keep wording minimal ("已恢复上一次选择")

- [ ] **Step 1: Write the failing test**

Real persistence tests live in the new file `liveStreamViewSettings.test.ts` (created in Step 3); end-to-end rehydration is verified in Task 4 (cypress).

Better — replace this Step 1 with creating `web/src/composables/liveStreamViewSettings.ts` plus its test:

`web/src/composables/liveStreamViewSettings.ts`:

```ts
// liveStreamViewSettings — localStorage persistence for the realtime
// swim-lane UI (groupBy dimension, multi-dim filter choices).
// 2026-07-25 SPEC §0 (operator requirement): page reload should
// restore the operator's last selected stream view.

import { reactive, watch } from 'vue'

const STORAGE_KEY = 'llmgw_stream_view_v1'

export interface StreamViewSettings {
  groupBy: 'vendor' | 'provider' | 'model'
  laneMode: 'small' | 'large'
  filters: {
    requestType: Array<'business' | 'probe'>
    status: string[]
    model: string[]
    provider: string[]
    vendor: string[]
  }
}

const defaults = (): StreamViewSettings => ({
  groupBy: 'vendor',
  laneMode: 'small',
  filters: {
    requestType: ['business', 'probe'],
    status: [],
    model: [],
    provider: [],
    vendor: [],
  },
})

let cache: StreamViewSettings | null = null
const subs = new Set<() => void>()

export function loadStreamViewSettings(): StreamViewSettings {
  if (cache) return cache
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const parsed = JSON.parse(raw) as Partial<StreamViewSettings>
      cache = { ...defaults(), ...parsed } as StreamViewSettings
      return cache
    }
  } catch {
    /* SSR / private mode / quota */
  }
  cache = defaults()
  return cache
}

export function saveStreamViewSettings(settings: StreamViewSettings) {
  cache = settings
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(settings))
  } catch {
    /* ignore */
  }
  for (const cb of subs) cb()
}

/** Subscribe to changes (e.g. for `Watch`-style hot reload). */
export function onStreamViewSettingsChange(cb: () => void): () => void {
  subs.add(cb)
  return () => subs.delete(cb)
}

/** Convenience: a single reactive() binding that's auto-persisted. */
export function useStreamViewSettings() {
  const settings = reactive<StreamViewSettings>(loadStreamViewSettings())
  watch(
    () => JSON.stringify(settings),
    () => saveStreamViewSettings({ ...settings }),
  )
  return settings
}
```

`web/src/composables/liveStreamViewSettings.test.ts`:

```ts
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { loadStreamViewSettings, saveStreamViewSettings } from './liveStreamViewSettings'

const KEY = 'llmgw_stream_view_v1'

describe('stream view settings persistence', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => localStorage.clear())

  it('returns defaults when nothing persisted', () => {
    const s = loadStreamViewSettings()
    expect(s.groupBy).toBe('vendor')
    expect(s.laneMode).toBe('small')
    expect(s.filters.requestType).toEqual(['business', 'probe'])
  })

  it('round-trips through localStorage', () => {
    saveStreamViewSettings({
      groupBy: 'model',
      laneMode: 'large',
      filters: {
        requestType: ['business'],
        status: ['failure'],
        model: ['minimax-m3'],
        provider: ['openai'],
        vendor: ['openai'],
      },
    })
    const stored = JSON.parse(localStorage.getItem(KEY)!)
    expect(stored.groupBy).toBe('model')
    expect(stored.filters.model).toEqual(['minimax-m3'])
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `pnpm vitest run src/composables/liveStreamViewSettings.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Create the module under test**

Files: create `web/src/composables/liveStreamViewSettings.ts` and `web/src/composables/liveStreamViewSettings.test.ts` with the source from Step 1.

- [ ] **Step 4: Run the test to verify it passes**

Run: `pnpm vitest run src/composables/liveStreamViewSettings.test.ts`
Expected: PASS.

- [ ] **Step 5: Wire the composable into `LiveRequestStreamV2.vue`**

In `web/src/components/LiveRequestStreamV2.vue`:

1. Add the import next to other live-stream composables:

```ts
import {
  loadStreamViewSettings,
  useStreamViewSettings,
} from '../composables/liveStreamViewSettings'
```

2. Inside `<script setup>` *before* the existing filter `ref()` declarations, add:

```ts
const settings = useStreamViewSettings()

// 2026-07-25 SPEC §0: rehydrate user-selected dimension, mode, and
// multi-dim filters from localStorage. Falls back to today’s defaults
// when nothing is persisted.
const persisted = loadStreamViewSettings()
```

3. Replace the in-place `ref` initializers to seed from `persisted`:

```ts
const filterDialog = ref<'status' | 'model' | 'provider' | 'vendor' | null>(null)

const requestTypeFilter = ref<Set<'business' | 'probe'>>(new Set(persisted.filters.requestType))
const statusFilter = ref<Set<LiveStatus>>(new Set(persisted.filters.status as LiveStatus[]))
const modelFilter = ref<Set<string>>(new Set(persisted.filters.model))
const providerFilter = ref<Set<string>>(new Set(persisted.filters.provider))
const vendorFilter = ref<Set<LiveModelCategory>>(new Set(persisted.filters.vendor as LiveModelCategory[]))
```

4. In `useSwimLane(...)` results, spread the persisted groupBy / mode (so the lane composable seeds with the operator's last choice):

```ts
const { groupBy: groupByRef, mode: modeRef, ... } = useSwimLane(liveSnapshot) as any
// Rehydrate the persisted dimension/mode from localStorage.
groupByRef.value = settings.groupBy
modeRef.value = settings.laneMode
```

5. Add watchers that persist on change (place them at the end of `<script setup>`):

```ts
watch(requestTypeFilter, (v) => (settings.filters.requestType = Array.from(v)), { deep: true })
watch(statusFilter,      (v) => (settings.filters.status      = Array.from(v)),     { deep: true })
watch(modelFilter,      (v) => (settings.filters.model      = Array.from(v)),     { deep: true })
watch(providerFilter,   (v) => (settings.filters.provider   = Array.from(v)),     { deep: true })
watch(vendorFilter,     (v) => (settings.filters.vendor     = Array.from(v)),     { deep: true })
watch(groupByRef, (v) => (settings.groupBy = v))
watch(modeRef,    (v) => (settings.laneMode = v))
```

6. `clearAllFilters()` now also rehydrates `requestTypeFilter` to `['business', 'probe']` (already does); the watcher above will save the cleared state.

- [ ] **Step 6: Run tests + type check**

Run:

```bash
pnpm vitest run
pnpm vue-tsc --noEmit
```

Expected: green for both.

- [ ] **Step 7: Commit**

```bash
git add web/src/composables/liveStreamViewSettings.ts web/src/composables/liveStreamViewSettings.test.ts web/src/components/LiveRequestStreamV2.vue
git commit -m "feat(live-stream): persist dashboard view (groupBy, mode, filters) to localStorage and rehydrate"
```

---

## Task 4: e2e cypress verification of all three SPEC links

**Files:**
- Modify: `web/cypress/e2e/live-stream.cy.ts` (new file)

- [ ] **Step 1: Write the e2e**

Add at `web/cypress/e2e/live-stream.cy.ts`:

```ts
/// <reference types="cypress" />

describe('realtime swim lane (2026-07-25 fixes)', () => {
  beforeEach(() => {
    cy.login()
    cy.visit('/dashboard?tab=stream')
  })

  it('keeps a single canonical lane per model across mixed casings', () => {
    cy.injectLiveStreamEvents([
      { ts: '2026-07-25T10:00:00Z', model: 'MiniMax-M3', provider_code: 'minimax', vendor: 'minimax' },
      { ts: '2026-07-25T10:00:01Z', model: 'minimax-m3', provider_code: 'minimax', vendor: 'minimax' },
    ])
    cy.get('[data-ls-dimension="model"]').click()
    cy.get('.lsfd-option-label').should('have.length', 1)
    cy.get('.lsfd-option-label').first().invoke('text').then((t) => {
      expect(t.trim()).to.match(/^minimax-m3$/)
    })
  })

  it('preserves filter dialog draft while SSE delta updates options', () => {
    cy.injectLiveStreamEvents([
      { ts: '2026-07-25T10:00:00Z', model: 'claude-3', provider_code: 'anthropic', vendor: 'anthropic' },
    ])

    cy.get('[data-ls-filter="model"]').click()
    cy.get('.lsfd-option-label').should('contain', 'claude-3')
    cy.contains('.lsfd-option', 'claude-3').find('input').check()

    cy.injectLiveStreamEvents([
      { ts: '2026-07-25T10:00:01Z', model: 'gpt-4o', provider_code: 'openai', vendor: 'openai' },
    ])

    cy.get('.lsfd-option-label').should('have.length', 1)
    cy.contains('.lsfd-option', 'claude-3').find('input').should('be.checked')
  })

  it('rehydrates selected view after reload', () => {
    cy.get('[data-ls-dimension="model"]').click()
    cy.reload()
    cy.get('[data-ls-dimension="model"]').should('have.class', 'active')
  })
})
```

- [ ] **Step 2: Run the e2e suite locally**

Run: `pnpm cypress:run --spec cypress/e2e/live-stream.cy.ts`
Expected: PASS for all three assertions.

- [ ] **Step 3: Add the helper fixture**

Create `web/cypress/support/live-stream-shim.ts`:

```ts
// Tiny SSE handler for cypress: posts a batch of live-stream envelopes
// to the dashboard via window.postMessage('injectLiveStreamEvents', batch)
// The widget listens to that channel and feeds events into the global
// live-stream store. Production tests rely on a stub server; this is a
// faster shim for in-browser assertions.

declare global {
  interface Window {
    injectLiveStreamEvents(events: any[]): void
  }
}

Cypress.Commands.add('injectLiveStreamEvents', (events: any[]) => {
  cy.window().then((win) => win.injectLiveStreamEvents(events))
})
```

Run:

```bash
grep -q "injectLiveStreamEvents" web/cypress/support/index.ts || cat >> web/cypress/support/index.ts <<'EOF'
import './live-stream-shim'
EOF
```

- [ ] **Step 4: Wire shim into the in-browser store**

In `web/src/composables/liveStreamStore.ts`:

After the existing `export function reconnectStream() { ... }` add:

```ts
// 2026-07-25 SPEC §1/§2: in-browser testability helper — let cypress
// push synthetic envelopes into the same store so e2e can deterministically
// drive lane aggregation / filter dialog semantics without running a
// stubbed SSE server.
if (typeof window !== 'undefined') {
  ;(window as any).injectLiveStreamEvents = (events: any[]) => {
    for (const env of events) handleEnvelope(env as LiveStreamEnvelope)
  }
}
```

Guard the assignment with a `import.meta.env.MODE !== 'production'` check to keep the surface area minimal in production bundles:

```ts
if (typeof window !== 'undefined' && import.meta.env.DEV) {
  ;(window as any).injectLiveStreamEvents = (events: any[]) => {
    for (const env of events) handleEnvelope(env as LiveStreamEnvelope)
  }
}
```

- [ ] **Step 5: Re-run cypress**

Run: `pnpm cypress:run --spec cypress/e2e/live-stream.cy.ts`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/cypress/e2e/live-stream.cy.ts web/cypress/support/live-stream-shim.ts web/cypress/support/index.ts web/src/composables/liveStreamStore.ts
git commit -m "test(live-stream): add cypress coverage for canonical lane + filter snapshot + view rehydration"
```

---

## Task 5: Documentation update + CHANGELOG entry

**Files:**
- Create: `docs/changelogs/2026-07-25-live-stream-fixes.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Write the changelog file**

Create `docs/changelogs/2026-07-25-live-stream-fixes.md`:

```markdown
# 2026-07-25 — 实时流前端两链路修复

## 摘要
- §1 实时流模型名聚合：`standardModelName` 改为只做 trim，避免与后端 `normalizeModelKey` / `liveStreamDimensionKey` 重复规范化导致同一 canonical 模型被拆成多个 lane。
- §2 多维筛选弹窗：打开期间 freeze `props.options` 快照，避免 SSE delta 引起选项列表抖动；关闭时回退到 `props.selected`。
- §0 持久化：仪表盘 groupBy / laneMode / 多维过滤通过 `liveStreamViewSettings` 写入 localStorage，刷新后自动恢复。

## 验证
- `pnpm vitest run`
- `pnpm vue-tsc --noEmit`
- SPEC §1.2 / §2.2 / §0 覆盖的验收用例
- 计划：`docs/superpowers/plans/2026-07-25-live-stream-frontend-fixes.md`

## 回滚
- 单一文件 revert：`git revert <commit>` 回到旧 `LiveRequestStreamV2.vue` / `LiveStreamFilterDialog.vue` 即可，不影响后端与 SQL。
```

- [ ] **Step 2: Append to `CHANGELOG.md`**

```markdown
- **feat(live-stream):** persist dashboard groupBy / laneMode / multi-dim filters to localStorage; reload restores operator view. SPEC §0.
- **fix(live-stream):** `standardModelName` is now a trim-only pass-through; backend `normalizeModelKey` remains the canonical source. SPEC §1.
- **fix(live-stream):** `LiveStreamFilterDialog` snapshots `props.options` at open time so SSE deltas no longer churn the option list while operator is editing. SPEC §2.
```

- [ ] **Step 3: Commit**

```bash
git add docs/changelogs/2026-07-25-live-stream-fixes.md CHANGELOG.md
git commit -m "docs(changelog): live stream filter snapshot + view persistence"
```

---

## Self-Review Checklist

- [x] SPEC §1 (canonical model name) → Task 1; e2e in Task 4.
- [x] SPEC §2 (filter dialog snapshot) → Task 2; e2e in Task 4.
- [x] SPEC §0 (operator requirement: persist view) → Task 3.
- [x] Placeholder scan: no `TBD`/`TODO`/`similar to Task N` placeholders.
- [x] Type consistency: `liveStreamViewSettings` returns the same `StreamViewSettings` interface used by watchers (no separate `string[]` / `LiveStatus[]` mapping drift).
- [x] No new global state in modules other than `liveStreamViewSettings` (centralised; the `liveStreamStore` test harness access via `window.injectLiveStreamEvents` is DEV-only).

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-25-live-stream-frontend-fixes.md`. Two execution options:

1. Subagent-Driven (recommended) — fresh subagent per task, two-stage review.
2. Inline Execution — execute tasks in this session with checkpoints.

Tell me which you prefer; per your default I'll choose **Subagent-Driven** unless you disagree.
