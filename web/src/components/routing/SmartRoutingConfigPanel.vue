<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import ModelPicker from '../ModelPicker.vue'
import { useWorkTypes } from '../../composables/useWorkTypes'
import {
  getWorkType,
  listWorkTypes,
  putWorkTypeRoutes,
  groupRoutesByLayer,
  type ModelRoute,
  type ModelRouteTier,
  type WorkTypeConfig,
} from '../../api-work-types'

const props = withDefaults(defineProps<{
  initialTaskType?: string
  compact?: boolean
}>(), { initialTaskType: '', compact: false })

const { t } = useI18n()
const { refreshWorkTypes } = useWorkTypes()
const tiers: ModelRouteTier[] = ['primary', 'secondary', 'fallback']
const maxRoutes = 5
const workTypes = ref<WorkTypeConfig[]>([])
const selectedKey = ref(props.initialTaskType || '')
const detail = ref<WorkTypeConfig | null>(null)
const draft = ref<Record<ModelRouteTier, ModelRoute[]>>({ primary: [], secondary: [], fallback: [] })
const loading = ref(false)
const saving = ref(false)
const loadVersion = ref(0)
const error = ref('')
const saved = ref(false)

const selected = computed(() => workTypes.value.find((wt) => wt.key === selectedKey.value) || null)

function emptyRoute(tier: ModelRouteTier): ModelRoute {
  return { canonical_name: '', weight: 1, min_score: 0, enabled: true, tier, task_quality_score: 0 }
}

function setDraft(routes?: ModelRoute[]) {
  const groups = groupRoutesByLayer(routes)
  draft.value = {
    primary: groups.primary.map((r) => ({ ...r })),
    secondary: groups.secondary.map((r) => ({ ...r })),
    fallback: groups.fallback.map((r) => ({ ...r })),
  }
}

