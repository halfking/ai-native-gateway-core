<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useFormat } from '../../i18n/useFormat'
import {
  updateProvider,
  batchRecoverCredentials,
  checkProvider,
  getProviderSettings,
  setProviderSetting,
  deleteProviderSetting,
  type ProviderDetail,
  type ProviderSetting,
} from '../../api'

const { t: td } = useI18n()
const ps = (k: string, params?: Record<string, unknown>): string =>
  td(`providerDetail.settings.${k}` as never, params as never)
const { fmtDateTime } = useFormat()

const props = defineProps<{ provider: ProviderDetail }>()
const emit = defineEmits(['refresh'])

// Provider settings state
const providerSettings = ref<ProviderSetting[]>([])
const settingsLoading = ref(false)
const settingsMsg = ref('')

// Editable settings values
const compressionMode = ref<string | null>(null)
const cacheEnabled = ref<boolean | null>(null)
const formatConversionEnabled = ref<boolean | null>(null)

// Basic provider settings
const editName = ref(props.provider.display_name)
const editBaseUrl = ref(props.provider.base_url)
const editProtocol = ref(props.provider.protocol)
const editKind = ref(props.provider.kind)
const editCategory = ref(props.provider.category)
const editDiscountRate = ref(props.provider.discount_rate)
const editEgressProfile = ref(props.provider.egress_profile || 'direct')
const editNotes = ref(props.provider.notes || '')
const saving = ref(false)
const msg = ref('')
const batchMsg = ref('')
const batchLoading = ref(false)
const checking = ref(false)
const checkMsg = ref('')

// Load provider settings on mount
onMounted(async () => {
  await loadProviderSettings()
})

async function loadProviderSettings() {
  settingsLoading.value = true
  settingsMsg.value = ''
  try {
    const resp = await getProviderSettings(props.provider.id)
    providerSettings.value = resp.settings || []
    
    // Parse settings into editable refs
    if (resp.settings && Array.isArray(resp.settings)) {
      resp.settings.forEach(s => {
        if (s.key === 'compression.mode' && s.enabled) {
          compressionMode.value = s.value as string
        } else if (s.key === 'cache.enabled' && s.enabled) {
          cacheEnabled.value = s.value as boolean
        } else if (s.key === 'format_conversion.enabled' && s.enabled) {
          formatConversionEnabled.value = s.value as boolean
        }
      })
    }
  } catch (e: unknown) {
    settingsMsg.value = ps('settingsLoadFailed', { msg: e instanceof Error ? e.message : String(e) })
  } finally {
    settingsLoading.value = false
  }
}

async function saveCompressionMode(mode: string | null) {
  settingsMsg.value = ''
  try {
    if (mode === null) {
      // Delete override (revert to platform default)
      await deleteProviderSetting(props.provider.id, 'compression.mode')
      compressionMode.value = null
      settingsMsg.value = ps('settingsRestored')
    } else {
      await setProviderSetting(props.provider.id, 'compression.mode', mode)
      compressionMode.value = mode
      settingsMsg.value = ps('settingsSaved')
    }
    setTimeout(() => { settingsMsg.value = '' }, 3000)
  } catch (e: unknown) {
    settingsMsg.value = ps('settingsSaveFailed', { msg: e instanceof Error ? e.message : String(e) })
  }
}

async function saveCacheEnabled(enabled: boolean | null) {
  settingsMsg.value = ''
  try {
    if (enabled === null) {
      await deleteProviderSetting(props.provider.id, 'cache.enabled')
      cacheEnabled.value = null
      settingsMsg.value = ps('settingsRestored')
    } else {
      await setProviderSetting(props.provider.id, 'cache.enabled', enabled)
      cacheEnabled.value = enabled
      settingsMsg.value = ps('settingsCacheSaved')
    }
    setTimeout(() => { settingsMsg.value = '' }, 3000)
  } catch (e: unknown) {
    settingsMsg.value = ps('settingsSaveFailed', { msg: e instanceof Error ? e.message : String(e) })
  }
}

