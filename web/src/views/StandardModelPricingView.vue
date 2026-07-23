<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { localeRef } from '../i18n'
import { ref, computed, onMounted } from 'vue'
import {
  getAdminMaasModelRates,
  updateAdminMaasSettings,
  upsertAdminMaasModelRate,
  deleteAdminMaasModelRate,
  resetAdminMaasModelRateFields,
  batchUpsertAdminMaasModelRates,
  batchResetAdminMaasModelRates,
  batchFillGlobalAdminMaasModelRates,
  type AdminMaasModelRate,
  type MaasAdminSettings,
  type MaasModelRateUpsert,
} from '../api'
import ModelCatalogFilterBar from '../components/ModelCatalogFilterBar.vue'
import { useModelCatalogFilters } from '../composables/useModelCatalogFilters'

const { t } = useI18n()


type RateField = 'in' | 'out' | 'cache_in' | 'cache_out' | 'image' | 'audio' | 'video'

const loading = ref(false)
const savingSettings = ref(false)
const error = ref('')
const settingsMsg = ref('')
const models = ref<AdminMaasModelRate[]>([])
const settings = ref<MaasAdminSettings | null>(null)

function isFullyManual(m: AdminMaasModelRate) {
  return m.manual_in && m.manual_out && m.manual_cache_in && m.manual_cache_out &&
    (m.manual_image || m.manual_audio || m.manual_video ||
      (m.credits_per_1m_image_tokens === 0 && m.credits_per_1m_audio_tokens === 0 && m.credits_per_1m_video_tokens === 0))
}

function isMultimodal(m: AdminMaasModelRate | null | undefined) {
  if (!m) return false
  const mod = (m.modality || '').toLowerCase()
  return mod === 'multimodal' || mod === 'vision' || mod === 'audio' || mod === 'video' ||
    m.manual_image || m.manual_audio || m.manual_video
}

function modalityLabel(m: AdminMaasModelRate): string {
  const mod = (m.modality || '').toLowerCase()
  if (mod === 'multimodal') return t('standardModelPricing.table.modalityMultimodal')
  if (mod === 'vision') return t('standardModelPricing.table.modalityVision')
  if (mod === 'audio') return t('standardModelPricing.table.modalityAudio')
  if (mod === 'video') return t('standardModelPricing.table.modalityVideo')
  if (mod === 'text') return t('standardModelPricing.table.modalityText')
  if (mod === 'embedding') return t('standardModelPricing.table.modalityEmbedding')
  return t('standardModelPricing.table.modalityOther')
}

const pricingStatusOptions = [
  { value: 'default', label: t('standardModelPricing.defaultOnly') },
  { value: 'custom', label: t('standardModelPricing.customOnly') },
  { value: 'partial', label: t('standardModelPricing.partialOnly') },
]

const {
  pickedModel,
  filterVendor,
  extraFilter: filterMode,
  vendorOptions,
  filtered,
  clearFilters: clearCatalogFilters,
} = useModelCatalogFilters<AdminMaasModelRate>({
  items: models,
  getVendor: (m) => m.vendor?.trim() || t('standardModelPricing.otherVendor'),
  getCanonicalName: (m) => m.canonical_name,
  getDisplayName: (m) => m.display_name,
  matchExtra: (m, mode) => {
    if (mode === 'custom') return m.is_custom
    if (mode === 'default') return !m.is_custom
    if (mode === 'partial') return m.is_custom && !isFullyManual(m)
    return true
  },
})

const selectedRows = ref<Set<number>>(new Set())
const clipboard = ref<{ in: number; out: number; cache_in: number; cache_out: number; image: number; audio: number; video: number } | null>(null)
const showBatchModal = ref(false)
const batchForm = ref({
  credits_per_1m_in: 0,
  credits_per_1m_out: 0,
  credits_per_1m_cache_in: 0,
  credits_per_1m_cache_out: 0,
  credits_per_1m_image_tokens: 0,
  credits_per_1m_audio_tokens: 0,
  credits_per_1m_video_tokens: 0,
})
const savingBatch = ref(false)
const batchMsg = ref('')

const editRow = ref<AdminMaasModelRate | null>(null)
const editForm = ref<MaasModelRateUpsert>(emptyEditForm())
const savingRow = ref(false)

function emptyEditForm(): MaasModelRateUpsert {
  return {
    credits_per_1m_in: 0,
    credits_per_1m_out: 0,
    credits_per_1m_cache_in: 0,
    credits_per_1m_cache_out: 0,
    manual_in: false,
    manual_out: false,
    manual_cache_in: false,
    manual_cache_out: false,
  }
}

const customCount = computed(() => models.value.filter((m) => m.is_custom).length)
const defaultCount = computed(() => models.value.length - customCount.value)
const multimodalCount = computed(() => models.value.filter((m) => m.manual_image || m.manual_audio || m.manual_video).length)

