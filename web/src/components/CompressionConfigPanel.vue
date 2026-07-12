<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { getSetting, getTenantSetting, updateSetting, updateTenantSetting } from '../api/settings'
import { getAvailableModels, type AvailableVersion } from '../api/models'
import { getCompressionStats, type CompressionStats } from '../api'
import { getCurrentTenantId } from '../store'

const { t } = useI18n()
const router = useRouter()
const tenantID = getCurrentTenantId()

type Key = 'compression.enabled' | 'compression.mode' | 'compression.window_fraction' | 'compression.llm_model' | 'handoff.enabled' | 'handoff.threshold' | 'handoff.summary_engine' | 'handoff.summary_model' | 'handoff.summary_keep_recent_n'
type State = { value: any; default: any; source: string; scope: 'platform' | 'tenant'; min?: number; max?: number; options?: string[] }

const loading = ref(true)
const saving = ref(false)
const error = ref('')
const success = ref('')
const dirty = ref(false)
const settings = ref<Partial<Record<Key, State>>>({})
const draft = ref<Record<string, any>>({})
const models = ref<AvailableVersion[]>([])
const stats = ref<CompressionStats | null>(null)
const statsLoading = ref(false)
const statsError = ref('')

const modeOptions = ['off', 'auto_threshold', 'on_4xx', 'smart', 'aggressive']
const summaryEngineOptions = ['llm', 'rule', 'hybrid']
const modelOptions = computed(() => models.value.filter((m) => m.modality !== 'embedding'))
const compressionModels = computed(() => String(draft.value['compression.llm_model'] || '').split(',').map((v) => v.trim()).filter(Boolean))

function normalizeMode(value: unknown): string {
  const legacy: Record<string, string> = { '0': 'off', '1': 'auto_threshold', '2': 'on_4xx' }
  const candidates = [legacy[String(value)], String(value), 'smart']
  for (const candidate of candidates) {
    if (candidate && modeOptions.includes(candidate)) return candidate
  }
  return 'smart'
}
function normalizeNumber(value: unknown, fallback: number, min: number, max: number) {
  const n = Number(value)
  return Number.isFinite(n) ? Math.min(max, Math.max(min, n)) : fallback
}
function clampWindow(value: unknown): number {
  const raw = Number(value)
  if (!Number.isFinite(raw)) return 0.8
  return Math.min(1, Math.max(0.5, raw))
}
function normalizeModels(value: unknown) {
  const seen = new Set<string>()
  return String(value || '').split(',').map((v) => v.trim()).filter((v) => v && !seen.has(v) && (seen.add(v), true)).join(',')
}
function setDraft(key: Key, value: any) {
  draft.value[key] = value
  dirty.value = true
}
function state(key: Key) { return settings.value[key] }
function reset(key: Key) { const item = state(key); if (item) setDraft(key, item.default) }
function modelName(model: AvailableVersion) { return model.display_name || model.canonical_name }
function vendorOf(model: AvailableVersion) {
  const family = model.aliases?.[0]?.split(':')[0]
  return family || t('sessions.config.modelVendorUnknown')
}
function contextLabel(model: AvailableVersion) { return model.context_window ? `${Math.round(model.context_window / 1000)}K` : t('sessions.config.modelContextUnknown') }
function addModel(name: string) {
  if (!compressionModels.value.includes(name)) setDraft('compression.llm_model', [...compressionModels.value, name].join(','))
}
function removeModel(name: string) { setDraft('compression.llm_model', compressionModels.value.filter((v) => v !== name).join(',')) }

