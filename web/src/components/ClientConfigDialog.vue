<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getKeys, listModels, getClientConfigFeatured, applyForKey, revealKey, type ApiKey, type ModelCanonical } from '../api'
import {
  TOOLS, OS_INFO,
  type ToolId, type OS, type ModelScope,
  type ModelItem,
  renderZCodeConfig, renderOpenCodeConfig,
  renderMiniMaxCodeProvider,
  generateShellScript, getManualSteps, TOOL_TEMPLATES, detectOS, validateProviderName, normalizeGatewayURL,
  downloadFile, auditAction,
} from '../composables/useClientConfig'
import { Key } from '@element-plus/icons-vue'

const props = defineProps<{ tool: ToolId; open: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()
const router = useRouter()
const { t } = useI18n()
const errorText = (error: string) => error.startsWith('clientConfigDialog.error.') ? t(error) : error

const toolInfo = computed(() => TOOLS.find(t => t.id === props.tool)!)

// ── Step 1: Key selection (all tenant keys for admin) ──────────────────────
const keys = ref<ApiKey[]>([])
const selectedKeyId = ref<number | null>(null)
const keysLoading = ref(false)
const applyDialogOpen = ref(false)
const applying = ref(false)
const applyError = ref('')
const applyForm = ref({ application_code: 'default-app', description: '' })

async function loadKeys() {
  keysLoading.value = true
  try {
    keys.value = (await getKeys()).filter(key => key.enabled && key.status === 'active' && (!key.expires_at || Date.parse(key.expires_at) > Date.now()))
    if (!keys.value.some(key => key.id === selectedKeyId.value)) selectedKeyId.value = keys.value[0]?.id ?? null
  } catch {
    keys.value = []
    selectedKeyId.value = null
  } finally {
    keysLoading.value = false
  }
}

async function submitApply() {
  applying.value = true
  applyError.value = ''
  try {
    const resp = await applyForKey({
      application_code: applyForm.value.application_code,
      description: applyForm.value.description,
    })
    applyDialogOpen.value = false
    await loadKeys()
    if (resp?.id && keys.value.length > 0) {
      const found = keys.value.find(k => k.id === resp.id)
      if (found) selectedKeyId.value = found.id
    }
    applyForm.value = { application_code: 'default-app', description: '' }
  } catch (e: any) {
    applyError.value = e?.message || 'clientConfigDialog.error.applyFailed'
  } finally {
    applying.value = false
  }
}

// ── Step 2: OS selection ─────────────────────────────────────────────────────
const selectedOS = ref<OS>(detectOS())
const providerName = ref('itestu')
const baseURL = ref('')
const generationError = ref('')

function defaultGatewayBaseURL(): string {
  const origin = typeof window !== 'undefined' ? window.location.origin : 'https://llm.itestu.cn'
  return `${origin.replace(/\/+$/, '')}/v1`
}

// ── Step 3: Model scope + custom selection ──────────────────────────────────
const selectedScope = ref<ModelScope>('featured')
const selectedModels = ref<string[]>([])
const allModels = ref<ModelCanonical[]>([])
const allModelsLoading = ref(false)
const allModelsLoaded = ref(false)
const featuredModels = ref<string[]>([])
const featuredLoading = ref(false)
const featuredInfo = ref<ModelItem[]>([])
const modelLoadError = ref('')
const allModelLoadError = ref('')
let catalogRequest = 0
let allCatalogRequest = 0
const modelSearch = ref('')

async function loadFeaturedModels() {
  const request = ++catalogRequest
  featuredModels.value = []
  featuredInfo.value = []
  if (!selectedKeyId.value || !props.open) return
  featuredLoading.value = true
  modelLoadError.value = ''
  try {
    const resp = await getClientConfigFeatured(selectedKeyId.value)
    if (request !== catalogRequest || !props.open) return
    featuredModels.value = [...new Set(resp.featured_models.map(id => id.trim()).filter(Boolean))]
    featuredInfo.value = resp.models
  } catch {
    if (request === catalogRequest) modelLoadError.value = 'clientConfigDialog.error.featuredLoadFailed'
  } finally {
    if (request === catalogRequest) featuredLoading.value = false
  }
}

async function loadAllModels() {
  if (allModelsLoaded.value) return
  const request = ++allCatalogRequest
  allModelsLoading.value = true
  allModelLoadError.value = ''
  try {
    const resp = await listModels({ status: 'active' })
    if (request !== allCatalogRequest || !props.open) return
    allModels.value = resp.items.filter(m => m.status === 'active')
    allModelsLoaded.value = true
  } catch {
    if (request !== allCatalogRequest) return
    allModels.value = []
    allModelLoadError.value = 'clientConfigDialog.error.catalogLoadFailed'
  } finally {
    if (request === allCatalogRequest) allModelsLoading.value = false
  }
}

function toggleModel(modelId: string) {
  const idx = selectedModels.value.indexOf(modelId)
  if (idx >= 0) {
    selectedModels.value.splice(idx, 1)
  } else {
    selectedModels.value.push(modelId)
  }
}

function selectAllModels() {
  selectedModels.value = filteredModels.value.map(m => m.canonical_name)
}

function deselectAllModels() {
  selectedModels.value = []
}

const filteredModels = computed(() => {
  const q = modelSearch.value.trim().toLowerCase()
  if (!q) return allModels.value
  return allModels.value.filter(m =>
    m.canonical_name.toLowerCase().includes(q) ||
    (m.vendor && m.vendor.toLowerCase().includes(q)) ||
    (m.display_name && m.display_name.toLowerCase().includes(q))
  )
})

const groupedModels = computed(() => {
  const groups = new Map<string, ModelCanonical[]>()
  for (const m of filteredModels.value) {
    const key = m.family || m.vendor || t('clientConfigDialog.otherFamily')
    if (!groups.has(key)) groups.set(key, [])
    groups.get(key)!.push(m)
  }
  return Array.from(groups.entries()).sort(([a], [b]) => a.localeCompare(b))
})

function groupSelection(family: string): 'all' | 'none' | 'some' {
  const list = groupedModels.value.find(([f]) => f === family)?.[1] || []
  if (list.length === 0) return 'none'
  const selectedCount = list.filter(m => selectedModels.value.includes(m.canonical_name)).length
  if (selectedCount === 0) return 'none'
  if (selectedCount === list.length) return 'all'
  return 'some'
}

function toggleGroup(family: string) {
  const list = groupedModels.value.find(([f]) => f === family)?.[1] || []
  const state = groupSelection(family)
  const ids = list.map(m => m.canonical_name)
  if (state === 'all') {
    selectedModels.value = selectedModels.value.filter(id => !ids.includes(id))
  } else {
    const set = new Set(selectedModels.value)
    for (const id of ids) set.add(id)
    selectedModels.value = Array.from(set)
  }
}

// ── Generated content ───────────────────────────────────────────────────────
const generatedFile = ref('')
const generatedScript = ref('')
const generatedManual = ref('')
const generatedSecret = ref('')
const filePreview = computed(() => generatedSecret.value
  ? generatedFile.value.replaceAll(JSON.stringify(generatedSecret.value).slice(1, -1), '••••••••').replaceAll(generatedSecret.value, '••••••••')
  : generatedFile.value)
const hasGenerated = ref(false)
const activeTab = ref<'file' | 'script' | 'manual'>('file')
const generating = ref(false)

function clearGenerated() {
  hasGenerated.value = false
  generatedFile.value = ''
  generatedScript.value = ''
  generatedManual.value = ''
  generatedSecret.value = ''
}
let generationRequest = 0
watch(() => props.open, (open) => {
  generationRequest++
  catalogRequest++
  allCatalogRequest++
  featuredLoading.value = false
  allModelsLoading.value = false
  modelLoadError.value = ''
  allModelLoadError.value = ''
  clearGenerated()
  featuredModels.value = []
  featuredInfo.value = []
  allModelsLoaded.value = false
  allModels.value = []
  selectedModels.value = []
  selectedKeyId.value = null
  generationError.value = ''
  if (open) {
    selectedScope.value = 'featured'
    selectedOS.value = detectOS()
    baseURL.value = defaultGatewayBaseURL()
    providerName.value = 'itestu'
    modelSearch.value = ''
    activeTab.value = 'file'
    loadKeys()
  }
}, { immediate: true })
watch(selectedKeyId, () => { generationRequest++; clearGenerated(); loadFeaturedModels() }, { flush: 'sync' })
watch(selectedScope, (scope) => { generationRequest++; clearGenerated(); if (scope === 'all') loadAllModels() }, { flush: 'sync' })
watch([baseURL, providerName, selectedOS, () => props.tool, () => [...selectedModels.value]], () => {
  generationRequest++
  clearGenerated()
}, { flush: 'sync' })
const configuredModelCount = ref(0)
const selectedKey = computed(() => keys.value.find(k => k.id === selectedKeyId.value))

const fileExt = computed(() => props.tool === 'minimax_code' ? '.yaml' : '.json')
const scriptExt = computed(() => selectedOS.value === 'windows' ? '.ps1' : '.sh')

async function generate() {
  if (!selectedKey.value || featuredLoading.value || (selectedScope.value === 'featured' && modelLoadError.value)) return
  const request = ++generationRequest
  clearGenerated()
  generating.value = true
  hasGenerated.value = false
  generationError.value = ''

  try {
    const key = selectedKey.value
    const gatewayURL = normalizeGatewayURL(baseURL.value)
    const name = validateProviderName(providerName.value)
    const models = selectedScope.value === 'featured' ? [...featuredModels.value] : [...selectedModels.value]
    if (models.length === 0) throw new Error('clientConfigDialog.error.selectModel')
    if (selectedScope.value === 'all' && !allModelsLoaded.value) await loadAllModels()
    const modelInfo: ModelItem[] = models.map((id) => {
      const featured = featuredInfo.value.find(item => item.id === id)
      const model = allModels.value.find((item) => item.canonical_name === id)
      return {
        id,
        name: id,
        contextWindow: featured?.contextWindow ?? model?.context_window ?? undefined,
      }
    })

    // The selected API key is revealed only for the generated client artifact.
    const manualOnly = ['cursor', 'cherry_studio', 'roocode'].includes(props.tool)
    const apiKey = manualOnly ? '' : (await revealKey(key.id)).api_key
    if (request !== generationRequest || !props.open) return
    if (!manualOnly && !apiKey) throw new Error('clientConfigDialog.error.keyUnavailable')
    generatedSecret.value = apiKey

    let fileContent: any
    if (props.tool === 'zcode') {
      fileContent = renderZCodeConfig(apiKey, models, gatewayURL, modelInfo, name)
    } else if (props.tool === 'opencode') {
      fileContent = renderOpenCodeConfig(apiKey, models, gatewayURL, modelInfo, name)
    } else if (props.tool === 'minimax_code') {
      const provider = renderMiniMaxCodeProvider(apiKey, models, gatewayURL, modelInfo, name)
      fileContent = `custom_provider:\n${provider.split('\n').filter(Boolean).map(line => `  ${line}`).join('\n')}\n`
    } else if (props.tool === 'cherry_studio') {
      fileContent = { provider: name, baseURL: gatewayURL, models, note: t('clientConfigDialog.notes.cherryStudio') }
    } else if (props.tool === 'roocode') {
      fileContent = { provider: name, baseURL: gatewayURL, models, note: t('clientConfigDialog.notes.rooCode') }
    } else {
      fileContent = { note: t('clientConfigDialog.cursorNotSupported') }
    }

    generatedFile.value = typeof fileContent === 'string'
      ? fileContent
      : JSON.stringify(fileContent, null, 2)
    generatedScript.value = generateShellScript(props.tool, selectedOS.value, generatedFile.value, apiKey, gatewayURL)
    generatedManual.value = getManualSteps(props.tool, selectedOS.value, gatewayURL, models, name)
    configuredModelCount.value = models.length
    hasGenerated.value = true

    await auditAction({
      action: 'generate',
      tool: props.tool,
      os: selectedOS.value,
      keyId: key.id,
      modelCount: models.length,
      modelScope: selectedScope.value,
    })
  } catch (error: any) {
    clearGenerated()
    generationError.value = error?.message || 'clientConfigDialog.error.generateFailed'
  } finally {
    generating.value = false
  }
}

async function copyFile() {
  try { await navigator.clipboard.writeText(generatedFile.value) }
  catch { generationError.value = 'clientConfigDialog.error.copyFailed'; return }
  auditAction({ action: 'copy_config', tool: props.tool, os: selectedOS.value, keyId: selectedKeyId.value || 0, modelCount: configuredModelCount.value, modelScope: selectedScope.value })
}

function downloadScript() {
  const filename = `${props.tool}-config${scriptExt.value}`
  downloadFile(generatedScript.value, filename, 'text/plain')
  auditAction({ action: 'download_script', tool: props.tool, os: selectedOS.value, keyId: selectedKeyId.value || 0, modelCount: configuredModelCount.value, modelScope: selectedScope.value })
}

function downloadFileContent() {
  const filename = `${props.tool}-config${fileExt.value}`
  downloadFile(generatedFile.value, filename, props.tool === 'minimax_code' ? 'application/yaml' : 'application/json')
}

function close() {
  generationRequest++
  clearGenerated()
  emit('close')
}

function goManageFeatured() {
  close()
  router.push('/routing-policy')
}
</script>

<template>
  <div v-if="open" class="drawer-backdrop" @click.self="close">
    <div class="drawer-panel drawer-panel-wide" @click.stop>
      <div class="drawer-header">
        <div class="dialog-title">
          <span>{{ toolInfo.icon }}</span>
          <span>{{ t('clientConfigDialog.title', { tool: toolInfo.name }) }}</span>
        </div>
        <button class="btn btn-ghost btn-sm" @click="close">{{ t('clientConfigDialog.close') }} ✕</button>
      </div>

      <div class="drawer-body-scroll">

        <!-- Step 1: API Key -->
        <div class="step-section">
          <div class="step-label">
            <span>{{ t('clientConfigDialog.step1.title') }}</span>
            <button class="btn btn-ghost btn-sm refresh-btn" :disabled="keysLoading" @click="loadKeys">
              {{ keysLoading ? t('clientConfigDialog.step1.refreshing') : '↻ ' + t('clientConfigDialog.step1.refresh') }}
            </button>
          </div>
          <select v-if="!keysLoading && keys.length > 0" v-model="selectedKeyId" class="select-field">
            <option v-for="k in keys" :key="k.id" :value="k.id">
              {{ k.key_prefix }}**** ({{ k.application_code }}, {{ k.status }})
            </option>
          </select>
          <div v-else-if="keysLoading" class="state-row">{{ t('clientConfigDialog.step1.loading') }}</div>
          <div v-else class="empty-state">
            <div class="empty-state-icon"><Key /></div>
            <div class="empty-state-title">{{ t('clientConfigDialog.step1.empty.title') }}</div>
            <div class="empty-state-desc">
              {{ t('clientConfigDialog.step1.empty.description') }}
            </div>
            <button class="btn btn-primary" @click="applyDialogOpen = true">
              {{ t('clientConfigDialog.step1.empty.applyButton') }}
            </button>
          </div>
          <div v-if="selectedKey" class="key-info">
            {{ t('clientConfigDialog.step1.selected') }}<code>{{ selectedKey.key_prefix }}****</code>
            <span class="badge" :class="selectedKey.status === 'active' ? 'badge-green' : 'badge-yellow'">{{ selectedKey.status }}</span>
          </div>
        </div>

        <!-- Step 2: OS -->
        <div class="step-section">
          <div class="step-label">{{ t('clientConfigDialog.step2.title') }}</div>
          <div class="os-tabs">
            <button
              v-for="(info, os) in OS_INFO"
              :key="os"
              :class="['os-tab', selectedOS === os ? 'active' : '']"
              @click="selectedOS = os as OS"
            >{{ info.label }}</button>
          </div>
          <div class="path-hint">
            {{ t('clientConfigDialog.step2.pathHint') }}<code>{{ OS_INFO[selectedOS].paths[tool] }}</code>
          </div>
        </div>

        <!-- Step 3: Gateway URL -->
        <div class="step-section">
          <div class="step-label">{{ t('clientConfigDialog.gateway.title') }}</div>
          <input v-model="baseURL" class="select-field" type="url" placeholder="https://llm.itestu.cn/v1" />
          <div class="path-hint">{{ t('clientConfigDialog.gateway.hint') }}</div>
        </div>

        <div class="step-section">
          <div class="step-label">{{ t('clientConfigDialog.provider.label') }}</div>
          <input v-model="providerName" class="select-field" maxlength="64" :aria-label="t('clientConfigDialog.provider.label')" />
          <div class="path-hint">{{ t('clientConfigDialog.provider.hint') }}</div>
        </div>
        <!-- Step 4: Model scope -->
        <div class="step-section">
          <div class="step-label">{{ t('clientConfigDialog.step3.title') }}</div>
          <div class="scope-radios">
            <label class="radio-label">
              <input type="radio" value="featured" v-model="selectedScope" />
              <span>{{ t('clientConfigDialog.step3.featured') }}</span>
            </label>
            <label class="radio-label">
              <input type="radio" value="all" v-model="selectedScope" />
              <span>{{ t('clientConfigDialog.step3.all') }}</span>
            </label>
          </div>

          <div v-if="selectedScope === 'featured' && modelLoadError" class="alert alert-danger">{{ errorText(modelLoadError) }}
            <button class="btn btn-ghost btn-sm" @click="loadFeaturedModels">{{ t('clientConfigDialog.retry') }}</button>
          </div>
          <div v-if="selectedScope === 'all' && allModelLoadError" class="alert alert-danger">{{ errorText(allModelLoadError) }}
            <button class="btn btn-ghost btn-sm" @click="loadAllModels">{{ t('clientConfigDialog.retry') }}</button>
          </div>
          <!-- Featured preview -->
          <div v-if="selectedScope === 'featured'" class="model-preview">
            <div v-if="featuredLoading" class="models-loading">{{ t('clientConfigDialog.step1.loading') }}</div>
            <div v-else-if="featuredModels.length === 0" class="empty-mini-state">
              <div class="empty-mini-title">{{ t('clientConfigDialog.step3.featuredPreview.empty') }}</div>
              <button class="btn btn-primary btn-sm" @click="goManageFeatured">
                {{ t('clientConfigDialog.step3.featuredPreview.manageButton') }}
              </button>
            </div>
            <template v-else>
              <span class="model-tag" v-for="m in featuredModels" :key="m">{{ m }}</span>
              <button class="btn btn-ghost btn-sm manage-link" @click="goManageFeatured">
                {{ t('clientConfigDialog.step3.featuredPreview.manage') }}
              </button>
            </template>
          </div>

          <!-- All models from API -->
          <div v-if="selectedScope === 'all'" class="all-models-panel">
            <div class="all-models-toolbar">
              <button class="btn btn-ghost btn-sm" @click="selectAllModels">{{ t('clientConfigDialog.step3.allModels.selectAll') }}</button>
              <button class="btn btn-ghost btn-sm" @click="deselectAllModels">{{ t('clientConfigDialog.step3.allModels.deselectAll') }}</button>
              <span class="model-count-label">
                <strong>{{ selectedModels.length }}</strong> / {{ allModels.length }} {{ t('clientConfigDialog.step3.allModels.selected') }}
                <span v-if="modelSearch" class="filter-hint">{{ t('clientConfigDialog.step3.allModels.filterHint', { count: filteredModels.length }) }}</span>
              </span>
            </div>
            <input
              v-model="modelSearch"
              type="text"
              class="model-search-input"
              :placeholder="t('clientConfigDialog.step3.allModels.searchPlaceholder')"
            />
            <div v-if="allModelsLoading" class="models-loading">{{ t('clientConfigDialog.step1.loading') }}</div>
            <div v-else-if="filteredModels.length === 0" class="models-loading">{{ t('clientConfigDialog.step3.allModels.noMatch') }}</div>
            <div v-else class="models-grouped">
              <div v-for="[family, list] in groupedModels" :key="family" class="model-family-group">
                <label class="model-family-header">
                  <input
                    type="checkbox"
                    class="group-master-checkbox"
                    :checked="groupSelection(family) === 'all'"
                    :indeterminate.prop="groupSelection(family) === 'some'"
                    @change="toggleGroup(family)"
                  />
                  <span class="model-family-name">{{ family }}</span>
                  <span class="model-family-count">{{ list.filter(m => selectedModels.includes(m.canonical_name)).length }} / {{ list.length }}</span>
                </label>
                <div class="models-checklist">
                  <label
                    v-for="m in list"
                    :key="m.canonical_name"
                    class="model-check-item"
                  >
                    <input
                      type="checkbox"
                      :checked="selectedModels.includes(m.canonical_name)"
                      @change="toggleModel(m.canonical_name)"
                    />
                    <span class="model-check-name" :title="m.canonical_name">{{ m.canonical_name }}</span>
                  </label>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Sticky footer with primary action -->
        <div class="drawer-footer">
          <div v-if="generationError" class="alert alert-danger">{{ errorText(generationError) }}</div>
          <div class="footer-info" v-if="hasGenerated">
            <span class="footer-hint">{{ t('clientConfigDialog.footer.generated', { count: configuredModelCount }) }}</span>
          </div>
          <button
            v-if="!hasGenerated"
            class="btn btn-primary footer-generate"
            :disabled="!selectedKeyId || generating || featuredLoading || allModelsLoading"
            @click="generate"
          >
            {{ generating ? t('clientConfigDialog.footer.generating') : t('clientConfigDialog.footer.generate') }}
          </button>
          <button
            v-else
            class="btn btn-ghost footer-regenerate"
            @click="generate"
            :disabled="generating"
          >
            {{ t('clientConfigDialog.footer.regenerate') }}
          </button>
        </div>

        <!-- Results -->
        <div v-if="hasGenerated" class="results-section">
          <div class="result-tabs">
            <button :class="['tab-btn', activeTab === 'file' ? 'active' : '']" @click="activeTab = 'file'">{{ tool === 'minimax_code' ? t('clientConfigDialog.results.tabs.yaml') : t('clientConfigDialog.results.tabs.file') }}</button>
            <button
              v-if="TOOL_TEMPLATES[tool].supportsScript"
              :class="['tab-btn', activeTab === 'script' ? 'active' : '']" @click="activeTab = 'script'"
            >{{ t('clientConfigDialog.results.tabs.script') }}</button>
            <button :class="['tab-btn', activeTab === 'manual' ? 'active' : '']" @click="activeTab = 'manual'">{{ t('clientConfigDialog.results.tabs.manual') }}</button>
          </div>

          <!-- File Tab -->
          <div v-if="activeTab === 'file'" class="tab-content">
            <pre class="code-preview">{{ filePreview }}</pre>
            <div class="result-actions">
              <button class="btn btn-ghost btn-sm" @click="copyFile">{{ t('clientConfigDialog.results.actions.copyContent') }}</button>
              <button class="btn btn-ghost btn-sm" @click="downloadFileContent">{{ t('clientConfigDialog.results.actions.downloadFile') }}</button>
            </div>
          </div>

          <!-- Script Tab -->
          <div v-if="activeTab === 'script'" class="tab-content">
            <pre class="code-preview script-code">{{ generatedScript ? t('clientConfigDialog.results.actions.scriptWarning') : '' }}</pre>
            <div class="result-actions">
              <button class="btn btn-primary btn-sm" @click="downloadScript">{{ t('clientConfigDialog.results.actions.downloadScript') }}</button>
              <span class="action-hint">{{ t('clientConfigDialog.results.actions.mergeHint', { provider: providerName }) }}</span>
            </div>
          </div>

          <!-- Manual Tab -->
          <div v-if="activeTab === 'manual'" class="tab-content">
            <pre class="code-preview manual-text">{{ generatedManual }}</pre>
          </div>
        </div>

      </div>
    </div>
  </div>

  <!-- Apply Key Dialog (separate from main drawer) -->
  <div v-if="applyDialogOpen" class="modal-backdrop" @click.self="applyDialogOpen = false">
    <div class="modal-panel card" @click.stop>
      <div class="modal-header">
        <h4 style="margin:0">{{ t('clientConfigDialog.applyDialog.title') }}</h4>
        <button class="btn btn-ghost btn-sm" @click="applyDialogOpen = false">{{ t('clientConfigDialog.close') }} ✕</button>
      </div>
      <div class="modal-body">
        <div class="form-group">
          <label>{{ t('clientConfigDialog.applyDialog.applicationCode') }}</label>
          <input v-model="applyForm.application_code" class="form-input" :placeholder="t('clientConfigDialog.applyDialog.applicationCodePlaceholder')" />
        </div>
        <div class="form-group">
          <label>{{ t('clientConfigDialog.applyDialog.description') }}</label>
          <textarea v-model="applyForm.description" class="form-input" rows="3" :placeholder="t('clientConfigDialog.applyDialog.descriptionPlaceholder')"></textarea>
        </div>
        <div v-if="applyError" class="alert alert-danger">{{ errorText(applyError) }}</div>
      </div>
      <div class="modal-footer">
        <button class="btn btn-ghost" @click="applyDialogOpen = false">{{ t('clientConfigDialog.applyDialog.cancel') }}</button>
        <button class="btn btn-primary" :disabled="applying" @click="submitApply">
          {{ applying ? t('clientConfigDialog.applyDialog.submitting') : t('clientConfigDialog.applyDialog.submit') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dialog-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
  font-size: 15px;
}

.drawer-body-scroll {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 18px;
  min-height: 0;
}

.step-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.step-label {
  font-weight: 600;
  font-size: 13px;
  color: var(--text);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.refresh-btn {
  font-weight: 400;
  font-size: 11px;
  padding: 2px 8px;
  color: var(--muted);
}

.select-field {
  width: 100%;
  padding: 8px 12px;
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--text);
  font-size: 13px;
  outline: none;
}

.select-field:focus {
  border-color: color-mix(in srgb, var(--accent) 50%, transparent);
}

.key-info {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--muted);
}

.badge {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 4px;
}

.badge-green {
  background: var(--success-bg);
  color: var(--success);
}

.badge-yellow {
  background: var(--warning-bg);
  color: var(--warning);
}

.os-tabs {
  display: flex;
  gap: 6px;
}

.os-tab {
  padding: 5px 14px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: none;
  color: var(--muted);
  font-size: 13px;
  cursor: pointer;
  transition: all 0.15s;
}

.os-tab.active {
  background: color-mix(in srgb, var(--accent) 20%, transparent);
  border-color: color-mix(in srgb, var(--accent) 50%, transparent);
  color: var(--accent-h);
}

.path-hint {
  font-size: 12px;
  color: var(--muted);
}

.scope-radios {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.radio-label {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
}

.model-preview {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 10px 12px;
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
  border-radius: 6px;
  align-items: center;
}

.empty-mini-state {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: flex-start;
  width: 100%;
  padding: 4px 0;
}

.empty-mini-title {
  font-size: 12px;
  color: var(--muted);
}

.manage-link {
  margin-left: auto;
  font-size: 11px;
  padding: 2px 8px;
}

.model-tag {
  font-size: 12px;
  padding: 2px 8px;
  background: color-mix(in srgb, var(--accent) 15%, transparent);
  color: var(--accent-h);
  border-radius: 4px;
}

.all-models-panel {
  display: flex;
  flex-direction: column;
  gap: 0;
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
  background: var(--overlay-light);
}

.all-models-toolbar {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 12px;
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
  border-bottom: 1px solid var(--border);
}

.model-count-label {
  font-size: 12px;
  color: var(--muted);
  margin-left: auto;
}

.model-count-label strong {
  color: var(--accent-h);
  font-size: 13px;
}

.filter-hint {
  color: var(--muted);
  font-size: 11px;
}

.model-search-input {
  width: 100%;
  padding: 8px 12px;
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
  border: none;
  border-bottom: 1px solid var(--border);
  color: var(--text);
  font-size: 13px;
  outline: none;
  box-sizing: border-box;
}

.model-search-input:focus {
  background: color-mix(in srgb, var(--accent) 8%, transparent);
  border-bottom-color: var(--accent);
}

.model-search-input::placeholder {
  color: var(--muted);
}

.models-loading {
  padding: 20px;
  text-align: center;
  font-size: 13px;
  color: var(--muted);
}

.models-grouped {
  max-height: 360px;
  overflow-y: auto;
  padding: 4px 0;
}

.model-family-group {
  margin-bottom: 4px;
}

.model-family-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 12px;
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
  border-top: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
  position: sticky;
  top: 0;
  z-index: 1;
}

.model-family-header:first-child {
  border-top: none;
}

.model-family-name {
  font-size: 11px;
  font-weight: 600;
  color: var(--muted);
  letter-spacing: 0.01em;
}

.model-family-count {
  font-size: 11px;
  color: var(--muted);
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
  padding: 1px 6px;
  border-radius: 8px;
}

.models-checklist {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 2px;
  padding: 4px 8px 8px;
}

.model-check-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 8px;
  cursor: pointer;
  font-size: 12px;
  border-radius: 4px;
  transition: background 0.1s;
}

