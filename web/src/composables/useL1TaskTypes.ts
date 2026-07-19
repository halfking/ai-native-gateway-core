// useL1TaskTypes.ts — single source for the L1 task-type taxonomy.
//
// 2026-07-20: L1 task types are now DB-driven. The composable:
//   - Seeds l1TaskTypes with the canonical 8 from L1_TASK_TYPES (api-work-types.ts)
//     so first paint is never blank.
//   - On first call to refreshL1TaskTypes(), fetches
//     /api/admin/work-types/l1-task-types which returns the union of
//     canonical 8 + any operator-added categories from work_type_config.
//   - Module-level state is shared across all callers — multiple components
//     on the same page only trigger one network round-trip.
//   - On fetch error, keeps the canonical seed (UI is never blank).
//
// 2026-07-20: l1Label() now prefers i18n over the backend label so
// switching locale to en-US shows "Code" instead of the Chinese "代码".
// Fallback chain: i18n key (common.l1TaskType.{key}) → backend label → key.
//
// Usage:
//   const { l1TaskTypes, l1Label, refreshL1TaskTypes } = useL1TaskTypes()
//   <option v-for="t in l1TaskTypes" :key="t.key" :value="t.key">{{ t.label }}</option>

import { ref, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { L1_TASK_TYPES, listL1TaskTypes, type L1TaskTypeMeta } from '../api-work-types'

type L1TaskType = Omit<L1TaskTypeMeta, 'count'>

// Module-level state: shared across all useL1TaskTypes() callers.
const l1TaskTypes: Ref<L1TaskType[]> = ref(L1_TASK_TYPES.map((t) => ({ ...t })))
const loading = ref(false)
const error = ref<string>('')
let inflight: Promise<void> | null = null

async function refreshL1TaskTypes(): Promise<void> {
  // De-dupe concurrent requests: if a fetch is in flight, await it.
  if (inflight) return inflight
  loading.value = true
  error.value = ''
  inflight = (async () => {
    try {
      const response = await listL1TaskTypes()
      // Only replace the seed if the backend returned data — guards against
      // an empty 200 response leaving the UI blank.
      if (response.items.length > 0) {
        l1TaskTypes.value = response.items.map((item) => ({
          key: item.key,
          label: item.label,
          icon: item.icon,
        }))
      }
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e)
      error.value = msg
      // Keep canonical seed in place so UI is still populated.
    } finally {
      loading.value = false
      inflight = null
    }
  })()
  return inflight
}

function l1Label(key: string, t?: (k: string) => string): string {
  if (!key) return ''
  // i18n takes priority so locale switch reflects localized labels
  // (e.g. en-US shows "Code" instead of the backend's Chinese "代码").
  // vue-i18n's t() returns the key itself when missing, so we explicitly
  // detect that to avoid showing raw keys.
  if (t) {
    const translated = t(`common.l1TaskType.${key}`)
    if (translated && translated !== `common.l1TaskType.${key}`) return translated
  }
  // Fallback: backend label (which is the canonical Chinese label the
  // classifier was trained with) → finally the raw key.
  return l1TaskTypes.value.find((task) => task.key === key)?.label ?? key
}

function l1Icon(key: string): string {
  if (!key) return ''
  return l1TaskTypes.value.find((t) => t.key === key)?.icon ?? '◆'
}

export function useL1TaskTypes() {
  const { t } = useI18n()
  return {
    l1TaskTypes,
    loading,
    error,
    refreshL1TaskTypes,
    // Bind t() so consumers don't need to thread useI18n() through.
    // We use a closure so locale changes are picked up reactively.
    l1Label: (key: string) => l1Label(key, t),
    l1Icon,
  }
}
