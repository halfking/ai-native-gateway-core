<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { getSetting, updateSetting } from '../api/settings'
import { getCompressionStats, type CompressionStats } from '../api'

const { t } = useI18n()
const router = useRouter()

// Settings keys we manage here. Each entry tracks the spec + current value
// + the optimistic value the user is editing, so we can revert on failure.
type SettingKey =
  | 'compression.enabled'
  | 'compression.mode'
  | 'compression.window_fraction'
  | 'compression.llm_model'
  | 'handoff.enabled'
  | 'handoff.threshold'

interface SettingState {
  value: any
  default: any
  source: string
  options?: string[]
  min?: number
  max?: number
}

const loading = ref(false)
const saving = ref<SettingKey | null>(null)
const error = ref('')
const successMsg = ref('')
const settings = ref<Partial<Record<SettingKey, SettingState>>>({})

// Compression stats summary (last 24h)
const stats = ref<CompressionStats | null>(null)
const statsLoading = ref(false)
const statsError = ref('')

const showAdvanced = ref(false)

const modeOptions = ['off', 'auto_threshold', 'on_4xx', 'smart', 'aggressive']

function modeLabel(opt: string): string {
  const map: Record<string, string> = {
    off: t('settings.compression.enumLabels.off'),
    auto_threshold: t('settings.compression.enumLabels.auto'),
    on_4xx: t('settings.compression.enumLabels.on4xx'),
    smart: t('settings.compression.enumLabels.smart'),
    aggressive: t('settings.compression.enumLabels.aggressive'),
  }
  return map[opt] || opt
}

async function loadSettings() {
  loading.value = true
  error.value = ''
  try {
    const keys: SettingKey[] = [
      'compression.enabled',
      'compression.mode',
      'compression.window_fraction',
      'compression.llm_model',
      'handoff.enabled',
      'handoff.threshold',
    ]
    const entries = await Promise.all(keys.map(async (k) => [k, await getSetting(k)] as const))
    const out: Partial<Record<SettingKey, SettingState>> = {}
    for (const [k, r] of entries) {
      out[k] = {
        value: r.value ?? r.spec.default,
        default: r.spec.default,
        source: r.source,
        options: r.spec.options,
        min: r.spec.min,
        max: r.spec.max,
      }
    }
    settings.value = out
  } catch (e: any) {
    error.value = e.message || t('sessions.config.loadError')
  } finally {
    loading.value = false
  }
}

async function loadStats() {
  statsLoading.value = true
  statsError.value = ''
  try {
    stats.value = await getCompressionStats({ hours: 24 })
  } catch (e: any) {
    statsError.value = e.message || t('sessions.config.statsLoadError')
  } finally {
    statsLoading.value = false
  }
}

async function save(key: SettingKey, value: any) {
  const prev = settings.value[key]
  if (!prev) return
  // optimistic
  prev.value = value
  saving.value = key
  successMsg.value = ''
  error.value = ''
  try {
    const r = await updateSetting(key, { value })
    prev.value = r.new_value ?? value
    successMsg.value = t('sessions.config.saveSuccess')
    // auto-dismiss success
    setTimeout(() => { if (successMsg.value) successMsg.value = '' }, 2000)
  } catch (e: any) {
    // revert to previous on failure
    error.value = e.message || t('sessions.config.saveError')
    await loadSettings()
  } finally {
    saving.value = null
  }
}

function revertToDefault(key: SettingKey) {
  const prev = settings.value[key]
  if (!prev) return
  void save(key, prev.default)
}