.model-check-item:hover {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
}

.model-check-item input[type="checkbox"] {
  flex-shrink: 0;
  margin: 0;
  cursor: pointer;
}

.model-check-name {
  color: var(--text);
  font-family: ui-monospace, 'SF Mono', Menlo, monospace;
  font-size: 11.5px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
}

.drawer-footer {
  position: sticky;
  bottom: -16px;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  padding: 12px 0;
  background: var(--card);
  border-top: 1px solid var(--border);
  margin: 8px -20px -16px -20px;
  padding-left: 20px;
  padding-right: 20px;
  z-index: 5;
}

.footer-info {
  margin-right: auto;
}

.footer-hint {
  font-size: 12px;
  color: var(--muted);
}

.footer-generate,
.footer-regenerate {
  min-width: 140px;
  font-weight: 600;
}

.generate-section {
  align-items: center;
}

.results-section {
  border-top: 1px solid var(--border);
  padding-top: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.result-tabs {
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--border);
  padding-bottom: 0;
}

.tab-btn {
  padding: 6px 14px;
  border: none;
  background: none;
  color: var(--muted);
  font-size: 13px;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  transition: all 0.15s;
}

.tab-btn.active {
  color: var(--accent-h);
  border-bottom-color: var(--accent-h);
}

.tab-content {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.code-preview {
  background: var(--probe-dark-bg);
  color: var(--surface-secondary);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 14px;
  font-size: 12px;
  line-height: 1.6;
  overflow-x: auto;
  max-height: 320px;
  overflow-y: auto;
  white-space: pre;
  margin: 0;
}

.script-code {
  color: var(--probe-cyan-light);
}

.manual-text {
  white-space: pre-wrap;
}

.result-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}

