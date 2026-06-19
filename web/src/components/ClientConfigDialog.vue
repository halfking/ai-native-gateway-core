<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { getKeys, type ApiKey } from '../api'
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

// Step 1: Key selection
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

// Step 2: OS selection
const selectedOS = ref<OS>('macos')

// Step 3: Model scope
const selectedScope = ref<ModelScope>('featured')
const selectedModels = ref<string[]>([...FEATURED_MODELS])

// Generated content
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
  }
})

const selectedKey = computed(() => keys.value.find(k => k.id === selectedKeyId.value))

const fileExt = computed(() => {
  if (props.tool === 'cherry_studio') return '.json'
  return '.json'
})

const scriptExt = computed(() => {
  return selectedOS.value === 'windows' ? '.ps1' : '.sh'
})

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
  <div v-if="open" class="dialog-backdrop" @click.self="close">
    <div class="dialog-panel">
      <div class="dialog-header">
        <div class="dialog-title">
          <span>{{ toolInfo.icon }}</span>
          <span>{{ toolInfo.name }} 配置生成器</span>
        </div>
        <button class="btn btn-ghost btn-sm" @click="close">关闭 ✕</button>
      </div>

      <div class="dialog-body">
        <!-- Step 1: API Key -->
        <div class="step-section">
          <div class="step-label">① 选择 API Key</div>
          <select v-model="selectedKeyId" class="select-field">
            <option v-if="keysLoading" value="">加载中…</option>
            <option v-if="!keysLoading && keys.length === 0" value="">无可用 Key</option>
            <option v-for="k in keys" :key="k.id" :value="k.id">
              {{ k.key_prefix }}**** ({{ k.application_code }}) — {{ k.status }}
            </option>
          </select>
          <div v-if="selectedKey" class="key-info">
            选中的 Key：<code>{{ selectedKey.key_prefix }}****</code>
          </div>
        </div>

        <!-- Step 2: OS -->
        <div class="step-section">
          <div class="step-label">② 选择操作系统</div>
          <div class="os-tabs">
            <button
              v-for="(info, os) in OS_INFO"
              :key="os"
              :class="['os-tab', selectedOS === os ? 'active' : '']"
              @click="selectedOS = os as OS"
            >{{ info.label }}</button>
          </div>
          <div class="path-hint">
            配置路径：<code>{{ OS_INFO[selectedOS].paths[tool] }}</code>
          </div>
        </div>

        <!-- Step 3: Model Scope -->
        <div class="step-section">
          <div class="step-label">③ 选择模型范围</div>
          <div class="scope-radios">
            <label class="radio-label">
              <input type="radio" value="featured" v-model="selectedScope" />
              <span>精选（8 个核心模型）</span>
            </label>
            <label class="radio-label">
              <input type="radio" value="all" v-model="selectedScope" />
              <span>全部可用模型（需联网获取）</span>
            </label>
            <label class="radio-label">
              <input type="radio" value="custom" v-model="selectedScope" />
              <span>自定义（暂不支持）</span>
            </label>
          </div>
          <div v-if="selectedScope === 'featured'" class="model-preview">
            模型清单：{{ FEATURED_MODELS.join(', ') }}
          </div>
        </div>

        <!-- Generate Button -->
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
          <!-- Tabs -->
          <div class="result-tabs">
            <button :class="['tab-btn', activeTab === 'file' ? 'active' : '']" @click="activeTab = 'file'">配置文件</button>
            <button v-if="tool !== 'cherry_studio' && tool !== 'cursor'" :class="['tab-btn', activeTab === 'script' ? 'active' : '']" @click="activeTab = 'script'">配置脚本</button>
            <button :class="['tab-btn', activeTab === 'manual' ? 'active' : '']" @click="activeTab = 'manual'">手动步骤</button>
          </div>

          <!-- File Panel -->
          <div v-if="activeTab === 'file'" class="tab-content">
            <pre class="code-preview">{{ generatedFile }}</pre>
            <div class="result-actions">
              <button class="btn btn-ghost btn-sm" @click="copyFile">复制内容</button>
              <button class="btn btn-ghost btn-sm" @click="downloadFileContent">下载文件</button>
            </div>
          </div>

          <!-- Script Panel -->
          <div v-if="activeTab === 'script'" class="tab-content">
            <pre class="code-preview script-code">{{ generatedScript }}</pre>
            <div class="result-actions">
              <button class="btn btn-primary btn-sm" @click="downloadScript">下载脚本</button>
              <span class="action-hint">脚本会自动备份旧配置文件</span>
            </div>
          </div>

          <!-- Manual Panel -->
          <div v-if="activeTab === 'manual'" class="tab-content">
            <pre class="code-preview manual-text">{{ generatedManual }}</pre>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dialog-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}

.dialog-panel {
  background: var(--bg-secondary, #1a1d23);
  border: 1px solid var(--border, #2d3139);
  border-radius: 14px;
  width: 100%;
  max-width: 680px;
  max-height: 90vh;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.5);
}

.dialog-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border, #2d3139);
}

.dialog-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
  font-size: 15px;
}

.dialog-body {
  padding: 20px;
  overflow-y: auto;
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
  font-size: 12px;
  color: var(--muted, #94a3b8);
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
  font-size: 12px;
  color: var(--muted, #94a3b8);
  padding: 8px 12px;
  background: rgba(255, 255, 255, 0.03);
  border-radius: 6px;
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
  max-height: 300px;
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
