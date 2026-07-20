// useWorkTypes.ts — shared source for work_type_config (DB-backed).
//
// Smart routing left rail and defaults picker must list the 20+ work types
// from GET /api/admin/work-types, not the hardcoded L1 taxonomy (8 keys).
// Module-level state + inflight de-dupe so multiple mounts share one fetch.

import { ref, type Ref } from 'vue'
import { listWorkTypes, type WorkTypeConfig } from '../api-work-types'

const workTypes: Ref<WorkTypeConfig[]> = ref([])
const loading = ref(false)
const error = ref('')
let inflight: Promise<void> | null = null

async function refreshWorkTypes(includeDisabled = false): Promise<void> {
  if (inflight) return inflight
  loading.value = true
  error.value = ''
  inflight = (async () => {
    try {
      const rows = await listWorkTypes(includeDisabled)
      workTypes.value = includeDisabled ? rows : rows.filter((w) => w.enabled)
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
      inflight = null
    }
  })()
  return inflight
}

function workTypeLabel(key: string): string {
  if (!key) return ''
  return workTypes.value.find((w) => w.key === key)?.label ?? key
}

function workTypeIcon(key: string): string {
  if (!key) return '◈'
  return workTypes.value.some((w) => w.key === key) ? '◈' : '◆'
}

export function useWorkTypes() {
  return {
    workTypes,
    loading,
    error,
    refreshWorkTypes,
    workTypeLabel,
    workTypeIcon,
  }
}