const discountPercent = computed({
  get: () => Math.round((settings.value?.global_discount ?? 1) * 100),
  set: (v: number) => {
    if (settings.value) settings.value.global_discount = Math.min(100, Math.max(1, v)) / 100
  },
})

function manualCount(m: AdminMaasModelRate) {
  return [m.manual_in, m.manual_out, m.manual_cache_in, m.manual_cache_out, m.manual_image, m.manual_audio, m.manual_video].filter(Boolean).length
}

function fmtCredits(n: number) {
  return n.toLocaleString(localeRef.value)
}

function pricePer1M(credits: number) {
  const st = settings.value
  if (!st || credits <= 0) return '—'
  const yuan = (credits * st.cents_per_credit) / 100
  return `${st.currency_display === 'CNY' ? '¥' : st.currency_display}${yuan.toFixed(4)}`
}

function effectiveGlobal(field: RateField) {
  const st = settings.value
  if (!st) return 0
  const disc = st.global_discount ?? 1
  const pick = (base: number | undefined, fallback: number) =>
    Math.ceil((base && base > 0 ? base : fallback) * disc)
  const inBase = st.base_credits_per_1m_in ?? st.base_credits_per_1m
  switch (field) {
    case 'in':
      return pick(st.base_credits_per_1m_in, inBase)
    case 'out':
      return pick(st.base_credits_per_1m_out, inBase)
    case 'cache_in':
      return pick(st.base_credits_per_1m_cache_in, inBase)
    case 'cache_out':
      return pick(st.base_credits_per_1m_cache_out, inBase)
    case 'image':
      return pick(undefined, inBase)
    case 'audio':
      return pick(undefined, inBase)
    case 'video':
      return pick(undefined, inBase)
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const rates = await getAdminMaasModelRates()
    models.value = rates.items ?? []
    settings.value = {
      ...rates.settings,
      base_credits_per_1m_in: rates.settings.base_credits_per_1m_in ?? rates.settings.base_credits_per_1m,
      base_credits_per_1m_out: rates.settings.base_credits_per_1m_out ?? rates.settings.base_credits_per_1m,
      base_credits_per_1m_cache_in: rates.settings.base_credits_per_1m_cache_in ?? rates.settings.base_credits_per_1m,
      base_credits_per_1m_cache_out: rates.settings.base_credits_per_1m_cache_out ?? rates.settings.base_credits_per_1m,
      global_discount: rates.settings.global_discount ?? 1,
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('standardModelPricing.loadFailed')
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  if (!settings.value) return
  savingSettings.value = true
  settingsMsg.value = ''
  error.value = ''
  try {
    await updateAdminMaasSettings({
      cents_per_credit: settings.value.cents_per_credit,
      base_credits_per_1m_in: settings.value.base_credits_per_1m_in ?? settings.value.base_credits_per_1m,
      base_credits_per_1m_out: settings.value.base_credits_per_1m_out,
      base_credits_per_1m_cache_in: settings.value.base_credits_per_1m_cache_in,
      base_credits_per_1m_cache_out: settings.value.base_credits_per_1m_cache_out,
      global_discount: settings.value.global_discount ?? 1,
      currency_display: settings.value.currency_display,
    })
    settingsMsg.value = t('standardModelPricing.saved')
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('standardModelPricing.saveFailed')
  } finally {
    savingSettings.value = false
  }
}

function openEdit(row: AdminMaasModelRate) {
  editRow.value = row
  editForm.value = {
    credits_per_1m_in: row.manual_in ? (row.custom_credits_per_1m_in ?? row.credits_per_1m_in) : effectiveGlobal('in'),
    credits_per_1m_out: row.manual_out ? (row.custom_credits_per_1m_out ?? row.credits_per_1m_out) : effectiveGlobal('out'),
    credits_per_1m_cache_in: row.manual_cache_in ? (row.custom_credits_per_1m_cache_in ?? row.credits_per_1m_cache_in) : effectiveGlobal('cache_in'),
    credits_per_1m_cache_out: row.manual_cache_out ? (row.custom_credits_per_1m_cache_out ?? row.credits_per_1m_cache_out) : effectiveGlobal('cache_out'),
    credits_per_1m_image_tokens: row.manual_image ? (row.custom_credits_per_1m_image_tokens ?? row.credits_per_1m_image_tokens) : effectiveGlobal('image'),
    credits_per_1m_audio_tokens: row.manual_audio ? (row.custom_credits_per_1m_audio_tokens ?? row.credits_per_1m_audio_tokens) : effectiveGlobal('audio'),
    credits_per_1m_video_tokens: row.manual_video ? (row.custom_credits_per_1m_video_tokens ?? row.credits_per_1m_video_tokens) : effectiveGlobal('video'),
    manual_in: row.manual_in,
    manual_out: row.manual_out,
    manual_cache_in: row.manual_cache_in,
    manual_cache_out: row.manual_cache_out,
    manual_image: row.manual_image,
    manual_audio: row.manual_audio,
    manual_video: row.manual_video,
  }
}

function closeEdit() {
  editRow.value = null
}

async function saveEdit() {
  if (!editRow.value) return
  if (!editForm.value.manual_in && !editForm.value.manual_out && !editForm.value.manual_cache_in && !editForm.value.manual_cache_out &&
      !editForm.value.manual_image && !editForm.value.manual_audio && !editForm.value.manual_video) {
    error.value = t('standardModelPricing.needOneManual')
    return
  }
  savingRow.value = true
  error.value = ''
  try {
    await upsertAdminMaasModelRate(editRow.value.canonical_id, { ...editForm.value })
    closeEdit()
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('standardModelPricing.saveFailed')
  } finally {
    savingRow.value = false
  }
}

async function resetAll(row: AdminMaasModelRate) {
  if (!row.is_custom) return
  if (!confirm(`${t('standardModelPricing.editModal.resetConfirm').replace('{name}', row.display_name)}`)) return
  error.value = ''
  try {
    await deleteAdminMaasModelRate(row.canonical_id)
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('standardModelPricing.resetFailed')
  }
}

async function resetField(row: AdminMaasModelRate, field: RateField) {
  error.value = ''
  try {
    await resetAdminMaasModelRateFields(row.canonical_id, [field])
    if (editRow.value?.canonical_id === row.canonical_id) closeEdit()
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('standardModelPricing.resetFailed')
  }
}

function fillGlobalToEdit() {
  editForm.value.credits_per_1m_in = effectiveGlobal('in')
  editForm.value.credits_per_1m_out = effectiveGlobal('out')
  editForm.value.credits_per_1m_cache_in = effectiveGlobal('cache_in')
  editForm.value.credits_per_1m_cache_out = effectiveGlobal('cache_out')
  editForm.value.credits_per_1m_image_tokens = effectiveGlobal('image')
  editForm.value.credits_per_1m_audio_tokens = effectiveGlobal('audio')
  editForm.value.credits_per_1m_video_tokens = effectiveGlobal('video')
}

const textRateFields: { key: 'in' | 'out' | 'cache_in' | 'cache_out'; label: string; formKey: 'credits_per_1m_in' | 'credits_per_1m_out' | 'credits_per_1m_cache_in' | 'credits_per_1m_cache_out'; manualKey: 'manual_in' | 'manual_out' | 'manual_cache_in' | 'manual_cache_out'; valueKey: 'credits_per_1m_in' | 'credits_per_1m_out' | 'credits_per_1m_cache_in' | 'credits_per_1m_cache_out'; manualFlag: 'manual_in' | 'manual_out' | 'manual_cache_in' | 'manual_cache_out' }[] = [
  { key: 'in', label: t('standardModelPricing.input'), formKey: 'credits_per_1m_in', manualKey: 'manual_in', valueKey: 'credits_per_1m_in', manualFlag: 'manual_in' },
  { key: 'out', label: t('standardModelPricing.output'), formKey: 'credits_per_1m_out', manualKey: 'manual_out', valueKey: 'credits_per_1m_out', manualFlag: 'manual_out' },
  { key: 'cache_in', label: t('standardModelPricing.cacheRead'), formKey: 'credits_per_1m_cache_in', manualKey: 'manual_cache_in', valueKey: 'credits_per_1m_cache_in', manualFlag: 'manual_cache_in' },
  { key: 'cache_out', label: t('standardModelPricing.cacheWrite'), formKey: 'credits_per_1m_cache_out', manualKey: 'manual_cache_out', valueKey: 'credits_per_1m_cache_out', manualFlag: 'manual_cache_out' },
]

const multimodalRateFields: { key: 'image' | 'audio' | 'video'; label: string; formKey: 'credits_per_1m_image_tokens' | 'credits_per_1m_audio_tokens' | 'credits_per_1m_video_tokens'; manualKey: 'manual_image' | 'manual_audio' | 'manual_video'; valueKey: 'credits_per_1m_image_tokens' | 'credits_per_1m_audio_tokens' | 'credits_per_1m_video_tokens'; manualFlag: 'manual_image' | 'manual_audio' | 'manual_video' }[] = [
  { key: 'image', label: t('standardModelPricing.field.image'), formKey: 'credits_per_1m_image_tokens', manualKey: 'manual_image', valueKey: 'credits_per_1m_image_tokens', manualFlag: 'manual_image' },
  { key: 'audio', label: t('standardModelPricing.field.audio'), formKey: 'credits_per_1m_audio_tokens', manualKey: 'manual_audio', valueKey: 'credits_per_1m_audio_tokens', manualFlag: 'manual_audio' },
  { key: 'video', label: t('standardModelPricing.field.video'), formKey: 'credits_per_1m_video_tokens', manualKey: 'manual_video', valueKey: 'credits_per_1m_video_tokens', manualFlag: 'manual_video' },
]

const rateFields = computed(() => [...textRateFields, ...multimodalRateFields])

const allSelected = computed(() => {
  return filtered.value.length > 0 && filtered.value.every(row => selectedRows.value.has(row.canonical_id))
})

function toggleRow(canonicalId: number) {
  if (selectedRows.value.has(canonicalId)) {
    selectedRows.value.delete(canonicalId)
  } else {
    selectedRows.value.add(canonicalId)
  }
}

function toggleSelectAll(e: Event) {
  const checked = (e.target as HTMLInputElement).checked
  if (checked) {
    filtered.value.forEach(row => selectedRows.value.add(row.canonical_id))
  } else {
    selectedRows.value.clear()
  }
}

function copyRow(row: AdminMaasModelRate) {
  clipboard.value = {
    in: row.credits_per_1m_in,
    out: row.credits_per_1m_out,
    cache_in: row.credits_per_1m_cache_in,
    cache_out: row.credits_per_1m_cache_out,
    image: row.credits_per_1m_image_tokens,
    audio: row.credits_per_1m_audio_tokens,
    video: row.credits_per_1m_video_tokens,
  }
}

function copyEditForm() {
  clipboard.value = {
    in: editForm.value.credits_per_1m_in,
    out: editForm.value.credits_per_1m_out,
    cache_in: editForm.value.credits_per_1m_cache_in,
    cache_out: editForm.value.credits_per_1m_cache_out,
    image: editForm.value.credits_per_1m_image_tokens ?? 0,
    audio: editForm.value.credits_per_1m_audio_tokens ?? 0,
    video: editForm.value.credits_per_1m_video_tokens ?? 0,
  }
}

function pasteEditForm() {
  if (!clipboard.value) return
  editForm.value.credits_per_1m_in = clipboard.value.in
  editForm.value.credits_per_1m_out = clipboard.value.out
  editForm.value.credits_per_1m_cache_in = clipboard.value.cache_in
  editForm.value.credits_per_1m_cache_out = clipboard.value.cache_out
  editForm.value.credits_per_1m_image_tokens = clipboard.value.image
  editForm.value.credits_per_1m_audio_tokens = clipboard.value.audio
  editForm.value.credits_per_1m_video_tokens = clipboard.value.video
  editForm.value.manual_in = true
  editForm.value.manual_out = true
  editForm.value.manual_cache_in = true
  editForm.value.manual_cache_out = true
  editForm.value.manual_image = true
  editForm.value.manual_audio = true
  editForm.value.manual_video = true
}

async function pasteToSelected() {
  if (!clipboard.value || selectedRows.value.size === 0) return
  savingBatch.value = true
  batchMsg.value = ''
  try {
    const updates = Array.from(selectedRows.value).map(canonical_id => ({
      canonical_id,
      credits_per_1m_in: clipboard.value!.in,
      credits_per_1m_out: clipboard.value!.out,
      credits_per_1m_cache_in: clipboard.value!.cache_in,
      credits_per_1m_cache_out: clipboard.value!.cache_out,
      credits_per_1m_image_tokens: clipboard.value!.image,
      credits_per_1m_audio_tokens: clipboard.value!.audio,
      credits_per_1m_video_tokens: clipboard.value!.video,
      manual_in: true,
      manual_out: true,
      manual_cache_in: true,
      manual_cache_out: true,
      manual_image: true,
      manual_audio: true,
      manual_video: true,
    }))
    const res = await batchUpsertAdminMaasModelRates(updates)
    batchMsg.value = t('standardModelPricing.batch.msgPasted').replace('{n}', String(res.updated))
    selectedRows.value.clear()
    await load()
  } catch (e: unknown) {
    batchMsg.value = t('standardModelPricing.batch.msgFailed')
  } finally {
    savingBatch.value = false
  }
}

function openBatchModal() {
  batchForm.value = {
    credits_per_1m_in: 0,
    credits_per_1m_out: 0,
    credits_per_1m_cache_in: 0,
    credits_per_1m_cache_out: 0,
    credits_per_1m_image_tokens: 0,
    credits_per_1m_audio_tokens: 0,
    credits_per_1m_video_tokens: 0,
  }
  showBatchModal.value = true
}

async function applyBatch() {
  if (selectedRows.value.size === 0) return
  savingBatch.value = true
  batchMsg.value = ''
  try {
    const updates = Array.from(selectedRows.value).map(canonical_id => ({
      canonical_id,
      credits_per_1m_in: batchForm.value.credits_per_1m_in,
      credits_per_1m_out: batchForm.value.credits_per_1m_out,
      credits_per_1m_cache_in: batchForm.value.credits_per_1m_cache_in,
      credits_per_1m_cache_out: batchForm.value.credits_per_1m_cache_out,
      credits_per_1m_image_tokens: batchForm.value.credits_per_1m_image_tokens,
      credits_per_1m_audio_tokens: batchForm.value.credits_per_1m_audio_tokens,
      credits_per_1m_video_tokens: batchForm.value.credits_per_1m_video_tokens,
      manual_in: true,
      manual_out: true,
      manual_cache_in: true,
      manual_cache_out: true,
      manual_image: true,
      manual_audio: true,
      manual_video: true,
    }))
    const res = await batchUpsertAdminMaasModelRates(updates)
    batchMsg.value = t('standardModelPricing.batch.msgPasted').replace('{n}', String(res.updated))
    showBatchModal.value = false
    selectedRows.value.clear()
    await load()
  } catch (e: unknown) {
    batchMsg.value = t('standardModelPricing.batch.msgFailed')
  } finally {
    savingBatch.value = false
  }
}

async function batchResetAll() {
  const ids = Array.from(selectedRows.value)
  if (ids.length === 0) return
  if (!confirm(t('standardModelPricing.batch.resetAllConfirm').replace('{n}', String(ids.length)))) return
  savingBatch.value = true
  batchMsg.value = ''
  try {
    const res = await batchResetAdminMaasModelRates(ids.map(canonical_id => ({ canonical_id, fields: ['all'] })))
    batchMsg.value = t('standardModelPricing.batch.msgReset').replace('{n}', String(res.updated))
    selectedRows.value.clear()
    await load()
  } catch (e: unknown) {
    batchMsg.value = t('standardModelPricing.batch.msgFailed')
  } finally {
    savingBatch.value = false
  }
}

async function batchFillGlobal() {
  const ids = Array.from(selectedRows.value)
  if (ids.length === 0) return
  if (!confirm(t('standardModelPricing.batch.fillGlobalConfirm').replace('{n}', String(ids.length)))) return
  savingBatch.value = true
  batchMsg.value = ''
  try {
    const res = await batchFillGlobalAdminMaasModelRates(ids.map(canonical_id => ({ canonical_id, all_dimensions: true })))
    batchMsg.value = t('standardModelPricing.batch.msgFilled').replace('{n}', String(res.updated))
    selectedRows.value.clear()
    await load()
  } catch (e: unknown) {
    batchMsg.value = t('standardModelPricing.batch.msgFailed')
  } finally {
    savingBatch.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="pricing-page">
    <div class="page-header">
      <h2>{{ t('standardModelPricing.page.title') }}</h2>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
        {{ loading ? t('standardModelPricing.page.refreshLoading') : t('standardModelPricing.page.refresh') }}
      </button>
    </div>

    <p class="page-desc" v-html="t('standardModelPricing.desc')"></p>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="settingsMsg" class="alert alert-success">{{ settingsMsg }}</div>

    <div v-if="settings" class="card settings-card">
      <h3 class="section-title">{{ t('standardModelPricing.settings.title') }}</h3>
      <div class="settings-grid">
        <label class="field">
          <span class="field-label">{{ t('standardModelPricing.settings.inputToken') }}</span>
          <input v-model.number="settings.base_credits_per_1m_in" type="number" min="1" class="input compact" />
          <span class="field-hint">{{ t('standardModelPricing.settings.effective', { value: fmtCredits(effectiveGlobal('in')) }) }}</span>
        </label>
        <label class="field">
          <span class="field-label">{{ t('standardModelPricing.settings.outputToken') }}</span>
          <input v-model.number="settings.base_credits_per_1m_out" type="number" min="1" class="input compact" />
          <span class="field-hint">{{ t('standardModelPricing.settings.effective', { value: fmtCredits(effectiveGlobal('out')) }) }}</span>
        </label>
        <label class="field">
          <span class="field-label">{{ t('standardModelPricing.settings.cacheReadToken') }}</span>
          <input v-model.number="settings.base_credits_per_1m_cache_in" type="number" min="1" class="input compact" />
          <span class="field-hint">{{ t('standardModelPricing.settings.effective', { value: fmtCredits(effectiveGlobal('cache_in')) }) }}</span>
        </label>
        <label class="field">
          <span class="field-label">{{ t('standardModelPricing.settings.cacheWriteToken') }}</span>
          <input v-model.number="settings.base_credits_per_1m_cache_out" type="number" min="1" class="input compact" />
          <span class="field-hint">{{ t('standardModelPricing.settings.effective', { value: fmtCredits(effectiveGlobal('cache_out')) }) }}</span>
        </label>
        <label class="field">
          <span class="field-label">{{ t('standardModelPricing.settings.discount') }}</span>
          <div class="discount-row">
            <input v-model.number="discountPercent" type="range" min="10" max="100" step="1" class="discount-slider" />
            <span class="discount-val">{{ discountPercent }}%</span>
          </div>
          <span class="field-hint">{{ t('standardModelPricing.settings.discountHint') }}</span>
        </label>
        <label class="field">
          <span class="field-label">{{ t('standardModelPricing.settings.centsPerCredit') }}</span>
          <input v-model.number="settings.cents_per_credit" type="number" min="0.0001" step="0.0001" class="input compact" />
        </label>
        <label class="field">
          <span class="field-label">{{ t('standardModelPricing.settings.currencyDisplay') }}</span>
          <select v-model="settings.currency_display" class="input compact">
            <option value="CNY">CNY</option>
            <option value="USD">USD</option>
          </select>
        </label>
      </div>
      <div class="settings-actions">
        <button class="btn btn-primary btn-sm" :disabled="savingSettings" @click="saveSettings">
          {{ savingSettings ? t('standardModelPricing.settings.saving') : t('standardModelPricing.settings.save') }}
        </button>
        <span class="cf-meta">
          {{ t('standardModelPricing.filter.summary', { custom: customCount, defaults: defaultCount, total: models.length }) }}
          <span v-if="multimodalCount > 0" class="multimodal-tag"> · {{ multimodalCount }} 含多模态手工</span>
        </span>
      </div>
    </div>

    <ModelCatalogFilterBar
      v-model:picked-model="pickedModel"
      v-model:filter-vendor="filterVendor"
      v-model:extra-filter="filterMode"
      :vendor-options="vendorOptions"
      :count="filtered.length"
      picker-title="定价管理 · 模型筛选"
      picker-placeholder="搜索标准模型…"
      status-label="全部定价"
      :status-options="pricingStatusOptions"
      @clear="clearCatalogFilters"
    />

    <div class="card table-wrap">
      <table class="data-table pricing-table">
        <thead>
          <tr>
            <th><input type="checkbox" :checked="allSelected" @change="toggleSelectAll" /></th>
            <th>{{ t('standardModelPricing.table.colModel') }}</th>
            <th>{{ t('standardModelPricing.table.colModality') }}</th>
            <th v-for="f in textRateFields" :key="f.key" class="num">{{ f.label }}</th>
            <th>{{ t('standardModelPricing.table.colStatus') }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!loading && filtered.length === 0">
            <td :colspan="5 + textRateFields.length" class="empty-cell">{{ t('standardModelPricing.table.empty') }}</td>
          </tr>
          <tr v-for="row in filtered" :key="row.canonical_id" :class="{ selected: selectedRows.has(row.canonical_id) }">
            <td><input type="checkbox" :checked="selectedRows.has(row.canonical_id)" @change="toggleRow(row.canonical_id)" /></td>
            <td>
              <div class="model-name">{{ row.display_name }}</div>
              <code class="mono-sm">{{ row.canonical_name }}</code>
            </td>
            <td>
              <span class="modality-badge" :class="'modality-' + (row.modality || 'other')">{{ modalityLabel(row) }}</span>
              <span v-if="row.manual_image || row.manual_audio || row.manual_video" class="multimodal-tag" :title="t('standardModelPricing.table.hasMultimodal')">MM</span>
            </td>
            <td v-for="f in textRateFields" :key="f.key" class="num rate-cell">
              <span>{{ fmtCredits(row[f.valueKey]) }}</span>
              <span v-if="row[f.manualFlag]" class="manual-tag" :title="t('standardModelPricing.table.manualTagTitle')">{{ t('standardModelPricing.table.manualTag') }}</span>
            </td>
            <td>
              <span v-if="!row.is_custom" class="badge badge-gray">{{ t('standardModelPricing.table.statusDefault') }}</span>
              <span v-else-if="isFullyManual(row)" class="badge badge-blue">{{ t('standardModelPricing.table.statusFullCustom') }}</span>
              <span v-else class="badge badge-yellow">{{ t('standardModelPricing.table.statusPartialCustom', { n: manualCount(row) }) }}</span>
            </td>
            <td class="actions">
              <button class="btn btn-ghost btn-sm" @click="openEdit(row)">{{ t('standardModelPricing.table.edit') }}</button>
              <button class="btn btn-ghost btn-sm" @click="copyRow(row)">复制</button>
              <button v-if="row.is_custom" class="btn btn-ghost btn-sm" @click="resetAll(row)">{{ t('standardModelPricing.table.reset') }}</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="selectedRows.size > 0" class="bulk-bar">
      <span>{{ t('standardModelPricing.batch.selected', { n: selectedRows.size }) }}</span>
      <button class="btn btn-ghost btn-sm" :disabled="!clipboard" @click="pasteToSelected">{{ t('standardModelPricing.batch.paste') }}</button>
      <button class="btn btn-primary btn-sm" @click="openBatchModal">{{ t('standardModelPricing.batch.price') }}</button>
      <button class="btn btn-ghost btn-sm" @click="batchFillGlobal">{{ t('standardModelPricing.batch.fillGlobal') }}</button>
      <button class="btn btn-ghost btn-sm" @click="batchResetAll">{{ t('standardModelPricing.batch.resetAll') }}</button>
      <span v-if="batchMsg" class="batch-msg">{{ batchMsg }}</span>
    </div>

    <div v-if="showBatchModal" class="modal-backdrop" @click.self="showBatchModal = false">
      <div class="modal card">
        <h3 class="section-title">{{ t('standardModelPricing.batch.price') }} · {{ selectedRows.size }} 个模型</h3>
        <p class="modal-hint">统一设置以下积分值，所有勾选模型的手工标记将全部开启。</p>
        <div class="edit-grid">
          <div v-for="f in textRateFields" :key="f.key" class="edit-field">
            <label class="edit-head">
              <span>{{ f.label }}</span>
            </label>
            <input
              v-model.number="batchForm[f.formKey]"
              type="number"
              min="1"
              class="input compact"
            />
          </div>
          <div v-for="f in multimodalRateFields" :key="f.key" class="edit-field">
            <label class="edit-head">
              <span>{{ f.label }}</span>
            </label>
            <input
              v-model.number="batchForm[f.formKey]"
              type="number"
              min="1"
              class="input compact"
            />
          </div>
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary btn-sm" :disabled="savingBatch" @click="applyBatch">
            {{ savingBatch ? '保存中…' : '应用到所选' }}
          </button>
          <button class="btn btn-ghost btn-sm" @click="showBatchModal = false">取消</button>
        </div>
      </div>
    </div>

    <div v-if="editRow" class="modal-backdrop" @click.self="closeEdit">
      <div class="modal card">
        <h3 class="section-title">{{ t('standardModelPricing.editModal.title', { name: editRow.display_name }) }}</h3>
        <code class="mono-sm modal-code">{{ editRow.canonical_name }} · {{ modalityLabel(editRow) }}</code>
        <p class="modal-hint">{{ t('standardModelPricing.editModal.hint') }}</p>

        <h4 class="section-sub">{{ t('standardModelPricing.editModal.sectionText') }}</h4>
        <div class="edit-grid">
          <div v-for="f in textRateFields" :key="f.key" class="edit-field">
            <label class="edit-head">
              <input v-model="editForm[f.manualKey]" type="checkbox" />
              <span>{{ f.label }}{{ t('standardModelPricing.editModal.fieldSuffix') }}</span>
              <button
                v-if="editRow[f.manualFlag]"
                type="button"
                class="link-sm"
                @click="resetField(editRow, f.key)"
              >{{ t('standardModelPricing.editModal.resetBase') }}</button>
            </label>
            <input
              v-model.number="editForm[f.formKey]"
              type="number"
              min="1"
              class="input compact"
              :disabled="!editForm[f.manualKey]"
            />
            <span class="field-hint">{{ t('standardModelPricing.editModal.globalApprox', { value: fmtCredits(effectiveGlobal(f.key)) }) }}</span>
          </div>
        </div>

        <h4 class="section-sub">
          {{ t('standardModelPricing.editModal.sectionMultimodal') }}
          <span v-if="!isMultimodal(editRow)" class="section-hint">(本模型按多模态记，仍可手工设置未来值)</span>
        </h4>
        <div class="edit-grid">
          <div v-for="f in multimodalRateFields" :key="f.key" class="edit-field">
            <label class="edit-head">
              <input v-model="editForm[f.manualKey]" type="checkbox" />
              <span>{{ f.label }}{{ t('standardModelPricing.editModal.fieldSuffix') }}</span>
              <button
                v-if="editRow[f.manualFlag]"
                type="button"
                class="link-sm"
                @click="resetField(editRow, f.key)"
              >{{ t('standardModelPricing.editModal.resetBase') }}</button>
            </label>
            <input
              v-model.number="editForm[f.formKey]"
              type="number"
              min="1"
              class="input compact"
              :disabled="!editForm[f.manualKey]"
            />
            <span class="field-hint">{{ t('standardModelPricing.editModal.globalApprox', { value: fmtCredits(effectiveGlobal(f.key)) }) }}</span>
          </div>
        </div>

        <div class="modal-actions">
          <button class="btn btn-primary btn-sm" :disabled="savingRow" @click="saveEdit">
            {{ savingRow ? t('standardModelPricing.editModal.saving') : t('standardModelPricing.editModal.save') }}
          </button>
          <button class="btn btn-ghost btn-sm" type="button" @click="copyEditForm">复制价格</button>
          <button class="btn btn-ghost btn-sm" type="button" :disabled="!clipboard" @click="pasteEditForm">粘贴</button>
          <button class="btn btn-ghost btn-sm" type="button" @click="fillGlobalToEdit">{{ t('standardModelPricing.editModal.fillGlobal') }}</button>
          <button class="btn btn-ghost btn-sm" type="button" @click="closeEdit">{{ t('standardModelPricing.editModal.cancel') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-desc { color: var(--muted); font-size: 13px; margin: -8px 0 16px; line-height: 1.6; }
.section-title { font-size: 14px; font-weight: 600; margin: 0 0 12px; }
.settings-card { margin-bottom: 16px; }
.settings-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 14px;
}
@media (max-width: 1100px) { .settings-grid { grid-template-columns: repeat(2, 1fr); } }
@media (max-width: 640px) { .settings-grid { grid-template-columns: 1fr; } }
.field { display: flex; flex-direction: column; gap: 4px; }
.field-label { font-size: 12px; color: var(--muted); }
.field-hint { font-size: 11px; color: var(--muted); }
.input.compact { width: 100%; max-width: 100%; }
.discount-row { display: flex; align-items: center; gap: 10px; }
.discount-slider { flex: 1; }
.discount-val { font-size: 13px; font-weight: 600; min-width: 42px; }
.settings-actions {
  display: flex; align-items: center; gap: 12px;
  margin-top: 16px; padding-top: 12px; border-top: 1px solid var(--border);
}
.table-wrap { overflow-x: auto; }
.pricing-table { width: 100%; font-size: 13px; min-width: 960px; }
.model-name { font-weight: 600; }
.mono-sm { font-family: ui-monospace, Menlo, monospace; font-size: 11px; color: var(--muted); }
.cell-muted { color: var(--muted); }
.num { text-align: right; font-variant-numeric: tabular-nums; }
.rate-cell { white-space: nowrap; }
.manual-tag {
  display: inline-block; margin-left: 4px; padding: 0 4px;
  font-size: 10px; border-radius: 4px;
  background: rgba(59, 130, 246, 0.15); color: #60a5fa;
}
.modality-badge {
  display: inline-block; padding: 2px 8px; border-radius: 8px;
  font-size: 11px; font-weight: 500; margin-right: 6px;
  background: rgba(148, 163, 184, 0.18); color: #94a3b8;
}
.modality-badge.modality-multimodal { background: color-mix(in srgb, #d946ef 18%, transparent); color: #c084fc; }
.modality-badge.modality-vision { background: rgba(34, 197, 94, 0.18); color: #4ade80; }
.modality-badge.modality-audio { background: rgba(245, 158, 11, 0.18); color: #fbbf24; }
.modality-badge.modality-video { background: rgba(244, 63, 94, 0.18); color: #fb7185; }
.modality-badge.modality-embedding { background: color-mix(in srgb, var(--accent) 18%, transparent); color: var(--accent-h); }
.multimodal-tag {
  display: inline-block; padding: 1px 6px; border-radius: 6px;
  font-size: 10px; font-weight: 600;
  background: color-mix(in srgb, #d946ef 18%, transparent); color: #c084fc;
  margin-left: 2px;
}
.section-sub { font-size: 12px; font-weight: 600; margin: 12px 0 8px; color: var(--muted); }
.section-hint { font-weight: 400; color: var(--muted); margin-left: 6px; }
.actions { white-space: nowrap; display: flex; gap: 6px; justify-content: flex-end; }
.empty-cell { text-align: center; color: var(--muted); padding: 24px; }
.badge { padding: 2px 8px; border-radius: 8px; font-size: 11px; }
.badge-gray { background: rgba(156,163,175,.15); color: #9ca3af; }
.badge-blue { background: rgba(59,130,246,.15); color: #60a5fa; }
.badge-yellow { background: rgba(234,179,8,.15); color: #fbbf24; }
.modal-backdrop {
  position: fixed; inset: 0; z-index: 100;
  background: rgba(0,0,0,.45); display: flex; align-items: center; justify-content: center; padding: 20px;
}
.modal { width: min(560px, 100%); padding: 20px; max-height: 90vh; overflow-y: auto; }
.modal-code { display: block; margin-bottom: 8px; }
.modal-hint { font-size: 12px; color: var(--muted); margin: 0 0 16px; }
.edit-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
@media (max-width: 520px) { .edit-grid { grid-template-columns: 1fr; } }
.edit-field { display: flex; flex-direction: column; gap: 6px; }
.edit-head { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.link-sm { font-size: 11px; margin-left: auto; background: none; border: none; color: var(--accent-h); cursor: pointer; }
.modal-actions { display: flex; gap: 8px; margin-top: 18px; padding-top: 12px; border-top: 1px solid var(--border); }
.bulk-bar {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 14px; background: var(--bg-subtle); border-radius: 8px;
  margin-top: 12px; font-size: 13px;
  color: var(--text);
}
.batch-msg { font-size: 12px; color: var(--success); }
tr.selected { background: var(--bg-secondary); }
.modal { background: var(--card); color: var(--text); border: 1px solid var(--border); border-radius: var(--radius); }
</style>