async function load() {
  loading.value = true; error.value = ''
  const platform: Key[] = ['compression.enabled', 'compression.mode', 'compression.window_fraction', 'compression.llm_model', 'handoff.enabled', 'handoff.threshold']
  const tenant: Key[] = ['handoff.summary_engine', 'handoff.summary_model', 'handoff.summary_keep_recent_n']
  try {
    const entries = await Promise.all([
      ...platform.map(async (key) => [key, await getSetting(key)] as const),
      ...tenant.map(async (key) => [key, await getTenantSetting(tenantID, key)] as const),
    ])
    const next: Partial<Record<Key, State>> = {}
    for (const [key, response] of entries) {
      const scope = tenant.includes(key) ? 'tenant' : 'platform'
      let value = response.value ?? response.spec.default
      if (key === 'compression.mode') value = normalizeMode(value)
      if (key === 'compression.window_fraction' || key === 'handoff.threshold') value = clampWindow(value)
      if (key === 'compression.llm_model') value = normalizeModels(value)
      next[key] = { value, default: response.spec.default, source: response.source || 'default', scope, min: response.spec.min, max: response.spec.max, options: response.spec.options }
    }
    settings.value = next
    draft.value = Object.fromEntries(Object.entries(next).map(([key, item]) => [key, item!.value]))
    dirty.value = false
  } catch (e: any) { error.value = e?.message || t('sessions.config.loadError') } finally { loading.value = false }
}
async function loadModels() {
  try {
    const response = await getAvailableModels()
    models.value = response.families.flatMap((family) => family.versions || [])
  } catch { models.value = [] }
}
async function loadStats() {
  statsLoading.value = true
  try { stats.value = await getCompressionStats({ hours: 24 }) } catch (e: any) { statsError.value = e?.message || t('sessions.config.statsLoadError') } finally { statsLoading.value = false }
}
async function save() {
  if (!dirty.value || saving.value) return
  saving.value = true; error.value = ''; success.value = ''
  try {
    const platformKeys: Key[] = ['compression.enabled', 'compression.mode', 'compression.window_fraction', 'compression.llm_model', 'handoff.enabled', 'handoff.threshold']
    const tenantKeys: Key[] = ['handoff.summary_engine', 'handoff.summary_model', 'handoff.summary_keep_recent_n']
    for (const key of platformKeys) {
      const value = key === 'compression.llm_model' ? normalizeModels(draft.value[key]) : draft.value[key]
      await updateSetting(key, { value })
    }
    for (const key of tenantKeys) await updateTenantSetting(tenantID, key, { value: draft.value[key] })
    await load(); success.value = t('sessions.config.saveSuccess')
  } catch (e: any) { error.value = e?.message || t('sessions.config.saveError') } finally { saving.value = false }
}
function fmtNum(n: number | undefined | null) { if (n == null) return '—'; if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`; if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`; return String(n) }
function fmtPct(n: number | undefined | null) { return n == null ? '—' : `${(n * 100).toFixed(1)}%` }

onMounted(() => { void load(); void loadModels(); void loadStats() })
</script>

