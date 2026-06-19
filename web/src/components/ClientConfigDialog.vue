<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { getKeys, listModels, type ApiKey, type ModelCanonical } from '../api'
import {
  TOOLS, OS_INFO, FEATURED_MODELS,
  type ToolId, type OS, type ModelScope,
  renderZCodeConfig, renderOpenCodeConfig,
  renderCherryStudioConfig, renderRooCodeSettings,
  generateShellScript, getManualSteps,
  downloadFile, auditAction,
} from '../composables/useClientConfig'

const props = defineProps<{ tool: ToolId; open: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const toolInfo = computed(() => TOOLS.find(t => t.id === props.tool)!)

// ── Step 1: Key selection (all tenant keys for admin) ──────────────────────
const keys = ref<ApiKey[]>([])
const selectedKeyId = ref<number | null>(null)
const keysLoading = ref(false)

async function loadKeys() {
  keysLoading.value = true
  try {
    keys.value = await getKeys()
    if (keys.value.length > 0 && !selectedKeyId.value) {
      selectedKeyId.value = keys.value[0].id
    }
  } catch {
    keys.value = []
  } finally {
    keysLoading.value = false
  }
}

// ── Step 2: OS selection ─────────────────────────────────────────────────────
const selectedOS = ref<OS>('macos')

// ── Step 3: Model scope + custom selection ──────────────────────────────────
const selectedScope = ref<ModelScope>('featured')
const selectedModels = ref<string[]>([...FEATURED_MODELS])
const allModels = ref<ModelCanonical[]>([])
const allModelsLoading = ref(false)
const allModelsLoaded = ref(false)

async function loadAllModels() {
  if (allModelsLoaded.value) return
  allModelsLoading.value = true
  try {
    const resp = await listModels({ status: 'active' })
    allModels.value = resp.items.filter(m => m.status === 'active')
    allModelsLoaded.value = true
  } catch {
    allModels.value = []
  } finally {
    allModelsLoading.value = false
  }
}

watch(selectedScope, async (scope) => {
  if (scope === 'all') {
    await loadAllModels()
  }
})

function toggleModel(modelId: string) {
  const idx = selectedModels.value.indexOf(modelId)
  if (idx >= 0) {
    selectedModels.value.splice(idx, 1)
  } else {
    selectedModels.value.push(modelId)
  }
}

function selectAllModels() {
  selectedModels.value = allModels.value.map(m => m.canonical_name)
}

function deselectAllModels() {
  selectedModels.value = []
}

// ── Generated content ───────────────────────────────────────────────────────
const generatedFile = ref('')
const generatedScript = ref('')
const generatedManual = ref('')
const hasGenerated = ref(false)
const activeTab = ref<'file' | 'script' | 'manual'>('file')
const generating = ref(false)

watch(() => props.open, (val) => {
  if (val) {
    loadKeys()
    hasGenerated.value = false
    generatedFile.value = ''
    generatedScript.value = ''
    activeTab.value = 'file'
    selectedScope.value = 'featured'
    selectedModels.value = [...FEATURED_MODELS]
    selectedOS.value = 'macos'
  }
})

const selectedKey = computed(() => keys.value.find(k => k.id === selectedKeyId.value))

const fileExt = computed(() => props.tool === 'cherry_studio' ? '.json' : '.json')
const scriptExt = computed(() => selectedOS.value === 'windows' ? '.ps1' : '.sh')

async function generate() {
  if (!selectedKey.value) return
  generating.value = true
  hasGenerated.value = false

  const key = selectedKey.value
  const baseURL = 'https://llmgo.kxpms.cn/v1'
  let models = selectedModels.value
  if (selectedScope.value === 'featured') models = [...FEATURED_MODELS]

  let fileContent: any
  if (props.tool === 'zcode') {
    fileContent = renderZCodeConfig(key, models)
  } else if (props.tool === 'opencode') {
    fileContent = renderOpenCodeConfig(key, models)
  } else if (props.tool === 'cherry_studio') {
    fileContent = renderCherryStudioConfig(key, models)
  } else if (props.tool === 'roocode') {
    fileContent = renderRooCodeSettings(key, baseURL)
  } else {
    fileContent = { note: 'Cursor 不支持文件写入，请在 Settings UI 中手动配置' }
  }

  generatedFile.value = JSON.stringify(fileContent, null, 2)
  generatedScript.value = generateShellScript(props.tool, selectedOS.value, generatedFile.value, key)
  generatedManual.value = getManualSteps(props.tool, selectedOS.value)
  hasGenerated.value = true
  generating.value = false

  await auditAction({
    action: 'generate',
    tool: props.tool,
    os: selectedOS.value,
    keyId: key.id,
    modelCount: models.length,
    modelScope: selectedScope.value,
  })
}

function copyFile() {
  navigator.clipboard.writeText(generatedFile.value)
  auditAction({ action: 'copy_config', tool: props.tool, os: selectedOS.value, keyId: selectedKeyId.value || 0, modelCount: selectedModels.value.length, modelScope: selectedScope.value })
}

function downloadScript() {
  const filename = `${props.tool}-config${scriptExt.value}`
  downloadFile(generatedScript.value, filename, 'text/plain')
  auditAction({ action: 'download_script', tool: props.tool, os: selectedOS.value, keyId: selectedKeyId.value || 0, modelCount: selectedModels.value.length, modelScope: selectedScope.value })
}

function downloadFileContent() {
  const filename = `${props.tool}-config${fileExt.value}`
  downloadFile(generatedFile.value, filename, 'application/json')
}

function close() {
  emit('close')
}
</script>

<template>
  <div v-if="open" class="drawer-backdrop" @click.self="close">
    <div class="drawer-panel">
      <div class="drawer-header">
        <div class="dialog-title">
          <span>{{ toolInfo.icon }}</span>
          <span>{{ toolInfo.name }} 配置生成器</span>
        </div>
        <button class="btn btn-ghost btn-sm" @click="close">关闭 ✕</button>
      </div>

      <div class="drawer-body-scroll" style="max-height: none; padding: 0; border: none; border-radius: 0; background: transparent;">

        <!-- Step 1: API Key -->
        <div class="step-section">
          <div class="step-label">① 选择 API Key（当前租户下所有密钥）</div>
          <select v-model="selectedKeyId" class="select-field">
            <option v-if="keysLoading" value="">加载中…</option>
            <option v-if="!keysLoading && keys.length === 0" value="">无可用 Key</option>
            <option v-for="k in keys" :key="k.id" :value="k.id">
              {{ k.key_prefix }}**** ({{ k.application_code }}, {{ k.status }})
            </option>
          </select>
          <div v-if="selectedKey" class="key-info">
            已选：<code>{{ selectedKey.key_prefix }}****</code>
            <span class="badge" :class="selectedKey.status === 'active' ? 'badge-green' : 'badge-yellow'">{{ selectedKey.status }}</span>
          </div>
        </div>

        <!-- Step 2: OS -->
        <div class="step-section">
          <div class="step-label">② 操作系统</div>
          <div class="os-tabs">
            <button
              v-for="(info, os) in OS_INFO"
              :key="os"
              :class="['os-tab', selectedOS === os ? 'active' : '']"
              @click="selectedOS = os as OS"
            >{{ info.label }}</button>
          </div>
          <div class="path-hint">
            配置文件路径：<code>{{ OS_INFO[selectedOS].paths[tool] }}</code>
          </div>
        </div>

        <!-- Step 3: Model scope -->
        <div class="step-section">
          <div class="step-label">③ 选择模型范围</div>
          <div class="scope-radios">
            <label class="radio-label">
              <input type="radio" value="featured" v-model="selectedScope" />
              <span>精选模型（8 个核心模型）</span>
            </label>
            <label class="radio-label">
              <input type="radio" value="all" v-model="selectedScope" />
              <span>全部可用模型（从网关拉取）</span>
            </label>
          </div>

          <!-- Featured preview -->
          <div v-if="selectedScope === 'featured'" class="model-preview">
            <span class="model-tag" v-for="m in FEATURED_MODELS" :key="m">{{ m }}</span>
          </div>

          <!-- All models from API -->
          <div v-if="selectedScope === 'all'" class="all-models-panel">
            <div class="all-models-toolbar">
              <button class="btn btn-ghost btn-sm" @click="selectAllModels">全选</button>
              <button class="btn btn-ghost btn-sm" @click="deselectAllModels">清空</button>
              <span class="model-count-label">{{ selectedModels.length }} / {{ allModels.length }} 已选</span>
            </div>
            <div v-if="allModelsLoading" class="models-loading">加载中…</div>
            <div v-else class="models-checklist">
              <label
                v-for="m in allModels"
                :key="m.canonical_name"
                class="model-check-item"
              >
                <input
                  type="checkbox"
                  :checked="selectedModels.includes(m.canonical_name)"
                  @change="toggleModel(m.canonical_name)"
                />
                <span class="model-check-name">{{ m.canonical_name }}</span>
                <span class="model-check-vendor">{{ m.vendor || m.family || '' }}</span>
              </label>
            </div>
          </div>
        </div>

        <!-- Generate -->
        <div class="step-section generate-section">
          <button
            class="btn btn-primary"
            :disabled="!selectedKeyId || generating"
            @click="generate"
          >
            {{ generating ? '生成中…' : '生成配置' }}
          </button>
        </div>

        <!-- Results -->
        <div v-if="hasGenerated" class="results-section">
          <div class="result-tabs">
            <button :class="['tab-btn', activeTab === 'file' ? 'active' : '']" @click="activeTab = 'file'">配置文件</button>
            <button
              v-if="tool !== 'cherry_studio' && tool !== 'cursor'"
              :class="['tab-btn', activeTab === 'script' ? 'active' : '']"
              @click="activeTab = 'script'"
            >配置脚本</button>
            <button :class="['tab-btn', activeTab === 'manual' ? 'active' : '']" @click="activeTab = 'manual'">手动步骤</button>
          </div>

          <!-- File Tab -->
          <div v-if="activeTab === 'file'" class="tab-content">
            <pre class="code-preview">{{ generatedFile }}</pre>
            <div class="result-actions">
              <button class="btn btn-ghost btn-sm" @click="copyFile">复制内容</button>
              <button class="btn btn-ghost btn-sm" @click="downloadFileContent">下载文件</button>
            </div>
          </div>

          <!-- Script Tab -->
          <div v-if="activeTab === 'script'" class="tab-content">
            <pre class="code-preview script-code">{{ generatedScript }}</pre>
            <div class="result-actions">
              <button class="btn btn-primary btn-sm" @click="downloadScript">下载脚本</button>
              <span class="action-hint">脚本自动备份旧配置文件</span>
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
}