const compressionEnabled = computed({
  get: () => settings.value['compression.enabled']?.value === true,
  set: (v: boolean) => { void save('compression.enabled', v) },
})
const compressionMode = computed({
  get: () => settings.value['compression.mode']?.value ?? settings.value['compression.mode']?.default ?? 'smart',
  set: (v: string) => { void save('compression.mode', v) },
})
const compressionWindow = computed({
  get: () => settings.value['compression.window_fraction']?.value ?? settings.value['compression.window_fraction']?.default ?? 0.8,
  set: (v: number) => { void save('compression.window_fraction', v) },
})
const compressionModel = computed({
  get: () => settings.value['compression.llm_model']?.value ?? settings.value['compression.llm_model']?.default ?? '',
  set: (v: string) => { void save('compression.llm_model', v) },
})
const handoffEnabled = computed({
  get: () => settings.value['handoff.enabled']?.value === true,
  set: (v: boolean) => { void save('handoff.enabled', v) },
})
const handoffThreshold = computed({
  get: () => settings.value['handoff.threshold']?.value ?? settings.value['handoff.threshold']?.default ?? 0.8,
  set: (v: number) => { void save('handoff.threshold', v) },
})

function fmtPct(v: number | undefined | null): string {
  if (v == null) return '—'
  return (Number(v) * 100).toFixed(1) + '%'
}
function fmtNum(n: number | undefined | null): string {
  if (n == null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}

onMounted(() => {
  loadSettings()
  loadStats()
})
</script>

<template>
  <div class="compression-config-panel">
    <p v-if="loading" class="state-text">{{ t('sessions.config.loading') }}</p>
    <p v-else-if="error && !settings['compression.enabled']" class="state-text err">{{ error }}</p>

    <template v-else>
      <div v-if="error" class="banner err">{{ error }}</div>
      <div v-if="successMsg" class="banner ok">{{ successMsg }}</div>

      <!-- Core settings -->
      <div class="config-section">
        <h4 class="section-title">{{ t('sessions.config.basicSettings') }}</h4>

        <div class="field-row">
          <div class="field-label">
            <span>{{ t('sessions.config.compressionEnabledLabel') }}</span>
            <span class="hint">{{ t('sessions.config.compressionEnabledHint') }}</span>
          </div>
          <label class="switch">
            <input type="checkbox" :checked="compressionEnabled" @change="compressionEnabled = ($event.target as HTMLInputElement).checked" :disabled="saving === 'compression.enabled'" />
            <span class="track"><span class="knob" /></span>
          </label>
        </div>

        <div class="field-row">
          <div class="field-label">
            <span>{{ t('sessions.config.compressionModeLabel') }}</span>
            <span class="hint">{{ t('sessions.config.compressionModeHint') }}</span>
          </div>
          <select class="select" :value="compressionMode" @change="compressionMode = ($event.target as HTMLSelectElement).value" :disabled="saving === 'compression.mode'">
            <option v-for="opt in modeOptions" :key="opt" :value="opt">{{ modeLabel(opt) }}</option>
          </select>
        </div>

        <div class="field-row">
          <div class="field-label">
            <span>{{ t('sessions.config.compressionWindowLabel') }}</span>
            <span class="hint">{{ t('sessions.config.compressionWindowHint') }}</span>
          </div>
          <div class="window-editor">
            <input
              type="range"
              class="slider"
              min="0.5" max="0.95" step="0.01"
              :value="compressionWindow"
              @input="compressionWindow = parseFloat(($event.target as HTMLInputElement).value)"
              :disabled="saving === 'compression.window_fraction'"
            />
            <input
              type="number"
              class="number"
              min="0" max="1" step="0.01"
              :value="compressionWindow"
              @change="compressionWindow = parseFloat(($event.target as HTMLInputElement).value)"
              :disabled="saving === 'compression.window_fraction'"
            />
          </div>
        </div>

        <div class="field-row">
          <div class="field-label">
            <span>{{ t('sessions.config.compressionModelLabel') }}</span>
            <span class="hint">{{ t('sessions.config.compressionModelHint') }}</span>
          </div>
          <div class="model-editor">
            <input
              type="text"
              class="text-input"
              :value="compressionModel"
              :placeholder="t('sessions.config.compressionModelPlaceholder')"
              @change="compressionModel = ($event.target as HTMLInputElement).value"
              :disabled="saving === 'compression.llm_model'"
            />
            <button class="btn-ghost btn-sm" @click="revertToDefault('compression.llm_model')" :disabled="saving === 'compression.llm_model'">
              {{ t('sessions.config.revertToDefault') }}
            </button>
          </div>
        </div>
      </div>

      <!-- Advanced (Handoff) -->
      <div class="config-section">
        <button class="advanced-toggle" @click="showAdvanced = !showAdvanced">
          <span class="caret" :class="{ open: showAdvanced }">▶</span>
          {{ t('sessions.config.advancedTitle') }}
        </button>
        <div v-if="showAdvanced" class="advanced-body">
          <div class="field-row">
            <div class="field-label">
              <span>{{ t('sessions.config.handoffEnabledLabel') }}</span>
              <span class="hint">{{ t('sessions.config.handoffEnabledHint') }}</span>
            </div>
            <label class="switch">
              <input type="checkbox" :checked="handoffEnabled" @change="handoffEnabled = ($event.target as HTMLInputElement).checked" :disabled="saving === 'handoff.enabled'" />
              <span class="track"><span class="knob" /></span>
            </label>
          </div>
          <div class="field-row">
            <div class="field-label">
              <span>{{ t('sessions.config.handoffThresholdLabel') }}</span>
              <span class="hint">{{ t('sessions.config.handoffThresholdHint') }}</span>
            </div>
            <div class="window-editor">
              <input
                type="range" class="slider" min="0.5" max="0.95" step="0.01"
                :value="handoffThreshold"
                @input="handoffThreshold = parseFloat(($event.target as HTMLInputElement).value)"
                :disabled="saving === 'handoff.threshold'"
              />
              <input
                type="number" class="number" min="0" max="1" step="0.01"
                :value="handoffThreshold"
                @change="handoffThreshold = parseFloat(($event.target as HTMLInputElement).value)"
                :disabled="saving === 'handoff.threshold'"
              />
            </div>
          </div>
        </div>
      </div>

      <!-- Stats summary card -->
      <div class="config-section stats-section">
        <div class="stats-head">
          <h4 class="section-title">{{ t('sessions.config.statsTitle') }}</h4>
          <button class="btn-link" @click="router.push('/admin/compression')">{{ t('sessions.config.statsViewFull') }}</button>
        </div>
        <p v-if="statsLoading" class="state-text">{{ t('sessions.config.loading') }}</p>
        <p v-else-if="statsError" class="state-text err">{{ statsError }}</p>
        <div v-else-if="stats" class="stats-grid">
          <div class="stat">
            <span class="stat-label">{{ t('sessions.config.statsTotalRequests') }}</span>
            <span class="stat-value">{{ fmtNum(stats.total_requests) }}</span>
          </div>
          <div class="stat">
            <span class="stat-label">{{ t('sessions.config.statsCompressed') }}</span>
            <span class="stat-value ok">{{ fmtNum(stats.compressed_total) }}</span>
          </div>
          <div class="stat">
            <span class="stat-label">{{ t('sessions.config.statsRate') }}</span>
            <span class="stat-value">{{ fmtPct(stats.compression_rate) }}</span>
          </div>
          <div class="stat">
            <span class="stat-label">{{ t('sessions.config.statsSaved') }}</span>
            <span class="stat-value warn">{{ stats.estimated_tokens_saved != null ? fmtNum(stats.estimated_tokens_saved) : '—' }}</span>
          </div>
        </div>
        <p v-else class="state-text muted">{{ t('sessions.config.statsNoData') }}</p>
      </div>
    </template>
  </div>
</template>

<style scoped>
.compression-config-panel {
  display: flex;
  flex-direction: column;
  gap: 16px;
  max-width: 880px;
}
.state-text { font-size: 13px; color: var(--text-secondary, #8b949e); padding: 12px 0; }
.state-text.err { color: var(--danger, #f87171); }
.state-text.muted { color: var(--text-muted, #6e7681); }
.banner { padding: 8px 12px; border-radius: 6px; font-size: 12px; }
.banner.err { background: rgba(248,113,113,.1); border: 1px solid rgba(248,113,113,.3); color: #f87171; }
.banner.ok { background: rgba(52,211,153,.1); border: 1px solid rgba(52,211,153,.3); color: #34d399; }

.config-section {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 10px;
  padding: 14px 16px;
}
.section-title { margin: 0 0 12px; font-size: 13px; font-weight: 600; color: var(--text-primary, #e6edf3); }

.field-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 10px 0;
  border-bottom: 1px solid var(--border, #30363d);
}
.field-row:last-child { border-bottom: none; }
.field-label { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
.field-label > span:first-child { font-size: 13px; color: var(--text-primary, #e6edf3); font-weight: 500; }
.hint { font-size: 11px; color: var(--text-secondary, #8b949e); }

/* toggle */
.switch { display: inline-flex; cursor: pointer; flex-shrink: 0; }
.switch input { position: absolute; opacity: 0; pointer-events: none; }
.track { display: block; width: 36px; height: 20px; background: var(--border, #30363d); border-radius: 10px; position: relative; transition: background .2s; }
.knob { position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; background: #fff; border-radius: 50%; transition: transform .2s; }
.switch input:checked + .track { background: var(--accent, #6366f1); }
.switch input:checked + .track .knob { transform: translateX(16px); }
.switch input:disabled + .track { opacity: .5; cursor: not-allowed; }

.select, .text-input, .number {
  padding: 6px 10px; background: var(--bg, #0f1117); border: 1px solid var(--border, #30363d);
  border-radius: 6px; color: var(--text-primary, #e6edf3); font-size: 13px; font-family: inherit;
}
.select:focus, .text-input:focus, .number:focus { outline: none; border-color: var(--accent, #6366f1); }
.select { min-width: 200px; }

.window-editor { display: flex; align-items: center; gap: 10px; }
.window-editor .slider { width: 160px; accent-color: var(--accent, #6366f1); }
.window-editor .number { width: 80px; }

.model-editor { display: flex; gap: 8px; align-items: center; }
.model-editor .text-input { flex: 1; min-width: 240px; }

.advanced-toggle {
  background: none; border: none; color: var(--text-secondary, #8b949e);
  font-size: 13px; cursor: pointer; padding: 0; display: flex; align-items: center; gap: 6px;
}
.advanced-toggle:hover { color: var(--text-primary, #e6edf3); }
.caret { font-size: 9px; transition: transform .2s; display: inline-block; }
.caret.open { transform: rotate(90deg); }
.advanced-body { margin-top: 10px; }

.btn-ghost, .btn-link {
  background: transparent; border: 1px solid var(--border, #30363d); border-radius: 6px;
  color: var(--text-primary, #e6edf3); font-size: 12px; cursor: pointer; padding: 4px 10px;
}
.btn-ghost:hover { background: var(--bg-hover, #21262d); }
.btn-link { border: none; color: var(--accent-h, #818cf8); padding: 0; }
.btn-link:hover { text-decoration: underline; }
.btn-sm { font-size: 11px; padding: 3px 8px; }

.stats-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.stats-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; }
.stat { display: flex; flex-direction: column; gap: 4px; padding: 10px; background: var(--bg, #0f1117); border-radius: 6px; }
.stat-label { font-size: 11px; color: var(--text-secondary, #8b949e); }
.stat-value { font-size: 18px; font-weight: 700; color: var(--text-primary, #e6edf3); font-variant-numeric: tabular-nums; }
.stat-value.ok { color: #34d399; }
.stat-value.warn { color: #f59e0b; }

@media (max-width: 700px) {
  .field-row { flex-direction: column; align-items: flex-start; }
  .stats-grid { grid-template-columns: repeat(2, 1fr); }
}
</style>