<template>
  <div class="compression-panel" :aria-busy="loading || saving">
    <div v-if="error" class="banner banner-error" role="alert">{{ error }}</div>
    <div v-if="success" class="banner banner-success" role="status">{{ success }}</div>
    <div v-if="loading" class="state">{{ t('sessions.config.loading') }}</div>
    <template v-else>
      <div class="action-bar">
        <div><strong>{{ t('sessions.config.compressionTab') }}</strong><span class="meta">{{ t('sessions.config.platformScope') }}</span></div>
        <button class="btn btn-primary btn-sm" :disabled="!dirty || saving" @click="save">{{ saving ? t('sessions.config.saving') : t('common.save') }}</button>
      </div>

      <section class="config-section">
        <div class="section-head"><div><h2>{{ t('sessions.config.compressionSection') }}</h2><p>{{ t('sessions.config.compressionSectionHint') }}</p></div></div>
        <div class="field-row"><div class="field-label"><label for="compression-enabled">{{ t('sessions.config.compressionEnabledLabel') }}</label><span>{{ t('sessions.config.compressionEnabledHint') }}</span></div><label class="switch"><input id="compression-enabled" type="checkbox" v-model="draft['compression.enabled']"><span class="track"><span class="knob" /></span></label></div>
        <div class="field-row"><div class="field-label"><label for="compression-mode">{{ t('sessions.config.compressionModeLabel') }}</label><span>{{ t('sessions.config.compressionModeHint') }}</span></div><select id="compression-mode" class="compact-select" v-model="draft['compression.mode']"><option v-for="option in modeOptions" :key="option" :value="option">{{ option }}</option></select></div>
        <div class="field-row"><div class="field-label"><label for="compression-window">{{ t('sessions.config.compressionWindowLabel') }}</label><span>{{ t('sessions.config.compressionWindowHint') }}</span></div><div class="range-control"><input id="compression-window" type="range" min="0" max="1" step="0.01" v-model.number="draft['compression.window_fraction']"><input class="compact-number" type="number" min="0" max="1" step="0.01" v-model.number="draft['compression.window_fraction']"></div></div>
        <div class="field-row field-row-stack"><div class="field-label"><label>{{ t('sessions.config.compressionModelLabel') }}</label><span>{{ t('sessions.config.compressionModelHint') }}</span></div><div class="model-picker-compact"><div class="chips"><span v-for="model in compressionModels" :key="model" class="chip">{{ model }}<button type="button" :aria-label="`${t('sessions.config.removeModel')} ${model}`" @click="removeModel(model)">×</button></span><span v-if="!compressionModels.length" class="placeholder">{{ t('sessions.config.modelNone') }}</span></div><div class="model-options"><button v-for="model in modelOptions.slice(0, 12)" :key="model.canonical_name" type="button" class="model-option" :class="{ selected: compressionModels.includes(model.canonical_name) }" @click="compressionModels.includes(model.canonical_name) ? removeModel(model.canonical_name) : addModel(model.canonical_name)"><span>{{ modelName(model) }}</span><small>{{ vendorOf(model) }} · {{ contextLabel(model) }}</small></button></div><div class="model-actions"><button type="button" class="btn btn-ghost btn-sm" @click="reset('compression.llm_model')">{{ t('sessions.config.revertToDefault') }}</button><span class="meta">{{ state('compression.llm_model')?.source }}</span></div></div></div>
      </section>

      <section class="config-section"><div class="section-head"><div><h2>{{ t('sessions.config.handoffSection') }}</h2><p>{{ t('sessions.config.handoffSectionHint') }}</p></div><span class="scope-badge">{{ t('sessions.config.tenantScope') }} · {{ tenantID }}</span></div>
        <div class="field-row"><div class="field-label"><label for="handoff-enabled">{{ t('sessions.config.handoffEnabledLabel') }}</label><span>{{ t('sessions.config.handoffEnabledHint') }}</span></div><label class="switch"><input id="handoff-enabled" type="checkbox" v-model="draft['handoff.enabled']"><span class="track"><span class="knob" /></span></label></div>
        <div class="field-row"><div class="field-label"><label for="handoff-threshold">{{ t('sessions.config.handoffThresholdLabel') }}</label><span>{{ t('sessions.config.handoffThresholdHint') }}</span></div><div class="range-control"><input id="handoff-threshold" type="range" min="0" max="1" step="0.01" v-model.number="draft['handoff.threshold']"><input class="compact-number" type="number" min="0" max="1" step="0.01" v-model.number="draft['handoff.threshold']"></div></div>
        <div class="field-row"><div class="field-label"><label for="summary-engine">{{ t('sessions.config.summaryEngineLabel') }}</label><span>{{ t('sessions.config.summaryEngineHint') }}</span></div><select id="summary-engine" class="compact-select" v-model="draft['handoff.summary_engine']"><option v-for="option in summaryEngineOptions" :key="option" :value="option">{{ option }}</option></select></div>
        <div class="field-row"><div class="field-label"><label for="summary-model">{{ t('sessions.config.summaryModelLabel') }}</label><span>{{ t('sessions.config.summaryModelHint') }}</span></div><input id="summary-model" class="compact-input" :value="draft['handoff.summary_model'] || ''" :placeholder="t('sessions.config.summaryModelFallback')" @input="setDraft('handoff.summary_model', ($event.target as HTMLInputElement).value)"><span class="source-badge">{{ state('handoff.summary_model')?.source }}</span></div>
        <div class="field-row"><div class="field-label"><label for="summary-keep">{{ t('sessions.config.summaryKeepLabel') }}</label><span>{{ t('sessions.config.summaryKeepHint') }}</span></div><input id="summary-keep" class="compact-number" type="number" min="0" max="50" step="1" v-model.number="draft['handoff.summary_keep_recent_n']"></div>
      </section>

      <section class="config-section stats-section"><div class="section-head"><div><h2>{{ t('sessions.config.statsTitle') }}</h2></div><button class="btn btn-ghost btn-sm" @click="router.push('/admin/compression')">{{ t('sessions.config.statsViewFull') }}</button></div><div v-if="statsLoading" class="state">{{ t('sessions.config.loading') }}</div><div v-else-if="statsError" class="state error-text">{{ statsError }}</div><div v-else-if="stats" class="stats-grid"><div class="stat"><span>{{ t('sessions.config.statsTotalRequests') }}</span><strong>{{ fmtNum(stats.total_requests) }}</strong></div><div class="stat"><span>{{ t('sessions.config.statsCompressed') }}</span><strong>{{ fmtNum(stats.compressed_total) }}</strong></div><div class="stat"><span>{{ t('sessions.config.statsRate') }}</span><strong>{{ fmtPct(stats.compression_rate) }}</strong></div><div class="stat"><span>{{ t('sessions.config.statsSaved') }}</span><strong>{{ fmtNum(stats.estimated_tokens_saved) }}</strong></div></div></section>
    </template>
  </div>
</template>

