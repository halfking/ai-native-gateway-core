<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import TaskTypeRail from './TaskTypeRail.vue'
import TierGroupList, { type TierKey } from './TierGroupList.vue'
import DefaultRoutingDetailDrawer from './DefaultRoutingDetailDrawer.vue'
import {
  getRoutingDefaults,
  createRoutingDefault,
  updateRoutingDefault,
  deleteRoutingDefault,
  type RoutingDefault,
  type RoutingDefaultUpdate,
} from '../../api/tuning'

const props = withDefaults(defineProps<{
  /** Prefill task-type filter when opened from overview. */
  initialTaskType?: string
  /** Compact layout for drawer embedding. */
  compact?: boolean
}>(), {
  initialTaskType: '',
  compact: false,
})

const { t } = useI18n()

const defaults = ref<RoutingDefault[]>([])
const loading = ref(false)
const error = ref('')
const filterActive = ref(true)
const selectedTask = ref(props.initialTaskType || '')
const busyId = ref<number | null>(null)
const detailRow = ref<RoutingDefault | null>(null)

watch(() => props.initialTaskType, (v) => {
  if (v !== undefined) selectedTask.value = v || ''
})

const filteredRows = computed(() => {
  if (!selectedTask.value) return defaults.value
  return defaults.value.filter((r) => r.task_type === selectedTask.value)
})

const taskCounts = computed(() => {
  const counts: Record<string, number> = {}
  for (const row of defaults.value) {
    counts[row.task_type] = (counts[row.task_type] || 0) + 1
  }
  return counts
})

async function loadDefaults() {
  loading.value = true
  error.value = ''
  try {
    const r = await getRoutingDefaults({ active: filterActive.value })
    defaults.value = r.defaults
  } catch (e: any) {
    error.value = e?.message ?? String(e)
  } finally {
    loading.value = false
  }
}

async function onAdd(payload: { tier: TierKey; canonical_model: string; profile: string; priority: number }) {
  if (!selectedTask.value) {
    error.value = t('routingDefault.empty.needTask')
    return
  }
  try {
    await createRoutingDefault({
      task_type: selectedTask.value,
      tier: payload.tier,
      canonical_model: payload.canonical_model,
      profile: payload.profile,
      priority: payload.priority,
      tenant_id: null,
      reason: '',
    })
    await loadDefaults()
  } catch (e: any) {
    error.value = e?.message ?? String(e)
  }
}

async function onPatch(payload: { id: number; body: Record<string, unknown> }) {
  busyId.value = payload.id
  error.value = ''
  try {
    await updateRoutingDefault(payload.id, payload.body as RoutingDefaultUpdate)
    await loadDefaults()
    if (detailRow.value?.id === payload.id) {
      detailRow.value = defaults.value.find((r) => r.id === payload.id) || null
    }
  } catch (e: any) {
    error.value = t('routingDefault.table.saveFailed') + (e?.message ?? e)
  } finally {
    busyId.value = null
  }
}

async function onSaveDetail(payload: { id: number; body: RoutingDefaultUpdate }) {
  await onPatch(payload)
  if (!error.value) detailRow.value = null
}

async function onRemove(row: RoutingDefault) {
  if (!confirm(t('routingDefault.table.deleteConfirm', {
    id: row.id,
    model: row.canonical_model,
    task: row.task_type,
  }))) return
  busyId.value = row.id
  try {
    await deleteRoutingDefault(row.id)
    if (detailRow.value?.id === row.id) detailRow.value = null
    await loadDefaults()
  } catch (e: any) {
    alert(t('routingDefault.table.deleteFailed') + (e?.message ?? e))
  } finally {
    busyId.value = null
  }
}

onMounted(loadDefaults)

defineExpose({ reload: loadDefaults })
</script>

<template>
  <div class="smart-routing-panel" :class="{ compact }">
    <div class="panel-head">
      <div>
        <h3 v-if="!compact">{{ t('routingDefault.title') }}</h3>
        <p class="subtitle">{{ t('routingDefault.subtitle') }}</p>
      </div>
      <div class="head-actions">
        <label class="active-only">
          <input v-model="filterActive" type="checkbox" @change="loadDefaults" />
          {{ t('routingDefault.filter.activeOnly') }}
        </label>
        <button type="button" class="btn btn-sm" :disabled="loading" @click="loadDefaults">
          {{ loading ? t('routingDefault.actions.loading') : t('routingDefault.actions.refresh') }}
        </button>
      </div>
    </div>

    <p v-if="error" class="error">{{ error }}</p>

    <div class="panel-body">
      <TaskTypeRail v-model="selectedTask" :counts="taskCounts" />
      <div class="panel-main">
        <p v-if="!loading && filteredRows.length === 0" class="empty">{{ t('routingDefault.empty.none') }}</p>
        <TierGroupList
          :rows="filteredRows"
          :task-type="selectedTask"
          :busy-id="busyId"
          @add="onAdd"
          @patch="onPatch"
          @detail="detailRow = $event"
          @remove="onRemove"
        />
      </div>
    </div>

    <DefaultRoutingDetailDrawer
      :row="detailRow"
      @close="detailRow = null"
      @save="onSaveDetail"
      @remove="onRemove"
    />
  </div>
</template>

<style scoped>
.smart-routing-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 480px;
}
.smart-routing-panel.compact {
  min-height: 0;
  height: 100%;
  gap: 8px;
}
.panel-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: flex-start;
}
.panel-head h3 {
  margin: 0 0 4px;
  font-size: 16px;
}
.subtitle {
  margin: 0;
  font-size: 12px;
  color: var(--text-muted, #6b7280);
  max-width: 720px;
}
.compact .subtitle {
  font-size: 11px;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}
.active-only {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
}
.panel-body {
  display: flex;
  border: 1px solid var(--border, #e5e7eb);
  border-radius: 10px;
  background: var(--bg-card, #fff);
  min-height: 420px;
  overflow: hidden;
  flex: 1;
}
.compact .panel-body {
  min-height: 0;
}
.panel-main {
  flex: 1;
  padding: 12px;
  overflow: auto;
}
.error { color: #b91c1c; font-size: 13px; margin: 0; }
.empty {
  color: var(--text-muted, #9ca3af);
  font-size: 13px;
  margin: 0 0 12px;
}
@media (max-width: 860px) {
  .panel-body { flex-direction: column; }
  .panel-head { flex-direction: column; }
}
</style>