.action-hint {
  font-size: 12px;
  color: var(--muted);
}

/* ── Empty state (no keys) ─────────────────────────────────────────── */
.state-row {
  padding: 8px 12px;
  font-size: 12px;
  color: var(--muted);
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
  border-radius: 6px;
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 24px 16px;
  background: color-mix(in srgb, var(--accent) 5%, transparent);
  border: 1px dashed color-mix(in srgb, var(--accent) 30%, transparent);
  border-radius: 10px;
  text-align: center;
}

.empty-state-icon {
  font-size: 28px;
  line-height: 1;
}

.empty-state-title {
  font-weight: 600;
  font-size: 14px;
  color: var(--text);
}

.empty-state-desc {
  font-size: 12px;
  color: var(--muted);
  line-height: 1.5;
  max-width: 320px;
}

/* ── Group master checkbox ─────────────────────────────────────────── */
.model-family-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
  border-top: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
  position: sticky;
  top: 0;
  z-index: 1;
  cursor: pointer;
  user-select: none;
}

.model-family-header:hover {
  background: color-mix(in srgb, var(--accent) 8%, transparent);
}

.group-master-checkbox {
  flex-shrink: 0;
  margin: 0;
  cursor: pointer;
  width: 14px;
  height: 14px;
  accent-color: var(--accent);
}

