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
const modelSearch = ref('')

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
    const key = m.family || m.vendor || '其它'
    if (!groups.has(key)) groups.set(key, [])
    groups.get(key)!.push(m)
  }
  return Array.from(groups.entries()).sort(([a], [b]) => a.localeCompare(b))
})

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
    <div class="drawer-panel drawer-panel-wide" @click.stop>
      <div class="drawer-header">
        <div class="dialog-title">
          <span>{{ toolInfo.icon }}</span>
          <span>{{ toolInfo.name }} 配置生成器</span>
        </div>
        <button class="btn btn-ghost btn-sm" @click="close">关闭 ✕</button>
      </div>

      <div class="drawer-body-scroll">

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
              <button class="btn btn-ghost btn-sm" @click="selectAllModels">全选当前</button>
              <button class="btn btn-ghost btn-sm" @click="deselectAllModels">清空</button>
              <span class="model-count-label">
                <strong>{{ selectedModels.length }}</strong> / {{ allModels.length }} 已选
                <span v-if="modelSearch" class="filter-hint">（{{ filteredModels.length }} 匹配搜索）</span>
              </span>
            </div>
            <input
              v-model="modelSearch"
              type="text"
              class="model-search-input"
              placeholder="🔍 搜索模型名称 / 厂商 / family…"
            />
            <div v-if="allModelsLoading" class="models-loading">加载中…</div>
            <div v-else-if="filteredModels.length === 0" class="models-loading">没有匹配的模型</div>
            <div v-else class="models-grouped">
              <div v-for="[family, list] in groupedModels" :key="family" class="model-family-group">
                <div class="model-family-header">
                  <span class="model-family-name">{{ family }}</span>
                  <span class="model-family-count">{{ list.length }}</span>
                </div>
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
          <div class="footer-info" v-if="hasGenerated">
            <span class="footer-hint">已生成 {{ selectedModels.length }} 个模型配置</span>
          </div>
          <button
            v-if="!hasGenerated"
            class="btn btn-primary footer-generate"
            :disabled="!selectedKeyId || generating"
            @click="generate"
          >
            {{ generating ? '生成中…' : '生成配置' }}
          </button>
          <button
            v-else
            class="btn btn-ghost footer-regenerate"
            @click="generate"
            :disabled="generating"
          >
            重新生成
          </button>
        </div>

        <!-- Results -->
        <div v-if="hasGenerated" class="results-section">
          <div class="result-tabs">
            <button :class="['tab-btn', activeTab === 'file' ? 'active' : '']" @click="activeTab = 'file'">配置文件</button>
            <button
              v-if="tool !== 'cherry_studio' && tool !== 'cursor'"
              :class="['tab-btn', activeTab === 'script' ? 'active' : '']" @click="activeTab = 'script'"
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
  color: var(--text, #e2e8f0);
}

.select-field {
  width: 100%;
  padding: 8px 12px;
  background: rgba(255, 255, 255, 0.04);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  color: var(--text, #e6edf3);
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
  color: var(--muted, #8b949e);
}

.badge {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 4px;
}

.badge-green {
  background: rgba(63, 185, 80, 0.15);
  color: #4ade80;
}

.badge-yellow {
  background: rgba(210, 153, 34, 0.15);
  color: #fbbf24;
}

.os-tabs {
  display: flex;
  gap: 6px;
}

.os-tab {
  padding: 5px 14px;
  border-radius: 6px;
  border: 1px solid var(--border, #30363d);
  background: none;
  color: var(--muted, #8b949e);
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
  color: var(--muted, #8b949e);
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
  color: var(--text, #e6edf3);
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
  gap: 0;
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  overflow: hidden;
  background: rgba(0, 0, 0, 0.15);
}

.all-models-toolbar {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 12px;
  background: rgba(255, 255, 255, 0.04);
  border-bottom: 1px solid var(--border, #30363d);
}

.model-count-label {
  font-size: 12px;
  color: var(--muted, #8b949e);
  margin-left: auto;
}

.model-count-label strong {
  color: var(--accent-h, #818cf8);
  font-size: 13px;
}

.filter-hint {
  color: var(--muted, #8b949e);
  font-size: 11px;
}

.model-search-input {
  width: 100%;
  padding: 8px 12px;
  background: rgba(255, 255, 255, 0.03);
  border: none;
  border-bottom: 1px solid var(--border, #30363d);
  color: var(--text, #e6edf3);
  font-size: 13px;
  outline: none;
  box-sizing: border-box;
}

.model-search-input:focus {
  background: rgba(99, 102, 241, 0.08);
  border-bottom-color: var(--accent, #6366f1);
}

.model-search-input::placeholder {
  color: var(--muted, #8b949e);
}

.models-loading {
  padding: 20px;
  text-align: center;
  font-size: 13px;
  color: var(--muted, #8b949e);
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
  background: rgba(255, 255, 255, 0.03);
  border-top: 1px solid var(--border, #30363d);
  border-bottom: 1px solid var(--border, #30363d);
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
  color: var(--muted, #8b949e);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.model-family-count {
  font-size: 11px;
  color: var(--muted, #8b949e);
  background: rgba(255, 255, 255, 0.05);
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
  background: rgba(99, 102, 241, 0.12);
}

.model-check-item input[type="checkbox"] {
  flex-shrink: 0;
  margin: 0;
  cursor: pointer;
}

.model-check-name {
  color: var(--text, #e6edf3);
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
  background: var(--card, #1c2128);
  border-top: 1px solid var(--border, #30363d);
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
  color: var(--muted, #8b949e);
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
  border-top: 1px solid var(--border, #30363d);
  padding-top: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.result-tabs {
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--border, #30363d);
  padding-bottom: 0;
}

.tab-btn {
  padding: 6px 14px;
  border: none;
  background: none;
  color: var(--muted, #8b949e);
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
  background: #0d1117;
  color: #e6edf3;
  border: 1px solid var(--border, #30363d);
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
  color: #79c0ff;
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
  color: var(--muted, #8b949e);
}
</style>