async function saveFormatConversion(enabled: boolean | null) {
  settingsMsg.value = ''
  try {
    if (enabled === null) {
      await deleteProviderSetting(props.provider.id, 'format_conversion.enabled')
      formatConversionEnabled.value = null
      settingsMsg.value = ps('settingsRestored')
    } else {
      await setProviderSetting(props.provider.id, 'format_conversion.enabled', enabled)
      formatConversionEnabled.value = enabled
      settingsMsg.value = ps('settingsFormatSaved')
    }
    setTimeout(() => { settingsMsg.value = '' }, 3000)
  } catch (e: unknown) {
    settingsMsg.value = ps('settingsSaveFailed', { msg: e instanceof Error ? e.message : String(e) })
  }
}

function syncFromProvider(p: ProviderDetail) {
  editName.value = p.display_name
  editBaseUrl.value = p.base_url
  editProtocol.value = p.protocol
  editKind.value = p.kind
  editCategory.value = p.category
  editDiscountRate.value = p.discount_rate
  editEgressProfile.value = p.egress_profile || 'direct'
  editNotes.value = p.notes || ''
}

watch(() => props.provider, syncFromProvider, { deep: true })

function fmtTime(v: string | null | undefined) {
  if (!v) return '—'
  return fmtDateTime(v)
}

async function save() {
  saving.value = true
  msg.value = ''
  try {
    await updateProvider(props.provider.id, {
      display_name: editName.value || undefined,
      base_url: editBaseUrl.value || undefined,
      protocol: editProtocol.value || undefined,
      kind: editKind.value || undefined,
      category: editCategory.value || undefined,
      discount_rate: editDiscountRate.value != null ? Number(editDiscountRate.value) : undefined,
      egress_profile: editEgressProfile.value || undefined,
      notes: editNotes.value || undefined,
    })
    msg.value = ps('saved')
    emit('refresh')
  } catch (e: unknown) {
    msg.value = ps('saveFailed', { msg: e instanceof Error ? e.message : '' })
  } finally {
    saving.value = false
  }
}

async function batchRecover() {
  if (!confirm(ps('batchRecoverConfirm'))) return
  batchLoading.value = true
  batchMsg.value = ''
  try {
    const r = await batchRecoverCredentials(props.provider.id)
    batchMsg.value = ps('batchRecoverMsg', { n: r.recovered })
    emit('refresh')
  } catch (e: unknown) {
    batchMsg.value = ps('batchRecoverFailed', { msg: e instanceof Error ? e.message : '' })
  } finally {
    batchLoading.value = false
  }
}

async function runHealthCheck() {
  checking.value = true
  checkMsg.value = ''
  try {
    const r = await checkProvider(props.provider.id)
    checkMsg.value = r.reason || ps('runCheck')
    setTimeout(() => emit('refresh'), 5000)
  } catch (e: unknown) {
    checkMsg.value = e instanceof Error ? e.message : ps('checkFailed')
  } finally {
    checking.value = false
  }
}
</script>