.group-master-checkbox:indeterminate {
  accent-color: var(--accent);
}

/* ── Modal (apply key) ──────────────────────────────────────────────── */
.modal-backdrop {
  position: fixed;
  inset: 0;
  background: var(--overlay-strong);
  z-index: 1100;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}

.modal-panel {
  width: 100%;
  max-width: 480px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  box-shadow: 0 24px 60px var(--overlay-strong);
  display: flex;
  flex-direction: column;
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 18px;
  border-bottom: 1px solid var(--border);
}

.modal-body {
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.modal-footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 18px;
  border-top: 1px solid var(--border);
}

.form-group {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.form-group label {
  font-size: 12px;
  color: var(--muted);
  font-weight: 500;
}

.form-input {
  width: 100%;
  padding: 8px 10px;
  background: color-mix(in srgb, var(--kx-text) 4%, transparent);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 13px;
  outline: none;
  font-family: inherit;
  box-sizing: border-box;
}

.form-input:focus {
  border-color: color-mix(in srgb, var(--accent) 50%, transparent);
  background: color-mix(in srgb, var(--accent) 5%, transparent);
}

textarea.form-input {
  resize: vertical;
  min-height: 60px;
}

.alert {
  padding: 8px 12px;
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.5;
}

.alert-danger {
  background: var(--danger-bg);
  color: var(--danger);
  border: 1px solid var(--danger-bd);
}
</style>