.step-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.step-label {
  font-weight: 600;
  font-size: 13px;
  color: var(--text, #e2e8f0);
}

.select-field {
  width: 100%;
  padding: 8px 12px;
  background: rgba(255, 255, 255, 0.05);
  border: 1px solid var(--border, #2d3139);
  border-radius: 8px;
  color: var(--text, #e2e8f0);
  font-size: 13px;
  outline: none;
}

.select-field:focus {
  border-color: rgba(99, 102, 241, 0.5);
}

.key-info {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--muted, #94a3b8);
}

.badge {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 4px;
}

.badge-green {
  background: rgba(34, 197, 94, 0.15);
  color: #4ade80;
}

.badge-yellow {
  background: rgba(234, 179, 8, 0.15);
  color: #facc15;
}

.os-tabs {
  display: flex;
  gap: 6px;
}

.os-tab {
  padding: 5px 14px;
  border-radius: 6px;
  border: 1px solid var(--border, #2d3139);
  background: none;
  color: var(--muted, #94a3b8);
  font-size: 13px;
  cursor: pointer;
  transition: all 0.15s;
}

.os-tab.active {
  background: rgba(99, 102, 241, 0.2);
  border-color: rgba(99, 102, 241, 0.5);
  color: #818cf8;
}

.path-hint {
  font-size: 12px;
  color: var(--muted, #94a3b8);
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
  color: var(--text, #e2e8f0);
  cursor: pointer;
}

.model-preview {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 10px 12px;
  background: rgba(255, 255, 255, 0.03);
  border-radius: 6px;
}

.model-tag {
  font-size: 12px;
  padding: 2px 8px;
  background: rgba(99, 102, 241, 0.15);
  color: #818cf8;
  border-radius: 4px;
}

.all-models-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
  border: 1px solid var(--border, #2d3139);
  border-radius: 8px;
  overflow: hidden;
}

.all-models-toolbar {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 12px;
  background: rgba(255, 255, 255, 0.03);
  border-bottom: 1px solid var(--border, #2d3139);
}

.model-count-label {
  font-size: 12px;
  color: var(--muted, #94a3b8);
  margin-left: auto;
}

.models-loading {
  padding: 20px;
  text-align: center;
  font-size: 13px;
  color: var(--muted, #94a3b8);
}

.models-checklist {
  max-height: 280px;
  overflow-y: auto;
  padding: 4px 0;
}

.model-check-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  cursor: pointer;
  font-size: 12px;
  transition: background 0.1s;
}

.model-check-item:hover {
  background: rgba(255, 255, 255, 0.04);
}

.model-check-name {
  color: var(--text, #e2e8f0);
  font-family: monospace;
  font-size: 12px;
}

.model-check-vendor {
  color: var(--muted, #94a3b8);
  font-size: 11px;
  margin-left: auto;
}

.generate-section {
  align-items: center;
}

.results-section {
  border-top: 1px solid var(--border, #2d3139);
  padding-top: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.result-tabs {
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--border, #2d3139);
  padding-bottom: 0;
}

.tab-btn {
  padding: 6px 14px;
  border: none;
  background: none;
  color: var(--muted, #94a3b8);
  font-size: 13px;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  transition: all 0.15s;
}

.tab-btn.active {
  color: #818cf8;
  border-bottom-color: #818cf8;
}

.tab-content {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.code-preview {
  background: #1a1d23;
  color: #e2e8f0;
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
  background: #0d1117;
  color: #79c0ff;
}

.manual-text {
  background: #0d1117;
  color: #e2e8f0;
  white-space: pre-wrap;
}

.result-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}

.action-hint {
  font-size: 12px;
  color: var(--muted, #94a3b8);
}
</style>