async function loadDetail() {
  const key = selectedKey.value
  const version = ++loadVersion.value
  if (!key) {
    detail.value = null
    setDraft([])
    loading.value = false
    return
  }
  loading.value = true
  error.value = ''
  try {
    const next = await getWorkType(key)
    if (version !== loadVersion.value || key !== selectedKey.value) return
    detail.value = next
    setDraft(next.model_routes)
  } catch (e: unknown) {
    if (version === loadVersion.value) {
      error.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (version === loadVersion.value) loading.value = false
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    workTypes.value = (await listWorkTypes(false)).filter((wt) => wt.enabled)
    if (!selectedKey.value || !workTypes.value.some((wt) => wt.key === selectedKey.value)) {
      selectedKey.value = workTypes.value[0]?.key || ''
    }
    await loadDetail()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
    loading.value = false
  }
}

function add(tier: ModelRouteTier) {
  if (draft.value[tier].length < maxRoutes) {
    draft.value = { ...draft.value, [tier]: [...draft.value[tier], emptyRoute(tier)] }
  }
}

function remove(tier: ModelRouteTier, index: number) {
  const rows = [...draft.value[tier]]
  rows.splice(index, 1)
  draft.value = { ...draft.value, [tier]: rows }
}

async function save() {
  const key = selectedKey.value
  if (!key) return
  saving.value = true
  saved.value = false
  error.value = ''
  const payload: ModelRoute[] = []
  for (const tier of tiers) {
    const rows = draft.value[tier]
    rows.forEach((row, index) => {
      const name = row.canonical_name.trim()
      if (!name) return
      payload.push({ ...row, canonical_name: name, tier, weight: rows.length - index })
    })
  }
  try {
    const result = await putWorkTypeRoutes(key, payload)
    if (key === selectedKey.value) {
      detail.value = { ...(detail.value as WorkTypeConfig), model_routes: result.model_routes }
      setDraft(result.model_routes)
    }
    await refreshWorkTypes()
    if (key === selectedKey.value) saved.value = true
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

watch(() => props.initialTaskType, (value) => {
  if (value && value !== selectedKey.value) {
    selectedKey.value = value
    void loadDetail()
  }
})
onMounted(load)

defineExpose({ reload: load })
</script>

<template>
  <div class="smart-routing-panel" :class="{ compact }">
    <div class="panel-head">
      <div>
        <h3 v-if="!compact">{{ t('routingDefault.title') }}</h3>
        <p class="subtitle">{{ t('routingDefault.subtitle') }}</p>
      </div>
      <div class="head-actions">
        <select v-model="selectedKey" :disabled="loading || saving" aria-label="work type" @change="loadDetail">
          <option value="">{{ t('routingDefault.rail.all') }}</option>
          <option v-for="wt in workTypes" :key="wt.key" :value="wt.key">{{ wt.label }}</option>
        </select>
        <button type="button" class="btn btn-sm" :disabled="loading || saving" @click="load">{{ t('routingDefault.actions.refresh') }}</button>
      </div>
    </div>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="saved" class="saved">{{ t('workTypes.savedOk') }}</p>
    <div v-if="!selected" class="empty">{{ t('routingDefault.empty.none') }}</div>
    <div v-else class="route-panel">
      <div v-for="tier in tiers" :key="tier" class="tier-group">
        <header class="tier-head">
          <h3>{{ t(`workTypes.layers.${tier}`) }} <small>{{ draft[tier].length }}/{{ maxRoutes }}</small></h3>
          <button type="button" class="btn btn-sm" :disabled="draft[tier].length >= maxRoutes" @click="add(tier)">+ {{ t(`workTypes.layers.${tier}`) }}</button>
        </header>
        <div v-if="!draft[tier].length" class="empty">{{ t(`workTypes.layers.empty${tier[0].toUpperCase() + tier.slice(1)}`) }}</div>
        <div v-for="(row, index) in draft[tier]" :key="`${tier}-${index}`" class="route-row">
          <span class="rank">{{ index + 1 }}</span>
          <ModelPicker v-model="row.canonical_name" :placeholder="t('workTypes.detail.selectModelPlaceholder')" />
          <input v-model.number="row.min_score" type="number" min="0" step="0.1" aria-label="minimum score" />
          <label class="enabled"><input v-model="row.enabled" type="checkbox" /> {{ t('workTypes.detail.routeEnabled') }}</label>
          <button type="button" class="btn btn-sm btn-ghost" @click="remove(tier, index)">×</button>
        </div>
      </div>
      <button type="button" class="btn btn-primary" :disabled="saving || loading" @click="save">{{ saving ? t('workTypes.saving') : t('workTypes.saveBtn') }}</button>
    </div>
  </div>
</template>

<style scoped>
.smart-routing-panel { display:flex; flex-direction:column; gap:12px; min-height:480px; }
.smart-routing-panel.compact { min-height:0; height:100%; }
.panel-head { display:flex; justify-content:space-between; gap:12px; align-items:flex-start; }
.panel-head h3 { margin:0 0 4px; font-size:16px; }
.subtitle { margin:0; font-size:12px; color:var(--muted); max-width:720px; }
.head-actions { display:flex; align-items:center; gap:8px; flex-shrink:0; }
.head-actions select { min-width:180px; }
.route-panel { display:flex; flex-direction:column; gap:12px; }
.tier-group { border:1px solid var(--border); border-radius:8px; padding:10px; background:var(--card); }
.tier-head { display:flex; align-items:center; justify-content:space-between; gap:8px; }
.tier-head h3 { margin:0; font-size:14px; }
.tier-head small { color:var(--muted); font-weight:400; }
.route-row { display:grid; grid-template-columns:28px minmax(180px,1fr) 100px auto 32px; gap:8px; align-items:center; padding:8px 0; border-top:1px solid var(--border); }
.rank { color:var(--muted); text-align:center; }
.enabled { display:flex; align-items:center; gap:4px; font-size:12px; white-space:nowrap; }
.empty { color:var(--muted); font-size:13px; padding:8px 0; }
.error { color:#b91c1c; font-size:13px; margin:0; }
.saved { color:#15803d; font-size:13px; margin:0; }
@media (max-width: 760px) { .panel-head { flex-direction:column; } .route-row { grid-template-columns:24px 1fr 32px; } .route-row input[type=number], .enabled { grid-column:2; } }
</style>