<style scoped>
.compression-panel { display: grid; gap: 12px; max-width: 980px; }
.action-bar,.section-head { display:flex; align-items:center; justify-content:space-between; gap:12px; }
.action-bar { min-height:34px; } .action-bar strong { font-size:14px; } .meta { color:var(--muted); font-size:11px; margin-left:8px; }
.config-section { background:var(--card); border:1px solid var(--border); border-radius:var(--radius); padding:14px 16px; }
.section-head { margin-bottom:8px; } h2 { font-size:13px; margin:0; color:var(--text); } .section-head p { margin:3px 0 0; color:var(--muted); font-size:11px; }
.field-row { display:grid; grid-template-columns:minmax(220px,1fr) minmax(180px,360px); align-items:center; gap:18px; padding:9px 0; border-bottom:1px solid var(--border); } .field-row:last-child { border-bottom:0; }
.field-label { min-width:0; display:grid; gap:2px; } .field-label label { color:var(--text); font-size:12px; font-weight:600; } .field-label span { color:var(--muted); font-size:11px; line-height:1.35; }
.compact-select { box-sizing:border-box; width:auto; min-width:130px; max-width:100%; height:32px; padding:5px 8px; color:var(--text); background:var(--bg); border:1px solid var(--border); border-radius:6px; font:inherit; font-size:12px; }
.compact-input,.compact-number { box-sizing:border-box; width:100%; min-width:0; height:32px; padding:5px 8px; color:var(--text); background:var(--bg); border:1px solid var(--border); border-radius:6px; font:inherit; font-size:12px; }
.compact-select:focus,.compact-input:focus,.compact-number:focus { outline:2px solid color-mix(in srgb,var(--accent) 45%,transparent); border-color:var(--accent); }
.range-control { display:grid; grid-template-columns:minmax(0,1fr) 72px; gap:8px; align-items:center; } .range-control input[type=range] { width:100%; accent-color:var(--accent); }
.switch { justify-self:end; cursor:pointer; } .switch input { position:absolute; opacity:0; } .track { display:block; width:34px; height:19px; background:var(--border); border-radius:10px; padding:2px; } .knob { display:block; width:15px; height:15px; border-radius:50%; background:white; transition:transform .15s; } .switch input:checked + .track { background:var(--accent); } .switch input:checked + .track .knob { transform:translateX(15px); }
.field-row-stack { align-items:start; } .model-picker-compact { min-width:0; } .chips { display:flex; flex-wrap:wrap; gap:5px; min-height:32px; align-items:center; } .chip { max-width:100%; border:1px solid color-mix(in srgb,var(--accent) 55%,var(--border)); background:color-mix(in srgb,var(--accent) 12%,var(--bg)); color:var(--text); border-radius:5px; padding:4px 6px; font-size:11px; overflow-wrap:anywhere; } .chip button { border:0; background:none; color:var(--muted); cursor:pointer; padding:0 0 0 5px; } .placeholder { color:var(--muted); font-size:12px; }
.model-options { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:5px; margin-top:7px; } .model-option { display:grid; gap:2px; text-align:left; color:var(--text); background:var(--bg); border:1px solid var(--border); border-radius:5px; padding:6px 8px; cursor:pointer; min-width:0; } .model-option:hover,.model-option.selected { border-color:var(--accent); background:color-mix(in srgb,var(--accent) 10%,var(--bg)); } .model-option span { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:11px; } .model-option small { color:var(--muted); font-size:10px; } .model-actions { display:flex; align-items:center; justify-content:space-between; margin-top:7px; }
.btn { border-radius:6px; border:1px solid var(--border); cursor:pointer; font:inherit; } .btn-sm { padding:5px 9px; font-size:11px; } .btn-primary { color:white; background:var(--accent); border-color:var(--accent); } .btn-ghost { color:var(--text); background:transparent; } .btn:disabled { opacity:.45; cursor:not-allowed; }
.banner { padding:8px 10px; border-radius:6px; font-size:12px; } .banner-error { color:var(--danger); border:1px solid color-mix(in srgb,var(--danger) 35%,var(--border)); background:color-mix(in srgb,var(--danger) 8%,transparent); } .banner-success { color:var(--success); border:1px solid color-mix(in srgb,var(--success) 35%,var(--border)); background:color-mix(in srgb,var(--success) 8%,transparent); } .state { color:var(--muted); font-size:12px; padding:10px 0; } .error-text { color:var(--danger); }
.scope-badge,.source-badge { color:var(--muted); font-size:10px; border:1px solid var(--border); border-radius:999px; padding:3px 7px; white-space:nowrap; } .stats-grid { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:8px; } .stat { display:grid; gap:4px; padding:9px; background:var(--bg); border:1px solid var(--border); border-radius:6px; } .stat span { color:var(--muted); font-size:10px; } .stat strong { color:var(--text); font-size:16px; }
@media (max-width:800px) { .field-row { grid-template-columns:1fr; gap:7px; } .switch { justify-self:start; } .model-options { grid-template-columns:1fr; } }
@media (max-width:480px) { .config-section { padding:11px 12px; } .stats-grid { grid-template-columns:repeat(2,minmax(0,1fr)); } .section-head { align-items:start; flex-direction:column; } }
</style>