<template>
  <div class="settings-tab provider-detail-grid">
    <!-- Provider-level Settings Override Section -->
    <section class="card settings-section">
      <h3 class="section-title">{{ ps('overrideTitle') }} <span style="font-size:12px;color:var(--muted)">{{ ps('overrideScope') }}</span></h3>
      <div v-if="settingsMsg" class="alert" :class="settingsMsg.includes('失败') ? 'alert-danger' : 'alert-success'">
        {{ settingsMsg }}
      </div>
      <div v-if="settingsLoading" class="empty">{{ ps('settingsLoading') }}</div>
      <div v-else class="settings-form">
        <div class="form-group">
          <label>{{ ps('fieldCompression') }}</label>
          <select
            :value="compressionMode ?? ''"
            @change="saveCompressionMode(($event.target as HTMLSelectElement).value || null)"
            class="input"
          >
            <option value="">{{ ps('followGlobal') }}</option>
            <option value="off">{{ ps('optionOff') }}</option>
            <option value="auto_threshold">{{ ps('optionAutoThreshold') }}</option>
            <option value="on_4xx">{{ ps('optionOn4xx') }}</option>
          </select>
          <div class="form-hint">
            {{ ps('compressionHint') }}
          </div>
        </div>

        <div class="form-group">
          <label>{{ ps('fieldCache') }}</label>
          <div style="display: flex; align-items: center; gap: 12px;">
            <select
              :value="cacheEnabled === null ? '' : (cacheEnabled ? 'true' : 'false')"
              @change="saveCacheEnabled(($event.target as HTMLSelectElement).value === '' ? null : ($event.target as HTMLSelectElement).value === 'true')"
              class="input"
              style="max-width: 200px;"
            >
              <option value="">{{ ps('followGlobal') }}</option>
              <option value="true">{{ ps('optionOn') }}</option>
              <option value="false">{{ ps('optionOff2') }}</option>
            </select>
          </div>
          <div class="form-hint">
            {{ ps('cacheHint') }}
          </div>
        </div>

        <div class="form-group">
          <label>{{ ps('fieldFormat') }}</label>
          <div style="display: flex; align-items: center; gap: 12px;">
            <select
              :value="formatConversionEnabled === null ? '' : (formatConversionEnabled ? 'true' : 'false')"
              @change="saveFormatConversion(($event.target as HTMLSelectElement).value === '' ? null : ($event.target as HTMLSelectElement).value === 'true')"
              class="input"
              style="max-width: 200px;"
            >
              <option value="">{{ ps('followGlobal') }}</option>
              <option value="true">{{ ps('optionOnRecommended') }}</option>
              <option value="false">{{ ps('optionOff2') }}</option>
            </select>
          </div>
          <div class="form-hint">
            {{ ps('formatHint') }}
          </div>
        </div>
      </div>
    </section>

    <section class="card settings-section settings-section--edit">
      <h3 class="section-title">{{ ps('editTitle') }}</h3>
      <div class="settings-form">
        <div class="form-grid">
          <div class="form-group">
            <label>{{ ps('fieldDisplayName') }}</label>
            <input v-model="editName" class="input" />
          </div>
          <div class="form-group">
            <label>{{ ps('fieldBaseUrl') }}</label>
            <input v-model="editBaseUrl" class="input" />
          </div>
          <div class="form-group">
            <label>{{ ps('fieldProtocol') }}</label>
            <select v-model="editProtocol" class="input">
              <option value="openai-completions">OpenAI Completions</option>
              <option value="openai-responses">OpenAI Responses</option>
              <option value="anthropic-messages">Anthropic Messages</option>
              <option value="google-gemini">Google Gemini</option>
              <option value="gemini-generate">Gemini Generate</option>
            </select>
          </div>
          <div class="form-group">
            <label>{{ ps('fieldEgressProfile') }}</label>
            <select v-model="editEgressProfile" class="input">
              <option value="direct">direct</option>
              <option value="proxy">proxy</option>
              <option value="relay">relay</option>
            </select>
          </div>
        </div>

        <div class="form-row-triple">
          <div class="form-group">
            <label>{{ ps('fieldKind') }}</label>
            <select v-model="editKind" class="input">
              <option value="cloud">Cloud</option>
              <option value="local">Local</option>
            </select>
          </div>
          <div class="form-group">
            <label>{{ ps('fieldCategory') }}</label>
            <select v-model="editCategory" class="input">
              <option value="official">Official</option>
              <option value="official_proxy">Official Proxy</option>
              <option value="third_party_relay">Third Party Relay</option>
              <option value="aggregator">Aggregator</option>
              <option value="self_host">Self Host</option>
            </select>
          </div>
          <div class="form-group">
            <label>{{ ps('fieldDiscountRate') }}</label>
            <input v-model.number="editDiscountRate" type="number" step="0.01" min="0" max="1" class="input" />
          </div>
        </div>

        <div class="form-group">
          <label>{{ ps('fieldNotes') }}</label>
          <textarea v-model="editNotes" class="input settings-notes" rows="2" :placeholder="ps('notesPlaceholder')" />
        </div>

        <div class="form-actions">
          <button class="btn btn-primary btn-sm" @click="save" :disabled="saving">
            {{ saving ? ps('saving') : ps('save') }}
          </button>
          <span v-if="msg" class="form-hint" :class="{ 'form-hint--error': msg.startsWith(ps('saveFailed').substring(0, 2)) }">{{ msg }}</span>
        </div>
      </div>
    </section>

    <section class="card settings-section settings-section--batch">
      <h3 class="section-title">{{ ps('batchTitle') }}</h3>
      <div class="batch-actions">
        <button class="btn btn-ghost btn-sm" @click="batchRecover" :disabled="batchLoading">
          {{ batchLoading ? ps('batchRecoverLoading') : ps('batchRecover') }}
        </button>
        <button class="btn btn-ghost btn-sm" @click="runHealthCheck" :disabled="checking">
          {{ checking ? ps('checking') : ps('runCheck') }}
        </button>
        <span v-if="batchMsg" class="form-hint">{{ batchMsg }}</span>
        <span v-if="checkMsg" class="form-hint">{{ checkMsg }}</span>
      </div>
    </section>

    <section class="card settings-section settings-section--info">
      <h3 class="section-title">{{ ps('infoTitle') }}</h3>
      <div class="info-grid">
        <div class="info-item"><span class="info-label">{{ ps('infoLabelId') }}</span><span>{{ provider.id }}</span></div>
        <div class="info-item"><span class="info-label">{{ ps('infoLabelCode') }}</span><code>{{ provider.code }}</code></div>
        <div class="info-item"><span class="info-label">{{ ps('infoLabelCatalog') }}</span><code>{{ provider.catalog_code || '—' }}</code></div>
        <div class="info-item"><span class="info-label">{{ ps('infoLabelVendor') }}</span><span>{{ provider.vendor_name || '—' }}</span></div>
        <div class="info-item"><span class="info-label">{{ ps('infoLabelHeaderProfile') }}</span><code>{{ provider.header_profile_code || '—' }}</code></div>
        <div class="info-item"><span class="info-label">{{ ps('infoLabelProtocol') }}</span><code>{{ provider.protocol }}</code></div>
        <div class="info-item info-item--wide"><span class="info-label">{{ ps('infoLabelBaseUrl') }}</span><code class="url">{{ provider.base_url || '—' }}</code></div>
        <div class="info-item"><span class="info-label">{{ ps('infoLabelKind') }}</span><span>{{ provider.kind }} / {{ provider.category }}</span></div>
        <div class="info-item"><span class="info-label">{{ ps('infoLabelEgress') }}</span><span>{{ provider.egress_profile || '—' }}</span></div>
        <div class="info-item"><span class="info-label">{{ ps('infoLabelDomestic') }}</span><span>{{ provider.domestic ? ps('domesticYes') : ps('domesticNo') }}</span></div>
        <div class="info-item"><span class="info-label">{{ ps('infoLabelDiscountRate') }}</span><span>{{ provider.discount_rate ?? '—' }}</span></div>
        <div class="info-item">
          <span class="info-label">{{ ps('infoLabelStatus') }}</span>
          <span v-if="provider.manual_disabled" class="badge badge-red">{{ ps('statusManualDisabled') }}</span>
          <span v-else-if="provider.enabled" class="badge badge-green">{{ ps('statusEnabled') }}</span>
          <span v-else class="badge badge-gray">{{ ps('statusDisabled') }}</span>
        </div>
        <div class="info-item"><span class="info-label">{{ ps('infoLabelCreatedAt') }}</span><span>{{ fmtTime(provider.created_at) }}</span></div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.settings-section {
  margin: 0;
  min-width: 0;
}

.section-title {
  margin: 0 0 14px;
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
}

.settings-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.form-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 12px;
}

.form-row-triple {
  display: grid;
  grid-template-columns: 1fr;
  gap: 12px;
}

.form-group label {
  display: block;
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 4px;
}

.settings-notes {
  min-height: 64px;
  resize: vertical;
}

.form-actions,
.batch-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.form-hint {
  font-size: 12px;
  color: var(--muted);
}
.form-hint--error {
  color: var(--danger);
}

.info-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 8px;
  font-size: 13px;
}

.info-item {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
}

.info-item--wide {
  grid-column: 1 / -1;
}

.info-label {
  flex: 0 0 84px;
  color: var(--muted);
  font-size: 12px;
}

.info-item code.url {
  word-break: break-all;
}

@media (max-width: 960px) {
  .info-item--wide {
    grid-column: auto;
  }
}
</style>
